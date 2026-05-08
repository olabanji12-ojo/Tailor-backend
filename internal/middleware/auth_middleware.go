package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/emman/Tailor-Backend/internal/utils"
)

type AuthContext struct {
	UserID   string
	Email    string
	ShopName string
}

type contextKey string

const authKey contextKey = "auth"

func GetAuthContext(r *http.Request) (*AuthContext, bool) {
	authValue := r.Context().Value(authKey)
	if authValue == nil {
		return nil, false
	}
	authCtx, ok := authValue.(AuthContext)
	return &authCtx, ok
}

func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			http.Error(w, "Unauthorized: No token provided", http.StatusUnauthorized)
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		_, claims, err := utils.ValidateToken(tokenString)
		if err != nil {
			http.Error(w, "Unauthorized: Invalid token", http.StatusUnauthorized)
			return
		}

		userID, _ := claims["user_id"].(string)
		email, _ := claims["email"].(string)
		shopName, _ := claims["shop_name"].(string)

		authCtx := AuthContext{
			UserID:   userID,
			Email:    email,
			ShopName: shopName,
		}

		ctx := context.WithValue(r.Context(), authKey, authCtx)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
