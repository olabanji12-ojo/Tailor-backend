package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/emman/Tailor-Backend/internal/database"
	"github.com/emman/Tailor-Backend/internal/handlers"
	"github.com/emman/Tailor-Backend/internal/repository"
	"github.com/emman/Tailor-Backend/internal/routes"
	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/rs/cors"
)

func main() {
	// Load .env file
	// Try to find .env in current, parent or grandparent directory
	paths := []string{".env", "../.env", "../../.env"}
	envLoaded := false
	for _, p := range paths {
		if err := godotenv.Load(p); err == nil {
			log.Printf("✅ Loaded environment from: %s", p)
			envLoaded = true
			break
		}
	}
	if !envLoaded {
		log.Println("⚠️ No .env file found, using default environment variables")
	}

	// Connect to Database
	database.ConnectDB()

	// Initialize Repositories
	customerRepo := repository.NewCustomerRepository()
	measurementRepo := repository.NewMeasurementRepository()
	userRepo := repository.NewUserRepository()

	// Initialize Handler
	h := handlers.NewHandler(customerRepo, measurementRepo, userRepo)

	// Initialize Router
	r := mux.NewRouter()

	// Register Routes
	routes.RegisterRoutes(r, h)

	// CORS Handling
	c := cors.New(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://localhost:5174", "https://tailor-measurment.vercel.app", "https://tailor-measurment.vercel.app/"}, // Added production frontend
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Shop-ID"},
		AllowCredentials: true,
	})

	handler := c.Handler(r)

	// Start Server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Printf("🚀 TailorVoice Backend running on http://localhost:%s\n", port)
	log.Fatal(http.ListenAndServe(":"+port, handler))
}
