package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
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

// voiceQuotaSeconds is the monthly voice transcription limit per boutique (8 minutes)
const voiceQuotaSeconds int64 = 480

// nextMonthReset returns a human-friendly date string for when the quota resets
func nextMonthReset() string {
	now := time.Now()
	first := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())
	return first.Format("January 2, 2006")
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
	monthKey := fmt.Sprintf("voice:seconds:%s:%s", shopID, time.Now().Format("2006_01"))
	redisCtx := context.Background()

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

	// buildQuota is a helper to produce a consistent quota response object
	buildQuota := func(charged int64) map[string]interface{} {
		remaining := voiceQuotaSeconds - charged
		if remaining < 0 {
			remaining = 0
		}
		level := "none"
		if !isAdmin {
			ratio := float64(charged) / float64(voiceQuotaSeconds)
			switch {
			case charged >= voiceQuotaSeconds:
				level = "exceeded"
			case ratio >= 0.95:
				level = "urgent"
			case ratio >= 0.80:
				level = "warning"
			}
		}
		return map[string]interface{}{
			"used_seconds":      charged,
			"limit_seconds":     voiceQuotaSeconds,
			"remaining_seconds": remaining,
			"warning_level":     level,
			"resets_on":         nextMonthReset(),
			"is_admin":          isAdmin,
		}
	}

	// 3. Fail Closed: if Redis is unavailable and user is not admin, deny voice immediately.
	// Pattern: Fail Secure (Twilio/Banking) — when the enforcement mechanism is down,
	// fail to the safe state rather than letting unlimited voice through.
	if !isAdmin && database.RedisClient == nil {
		logVoiceEvent(shopID, header.Size, estimatedSeconds, 0, 0, 0, "unavailable")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error":   "voice_service_unavailable",
			"message": "Voice is temporarily unavailable. Please use the manual keypad.",
		})
		return
	}

	// 4. Read current usage, pre-flight check, then PRE-CHARGE (skip for admins)
	var usedSeconds int64 = 0
	var quotaBefore int64 = 0 // snapshot before pre-charge — used in audit log
	if !isAdmin {
		val, err := database.RedisClient.Get(redisCtx, monthKey).Int64()
		if err == nil {
			usedSeconds = val
		}
		quotaBefore = usedSeconds // lock in the pre-charge snapshot

		// Pre-flight: would this recording push the tailor over the limit?
		if usedSeconds+estimatedSeconds > voiceQuotaSeconds {
			logVoiceEvent(shopID, header.Size, estimatedSeconds, 0, quotaBefore, quotaBefore, "quota_exceeded")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"error": "voice_quota_exceeded",
				"quota": buildQuota(usedSeconds),
			})
			return
		}

		// PRE-CHARGE: deduct estimated seconds the moment audio arrives at the server.
		// This ensures Emmanuel is never absorbing Whisper costs — the tailor always pays.
		newUsed, _ := database.RedisClient.IncrBy(redisCtx, monthKey, estimatedSeconds).Result()
		database.RedisClient.Expire(redisCtx, monthKey, 45*24*time.Hour)
		usedSeconds = newUsed
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
	writer.WriteField("prompt", "The speaker is a professional tailor recording body measurements (e.g., Waist, Shoulder, Chest, Hip, Sleeve, Inseam). Focus on capturing names of body parts followed by numerical values. Ignore background music and noise.")
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
			"quota": buildQuota(usedSeconds),
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
			"quota": buildQuota(usedSeconds),
		})
		return
	}

	// 7. Bidirectional reconciliation (Authorize-Capture pattern, Stripe-style).
	// Actual duration always wins — delta can be negative (refund) or positive (top-up).
	actualSeconds := int64(math.Ceil(whisperResp.Duration))
	if actualSeconds < 1 {
		actualSeconds = 1
	}
	if !isAdmin && database.RedisClient != nil {
		delta := actualSeconds - estimatedSeconds // signed: negative = tailor was over-estimated
		if delta != 0 {
			reconciled, _ := database.RedisClient.IncrBy(redisCtx, monthKey, delta).Result()
			if reconciled < 0 {
				// Floor guard: Redis should never go below 0
				database.RedisClient.Set(redisCtx, monthKey, 0, 45*24*time.Hour)
				usedSeconds = 0
			} else {
				usedSeconds = reconciled
			}
		}
	}

	// 8. Return transcript + final quota metadata
	logVoiceEvent(shopID, header.Size, estimatedSeconds, actualSeconds, quotaBefore, usedSeconds, "success")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"text":  whisperResp.Text,
		"quota": buildQuota(usedSeconds),
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

	authCtx, _ := middleware.GetAuthContext(r)
	customers, err := h.customerRepo.GetAll(ctx, authCtx.ShopName)
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

	authCtx, _ := middleware.GetAuthContext(r)
	shopID := authCtx.ShopName

	pageStr := r.URL.Query().Get("page")
	limitStr := r.URL.Query().Get("limit")
	page, _ := strconv.ParseInt(pageStr, 10, 64)
	limit, _ := strconv.ParseInt(limitStr, 10, 64)

	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}
	offset := (page - 1) * limit

	// Redis Cache Check
	cacheKey := fmt.Sprintf("cache:measurements:shop:%s:page:%d:limit:%d", shopID, page, limit)
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

	measurements, total, err := h.measurementRepo.GetAll(ctx, shopID, limit, offset)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	var responseData []models.MeasurementResponse
	customerMap := make(map[primitive.ObjectID]string)

	for _, m := range measurements {
		name, ok := customerMap[m.CustomerID]
		if !ok {
			c, err := h.customerRepo.FindByID(ctx, m.CustomerID)
			if err == nil {
				name = c.Name
				customerMap[m.CustomerID] = name
			} else {
				name = "Unknown"
			}
		}

		responseData = append(responseData, models.MeasurementResponse{
			ID:           m.ID,
			CustomerID:   m.CustomerID,
			CustomerName: name,
			Date:         m.Date,
			Data:         m.Data,
			Transcript:   m.Transcript,
			Unit:         m.Unit,
			ShopID:       m.ShopID,
			StylePhotos:  m.StylePhotos,
			ClothPhotos:  m.ClothPhotos,
			Gender:       m.Gender,
			Garment:      m.Garment,
			DeliveryDate: m.DeliveryDate,
			ReminderDate: m.ReminderDate,
			TotalCost:    m.TotalCost,
			AmountPaid:   m.AmountPaid,
			DesignNotes:  m.DesignNotes,
			ClientPhoto:  m.ClientPhoto,
		})
	}

	respBytes, err := json.Marshal(map[string]interface{}{
		"data":  responseData,
		"total": total,
		"page":  page,
		"limit": limit,
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

	var req models.MeasurementRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.CustomerName == "" {
		http.Error(w, "Customer name is required", http.StatusBadRequest)
		return
	}

	authCtx, _ := middleware.GetAuthContext(r)
	shopID := authCtx.ShopName

	// 1. Find or Create Customer
	customer, err := h.customerRepo.FindByName(ctx, req.CustomerName, shopID)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			// Create new customer
			customer = &models.Customer{
				Name:   req.CustomerName,
				ShopID: shopID,
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

	// 2. Save Measurement
	measurement := &models.Measurement{
		CustomerID:   customer.ID,
		Date:         time.Now(),
		Data:         req.Data,
		Transcript:   req.Transcript,
		Unit:         req.Unit,
		ShopID:       req.ShopID,
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
	}

	if err := h.measurementRepo.Save(ctx, measurement); err != nil {
		http.Error(w, "Failed to save measurement", http.StatusInternalServerError)
		return
	}

	h.InvalidateMeasurementsCache(shopID)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(measurement)
}

func (h *Handler) UpdateMeasurement(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

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

	if err := h.measurementRepo.Update(ctx, id, updates); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	authCtx, _ := middleware.GetAuthContext(r)
	h.InvalidateMeasurementsCache(authCtx.ShopName)

	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) GetCustomerHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	params := mux.Vars(r)
	customerID, err := primitive.ObjectIDFromHex(params["id"])
	if err != nil {
		http.Error(w, "Invalid customer ID", http.StatusBadRequest)
		return
	}

	measurements, err := h.measurementRepo.GetByCustomerID(ctx, customerID)
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

	params := mux.Vars(r)
	id, err := primitive.ObjectIDFromHex(params["id"])
	if err != nil {
		http.Error(w, "Invalid ID", http.StatusBadRequest)
		return
	}

	if err := h.measurementRepo.Delete(ctx, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	authCtx, _ := middleware.GetAuthContext(r)
	h.InvalidateMeasurementsCache(authCtx.ShopName)

	w.WriteHeader(http.StatusNoContent)
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
				"content": "You are a professional tailor's transcription NLP translator. Extract body measurements mentioned in the text. Respond ONLY with a valid, clean JSON object mapping body parts to numbers. Numbers must be floats. If fractions like 'and a half' are mentioned, convert them (e.g. '24 and a half' -> 24.5). Do not include any explanation or markdown formatting, just raw JSON. If no measurements are found, return empty JSON {}.",
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

func (h *Handler) InvalidateMeasurementsCache(shopID string) {
	if database.RedisClient == nil {
		return
	}
	ctx := context.Background()
	pattern := fmt.Sprintf("cache:measurements:shop:%s:*", shopID)

	var cursor uint64
	for {
		keys, nextCursor, err := database.RedisClient.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			log.Printf("⚠️ Failed to scan Redis keys for cache invalidation: %v", err)
			return
		}

		if len(keys) > 0 {
			_, err = database.RedisClient.Del(ctx, keys...).Result()
			if err != nil {
				log.Printf("⚠️ Failed to delete Redis keys: %v", err)
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
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
