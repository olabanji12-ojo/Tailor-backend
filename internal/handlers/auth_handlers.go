package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/emman/Tailor-Backend/internal/models"
	"github.com/emman/Tailor-Backend/internal/utils"
)

func (h *Handler) Signup(w http.ResponseWriter, r *http.Request) {
	var req models.SignupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Check if user already exists
	existingUser, _ := h.userRepo.FindByEmail(ctx, req.Email)
	if existingUser != nil {
		http.Error(w, "User with this email already exists", http.StatusConflict)
		return
	}

	hashedPassword, err := utils.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "Error hashing password", http.StatusInternalServerError)
		return
	}

	// All regular signups default to "tailor_free" role — role is never exposed in the UI
	user := &models.User{
		Email:    req.Email,
		Password: hashedPassword,
		ShopName: req.ShopName,
		Role:     "tailor_free",
	}

	if err := h.userRepo.Create(ctx, user); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	token, err := utils.GenerateToken(user.ID.Hex(), user.Email, user.ShopName, user.Role)
	if err != nil {
		http.Error(w, "Error generating token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.AuthResponse{
		Token: token,
		User:  *user,
	})
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req models.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	user, err := h.userRepo.FindByEmail(ctx, req.Email)
	if err != nil {
		http.Error(w, "Invalid email or password", http.StatusUnauthorized)
		return
	}

	if !utils.CheckPasswordHash(req.Password, user.Password) {
		http.Error(w, "Invalid email or password", http.StatusUnauthorized)
		return
	}

	// Default legacy accounts that have no role yet
	if user.Role == "" {
		user.Role = "tailor_free"
	}

	token, err := utils.GenerateToken(user.ID.Hex(), user.Email, user.ShopName, user.Role)
	if err != nil {
		http.Error(w, "Error generating token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(models.AuthResponse{
		Token: token,
		User:  *user,
	})
}

// AdminCreateAccount is a secret endpoint protected by X-Admin-Secret header.
// It allows Emmanuel to create admin accounts without exposing role selection in the UI.
// Route: POST /api/admin/create-account (public route, no JWT required)
func (h *Handler) AdminCreateAccount(w http.ResponseWriter, r *http.Request) {
	// 1. Verify the secret key from the request header
	adminSecret := os.Getenv("ADMIN_SECRET_KEY")
	providedSecret := r.Header.Get("X-Admin-Secret")
	if adminSecret == "" || providedSecret != adminSecret {
		http.Error(w, "Forbidden: Invalid admin secret key", http.StatusForbidden)
		return
	}

	// 2. Parse the request body
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		ShopName string `json:"shop_name"`
		Role     string `json:"role"` // "admin", "tailor_free", "tailor_starter", "tailor_pro"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Email == "" || req.Password == "" {
		http.Error(w, "email, password, and shop_name are required", http.StatusBadRequest)
		return
	}

	// 3. Default role to "admin" if not specified
	if req.Role == "" {
		req.Role = "admin"
	}
	if req.ShopName == "" {
		req.ShopName = "TailorVoice HQ"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 4. Check for existing account
	existingUser, _ := h.userRepo.FindByEmail(ctx, req.Email)
	if existingUser != nil {
		http.Error(w, "User with this email already exists", http.StatusConflict)
		return
	}

	// 5. Hash password and create user
	hashedPassword, err := utils.HashPassword(req.Password)
	if err != nil {
		http.Error(w, "Error hashing password", http.StatusInternalServerError)
		return
	}

	user := &models.User{
		Email:    req.Email,
		Password: hashedPassword,
		ShopName: req.ShopName,
		Role:     req.Role,
	}

	if err := h.userRepo.Create(ctx, user); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 6. Generate and return token
	token, err := utils.GenerateToken(user.ID.Hex(), user.Email, user.ShopName, user.Role)
	if err != nil {
		http.Error(w, "Error generating token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"message": "Admin account created successfully",
		"token":   token,
		"user": map[string]string{
			"email":     user.Email,
			"shop_name": user.ShopName,
			"role":      user.Role,
		},
	})
}

func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Authenticated"))
}
