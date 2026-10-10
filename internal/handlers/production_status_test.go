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

// TestProductionLifecycleSuite tests the entire production status workflow (TASK-03)
func TestProductionLifecycleSuite(t *testing.T) {
	router, _ := setupTestRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	usersColl := database.GetCollection("users")
	custColl := database.GetCollection("customers")
	measColl := database.GetCollection("measurements")

	// Setup Tailor A
	userA := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("tailor_a_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Kaftan King Lagos",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}
	// Setup Tailor B (for multi-tenant isolation testing)
	userB := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("tailor_b_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Abuja Royal Fits",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}

	_, _ = usersColl.InsertOne(ctx, userA)
	_, _ = usersColl.InsertOne(ctx, userB)

	defer func() {
		cleanupCtx, cCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cCancel()
		_, _ = usersColl.DeleteMany(cleanupCtx, bson.M{"_id": bson.M{"$in": []primitive.ObjectID{userA.ID, userB.ID}}})
		_, _ = custColl.DeleteMany(cleanupCtx, bson.M{"user_id": bson.M{"$in": []string{userA.ID.Hex(), userB.ID.Hex()}}})
		_, _ = measColl.DeleteMany(cleanupCtx, bson.M{"user_id": bson.M{"$in": []string{userA.ID.Hex(), userB.ID.Hex()}}})
	}()

	tokenA, _ := utils.GenerateToken(userA.ID.Hex(), userA.Email, userA.ShopName, userA.Role)
	tokenB, _ := utils.GenerateToken(userB.ID.Hex(), userB.Email, userB.ShopName, userB.Role)

	// Helper to send PATCH /api/measurements/{id}/status
	updateStatus := func(measID, newStatus, token string) (*httptest.ResponseRecorder, map[string]interface{}) {
		body, _ := json.Marshal(map[string]string{"status": newStatus})
		req := httptest.NewRequest("PATCH", fmt.Sprintf("/api/measurements/%s/status", measID), bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return w, res
	}

	// ─────────────────────────────────────────────────────────────────
	// TEST 01: New Order receives default status 'received'
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 01: New Order Defaults to Received", func(t *testing.T) {
		createPayload := map[string]interface{}{
			"customer_name":  "Chief Emeka Obi",
			"customer_phone": "+2348012345678",
			"garment":        "Royal Agbada 3-Piece",
			"gender":         "Male",
			"delivery_date":  time.Now().AddDate(0, 0, 7).Format("2006-01-02"),
			"data":           map[string]float64{"chest": 44.5, "waist": 38.0, "shoulder": 19.5},
			"total_cost":     75000.0,
			"amount_paid":    50000.0,
		}
		body, _ := json.Marshal(createPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected HTTP 201 Created, got %d: %s", w.Code, w.Body.String())
		}

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		measID, ok := res["id"].(string)
		if !ok || measID == "" {
			t.Fatalf("Expected id in response, got %+v", res)
		}

		// Verify in MongoDB
		objID, _ := primitive.ObjectIDFromHex(measID)
		var savedDoc models.Measurement
		err := measColl.FindOne(ctx, bson.M{"_id": objID}).Decode(&savedDoc)
		if err != nil {
			t.Fatalf("Failed to fetch created measurement from Mongo: %v", err)
		}

		if savedDoc.Status != "received" {
			t.Errorf("Expected default status 'received', got '%s'", savedDoc.Status)
		}
		if len(savedDoc.StatusHistory) == 0 {
			t.Errorf("Expected initial status history entry, got empty history")
		} else if savedDoc.StatusHistory[0].NewStatus != "received" {
			t.Errorf("Expected status history new_status 'received', got '%s'", savedDoc.StatusHistory[0].NewStatus)
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 02: Normal Progression: received -> cutting -> sewing -> fitting -> ready -> delivered
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 02: Normal Progression Across All 6 Stages", func(t *testing.T) {
		createPayload := map[string]interface{}{
			"customer_name":  "Alhaji Sani Bello",
			"customer_phone": "+2348099887766",
			"garment":        "Senator Suit",
			"gender":         "Male",
			"total_cost":     60000.0,
			"amount_paid":    30000.0,
		}
		body, _ := json.Marshal(createPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		measID := res["id"].(string)

		progression := []string{"cutting", "sewing", "fitting", "ready", "delivered"}
		for _, stage := range progression {
			rec, out := updateStatus(measID, stage, tokenA)
			if rec.Code != http.StatusOK {
				t.Fatalf("Expected 200 advancing to %s, got %d: %s", stage, rec.Code, rec.Body.String())
			}
			if out["status"] != stage {
				t.Fatalf("Expected status '%s', got '%v'", stage, out["status"])
			}
		}

		// Verify final document in MongoDB
		objID, _ := primitive.ObjectIDFromHex(measID)
		var finalDoc models.Measurement
		_ = measColl.FindOne(ctx, bson.M{"_id": objID}).Decode(&finalDoc)

		if finalDoc.Status != "delivered" {
			t.Errorf("Expected final status 'delivered', got '%s'", finalDoc.Status)
		}
		// Initial creation (received) + 5 updates = 6 history records
		if len(finalDoc.StatusHistory) < 6 {
			t.Errorf("Expected at least 6 history entries, got %d", len(finalDoc.StatusHistory))
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 03: Skip Fitting Progression: received -> cutting -> sewing -> ready -> delivered
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 03: Skip Optional Fitting Workflow", func(t *testing.T) {
		createPayload := map[string]interface{}{
			"customer_name":  "Tunde Bakare",
			"customer_phone": "+2348055443322",
			"garment":        "Danshiki Casual",
			"gender":         "Male",
		}
		body, _ := json.Marshal(createPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		measID := res["id"].(string)

		// 1. Move to cutting
		rec, _ := updateStatus(measID, "cutting", tokenA)
		if rec.Code != http.StatusOK {
			t.Fatalf("Failed cutting: %d", rec.Code)
		}
		// 2. Move to sewing
		rec, _ = updateStatus(measID, "sewing", tokenA)
		if rec.Code != http.StatusOK {
			t.Fatalf("Failed sewing: %d", rec.Code)
		}
		// 3. SKIP FITTING DIRECTLY TO READY
		rec, out := updateStatus(measID, "ready", tokenA)
		if rec.Code != http.StatusOK {
			t.Fatalf("Failed skip fitting to ready: %d", rec.Code)
		}
		if out["status"] != "ready" {
			t.Errorf("Expected status 'ready' after skipping fitting, got %v", out["status"])
		}
		// 4. Move to delivered
		rec, out = updateStatus(measID, "delivered", tokenA)
		if rec.Code != http.StatusOK {
			t.Fatalf("Failed delivered: %d", rec.Code)
		}
		if out["status"] != "delivered" {
			t.Errorf("Expected status 'delivered', got %v", out["status"])
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 04: Correction (backward transition: ready -> sewing)
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 04: Stage Correction Backwards", func(t *testing.T) {
		createPayload := map[string]interface{}{
			"customer_name": "Ngozi Eze",
			"garment":       "Peplum Top",
		}
		body, _ := json.Marshal(createPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		measID := res["id"].(string)

		// Set to ready
		updateStatus(measID, "ready", tokenA)

		// Mistake correction: garment needs alteration back to sewing!
		rec, out := updateStatus(measID, "sewing", tokenA)
		if rec.Code != http.StatusOK {
			t.Fatalf("Failed to step back to sewing: %d", rec.Code)
		}
		if out["status"] != "sewing" {
			t.Errorf("Expected status corrected to 'sewing', got %v", out["status"])
		}

		// Verify status history recorded previous was ready and new is sewing
		objID, _ := primitive.ObjectIDFromHex(measID)
		var doc models.Measurement
		_ = measColl.FindOne(ctx, bson.M{"_id": objID}).Decode(&doc)
		lastEntry := doc.StatusHistory[len(doc.StatusHistory)-1]
		if lastEntry.PreviousStatus != "ready" || lastEntry.NewStatus != "sewing" {
			t.Errorf("Expected history ready->sewing, got %s->%s", lastEntry.PreviousStatus, lastEntry.NewStatus)
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 05 & 06: Overdue Order remains in production status
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 06: Overdue Order Does Not Become Delivered", func(t *testing.T) {
		yesterday := time.Now().AddDate(0, 0, -2).Format("2006-01-02")
		createPayload := map[string]interface{}{
			"customer_name": "Femi Adebayo",
			"garment":       "Safari Jacket",
			"delivery_date": yesterday, // Past delivery deadline!
		}
		body, _ := json.Marshal(createPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		measID := res["id"].(string)

		// Move to sewing
		updateStatus(measID, "sewing", tokenA)

		// Fetch measurement list via API
		getReq := httptest.NewRequest("GET", "/api/measurements", nil)
		getReq.Header.Set("Authorization", "Bearer "+tokenA)
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)

		if getRec.Code != http.StatusOK {
			t.Fatalf("Failed to get measurements: %d", getRec.Code)
		}

		var respObj struct {
			Data  []map[string]interface{} `json:"data"`
			Total int64                    `json:"total"`
		}
		_ = json.Unmarshal(getRec.Body.Bytes(), &respObj)

		var targetMeas map[string]interface{}
		for _, m := range respObj.Data {
			if m["id"] == measID {
				targetMeas = m
				break
			}
		}

		if targetMeas == nil {
			t.Fatalf("Target measurement not found in GET response: %s", getRec.Body.String())
		}

		// Critical check: Even though delivery_date is in the past, status MUST be 'sewing', NOT 'delivered'!
		if targetMeas["status"] != "sewing" {
			t.Errorf("Overdue order falsely altered status to '%v' instead of 'sewing'!", targetMeas["status"])
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 07: Legacy Record Handling
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 07: Legacy Record Without Status", func(t *testing.T) {
		// Insert directly into MongoDB simulating a legacy record prior to TASK-03
		legacyDoc := bson.M{
			"_id":           primitive.NewObjectID(),
			"user_id":       userA.ID.Hex(),
			"customer_name": "Mama Funke Legacy",
			"garment":       "Vintage Iro & Buba",
			"delivery_date": "2023-01-01", // Long past deadline
			"total_cost":    25000.0,
			"created_at":    time.Now().AddDate(-1, 0, 0),
			// NOTE: No "status" field!
		}
		_, err := measColl.InsertOne(ctx, legacyDoc)
		if err != nil {
			t.Fatalf("Failed to insert legacy doc: %v", err)
		}
		legacyID := legacyDoc["_id"].(primitive.ObjectID).Hex()

		// Fetch via API
		getReq := httptest.NewRequest("GET", "/api/measurements", nil)
		getReq.Header.Set("Authorization", "Bearer "+tokenA)
		getRec := httptest.NewRecorder()
		router.ServeHTTP(getRec, getReq)

		var legacyRespObj struct {
			Data  []map[string]interface{} `json:"data"`
			Total int64                    `json:"total"`
		}
		_ = json.Unmarshal(getRec.Body.Bytes(), &legacyRespObj)

		var fetchedLegacy map[string]interface{}
		for _, m := range legacyRespObj.Data {
			if m["id"] == legacyID {
				fetchedLegacy = m
				break
			}
		}

		if fetchedLegacy == nil {
			t.Fatalf("Legacy measurement not returned by API: %s", getRec.Body.String())
		}

		// Must NOT be inferred as "delivered" or "fitting" by backend
		statusVal, _ := fetchedLegacy["status"].(string)
		if statusVal == "delivered" {
			t.Errorf("Backend falsely set legacy record status to 'delivered'!")
		}

		// Tailor can explicitly update legacy record to a valid stage
		updateRec, updateOut := updateStatus(legacyID, "ready", tokenA)
		if updateRec.Code != http.StatusOK {
			t.Fatalf("Failed to update legacy record status: %d: %s", updateRec.Code, updateRec.Body.String())
		}
		if updateOut["status"] != "ready" {
			t.Errorf("Expected legacy record updated to 'ready', got %v", updateOut["status"])
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 08: Unauthorized Status Update (Tenant Protection)
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 08: Tenant Authorization Guard", func(t *testing.T) {
		// Create record belonging to Tailor A
		createPayload := map[string]interface{}{
			"customer_name": "Senator Adeleke",
			"garment":       "Agbada Gold",
		}
		body, _ := json.Marshal(createPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		measID := res["id"].(string)

		// Tailor B attempts to modify Tailor A's measurement status
		rec, _ := updateStatus(measID, "delivered", tokenB)

		// Must be rejected with 403 Forbidden or 404 Not Found
		if rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Fatalf("Tenant B was able to modify Tenant A's status! Got HTTP %d (expected 403 or 404)", rec.Code)
		}

		// Verify Tailor A's record remained 'received'
		objID, _ := primitive.ObjectIDFromHex(measID)
		var checkDoc models.Measurement
		_ = measColl.FindOne(ctx, bson.M{"_id": objID}).Decode(&checkDoc)
		if checkDoc.Status != "received" {
			t.Errorf("Tailor A's record was compromised by Tailor B! Status is %s", checkDoc.Status)
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 09: Invalid Status is Rejected
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 09: Invalid Status Rejected", func(t *testing.T) {
		createPayload := map[string]interface{}{
			"customer_name": "Valid Client",
			"garment":       "Shirt",
		}
		body, _ := json.Marshal(createPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		measID := res["id"].(string)

		invalidStatuses := []string{"shipped", "cancelled", "unknown", "in_transit", "123", ""}
		for _, badStatus := range invalidStatuses {
			rec, _ := updateStatus(measID, badStatus, tokenA)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("Expected HTTP 400 for invalid status '%s', got %d: %s", badStatus, rec.Code, rec.Body.String())
			}
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 10: Multiple Jobs for Same Customer have Independent Statuses
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 10: Multiple Orders Have Independent Statuses", func(t *testing.T) {
		customerPhone := "+2348077777777"
		clientName := "Olori Naomi"

		// Order 1: Kaftan
		payload1 := map[string]interface{}{
			"customer_name":  clientName,
			"customer_phone": customerPhone,
			"garment":        "Silk Kaftan",
		}
		b1, _ := json.Marshal(payload1)
		req1 := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(b1))
		req1.Header.Set("Content-Type", "application/json")
		req1.Header.Set("Authorization", "Bearer "+tokenA)
		w1 := httptest.NewRecorder()
		router.ServeHTTP(w1, req1)
		var r1 map[string]interface{}
		_ = json.Unmarshal(w1.Body.Bytes(), &r1)
		id1 := r1["id"].(string)

		// Order 2: Evening Gown
		payload2 := map[string]interface{}{
			"customer_name":  clientName,
			"customer_phone": customerPhone,
			"garment":        "Evening Gown",
		}
		b2, _ := json.Marshal(payload2)
		req2 := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(b2))
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("Authorization", "Bearer "+tokenA)
		w2 := httptest.NewRecorder()
		router.ServeHTTP(w2, req2)
		var r2 map[string]interface{}
		_ = json.Unmarshal(w2.Body.Bytes(), &r2)
		id2 := r2["id"].(string)

		// Advance Order 1 to 'delivered'
		updateStatus(id1, "delivered", tokenA)

		// Advance Order 2 to 'cutting'
		updateStatus(id2, "cutting", tokenA)

		// Verify Order 1 is delivered
		obj1, _ := primitive.ObjectIDFromHex(id1)
		var doc1 models.Measurement
		_ = measColl.FindOne(ctx, bson.M{"_id": obj1}).Decode(&doc1)
		if doc1.Status != "delivered" {
			t.Errorf("Expected Order 1 status 'delivered', got '%s'", doc1.Status)
		}

		// Verify Order 2 is cutting
		obj2, _ := primitive.ObjectIDFromHex(id2)
		var doc2 models.Measurement
		_ = measColl.FindOne(ctx, bson.M{"_id": obj2}).Decode(&doc2)
		if doc2.Status != "cutting" {
			t.Errorf("Expected Order 2 status 'cutting', got '%s'", doc2.Status)
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 15: Payment Independence (Status does not alter finance/debtor)
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 15: Status and Payment Independence", func(t *testing.T) {
		createPayload := map[string]interface{}{
			"customer_name": "Prince Kunle",
			"garment":       "Brocade Agbada",
			"total_cost":    100000.0,
			"amount_paid":   40000.0, // Outstanding balance: 60,000
		}
		body, _ := json.Marshal(createPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		measID := res["id"].(string)

		// Advance to delivered (garment handed over before balance cleared)
		rec, _ := updateStatus(measID, "delivered", tokenA)
		if rec.Code != http.StatusOK {
			t.Fatalf("Failed to mark delivered: %d", rec.Code)
		}

		// Check MongoDB document finance fields
		objID, _ := primitive.ObjectIDFromHex(measID)
		var doc models.Measurement
		_ = measColl.FindOne(ctx, bson.M{"_id": objID}).Decode(&doc)

		if doc.Status != "delivered" {
			t.Errorf("Expected status 'delivered', got '%s'", doc.Status)
		}
		if doc.TotalCost != 100000.0 {
			t.Errorf("TotalCost was unexpectedly modified! Expected 100000, got %f", doc.TotalCost)
		}
		if doc.AmountPaid != 40000.0 {
			t.Errorf("AmountPaid was unexpectedly modified! Expected 40000, got %f", doc.AmountPaid)
		}
		// Debtor calculation remains 60,000 balance owed
		balance := doc.TotalCost - doc.AmountPaid
		if balance != 60000.0 {
			t.Errorf("Expected outstanding balance 60000, got %f", balance)
		}
	})
}
