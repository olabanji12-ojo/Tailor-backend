package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/emman/Tailor-Backend/internal/handlers"
	"github.com/emman/Tailor-Backend/internal/routes"
	"github.com/emman/Tailor-Backend/internal/utils"
	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
)

func init() {
	paths := []string{".env", "../.env", "../../.env"}
	for _, p := range paths {
		if err := godotenv.Load(p); err == nil {
			break
		}
	}
}

func TestUploadAuth_UnauthorizedWithoutToken(t *testing.T) {
	h := handlers.NewHandler(nil, nil, nil)
	r := mux.NewRouter()
	routes.RegisterRoutes(r, h)

	req := httptest.NewRequest("GET", "/api/upload-auth", nil)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for missing token, got %d", w.Code)
	}
}

func TestUploadAuth_SuccessWithTenantFolder(t *testing.T) {
	h := handlers.NewHandler(nil, nil, nil)
	r := mux.NewRouter()
	routes.RegisterRoutes(r, h)

	userAID := "651a2b3c4d5e6f7a8b9c0d1e"
	tokenA, err := utils.GenerateToken(userAID, "tailor_a@atelier.com", "Lagos Threads", "tailor_free")
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/upload-auth", nil)
	req.Header.Set("Authorization", "Bearer "+tokenA)
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d. Body: %s", w.Code, w.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	expectedCloud := os.Getenv("CLOUDINARY_CLOUD_NAME")
	if expectedCloud == "" {
		expectedCloud = os.Getenv("CloudName")
	}
	expectedPreset := os.Getenv("CLOUDINARY_UPLOAD_PRESET")
	if expectedPreset == "" {
		expectedPreset = os.Getenv("UNSIGNED_NAME")
	}

	if resp["cloud_name"] != expectedCloud {
		t.Errorf("expected cloud_name '%s', got '%v'", expectedCloud, resp["cloud_name"])
	}

	if resp["upload_preset"] != expectedPreset {
		t.Errorf("expected upload_preset '%s', got '%v'", expectedPreset, resp["upload_preset"])
	}

	expectedFolder := "tailors/" + userAID
	if resp["folder"] != expectedFolder {
		t.Errorf("expected folder '%s', got '%v'", expectedFolder, resp["folder"])
	}

	if resp["user_id"] != userAID {
		t.Errorf("expected user_id '%s', got '%v'", userAID, resp["user_id"])
	}
}

func TestUploadAuth_TenantIsolationBetweenUsers(t *testing.T) {
	h := handlers.NewHandler(nil, nil, nil)
	r := mux.NewRouter()
	routes.RegisterRoutes(r, h)

	userAID := "651a2b3c4d5e6f7a8b9c0d1e"
	userBID := "651a2b3c4d5e6f7a8b9c0d2f"

	tokenA, _ := utils.GenerateToken(userAID, "tailor_a@atelier.com", "Lagos Threads", "tailor_free")
	tokenB, _ := utils.GenerateToken(userBID, "tailor_b@atelier.com", "Kano Couture", "tailor_free")

	// User A
	reqA := httptest.NewRequest("GET", "/api/upload-auth", nil)
	reqA.Header.Set("Authorization", "Bearer "+tokenA)
	wA := httptest.NewRecorder()
	r.ServeHTTP(wA, reqA)

	if wA.Code != http.StatusOK {
		t.Fatalf("User A request failed with %d", wA.Code)
	}

	var respA map[string]interface{}
	json.Unmarshal(wA.Body.Bytes(), &respA)

	// User B
	reqB := httptest.NewRequest("GET", "/api/upload-auth", nil)
	reqB.Header.Set("Authorization", "Bearer "+tokenB)
	wB := httptest.NewRecorder()
	r.ServeHTTP(wB, reqB)

	if wB.Code != http.StatusOK {
		t.Fatalf("User B request failed with %d", wB.Code)
	}

	var respB map[string]interface{}
	json.Unmarshal(wB.Body.Bytes(), &respB)

	// Verify Tenant Isolation
	if respA["folder"] == respB["folder"] {
		t.Errorf("critical failure: User A and User B received identical upload folder %v", respA["folder"])
	}

	if respA["folder"] != "tailors/"+userAID {
		t.Errorf("User A folder mismatch: expected 'tailors/%s', got '%v'", userAID, respA["folder"])
	}

	if respB["folder"] != "tailors/"+userBID {
		t.Errorf("User B folder mismatch: expected 'tailors/%s', got '%v'", userBID, respB["folder"])
	}
}
