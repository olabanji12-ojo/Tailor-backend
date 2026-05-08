package routes

import (
	"fmt"
	"net/http"

	"github.com/emman/Tailor-Backend/internal/handlers"
	"github.com/emman/Tailor-Backend/internal/middleware"
	"github.com/gorilla/mux"
)

func RegisterRoutes(r *mux.Router, h *handlers.Handler) {
	api := r.PathPrefix("/api").Subrouter()
	
	// Home Route
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status": "active", "message": "TailorVoice API is running"}`)
	}).Methods("GET")

	// Public Auth Routes
	api.HandleFunc("/signup", h.Signup).Methods("POST")
	api.HandleFunc("/login", h.Login).Methods("POST")

	// Protected Routes (Require Token)
	protected := api.PathPrefix("").Subrouter()
	protected.Use(middleware.AuthMiddleware)

	protected.HandleFunc("/customers", h.GetCustomers).Methods("GET")
	protected.HandleFunc("/measurements", h.GetMeasurements).Methods("GET")
	protected.HandleFunc("/measurements", h.SaveMeasurement).Methods("POST")
	protected.HandleFunc("/measurements/{id}", h.UpdateMeasurement).Methods("PUT")
	protected.HandleFunc("/measurements/{id}", h.DeleteMeasurement).Methods("DELETE")
	protected.HandleFunc("/customers/{id}/measurements", h.GetCustomerHistory).Methods("GET")
	protected.HandleFunc("/transcribe", h.Transcribe).Methods("POST")
}
