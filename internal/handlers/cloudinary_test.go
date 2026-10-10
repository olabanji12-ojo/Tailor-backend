package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/emman/Tailor-Backend/internal/database"
	"github.com/emman/Tailor-Backend/internal/models"
	"github.com/emman/Tailor-Backend/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestCloudinaryUploadAuthSuite(t *testing.T) {
	router, _ := setupTestRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	usersColl := database.GetCollection("users")

	userA := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("cloudinary_tailor_a_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Lagos Couture House",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}
	userB := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("cloudinary_tailor_b_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Abuja Fashion Atelier",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}

	_, _ = usersColl.InsertOne(ctx, userA)
	_, _ = usersColl.InsertOne(ctx, userB)

	defer func() {
		_, _ = usersColl.DeleteOne(ctx, bson.M{"_id": userA.ID})
		_, _ = usersColl.DeleteOne(ctx, bson.M{"_id": userB.ID})
	}()

	tokenA, _ := utils.GenerateToken(userA.ID.Hex(), userA.Email, userA.ShopName, userA.Role)
	tokenB, _ := utils.GenerateToken(userB.ID.Hex(), userB.Email, userB.ShopName, userB.Role)

	t.Run("TEST_01: Authenticated Tailor Receives Authorized Config Scoped to Tenant", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/api/upload-auth", nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", rr.Code, rr.Body.String())
		}

		var resp map[string]interface{}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("Failed to parse JSON response: %v", err)
		}

		// Cloud Name and Upload Preset must match configured environment
		cloudName, ok := resp["cloud_name"].(string)
		if !ok || cloudName == "" {
			t.Errorf("Expected valid cloud_name string, got: %v", resp["cloud_name"])
		}
		preset, ok := resp["upload_preset"].(string)
		if !ok || preset == "" {
			t.Errorf("Expected valid upload_preset string, got: %v", resp["upload_preset"])
		}

		// Tenant folder must strictly scope to userA.ID
		expectedFolder := fmt.Sprintf("tailors/%s", userA.ID.Hex())
		if folder, ok := resp["folder"].(string); !ok || folder != expectedFolder {
			t.Errorf("Expected folder '%s', got '%s'", expectedFolder, folder)
		}

		// Must NOT leak API secret in auth response
		if _, hasSecret := resp["api_secret"]; hasSecret {
			t.Errorf("SECURITY BREACH: api_secret leaked in client response!")
		}
	})

	t.Run("TEST_02: Unauthenticated Request Is Denied", func(t *testing.T) {
		req, _ := http.NewRequest("GET", "/api/upload-auth", nil)
		// No Authorization header
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("Expected 401 Unauthorized for missing token, got %d", rr.Code)
		}
	})

	t.Run("TEST_03: Tenant Isolation of Upload Folders Between Tailor A and Tailor B", func(t *testing.T) {
		reqB, _ := http.NewRequest("GET", "/api/upload-auth", nil)
		reqB.Header.Set("Authorization", "Bearer "+tokenB)
		rrB := httptest.NewRecorder()
		router.ServeHTTP(rrB, reqB)

		if rrB.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK for Tailor B, got %d", rrB.Code)
		}

		var respB map[string]interface{}
		json.Unmarshal(rrB.Body.Bytes(), &respB)

		expectedFolderB := fmt.Sprintf("tailors/%s", userB.ID.Hex())
		if folderB, ok := respB["folder"].(string); !ok || folderB != expectedFolderB {
			t.Errorf("Expected Tailor B folder '%s', got '%s'", expectedFolderB, folderB)
		}
	})

	t.Run("TEST_04: Missing Configuration Returns Explicit Error Without Silent Fallback", func(t *testing.T) {
		// Save original env values
		origCloud := os.Getenv("CLOUDINARY_CLOUD_NAME")
		origCloudOld := os.Getenv("CloudName")
		origPreset := os.Getenv("CLOUDINARY_UPLOAD_PRESET")
		origPresetOld := os.Getenv("UNSIGNED_NAME")

		// Temporarily wipe env vars
		os.Unsetenv("CLOUDINARY_CLOUD_NAME")
		os.Unsetenv("CloudName")
		os.Unsetenv("CLOUDINARY_UPLOAD_PRESET")
		os.Unsetenv("UNSIGNED_NAME")

		defer func() {
			// Restore original env vars
			if origCloud != "" {
				os.Setenv("CLOUDINARY_CLOUD_NAME", origCloud)
			}
			if origCloudOld != "" {
				os.Setenv("CloudName", origCloudOld)
			}
			if origPreset != "" {
				os.Setenv("CLOUDINARY_UPLOAD_PRESET", origPreset)
			}
			if origPresetOld != "" {
				os.Setenv("UNSIGNED_NAME", origPresetOld)
			}
		}()

		req, _ := http.NewRequest("GET", "/api/upload-auth", nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		// Must return 503 Service Unavailable, NEVER 200 with hardcoded fallback!
		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("Expected 503 Service Unavailable when unconfigured, got %d: %s", rr.Code, rr.Body.String())
		}

		var errResp map[string]interface{}
		json.Unmarshal(rr.Body.Bytes(), &errResp)
		if errCode, ok := errResp["error"].(string); !ok || errCode != "cloudinary_unconfigured" {
			t.Errorf("Expected error code 'cloudinary_unconfigured', got: %v", errResp["error"])
		}
	})
}
