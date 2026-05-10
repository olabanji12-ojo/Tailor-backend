package utils

import (
	
	"errors"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v4"
)

func getJWTSecret() []byte {
	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return []byte("tailor_secret_key_change_in_prod")
	}
	return []byte(secret)
}

var jwtSecret = getJWTSecret()

func GenerateToken(userID, email, shopName string) (string, error) {
	claims := jwt.MapClaims{
		"user_id":   userID,
		"email":     email,
		"shop_name": shopName,
		"exp":       time.Now().Add(7 * 24 * time.Hour).Unix(), // 7 days
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

func ValidateToken(tokenString string) (*jwt.Token, jwt.MapClaims, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return jwtSecret, nil
	})

	if err != nil {
		return nil, nil, err
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return nil, nil, errors.New("invalid token claims")
	}

	return token, claims, nil
}
