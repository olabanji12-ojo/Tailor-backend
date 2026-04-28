package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/emman/Tailor-Backend/internal/models"
	"github.com/emman/Tailor-Backend/internal/repository"
	"github.com/gorilla/mux"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type Handler struct {
	customerRepo    *repository.CustomerRepository
	measurementRepo *repository.MeasurementRepository
}

func NewHandler(cRepo *repository.CustomerRepository, mRepo *repository.MeasurementRepository) *Handler {
	return &Handler{
		customerRepo:    cRepo,
		measurementRepo: mRepo,
	}
}

func (h *Handler) GetCustomers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	customers, err := h.customerRepo.GetAll(ctx)
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

	measurements, err := h.measurementRepo.GetAll(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// For MVP, we'll manually join customer names
	// In production, use MongoDB $lookup
	var response []models.MeasurementResponse
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

		response = append(response, models.MeasurementResponse{
			ID:           m.ID,
			CustomerID:   m.CustomerID,
			CustomerName: name,
			Date:         m.Date,
			Data:         m.Data,
			Transcript:   m.Transcript,
			Unit:         m.Unit,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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

	// 1. Find or Create Customer
	customer, err := h.customerRepo.FindByName(ctx, req.CustomerName)
	if err != nil {
		if err == mongo.ErrNoDocuments {
			// Create new customer
			customer = &models.Customer{
				Name: req.CustomerName,
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
		CustomerID: customer.ID,
		Data:       req.Data,
		Transcript: req.Transcript,
		Unit:       req.Unit,
	}

	if err := h.measurementRepo.Save(ctx, measurement); err != nil {
		http.Error(w, "Failed to save measurement", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(measurement)
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
