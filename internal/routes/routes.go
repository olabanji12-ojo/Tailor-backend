package routes

import (
	"github.com/emman/Tailor-Backend/internal/handlers"
	"github.com/gorilla/mux"
)

func RegisterRoutes(r *mux.Router, h *handlers.Handler) {
	api := r.PathPrefix("/api").Subrouter()

	api.HandleFunc("/customers", h.GetCustomers).Methods("GET")
	api.HandleFunc("/measurements", h.GetMeasurements).Methods("GET")
	api.HandleFunc("/measurements", h.SaveMeasurement).Methods("POST")
	api.HandleFunc("/measurements/{id}", h.UpdateMeasurement).Methods("PUT")
	api.HandleFunc("/customers/{id}/measurements", h.GetCustomerHistory).Methods("GET")
	api.HandleFunc("/transcribe", h.Transcribe).Methods("POST")
}
