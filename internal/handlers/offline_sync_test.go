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
	"github.com/emman/Tailor-Backend/internal/models"
	"github.com/emman/Tailor-Backend/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestOfflineSyncSuite(t *testing.T) {
	router, _ := setupTestRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	usersColl := database.GetCollection("users")
	measColl := database.GetCollection("measurements")
	custColl := database.GetCollection("customers")

	// Setup Tailor A
	userA := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("offline_tailor_a_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Kano Bespoke Studio",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}
	// Setup Tailor B
	userB := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("offline_tailor_b_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Ibadan Stitches",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}

	_, _ = usersColl.InsertOne(ctx, userA)
	_, _ = usersColl.InsertOne(ctx, userB)

	defer func() {
		_, _ = usersColl.DeleteOne(ctx, bson.M{"_id": userA.ID})
		_, _ = usersColl.DeleteOne(ctx, bson.M{"_id": userB.ID})
		_, _ = measColl.DeleteMany(ctx, bson.M{"user_id": userA.ID.Hex()})
		_, _ = measColl.DeleteMany(ctx, bson.M{"user_id": userB.ID.Hex()})
		_, _ = custColl.DeleteMany(ctx, bson.M{"user_id": userA.ID.Hex()})
		_, _ = custColl.DeleteMany(ctx, bson.M{"user_id": userB.ID.Hex()})
	}()

	tokenA, _ := utils.GenerateToken(userA.ID.Hex(), userA.Email, userA.ShopName, userA.Role)
	tokenB, _ := utils.GenerateToken(userB.ID.Hex(), userB.Email, userB.ShopName, userB.Role)

	t.Run("SCENARIO_A: Newer 'measurements' Canonical Payload Successfully Persisted", func(t *testing.T) {
		clientOpID := fmt.Sprintf("offline_op_%d", time.Now().UnixNano())
		payload := map[string]interface{}{
			"customer_name":        "Alhaji Abubakar",
			"customer_phone":       "+2348031112233",
			"gender":               "male",
			"garment":              "Senator",
			"delivery_date":        time.Now().Add(7 * 24 * time.Hour).Format("2006-01-02"),
			"total_cost":           45000.0,
			"amount_paid":          25000.0,
			"measurements": map[string]interface{}{
				"Senator": map[string]interface{}{
					"chest":          42.5,
					"shoulder":       19.0,
					"trouser_length": 41.0,
				},
			},
			"client_operation_id": clientOpID,
		}

		bodyBytes, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
			t.Fatalf("Expected 201/200, got %d: %s", rr.Code, rr.Body.String())
		}

		var created models.Measurement
		json.Unmarshal(rr.Body.Bytes(), &created)
		if created.Data == nil || len(created.Data) == 0 {
			t.Fatalf("Expected measurements to be stored in Data map, got empty: %+v", created)
		}
		senatorData, ok := created.Data["Senator"].(map[string]interface{})
		if !ok {
			t.Fatalf("Expected 'Senator' map in Data, got: %+v", created.Data)
		}
		if chest, ok := senatorData["chest"].(float64); !ok || chest != 42.5 {
			t.Errorf("Expected chest 42.5, got %v", senatorData["chest"])
		}
	})

	t.Run("SCENARIO_B: Nigerian Garment Fields (Agbada Wingspan, Lap, Round Sleeve, Cap Size)", func(t *testing.T) {
		clientOpID := fmt.Sprintf("offline_agbada_%d", time.Now().UnixNano())
		payload := map[string]interface{}{
			"customer_name":  "Chief Adeleke",
			"customer_phone": "+2348055556677",
			"gender":         "male",
			"garment":        "Agbada",
			"delivery_date":  time.Now().Add(14 * 24 * time.Hour).Format("2006-01-02"),
			"total_cost":     120000.0,
			"amount_paid":    60000.0,
			"measurements": map[string]interface{}{
				"Agbada": map[string]interface{}{
					"agbada_wingspan": 62.0,
					"lap":             28.0,
					"round_sleeve":    16.0,
					"cap_size":        22.5,
					"buba_length":     36.0,
				},
			},
			"client_operation_id": clientOpID,
		}

		bodyBytes, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated {
			t.Fatalf("Expected 201 Created, got %d: %s", rr.Code, rr.Body.String())
		}

		var created models.Measurement
		json.Unmarshal(rr.Body.Bytes(), &created)
		agbadaMap := created.Data["Agbada"].(map[string]interface{})

		if wingspan := agbadaMap["agbada_wingspan"].(float64); wingspan != 62.0 {
			t.Errorf("Expected wingspan 62.0, got %v", wingspan)
		}
		if lap := agbadaMap["lap"].(float64); lap != 28.0 {
			t.Errorf("Expected lap 28.0, got %v", lap)
		}
		if sleeve := agbadaMap["round_sleeve"].(float64); sleeve != 16.0 {
			t.Errorf("Expected round_sleeve 16.0, got %v", sleeve)
		}
		if capSize := agbadaMap["cap_size"].(float64); capSize != 22.5 {
			t.Errorf("Expected cap_size 22.5, got %v", capSize)
		}
	})

	t.Run("SCENARIO_C: Legacy 'data' Payload Compatibility Survives Synchronization", func(t *testing.T) {
		clientOpID := fmt.Sprintf("offline_legacy_%d", time.Now().UnixNano())
		// Only sending legacy "data", no "measurements" key
		payload := map[string]interface{}{
			"customer_name":  "Malam Bello",
			"customer_phone": "+2348099887766",
			"gender":         "male",
			"garment":        "Kaftan",
			"total_cost":     35000.0,
			"amount_paid":    15000.0,
			"data": map[string]interface{}{
				"Kaftan": map[string]interface{}{
					"length": 55.0,
					"chest":  40.0,
				},
			},
			"client_operation_id": clientOpID,
		}

		bodyBytes, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated {
			t.Fatalf("Expected 201 Created for legacy data payload, got %d: %s", rr.Code, rr.Body.String())
		}

		var created models.Measurement
		json.Unmarshal(rr.Body.Bytes(), &created)
		kaftanMap := created.Data["Kaftan"].(map[string]interface{})
		if lenVal := kaftanMap["length"].(float64); lenVal != 55.0 {
			t.Errorf("Expected Kaftan length 55.0, got %v", lenVal)
		}
	})

	t.Run("SCENARIO_D: Duplicate Protection & Idempotent Retry", func(t *testing.T) {
		clientOpID := fmt.Sprintf("offline_retry_%d", time.Now().UnixNano())
		payload := map[string]interface{}{
			"customer_name":       "Ibrahim Danjuma",
			"total_cost":          50000.0,
			"amount_paid":         50000.0,
			"measurements": map[string]interface{}{
				"Buba & Sokoto": map[string]interface{}{
					"buba_length": 34.0,
				},
			},
			"client_operation_id": clientOpID,
		}

		bodyBytes, _ := json.Marshal(payload)

		// First Sync Attempt
		req1, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req1.Header.Set("Authorization", "Bearer "+tokenA)
		req1.Header.Set("Content-Type", "application/json")
		rr1 := httptest.NewRecorder()
		router.ServeHTTP(rr1, req1)
		if rr1.Code != http.StatusCreated {
			t.Fatalf("Attempt 1 expected 201 Created, got %d", rr1.Code)
		}

		// Count records before retry
		countBefore, err := measColl.CountDocuments(ctx, bson.M{
			"user_id":             userA.ID.Hex(),
			"client_operation_id": clientOpID,
		})
		if err != nil || countBefore != 1 {
			t.Fatalf("Expected 1 document, got %d (err: %v)", countBefore, err)
		}

		// Second Sync Attempt (Network drop simulation / retry)
		req2, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req2.Header.Set("Authorization", "Bearer "+tokenA)
		req2.Header.Set("Content-Type", "application/json")
		rr2 := httptest.NewRecorder()
		router.ServeHTTP(rr2, req2)

		// Must return 200 OK (idempotent success)
		if rr2.Code != http.StatusOK {
			t.Fatalf("Attempt 2 expected 200 OK for idempotent retry, got %d: %s", rr2.Code, rr2.Body.String())
		}

		// Count records after retry — MUST STILL BE EXACTLY 1!
		countAfter, _ := measColl.CountDocuments(ctx, bson.M{
			"user_id":             userA.ID.Hex(),
			"client_operation_id": clientOpID,
		})
		if countAfter != 1 {
			t.Fatalf("Duplicate detected! Expected 1 document after retry, found %d", countAfter)
		}
	})

	t.Run("SCENARIO_E: Cross-Tenant Isolation with Offline Queued Sync", func(t *testing.T) {
		// When tailor B tries to sync, it is anchored to tailor B's user_id
		clientOpID := fmt.Sprintf("offline_tenant_b_%d", time.Now().UnixNano())
		payload := map[string]interface{}{
			"customer_name":       "Alhaji Abubakar", // Same customer name as tailor A
			"total_cost":          30000.0,
			"amount_paid":         10000.0,
			"client_operation_id": clientOpID,
		}

		bodyBytes, _ := json.Marshal(payload)
		req, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+tokenB)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated {
			t.Fatalf("Expected 201 Created for tailor B, got %d", rr.Code)
		}

		var createdB models.Measurement
		json.Unmarshal(rr.Body.Bytes(), &createdB)
		if createdB.UserID != userB.ID.Hex() {
			t.Fatalf("Tenant leak! Expected UserID %s, got %s", userB.ID.Hex(), createdB.UserID)
		}

		// Verify Tailor A cannot see Tailor B's measurement
		reqA, _ := http.NewRequest("GET", "/api/measurements/"+createdB.ID.Hex(), nil)
		reqA.Header.Set("Authorization", "Bearer "+tokenA)
		rrA := httptest.NewRecorder()
		router.ServeHTTP(rrA, reqA)
		if rrA.Code != http.StatusNotFound && rrA.Code != http.StatusForbidden {
			t.Fatalf("Security failure! Tailor A was able to read Tailor B's measurement: %d", rrA.Code)
		}
	})
}
