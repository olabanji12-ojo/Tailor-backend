package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"runtime"

	"github.com/emman/Tailor-Backend/internal/database"
	"github.com/emman/Tailor-Backend/internal/middleware"
	"github.com/emman/Tailor-Backend/internal/models"
	"github.com/emman/Tailor-Backend/internal/repository"
	"github.com/gorilla/mux"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// Valid 6-stage production lifecycle
var validProductionStatuses = map[string]bool{
	"received":  true,
	"cutting":   true,
	"sewing":    true,
	"fitting":   true,
	"ready":     true,
	"delivered": true,
}

func isValidProductionStatus(status string) bool {
	return validProductionStatuses[strings.ToLower(strings.TrimSpace(status))]
}

// logVoiceEvent writes an immutable receipt to MongoDB voice_events — fire and forget.
// Pattern: Event Sourcing. Redis = fast counter. MongoDB = source of truth for disputes.
func logVoiceEvent(shopID string, fileBytes, estimated, actual, quotaBefore, quotaAfter int64, outcome string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		event := models.VoiceEvent{
			ID:               primitive.NewObjectID(),
			ShopID:           shopID,
			Timestamp:        time.Now(),
			FileSizeBytes:    fileBytes,
			EstimatedSeconds: estimated,
			ActualSeconds:    actual,
			Outcome:          outcome,
			QuotaBefore:      quotaBefore,
			QuotaAfter:       quotaAfter,
		}
		if _, err := database.GetCollection("voice_events").InsertOne(ctx, event); err != nil {
			log.Printf("⚠️ voice_events log failed: %v", err)
		}
	}()
}

func (h *Handler) Transcribe(w http.ResponseWriter, r *http.Request) {
	// 0. Get auth context
	authCtx, ok := middleware.GetAuthContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	shopID := authCtx.UserID
	isAdmin := authCtx.Role == "admin"

	// 1. Parse multipart form + get file FIRST (we need file size before any quota logic)
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		http.Error(w, "File too large", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("audio")
	if err != nil {
		http.Error(w, "Audio file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 2. Estimate duration from file size — WebM Opus ~4000 bytes/sec at 32kbps
	// Used to PRE-CHARGE the tailor before Whisper is called.
	estimatedSeconds := int64(math.Ceil(float64(header.Size) / 4000.0))
	if estimatedSeconds < 1 {
		estimatedSeconds = 1
	}

	// 3. Concurrency-Safe Atomic Quota Reservation (Authoritative MongoDB with Redis caching & failover)
	// If Redis is down, MongoDB fallback activates automatically.
	// If both are down, fails safely to protect against unmetered API consumption.
	quotaBefore, usedSeconds, isFallback, qErr := database.ReserveVoiceQuota(r.Context(), shopID, estimatedSeconds, isAdmin)
	if qErr != nil {
		if errors.Is(qErr, database.ErrQuotaExceeded) {
			logVoiceEvent(shopID, header.Size, estimatedSeconds, 0, quotaBefore, usedSeconds, "quota_exceeded")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			quotaResp := database.BuildVoiceQuotaResponse(usedSeconds, isAdmin)
			quotaResp["warning_level"] = "exceeded"
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error":   "voice_quota_exceeded",
				"message": "You've reached your voice usage limit for this period. You can continue entering measurements manually.",
				"quota":   quotaResp,
			})
			return
		}

		// Fail closed when storage dependencies are unavailable
		logVoiceEvent(shopID, header.Size, estimatedSeconds, 0, 0, 0, "unavailable")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "voice_service_unavailable",
			"message": "Voice recording is temporarily unavailable. You can still enter measurements manually.",
		})
		return
	}

	if isFallback {
		log.Printf("ℹ️ Voice transcription for shop %s operating under MongoDB quota fallback", shopID)
	}

	// 4. Build OpenAI Whisper request
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		http.Error(w, "OpenAI API Key not configured", http.StatusInternalServerError)
		return
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", header.Filename)
	if err != nil {
		http.Error(w, "Failed to create multipart form", http.StatusInternalServerError)
		return
	}
	io.Copy(part, file)
	writer.WriteField("model", "whisper-1")
	writer.WriteField("response_format", "verbose_json")
	writer.WriteField("prompt", "The speaker is a professional Nigerian bespoke tailor and fashion designer recording body and garment measurements (e.g., Agbada length, Buba length, wingspan, lap, thigh, round sleeve, bicep, neck, chest, shoulder, waist, hip, trouser length, top length, cap size, fila size, head circumference, ankle). Measurements may include fractions like half, quarter (e.g., 22 and a half). Focus on capturing garment and body parts followed by numerical values accurately. Ignore background music and noise.")
	writer.Close()

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", body)
	if err != nil {
		http.Error(w, "Failed to create OpenAI request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// 5. Execute the Whisper API call (30s backend timeout)
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Whisper timed out or connection failed — pre-charge already deducted, return quota info
		logVoiceEvent(shopID, header.Size, estimatedSeconds, 0, quotaBefore, usedSeconds, "timeout")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGatewayTimeout)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "whisper_timeout",
			"quota": database.BuildVoiceQuotaResponse(usedSeconds, isAdmin),
		})
		return
	}
	defer resp.Body.Close()

	// 6. Parse Whisper verbose_json response (need text + actual duration)
	var whisperResp struct {
		Text     string  `json:"text"`
		Duration float64 `json:"duration"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&whisperResp); err != nil || whisperResp.Text == "" {
		// Empty/failed transcript — pre-charge stands (silence still costs Whisper)
		logVoiceEvent(shopID, header.Size, estimatedSeconds, 0, quotaBefore, usedSeconds, "empty")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "transcription_empty",
			"quota": database.BuildVoiceQuotaResponse(usedSeconds, isAdmin),
		})
		return
	}

	// 7. Bidirectional reconciliation (Authorize-Capture pattern, Stripe-style).
	// Actual duration always wins — delta can be negative (refund) or positive (top-up).
	actualSeconds := int64(math.Ceil(whisperResp.Duration))
	if actualSeconds < 1 {
		actualSeconds = 1
	}
	reconciledSeconds, rErr := database.ReconcileVoiceQuota(r.Context(), shopID, estimatedSeconds, actualSeconds, isAdmin)
	if rErr == nil && !isAdmin {
		usedSeconds = reconciledSeconds
	}

	// 8. Return transcript + final quota metadata
	logVoiceEvent(shopID, header.Size, estimatedSeconds, actualSeconds, quotaBefore, usedSeconds, "success")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"text":  whisperResp.Text,
		"quota": database.BuildVoiceQuotaResponse(usedSeconds, isAdmin),
	})
}

type Handler struct {
	customerRepo    *repository.CustomerRepository
	measurementRepo *repository.MeasurementRepository
	userRepo        *repository.UserRepository
}

func NewHandler(cRepo *repository.CustomerRepository, mRepo *repository.MeasurementRepository, uRepo *repository.UserRepository) *Handler {
	return &Handler{
		customerRepo:    cRepo,
		measurementRepo: mRepo,
		userRepo:        uRepo,
	}
}

func (h *Handler) GetCustomers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query().Get("q")
	limitStr := r.URL.Query().Get("limit")
	limit, _ := strconv.ParseInt(limitStr, 10, 64)
	if limit <= 0 {
		limit = 20
	}

	customers, err := h.customerRepo.Search(ctx, authCtx.UserID, authCtx.ShopName, q, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(customers)
}

func (h *Handler) GetMeasurements(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID := authCtx.UserID
	shopName := authCtx.ShopName

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > 100 {
		q = q[:100] // bound search query length to avoid runaway regex
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))

	pageStr := r.URL.Query().Get("page")
	limitStr := r.URL.Query().Get("limit")
	page, _ := strconv.ParseInt(pageStr, 10, 64)
	limit, _ := strconv.ParseInt(limitStr, 10, 64)

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	// Redis Cache Check — Keyed by unique UserID, q, status, page, limit to prevent cross-tenant/filter bleed
	cacheKey := fmt.Sprintf("cache:measurements:user:%s:q:%s:status:%s:page:%d:limit:%d", userID, url.QueryEscape(q), url.QueryEscape(status), page, limit)
	var cachedData []byte
	var cacheHit = false
	if database.RedisClient != nil {
		val, err := database.RedisClient.Get(ctx, cacheKey).Bytes()
		if err == nil {
			cachedData = val
			cacheHit = true
		}
	}

	if cacheHit {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cachedData)
		return
	}

	responseData, total, err := h.measurementRepo.SearchAndPaginate(ctx, userID, shopName, q, status, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	totalPages := int64(0)
	if limit > 0 {
		totalPages = (total + limit - 1) / limit
	}
	hasMore := page < totalPages

	respBytes, err := json.Marshal(map[string]interface{}{
		"data":       responseData,
		"total":      total,
		"page":       page,
		"limit":      limit,
		"totalPages": totalPages,
		"hasMore":    hasMore,
	})
	if err == nil && database.RedisClient != nil {
		database.RedisClient.Set(ctx, cacheKey, respBytes, 10*time.Minute)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(respBytes)
}

func (h *Handler) SaveMeasurement(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID := authCtx.UserID
	shopName := authCtx.ShopName

	var req models.MeasurementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.CustomerName == "" {
		http.Error(w, "Customer name is required", http.StatusBadRequest)
		return
	}

	// 1. Find or Create Customer
	var customer *models.Customer
	var err error

	// If customer_id was provided (e.g. from selecting an existing client), prioritize ID lookup
	// CRITICAL SECURITY CHECK: Ensure referenced customer belongs exclusively to the authenticated tenant!
	if req.CustomerID != "" {
		if objID, idErr := primitive.ObjectIDFromHex(req.CustomerID); idErr == nil {
			customer, err = h.customerRepo.FindByID(ctx, objID, userID, shopName)
			if err != nil && err != mongo.ErrNoDocuments {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if customer == nil {
				// Prevent associating measurements with another tenant's customer ID
				http.Error(w, "Customer not found or unauthorized", http.StatusNotFound)
				return
			}
		}
	}

	// If not found by ID or no ID provided, lookup by Name within the authenticated tenant
	if customer == nil {
		customer, err = h.customerRepo.FindByName(ctx, req.CustomerName, userID, shopName)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				// Create new customer with phone, gender, and client photo scoped to authenticated user!
				customer = &models.Customer{
					Name:        req.CustomerName,
					Phone:       req.CustomerPhone,
					ShopID:      shopName,
					UserID:      userID,
					Gender:      req.Gender,
					ClientPhoto: req.ClientPhoto,
				}
				if err := h.customerRepo.Create(ctx, customer); err != nil {
					http.Error(w, "Failed to create customer", http.StatusInternalServerError)
					return
				}
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
	}

	// Update customer attributes if provided and changed
	updates := bson.M{}
	if req.CustomerPhone != "" && customer.Phone != req.CustomerPhone {
		updates["phone"] = req.CustomerPhone
		customer.Phone = req.CustomerPhone
	}
	if req.Gender != "" && customer.Gender != req.Gender {
		updates["gender"] = req.Gender
		customer.Gender = req.Gender
	}
	if req.ClientPhoto != "" && customer.ClientPhoto != req.ClientPhoto {
		updates["client_photo"] = req.ClientPhoto
		customer.ClientPhoto = req.ClientPhoto
	}
	if req.CustomerName != "" && customer.Name != req.CustomerName {
		updates["name"] = req.CustomerName
		customer.Name = req.CustomerName
	}
	if len(updates) > 0 {
		_, _ = h.customerRepo.Update(ctx, customer.ID, userID, shopName, updates)
	}

	// 2. Save Measurement with production status lifecycle
	// Defaults to "received" for new orders; validates if explicitly supplied
	orderStatus := strings.ToLower(strings.TrimSpace(req.Status))
	if orderStatus == "" {
		orderStatus = "received"
	} else if !isValidProductionStatus(orderStatus) {
		http.Error(w, "Invalid status. Allowed: received, cutting, sewing, fitting, ready, delivered", http.StatusBadRequest)
		return
	}

	// TASK-09 Idempotency Check: if client_operation_id is provided, check if already saved
	if req.ClientOperationId != "" {
		existing, findErr := h.measurementRepo.FindByClientOperationId(ctx, req.ClientOperationId, userID)
		if findErr == nil && existing != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(existing)
			return
		}
	}

	// TASK-09 Dual Payload Compatibility: accept both "data" and "measurements"
	dataMap := req.Data
	if (dataMap == nil || len(dataMap) == 0) && len(req.Measurements) > 0 {
		dataMap = req.Measurements
	}
	if dataMap == nil {
		dataMap = make(map[string]interface{})
	}

	// CRITICAL SECURITY ENFORCEMENT:
	// ShopID and UserID are derived SOLELY from verified server authCtx, NEVER trusting client req.ShopID!
	measurement := &models.Measurement{
		CustomerID:        customer.ID,
		Date:              time.Now(),
		Data:              dataMap,
		ClientOperationId: req.ClientOperationId,
		Transcript:        req.Transcript,
		Unit:              req.Unit,
		ShopID:            shopName,
		UserID:            userID,
		StylePhotos:  req.StylePhotos,
		ClothPhotos:  req.ClothPhotos,
		Gender:       req.Gender,
		Garment:      req.Garment,
		DeliveryDate: req.DeliveryDate,
		ReminderDate: req.ReminderDate,
		TotalCost:    req.TotalCost,
		AmountPaid:   req.AmountPaid,
		DesignNotes:  req.DesignNotes,
		ClientPhoto:  req.ClientPhoto,
		Status:       orderStatus,
		Payments:     []models.PaymentRecord{},
		StatusHistory: []models.StatusHistoryEntry{
			{
				PreviousStatus: "",
				NewStatus:      orderStatus,
				Timestamp:      time.Now(),
				UpdatedBy:      userID,
			},
		},
	}

	if req.AmountPaid > 0 {
		measurement.Payments = append(measurement.Payments, models.PaymentRecord{
			ID:        primitive.NewObjectID(),
			Amount:    req.AmountPaid,
			Method:    "cash",
			Date:      time.Now(),
			Note:      "Initial deposit",
			CreatedBy: userID,
			CreatedAt: time.Now(),
		})
	}

	if err := h.measurementRepo.Save(ctx, measurement); err != nil {
		http.Error(w, "Failed to save measurement", http.StatusInternalServerError)
		return
	}

	h.InvalidateMeasurementsCache(userID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(measurement)
}

func (h *Handler) UpdateMeasurement(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID := authCtx.UserID
	shopName := authCtx.ShopName

	params := mux.Vars(r)
	id, err := primitive.ObjectIDFromHex(params["id"])
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	var updates map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&updates); err != nil {
		http.Error(w, "Invalid body", http.StatusBadRequest)
		return
	}

	// CRITICAL SECURITY CHECK: Ensure measurement belongs to authenticated tenant before proceeding!
	existingM, errFind := h.measurementRepo.FindByID(ctx, id, userID, shopName)
	if errFind != nil || existingM == nil {
		http.Error(w, "Measurement not found", http.StatusNotFound)
		return
	}

	// Sync customer phone or name if included in update payload
	if phoneVal, ok := updates["customer_phone"]; ok {
		if phoneStr, okStr := phoneVal.(string); okStr {
			_, _ = h.customerRepo.UpdatePhone(ctx, existingM.CustomerID, userID, shopName, phoneStr)
		}
		delete(updates, "customer_phone")
	}
	if nameVal, ok := updates["customer_name"]; ok {
		if nameStr, okStr := nameVal.(string); okStr && nameStr != "" {
			_, _ = h.customerRepo.Update(ctx, existingM.CustomerID, userID, shopName, bson.M{"name": nameStr})
		}
		delete(updates, "customer_name")
	}

	matched, err := h.measurementRepo.Update(ctx, id, userID, shopName, updates)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if matched == 0 {
		http.Error(w, "Measurement not found", http.StatusNotFound)
		return
	}

	h.InvalidateMeasurementsCache(userID)

	w.WriteHeader(http.StatusNoContent)
}

// UpdateMeasurementStatus transitions an order through the 6-stage production lifecycle
// PATCH /api/measurements/{id}/status
func (h *Handler) UpdateMeasurementStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID := authCtx.UserID
	shopName := authCtx.ShopName

	params := mux.Vars(r)
	id, err := primitive.ObjectIDFromHex(params["id"])
	if err != nil {
		http.Error(w, "Invalid measurement ID", http.StatusBadRequest)
		return
	}

	var req models.StatusUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	newStatus := strings.ToLower(strings.TrimSpace(req.Status))
	if !isValidProductionStatus(newStatus) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "invalid_status",
			"message": "Invalid production status. Allowed values: received, cutting, sewing, fitting, ready, delivered",
		})
		return
	}

	// 1. Fetch current measurement to confirm tenant ownership and record previous state
	existingM, err := h.measurementRepo.FindByID(ctx, id, userID, shopName)
	if err != nil || existingM == nil {
		http.Error(w, "Measurement not found or unauthorized", http.StatusNotFound)
		return
	}

	prevStatus := existingM.Status
	historyEntry := models.StatusHistoryEntry{
		PreviousStatus: prevStatus,
		NewStatus:      newStatus,
		Timestamp:      time.Now(),
		UpdatedBy:      userID,
	}

	// 2. Atomically update status and append audit history
	updatedM, err := h.measurementRepo.UpdateStatus(ctx, id, userID, shopName, newStatus, historyEntry)
	if err != nil {
		http.Error(w, "Failed to update production status: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.InvalidateMeasurementsCache(userID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updatedM)
}

func (h *Handler) UpdateCustomer(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID := authCtx.UserID
	shopName := authCtx.ShopName

	params := mux.Vars(r)
	id, err := primitive.ObjectIDFromHex(params["id"])
	if err != nil {
		http.Error(w, "Invalid customer ID", http.StatusBadRequest)
		return
	}

	var req models.CustomerUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	updates := bson.M{}
	if req.Name != "" {
		updates["name"] = req.Name
	}
	if req.Phone != "" {
		updates["phone"] = req.Phone
	}
	if req.Email != "" {
		updates["email"] = req.Email
	}

	if len(updates) > 0 {
		matched, err := h.customerRepo.Update(ctx, id, userID, shopName, updates)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if matched == 0 {
			http.Error(w, "Customer not found", http.StatusNotFound)
			return
		}
	}

	h.InvalidateMeasurementsCache(userID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "success"})
}

func (h *Handler) GetCustomerHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID := authCtx.UserID
	shopName := authCtx.ShopName

	params := mux.Vars(r)
	customerID, err := primitive.ObjectIDFromHex(params["id"])
	if err != nil {
		http.Error(w, "Invalid customer ID", http.StatusBadRequest)
		return
	}

	// CRITICAL SECURITY CHECK: Verify customer belongs to this tenant!
	customer, err := h.customerRepo.FindByID(ctx, customerID, userID, shopName)
	if err != nil || customer == nil {
		http.Error(w, "Customer not found", http.StatusNotFound)
		return
	}

	measurements, err := h.measurementRepo.GetByCustomerID(ctx, customerID, userID, shopName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(measurements)
}

func (h *Handler) DeleteMeasurement(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID := authCtx.UserID
	shopName := authCtx.ShopName

	params := mux.Vars(r)
	id, err := primitive.ObjectIDFromHex(params["id"])
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	deleted, err := h.measurementRepo.Delete(ctx, id, userID, shopName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if deleted == 0 {
		http.Error(w, "Measurement not found", http.StatusNotFound)
		return
	}

	h.InvalidateMeasurementsCache(userID)

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) InvalidateMeasurementsCache(userOrShopID string) {
	if database.RedisClient == nil {
		return
	}
	ctx := context.Background()
	patterns := []string{
		fmt.Sprintf("cache:measurements:user:%s:*", userOrShopID),
		fmt.Sprintf("cache:measurements:shop:%s:*", userOrShopID),
	}
	for _, pattern := range patterns {
		iter := database.RedisClient.Scan(ctx, 0, pattern, 0).Iterator()
		for iter.Next(ctx) {
			database.RedisClient.Del(ctx, iter.Val())
		}
	}
}

var serverStartTime time.Time

func init() {
	serverStartTime = time.Now()
}

func (h *Handler) GetDiagnostics(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Strict role-based admin check
	authCtx, ok := middleware.GetAuthContext(r)
	if !ok || authCtx.Role != "admin" {
		http.Error(w, "Access Denied: Admin Clearance Required", http.StatusForbidden)
		return
	}

	// 1. Gather Database stats
	usersColl := database.GetCollection("users")
	measurementsColl := database.GetCollection("measurements")
	customersColl := database.GetCollection("customers")

	totalUsers, _ := usersColl.CountDocuments(ctx, bson.M{})
	totalMeasurements, _ := measurementsColl.CountDocuments(ctx, bson.M{})
	totalCustomers, _ := customersColl.CountDocuments(ctx, bson.M{})

	// Aggregate unique shops
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$group", Value: bson.D{{Key: "_id", Value: "$shop_name"}}}},
		bson.D{{Key: "$count", Value: "count"}},
	}
	cursor, err := usersColl.Aggregate(ctx, pipeline)
	var totalShops int64 = 0
	if err == nil && cursor != nil {
		if cursor.Next(ctx) {
			var result struct {
				Count int64 `bson:"count"`
			}
			if err := cursor.Decode(&result); err == nil {
				totalShops = result.Count
			}
		}
		cursor.Close(ctx)
	}

	// 2. Fetch System/Runtime Telemetry
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// Memory usage in Megabytes
	ramUsageMb := float64(memStats.Alloc) / 1024 / 1024

	// System latency check (ping MongoDB)
	startPing := time.Now()
	pingErr := database.DB.Client().Ping(ctx, nil)
	dbLatencyMs := time.Since(startPing).Milliseconds()
	dbStatus := "healthy"
	if pingErr != nil {
		dbStatus = "unhealthy"
	}

	// Dynamic Redis telemetry check
	redisStatus := "disconnected"
	var redisLatencyMs int64 = 0
	if database.RedisClient != nil {
		startRedisPing := time.Now()
		redisPingErr := database.RedisClient.Ping(ctx).Err()
		redisLatencyMs = time.Since(startRedisPing).Milliseconds()
		if redisPingErr == nil {
			redisStatus = "healthy"
		} else {
			redisStatus = "unhealthy"
		}
	}

	// Calculate mock Whisper billing details (or real counts if tracked)
	mockWhisperCost := float64(totalMeasurements) * 0.003 // Whisper rate is $0.006/minute, assume 30s per record average

	diagnostics := map[string]interface{}{
		"system": map[string]interface{}{
			"status":           "operational",
			"uptime":           time.Since(serverStartTime).String(),
			"goroutines":       runtime.NumGoroutine(),
			"ram_usage_mb":     ramUsageMb,
			"db_status":        dbStatus,
			"db_latency_ms":    dbLatencyMs,
			"redis_status":     redisStatus,
			"redis_latency_ms": redisLatencyMs,
		},
		"ateliers": map[string]interface{}{
			"total_registered_shops": totalShops,
			"total_tailor_users":     totalUsers,
			"total_customers":        totalCustomers,
			"total_measurements":     totalMeasurements,
		},
		"voice_ai": map[string]interface{}{
			"total_whisper_minutes": float64(totalMeasurements) * 0.5,
			"estimated_cost_usd":    mockWhisperCost,
			"average_latency_ms":    850,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*") // Ensure cross-origin accessibility
	w.Header().Set("Access-Control-Allow-Headers", "*")
	json.NewEncoder(w).Encode(diagnostics)
}

func (h *Handler) ParseVoice(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Parse input
	var requestBody struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil || requestBody.Text == "" {
		http.Error(w, "Text parameter is required", http.StatusBadRequest)
		return
	}

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		http.Error(w, "OpenAI API Key is not configured on server", http.StatusInternalServerError)
		return
	}

	// Prepare OpenAI Chat Completions payload
	payload := map[string]interface{}{
		"model": "gpt-4o-mini",
		"messages": []map[string]string{
			{
				"role":    "system",
				"content": "You are a professional African and Nigerian bespoke tailor's transcription NLP translator. Extract garment and body measurements mentioned in the text. Standard canonical keys (lowercase, snake_case) include:\n- 'agbada_length' (Agbada full length)\n- 'buba_length' (Buba length, strictly distinct from Agbada length)\n- 'wingspan' (Agbada wingspan or span)\n- 'lap' (Lap or thigh)\n- 'round_sleeve' (Round sleeve, round arm, or bicep)\n- 'cap_size' (Cap size or Fila size)\n- 'head_circumference' (Head circumference)\n- 'cap_height' (Cap height or depth)\n- 'top_length', 'trouser_length', 'chest', 'shoulder', 'neck', 'waist', 'hip', 'sleeve', 'knee', 'ankle', 'underbust', 'bust_point', 'dart_length', 'kaftan_length', 'gown_length', 'skirt_length', 'shorts_length'.\nMap synonyms appropriately: 'wingspan'/'agbada span' -> 'wingspan'; 'lap'/'thigh' -> 'lap'; 'round sleeve'/'round arm' -> 'round_sleeve'; 'fila size' -> 'cap_size'; do not confuse 'buba_length' and 'agbada_length'. Do NOT guess ambiguous terms (e.g. 'hand' or 'bottom') without clear garment context. Numbers must be floats. Accurately convert spoken fractions (e.g., '22 and a half' -> 22.5, '42 and a quarter' -> 42.25, '34 three quarters' -> 34.75). Respond ONLY with a valid, clean JSON object mapping part keys to numeric float values. Do not include markdown formatting or explanations. If no measurements are found, return empty JSON {}.",
			},
			{
				"role":    "user",
				"content": requestBody.Text,
			},
		},
		"response_format": map[string]string{
			"type": "json_object", // Ensure structural JSON output compliance!
		},
	}

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "Failed to marshal OpenAI payload", http.StatusInternalServerError)
		return
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonBytes))
	if err != nil {
		http.Error(w, "Failed to build OpenAI client query", http.StatusInternalServerError)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to communicate with OpenAI Translator API", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		http.Error(w, "OpenAI returns error status: "+string(bodyBytes), http.StatusBadGateway)
		return
	}

	// Extract the JSON content inside choices[0].message.content
	var openAIResponse struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&openAIResponse); err != nil || len(openAIResponse.Choices) == 0 {
		http.Error(w, "Failed to decode translation response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "*")

	// Pass the clean extracted JSON back to client
	w.Write([]byte(openAIResponse.Choices[0].Message.Content))
}


func (h *Handler) TriggerBackup(w http.ResponseWriter, r *http.Request) {
	// Role-based admin guard
	authCtx, ok := middleware.GetAuthContext(r)
	if !ok || authCtx.Role != "admin" {
		http.Error(w, "Access Forbidden: Administrative credentials required", http.StatusForbidden)
		return
	}

	// Execute manual backup
	err := database.RunBackupJob()
	if err != nil {
		http.Error(w, "Failed to execute database backup: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "Database backup successfully generated and archived in private GitHub repository",
	})
}

// GetUploadAuth provides authenticated, tenant-scoped Cloudinary upload authorization.
// Enforces tenant isolation so photos and fabric references are strictly scoped to the authenticated tailor.
func (h *Handler) GetUploadAuth(w http.ResponseWriter, r *http.Request) {
	authCtx, ok := middleware.GetAuthContext(r)
	if !ok || authCtx.UserID == "" {
		http.Error(w, "Unauthorized: Valid tailor session required", http.StatusUnauthorized)
		return
	}

	cloudName := os.Getenv("CLOUDINARY_CLOUD_NAME")
	if cloudName == "" {
		cloudName = os.Getenv("CloudName")
	}

	preset := os.Getenv("CLOUDINARY_UPLOAD_PRESET")
	if preset == "" {
		preset = os.Getenv("UNSIGNED_NAME")
	}

	// BUG-TV-03 Remediation: Eliminate silent hardcoded defaults!
	// If credentials or preset are missing, return an explicit error rather than silently pointing to a rogue or default account.
	if cloudName == "" || preset == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "cloudinary_unconfigured",
			"message": "Cloudinary environment variables are missing on the server. Please configure CLOUDINARY_CLOUD_NAME and CLOUDINARY_UPLOAD_PRESET.",
		})
		return
	}

	tenantFolder := fmt.Sprintf("tailors/%s", authCtx.UserID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"cloud_name":    cloudName,
		"upload_preset": preset,
		"folder":        tenantFolder,
		"user_id":       authCtx.UserID,
		"timestamp":     time.Now().Unix(),
	})
}

// AddPayment records a new installment payment against an order/measurement
func (h *Handler) AddPayment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok || authCtx.UserID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID := authCtx.UserID
	shopName := authCtx.ShopName

	params := mux.Vars(r)
	measurementID, err := primitive.ObjectIDFromHex(params["id"])
	if err != nil {
		http.Error(w, "Invalid measurement ID", http.StatusBadRequest)
		return
	}

	var req models.PaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid payment request body", http.StatusBadRequest)
		return
	}

	if req.Amount <= 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "invalid_amount",
			"message": "Payment amount must be greater than zero",
		})
		return
	}

	method := strings.ToLower(strings.TrimSpace(req.Method))
	if method == "" {
		method = "cash"
	}
	switch method {
	case "cash", "bank_transfer", "pos", "other":
	default:
		method = "other"
	}

	paymentDate := time.Now()
	if req.Date != "" {
		if parsed, err := time.Parse(time.RFC3339, req.Date); err == nil {
			paymentDate = parsed
		} else if parsed, err := time.Parse("2006-01-02", req.Date); err == nil {
			paymentDate = parsed
		}
	}

	// Verify measurement exists and belongs to this tenant
	current, err := h.measurementRepo.FindByID(ctx, measurementID, userID, shopName)
	if err != nil || current == nil {
		http.Error(w, "Measurement not found or unauthorized", http.StatusNotFound)
		return
	}

	// Overpayment policy: payment cannot exceed outstanding balance
	remainingBalance := current.TotalCost - current.AmountPaid
	if current.TotalCost > 0 && req.Amount > remainingBalance {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "overpayment_not_allowed",
			"message": fmt.Sprintf("Payment amount (₦%.2f) exceeds outstanding balance (₦%.2f)", req.Amount, remainingBalance),
		})
		return
	}

	payment := models.PaymentRecord{
		ID:             primitive.NewObjectID(),
		Amount:         req.Amount,
		Method:         method,
		Date:           paymentDate,
		Note:           strings.TrimSpace(req.Note),
		IdempotencyKey: strings.TrimSpace(req.IdempotencyKey),
		CreatedBy:      userID,
		CreatedAt:      time.Now(),
	}

	updated, err := h.measurementRepo.AddPayment(ctx, measurementID, userID, shopName, payment)
	if err != nil {
		http.Error(w, "Failed to record payment: "+err.Error(), http.StatusInternalServerError)
		return
	}

	h.InvalidateMeasurementsCache(userID)

	balance := updated.TotalCost - updated.AmountPaid
	if balance < 0 {
		balance = 0
	}
	paymentStatus := "unpaid"
	if updated.AmountPaid >= updated.TotalCost && updated.TotalCost > 0 {
		paymentStatus = "fully_paid"
	} else if updated.AmountPaid > 0 {
		paymentStatus = "partially_paid"
	}

	payments := updated.Payments
	if payments == nil {
		payments = []models.PaymentRecord{}
	}

	summary := models.FinancialSummary{
		TotalCost:     updated.TotalCost,
		AmountPaid:    updated.AmountPaid,
		TotalPaid:     updated.AmountPaid,
		Balance:       balance,
		PaymentStatus: paymentStatus,
		Payments:      payments,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":      "success",
		"message":     "Payment recorded successfully",
		"summary":     summary,
		"measurement": updated,
		"amount_paid": updated.AmountPaid,
		"balance":     balance,
	})
}

// GetPaymentHistory retrieves authorized payment history and financial summary for an order
func (h *Handler) GetPaymentHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok || authCtx.UserID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	userID := authCtx.UserID
	shopName := authCtx.ShopName

	params := mux.Vars(r)
	measurementID, err := primitive.ObjectIDFromHex(params["id"])
	if err != nil {
		http.Error(w, "Invalid measurement ID", http.StatusBadRequest)
		return
	}

	m, err := h.measurementRepo.FindByID(ctx, measurementID, userID, shopName)
	if err != nil || m == nil {
		http.Error(w, "Measurement not found or unauthorized", http.StatusNotFound)
		return
	}

	balance := m.TotalCost - m.AmountPaid
	if balance < 0 {
		balance = 0
	}
	paymentStatus := "unpaid"
	if m.AmountPaid >= m.TotalCost && m.TotalCost > 0 {
		paymentStatus = "fully_paid"
	} else if m.AmountPaid > 0 {
		paymentStatus = "partially_paid"
	}

	payments := m.Payments
	if payments == nil {
		payments = []models.PaymentRecord{}
	}

	summary := models.FinancialSummary{
		TotalCost:     m.TotalCost,
		AmountPaid:    m.AmountPaid,
		TotalPaid:     m.AmountPaid,
		Balance:       balance,
		PaymentStatus: paymentStatus,
		Payments:      payments,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

// GetAnalyticsSummary provides server-authoritative tenant-wide financial totals, job counts, and debtor rosters.
func (h *Handler) GetAnalyticsSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	authCtx, ok := middleware.GetAuthContext(r)
	if !ok || authCtx.UserID == "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	summary, err := h.measurementRepo.GetAnalyticsSummary(ctx, authCtx.UserID, authCtx.ShopName)
	if err != nil {
		http.Error(w, "Failed to calculate analytics summary: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(summary)
}

