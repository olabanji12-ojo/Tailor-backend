package handlers

import (
	
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/emman/Tailor-Backend/internal/models"
	"github.com/emman/Tailor-Backend/internal/repository"
	"github.com/emman/Tailor-Backend/internal/middleware"
	"github.com/gorilla/mux"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"

)

func (h *Handler) Transcribe(w http.ResponseWriter, r *http.Request) {
	// 1. Get the file from the request
	err := r.ParseMultipartForm(10 << 20) // 10 MB max
	if err != nil {
		http.Error(w, "File too large", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("audio")
	if err != nil {
		http.Error(w, "Audio file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// 2. Prepare the request to OpenAI
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
	writer.Close()

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/audio/transcriptions", body)
	if err != nil {
		http.Error(w, "Failed to create OpenAI request", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	// 3. Execute the request
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		http.Error(w, "Failed to connect to OpenAI", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// 4. Proxy the response back to the client
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
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

	if page < 1 { page = 1 }
	if limit < 1 { limit = 20 }
	offset := (page - 1) * limit

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
			TotalCost:    m.TotalCost,
			AmountPaid:   m.AmountPaid,
			DesignNotes:  m.DesignNotes,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"data":  responseData,
		"total": total,
		"page":  page,
		"limit": limit,
	})
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
				Name: req.CustomerName,
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
		TotalCost:    req.TotalCost,
		AmountPaid:   req.AmountPaid,
		DesignNotes:  req.DesignNotes,
	}

	if err := h.measurementRepo.Save(ctx, measurement); err != nil {
		http.Error(w, "Failed to save measurement", http.StatusInternalServerError)
		return
	}

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

	w.WriteHeader(http.StatusNoContent)
}
