package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/emman/Tailor-Backend/internal/database"
	"github.com/emman/Tailor-Backend/internal/handlers"
	"github.com/emman/Tailor-Backend/internal/models"
	"github.com/emman/Tailor-Backend/internal/repository"
	"github.com/emman/Tailor-Backend/internal/routes"
	"github.com/emman/Tailor-Backend/internal/utils"
	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func setupTestRouter() (*mux.Router, *handlers.Handler) {
	paths := []string{".env", "../.env", "../../.env"}
	for _, p := range paths {
		if err := godotenv.Load(p); err == nil {
			break
		}
	}
	db := database.ConnectDB()
	if db == nil {
		panic("Failed to connect to MongoDB in test setup")
	}

	cRepo := repository.NewCustomerRepository()
	mRepo := repository.NewMeasurementRepository()
	uRepo := repository.NewUserRepository()
	h := handlers.NewHandler(cRepo, mRepo, uRepo)

	router := mux.NewRouter()
	routes.RegisterRoutes(router, h)
	return router, h
}

func TestMultiTenantDataIsolation(t *testing.T) {
	router, _ := setupTestRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	usersColl := database.GetCollection("users")
	custColl := database.GetCollection("customers")
	measColl := database.GetCollection("measurements")

	// ─── 1. SETUP 3 ISOLATED TEST USERS ───
	// Note: Tailor A and Tailor C deliberately share the EXACT same shop name ("Atelier Royal")
	// to verify that duplicate boutique names NEVER compromise tenant data isolation.
	userA := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("tenant_a_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Atelier Royal",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}
	userB := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("tenant_b_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Atelier Sovereign",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}
	userC := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("tenant_c_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Atelier Royal", // Same shop name as User A!
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}

	_, _ = usersColl.InsertOne(ctx, userA)
	_, _ = usersColl.InsertOne(ctx, userB)
	_, _ = usersColl.InsertOne(ctx, userC)

	// Clean up test data after test completion
	defer func() {
		cleanupCtx, cCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cCancel()
		_, _ = usersColl.DeleteMany(cleanupCtx, bson.M{"_id": bson.M{"$in": []primitive.ObjectID{userA.ID, userB.ID, userC.ID}}})
		_, _ = custColl.DeleteMany(cleanupCtx, bson.M{"user_id": bson.M{"$in": []string{userA.ID.Hex(), userB.ID.Hex(), userC.ID.Hex()}}})
		_, _ = measColl.DeleteMany(cleanupCtx, bson.M{"user_id": bson.M{"$in": []string{userA.ID.Hex(), userB.ID.Hex(), userC.ID.Hex()}}})
	}()

	tokenA, _ := utils.GenerateToken(userA.ID.Hex(), userA.Email, userA.ShopName, userA.Role)
	tokenB, _ := utils.GenerateToken(userB.ID.Hex(), userB.Email, userB.ShopName, userB.Role)
	tokenC, _ := utils.GenerateToken(userC.ID.Hex(), userC.Email, userC.ShopName, userC.Role)

	// ─── 2. SEED INITIAL TENANT RECORDS ───
	// Tailor A creates Customer A & Measurement A
	saveMeasPayloadA := map[string]interface{}{
		"customer_name":  "Alhaji Dangote A",
		"customer_phone": "+2348011111111",
		"garment":        "Agbada Royal",
		"total_cost":     150000.0,
		"amount_paid":    100000.0,
		"data":           map[string]interface{}{"chest": 44.0, "waist": 38.0},
	}
	bodyA, _ := json.Marshal(saveMeasPayloadA)
	reqA := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyA))
	reqA.Header.Set("Authorization", "Bearer "+tokenA)
	reqA.Header.Set("Content-Type", "application/json")
	recA := httptest.NewRecorder()
	router.ServeHTTP(recA, reqA)

	if recA.Code != http.StatusCreated {
		t.Fatalf("Failed to create Tailor A measurement: %d - %s", recA.Code, recA.Body.String())
	}
	var createdMeasA models.Measurement
	_ = json.Unmarshal(recA.Body.Bytes(), &createdMeasA)

	// Tailor B creates Customer B & Measurement B
	saveMeasPayloadB := map[string]interface{}{
		"customer_name":  "Senator Adeleke B",
		"customer_phone": "+2348022222222",
		"garment":        "Senator Sovereign",
		"total_cost":     90000.0,
		"amount_paid":    45000.0,
		"data":           map[string]interface{}{"chest": 40.0, "waist": 34.0},
	}
	bodyB, _ := json.Marshal(saveMeasPayloadB)
	reqB := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyB))
	reqB.Header.Set("Authorization", "Bearer "+tokenB)
	reqB.Header.Set("Content-Type", "application/json")
	recB := httptest.NewRecorder()
	router.ServeHTTP(recB, reqB)

	if recB.Code != http.StatusCreated {
		t.Fatalf("Failed to create Tailor B measurement: %d - %s", recB.Code, recB.Body.String())
	}
	var createdMeasB models.Measurement
	_ = json.Unmarshal(recB.Body.Bytes(), &createdMeasB)

	// Verify server assigned correct UserID ownership
	if createdMeasA.UserID != userA.ID.Hex() {
		t.Errorf("Expected Measurement A UserID to be %s, got %s", userA.ID.Hex(), createdMeasA.UserID)
	}
	if createdMeasB.UserID != userB.ID.Hex() {
		t.Errorf("Expected Measurement B UserID to be %s, got %s", userB.ID.Hex(), createdMeasB.UserID)
	}

	// ─── SCENARIO 1: AUTHORIZED READ ───
	t.Run("Scenario 1: Authorized Read - Tailor A retrieves own measurements", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/measurements", nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d", rec.Code)
		}
		var resp struct {
			Data []models.MeasurementResponse `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)

		foundA := false
		for _, m := range resp.Data {
			if m.ID == createdMeasA.ID {
				foundA = true
			}
			if m.ID == createdMeasB.ID {
				t.Errorf("CRITICAL SECURITY LEAK: Tailor A can see Tailor B's measurement ID %s!", m.ID.Hex())
			}
		}
		if !foundA {
			t.Errorf("Tailor A could not find their own measurement %s", createdMeasA.ID.Hex())
		}
	})

	// ─── SCENARIO 2: UNAUTHORIZED READ ISOLATION ───
	t.Run("Scenario 2: Unauthorized Read - Tailor B list does not expose Tailor A records", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/measurements", nil)
		req.Header.Set("Authorization", "Bearer "+tokenB)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d", rec.Code)
		}
		var resp struct {
			Data []models.MeasurementResponse `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)

		for _, m := range resp.Data {
			if m.ID == createdMeasA.ID {
				t.Fatalf("CRITICAL SECURITY LEAK: Tailor B received Tailor A's measurement %s!", m.ID.Hex())
			}
		}
	})

	// ─── SCENARIO 3: UNAUTHORIZED UPDATE ATTEMPT (IDOR) ───
	t.Run("Scenario 3: Unauthorized Update - Tailor A attempts to update Tailor B measurement", func(t *testing.T) {
		maliciousPayload := map[string]interface{}{
			"garment":    "Hacked Outfit",
			"total_cost": 1.0,
		}
		mBody, _ := json.Marshal(maliciousPayload)
		req := httptest.NewRequest("PUT", "/api/measurements/"+createdMeasB.ID.Hex(), bytes.NewBuffer(mBody))
		req.Header.Set("Authorization", "Bearer "+tokenA) // Tailor A's token!
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
			t.Fatalf("Expected 404/403 on unauthorized update attempt, got %d", rec.Code)
		}

		// Verify database record was NOT mutated
		var freshMeasB models.Measurement
		_ = measColl.FindOne(ctx, bson.M{"_id": createdMeasB.ID}).Decode(&freshMeasB)
		if freshMeasB.Garment == "Hacked Outfit" || freshMeasB.TotalCost == 1.0 {
			t.Fatalf("CRITICAL SECURITY VULNERABILITY: Tailor A successfully modified Tailor B's measurement!")
		}
	})

	// ─── SCENARIO 4: UNAUTHORIZED DELETE ATTEMPT ───
	t.Run("Scenario 4: Unauthorized Delete - Tailor A attempts to delete Tailor B measurement", func(t *testing.T) {
		req := httptest.NewRequest("DELETE", "/api/measurements/"+createdMeasB.ID.Hex(), nil)
		req.Header.Set("Authorization", "Bearer "+tokenA) // Tailor A's token!
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
			t.Fatalf("Expected 404/403 on unauthorized delete attempt, got %d", rec.Code)
		}

		// Verify document remains intact in MongoDB
		count, _ := measColl.CountDocuments(ctx, bson.M{"_id": createdMeasB.ID})
		if count != 1 {
			t.Fatalf("CRITICAL SECURITY VULNERABILITY: Tailor A successfully deleted Tailor B's measurement!")
		}
	})

	// ─── SCENARIO 5: UNAUTHORIZED CUSTOMER HISTORY ───
	t.Run("Scenario 5: Unauthorized Customer History - Tailor A attempts to view Tailor B customer history", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/customers/"+createdMeasB.CustomerID.Hex()+"/measurements", nil)
		req.Header.Set("Authorization", "Bearer "+tokenA) // Tailor A's token!
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
			t.Fatalf("Expected 404/403 for cross-tenant customer history, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	// ─── SCENARIO 6: TENANT SPOOFING IN BODY ───
	t.Run("Scenario 6: Tenant Spoofing - Tailor A submits Tailor B shop_id in body", func(t *testing.T) {
		spoofedPayload := map[string]interface{}{
			"customer_name": "Spoofed Client",
			"shop_id":       userB.ShopName, // Tailor A trying to forge Tailor B's shop
			"garment":       "Kaftan",
		}
		sBody, _ := json.Marshal(spoofedPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(sBody))
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("Expected 201 Created, got %d", rec.Code)
		}
		var created models.Measurement
		_ = json.Unmarshal(rec.Body.Bytes(), &created)

		// Verify backend overrode spoofed shop_id and assigned Tailor A's identity
		if created.UserID != userA.ID.Hex() {
			t.Errorf("Backend accepted spoofed identity! UserID is %s, expected %s", created.UserID, userA.ID.Hex())
		}
		if created.ShopID != userA.ShopName {
			t.Errorf("Backend accepted spoofed shop_id! ShopID is %s, expected %s", created.ShopID, userA.ShopName)
		}
	})

	// ─── SCENARIO 7: CROSS-TENANT CUSTOMER ID ASSOCIATION ───
	t.Run("Scenario 7: Cross-Tenant Association - Tailor A links measurement to Tailor B customer ID", func(t *testing.T) {
		crossPayload := map[string]interface{}{
			"customer_id":   createdMeasB.CustomerID.Hex(), // Tailor B's customer!
			"customer_name": "Senator Adeleke B",
			"garment":       "Illegal Association Garment",
		}
		cBody, _ := json.Marshal(crossPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(cBody))
		req.Header.Set("Authorization", "Bearer "+tokenA) // Tailor A's token!
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
			t.Fatalf("Expected 404/403 when linking to another tenant's customer, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	// ─── SCENARIO 8: SEARCH ISOLATION ───
	t.Run("Scenario 8: Search Isolation - Searching for Tailor B customer returns empty for Tailor A", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/customers?q=Adeleke", nil)
		req.Header.Set("Authorization", "Bearer "+tokenA) // Tailor A searching!
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d", rec.Code)
		}
		var customers []models.Customer
		_ = json.Unmarshal(rec.Body.Bytes(), &customers)

		if len(customers) != 0 {
			t.Fatalf("CRITICAL SEARCH LEAK: Tailor A found Tailor B's customer via search! Found: %v", customers)
		}
	})

	// ─── SCENARIO 9: AUTHORIZED MODIFICATION ───
	t.Run("Scenario 9: Authorized Modification - Tailor A successfully updates own measurement", func(t *testing.T) {
		updatePayload := map[string]interface{}{
			"garment":     "Agbada Royal Extended",
			"amount_paid": 120000.0,
		}
		uBody, _ := json.Marshal(updatePayload)
		req := httptest.NewRequest("PUT", "/api/measurements/"+createdMeasA.ID.Hex(), bytes.NewBuffer(uBody))
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("Expected 204 No Content on authorized update, got %d", rec.Code)
		}

		var freshMeasA models.Measurement
		_ = measColl.FindOne(ctx, bson.M{"_id": createdMeasA.ID}).Decode(&freshMeasA)
		if freshMeasA.Garment != "Agbada Royal Extended" || freshMeasA.AmountPaid != 120000.0 {
			t.Errorf("Authorized update did not apply correctly: %+v", freshMeasA)
		}
	})

	// ─── SCENARIO 10: MISSING / INVALID AUTHENTICATION ───
	t.Run("Scenario 10: Missing Authentication - Unauthenticated access is rejected", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/measurements", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("Expected 401 Unauthorized for unauthenticated request, got %d", rec.Code)
		}
	})

	// ─── SCENARIO 11: DUPLICATE SHOP NAMES ISOLATION (SPECIAL REQUIREMENT) ───
	t.Run("Scenario 11: Duplicate Shop Name Isolation - Tailor C with same shop name cannot see Tailor A data", func(t *testing.T) {
		// Tailor C has ShopName: "Atelier Royal", identical to Tailor A!
		req := httptest.NewRequest("GET", "/api/measurements", nil)
		req.Header.Set("Authorization", "Bearer "+tokenC) // Tailor C's token!
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d", rec.Code)
		}
		var resp struct {
			Data []models.MeasurementResponse `json:"data"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)

		for _, m := range resp.Data {
			if m.ID == createdMeasA.ID {
				t.Fatalf("CRITICAL SECURITY LEAK: Tailor C saw Tailor A's measurement because both shops share the name 'Atelier Royal'!")
			}
		}

		// Tailor C creates their own customer & measurement
		measPayloadC := map[string]interface{}{
			"customer_name": "Chief Okeke C",
			"garment":       "Senator C Outfit",
		}
		cBody, _ := json.Marshal(measPayloadC)
		reqCreateC := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(cBody))
		reqCreateC.Header.Set("Authorization", "Bearer "+tokenC)
		reqCreateC.Header.Set("Content-Type", "application/json")
		recCreateC := httptest.NewRecorder()
		router.ServeHTTP(recCreateC, reqCreateC)

		if recCreateC.Code != http.StatusCreated {
			t.Fatalf("Failed to create Tailor C measurement: %d", recCreateC.Code)
		}
		var createdMeasC models.Measurement
		_ = json.Unmarshal(recCreateC.Body.Bytes(), &createdMeasC)

		// Verify Tailor A cannot see Tailor C's record either
		reqA := httptest.NewRequest("GET", "/api/measurements", nil)
		reqA.Header.Set("Authorization", "Bearer "+tokenA)
		recA := httptest.NewRecorder()
		router.ServeHTTP(recA, reqA)

		var respA struct {
			Data []models.MeasurementResponse `json:"data"`
		}
		_ = json.Unmarshal(recA.Body.Bytes(), &respA)

		for _, m := range respA.Data {
			if m.ID == createdMeasC.ID {
				t.Fatalf("CRITICAL SECURITY LEAK: Tailor A saw Tailor C's measurement under shared shop name!")
			}
		}
	})
}
