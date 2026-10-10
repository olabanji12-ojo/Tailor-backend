package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/emman/Tailor-Backend/internal/database"
	"github.com/emman/Tailor-Backend/internal/models"
	"github.com/emman/Tailor-Backend/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// TestArchiveSearchSuite validates TASK-04 server-side search, pagination, and multi-tenant accuracy
func TestArchiveSearchSuite(t *testing.T) {
	router, _ := setupTestRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	usersColl := database.GetCollection("users")
	custColl := database.GetCollection("customers")
	measColl := database.GetCollection("measurements")

	// Setup Tailor A
	userA := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("search_tailor_a_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Elite Stitches Lagos",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}
	// Setup Tailor B (for tenant isolation verification)
	userB := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("search_tailor_b_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Elite Stitches Abuja",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}

	_, _ = usersColl.InsertOne(ctx, userA)
	_, _ = usersColl.InsertOne(ctx, userB)

	defer func() {
		cleanupCtx, cCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cCancel()
		_, _ = usersColl.DeleteMany(cleanupCtx, bson.M{"_id": bson.M{"$in": []primitive.ObjectID{userA.ID, userB.ID}}})
		_, _ = custColl.DeleteMany(cleanupCtx, bson.M{"user_id": bson.M{"$in": []string{userA.ID.Hex(), userB.ID.Hex()}}})
		_, _ = measColl.DeleteMany(cleanupCtx, bson.M{"user_id": bson.M{"$in": []string{userA.ID.Hex(), userB.ID.Hex()}}})
	}()

	tokenA, _ := utils.GenerateToken(userA.ID.Hex(), userA.Email, userA.ShopName, userA.Role)
	tokenB, _ := utils.GenerateToken(userB.ID.Hex(), userB.Email, userB.ShopName, userB.Role)

	// Helper to send GET /api/measurements with params
	getMeasurements := func(token string, q, status string, page, limit int) (*httptest.ResponseRecorder, map[string]interface{}) {
		queryVals := url.Values{}
		if q != "" {
			queryVals.Set("q", q)
		}
		if status != "" {
			queryVals.Set("status", status)
		}
		if page > 0 {
			queryVals.Set("page", fmt.Sprintf("%d", page))
		}
		if limit > 0 {
			queryVals.Set("limit", fmt.Sprintf("%d", limit))
		}

		reqURL := "/api/measurements"
		if len(queryVals) > 0 {
			reqURL += "?" + queryVals.Encode()
		}

		req := httptest.NewRequest("GET", reqURL, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		return w, res
	}

	// Helper to create an order via API
	createOrder := func(token string, custName, phone, garment, status string) string {
		payload := map[string]interface{}{
			"customer_name":  custName,
			"customer_phone": phone,
			"garment":        garment,
			"status":         status,
			"total_cost":     50000.0,
			"amount_paid":    25000.0,
		}
		b, _ := json.Marshal(payload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		id, _ := res["id"].(string)
		return id
	}

	// ─── SEED 25 TEST RECORDS FOR TAILOR A ───
	// Seed 25 records to rigorously test pagination beyond page 1 (page size 10)
	for i := 1; i <= 25; i++ {
		custName := fmt.Sprintf("Standard Client %02d", i)
		phone := fmt.Sprintf("+23480111111%02d", i)
		garment := "Kaftan"
		status := "received"

		// Record #2 is our uniquely identifiable target beyond page 1 (oldest batch -> page 3 of descending sort)
		if i == 2 {
			custName = "Chief Adeola Balogun"
			phone = "+2348039988776"
			garment = "Royal Brocade Agbada"
			status = "sewing"
		} else if i == 5 {
			custName = "Senator Adeleke"
			phone = "+2348023456789"
			garment = "Senator Suit"
			status = "ready"
		} else if i%3 == 0 {
			status = "cutting"
		} else if i%5 == 0 {
			status = "fitting"
		}

		createOrder(tokenA, custName, phone, garment, status)
	}

	// Tailor B also creates an order with SIMILAR name for tenant isolation tests
	createOrder(tokenB, "Chief Adeola Balogun", "+2348039988776", "Counterfeit Agbada", "delivered")

	// ─────────────────────────────────────────────────────────────────
	// TEST 01: Search Beyond Page One
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 01: Search Beyond Page One Finds Distant Target", func(t *testing.T) {
		// When requesting page 1 without query (limit 10), Chief Adeola (#22) is NOT in the first 10
		w1, res1 := getMeasurements(tokenA, "", "", 1, 10)
		if w1.Code != http.StatusOK {
			t.Fatalf("Failed to fetch page 1: %d", w1.Code)
		}
		data1 := res1["data"].([]interface{})
		for _, item := range data1 {
			m := item.(map[string]interface{})
			if m["customer_name"] == "Chief Adeola Balogun" {
				t.Fatalf("Target #22 should not be on page 1 of default sort")
			}
		}

		// When searching for "Adeola", backend queries all 25 records and returns it immediately on page 1 of results!
		wSearch, resSearch := getMeasurements(tokenA, "Adeola", "", 1, 10)
		if wSearch.Code != http.StatusOK {
			t.Fatalf("Search failed: %d", wSearch.Code)
		}
		dataSearch := resSearch["data"].([]interface{})
		if len(dataSearch) != 1 {
			t.Fatalf("Expected exactly 1 result searching 'Adeola', got %d", len(dataSearch))
		}
		found := dataSearch[0].(map[string]interface{})
		if found["customer_name"] != "Chief Adeola Balogun" {
			t.Errorf("Expected 'Chief Adeola Balogun', got '%v'", found["customer_name"])
		}
		if found["garment"] != "Royal Brocade Agbada" {
			t.Errorf("Expected 'Royal Brocade Agbada', got '%v'", found["garment"])
		}
		// Total matching count should be 1
		if resSearch["total"].(float64) != 1 {
			t.Errorf("Expected total=1, got %v", resSearch["total"])
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 02 & 03: Customer Name Search (Full and Partial)
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 02 & 03: Full and Partial Name Search", func(t *testing.T) {
		// Partial search: "Adeleke"
		wPartial, resPartial := getMeasurements(tokenA, "adeleke", "", 1, 10)
		if wPartial.Code != http.StatusOK {
			t.Fatalf("Partial search failed: %d", wPartial.Code)
		}
		dataPartial := resPartial["data"].([]interface{})
		if len(dataPartial) != 1 {
			t.Fatalf("Expected 1 result for partial name 'adeleke', got %d", len(dataPartial))
		}
		if dataPartial[0].(map[string]interface{})["customer_name"] != "Senator Adeleke" {
			t.Errorf("Expected 'Senator Adeleke', got '%v'", dataPartial[0].(map[string]interface{})["customer_name"])
		}

		// Partial search matching multiple clients: "Standard Client"
		wMulti, resMulti := getMeasurements(tokenA, "Standard", "", 1, 10)
		if wMulti.Code != http.StatusOK {
			t.Fatalf("Multi name search failed: %d", wMulti.Code)
		}
		// 23 standard clients out of 25
		if resMulti["total"].(float64) != 23 {
			t.Errorf("Expected total=23 matching 'Standard', got %v", resMulti["total"])
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 04: Phone Number Search
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 04: Phone Number Search", func(t *testing.T) {
		// Search with local Nigerian phone number format without country code: "08039988776"
		// Stored in database as "+2348039988776"
		wPhone, resPhone := getMeasurements(tokenA, "08039988776", "", 1, 10)
		if wPhone.Code != http.StatusOK {
			t.Fatalf("Phone search failed: %d", wPhone.Code)
		}
		dataPhone := resPhone["data"].([]interface{})
		if len(dataPhone) != 1 {
			t.Fatalf("Expected 1 result searching phone '08039988776', got %d", len(dataPhone))
		}
		if dataPhone[0].(map[string]interface{})["customer_name"] != "Chief Adeola Balogun" {
			t.Errorf("Expected 'Chief Adeola Balogun', got '%v'", dataPhone[0].(map[string]interface{})["customer_name"])
		}

		// Also search with full international format: "+2348039988776"
		wFullPhone, resFullPhone := getMeasurements(tokenA, "+2348039988776", "", 1, 10)
		if wFullPhone.Code != http.StatusOK {
			t.Fatalf("Full phone search failed: %d", wFullPhone.Code)
		}
		dataFullPhone := resFullPhone["data"].([]interface{})
		if len(dataFullPhone) != 1 {
			t.Fatalf("Expected 1 result searching phone '+2348039988776', got %d", len(dataFullPhone))
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 05: Garment Search
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 05: Garment / Outfit Type Search", func(t *testing.T) {
		// Search by garment: "Agbada"
		wGarment, resGarment := getMeasurements(tokenA, "Agbada", "", 1, 10)
		if wGarment.Code != http.StatusOK {
			t.Fatalf("Garment search failed: %d", wGarment.Code)
		}
		dataGarment := resGarment["data"].([]interface{})
		if len(dataGarment) != 1 {
			t.Fatalf("Expected 1 result for 'Agbada', got %d", len(dataGarment))
		}
		if dataGarment[0].(map[string]interface{})["garment"] != "Royal Brocade Agbada" {
			t.Errorf("Expected 'Royal Brocade Agbada', got '%v'", dataGarment[0].(map[string]interface{})["garment"])
		}

		// Case-insensitive search: "senator suit"
		wSenator, resSenator := getMeasurements(tokenA, "senator suit", "", 1, 10)
		if wSenator.Code != http.StatusOK {
			t.Fatalf("Senator suit search failed: %d", wSenator.Code)
		}
		dataSenator := resSenator["data"].([]interface{})
		if len(dataSenator) != 1 {
			t.Fatalf("Expected 1 result for 'senator suit', got %d", len(dataSenator))
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 06: Production Status Filter Across Entire Database
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 06: Production Status Filter", func(t *testing.T) {
		// Filter by status "sewing" (Chief Adeola is in sewing)
		wSewing, resSewing := getMeasurements(tokenA, "", "sewing", 1, 10)
		if wSewing.Code != http.StatusOK {
			t.Fatalf("Status sewing query failed: %d", wSewing.Code)
		}
		dataSewing := resSewing["data"].([]interface{})
		if len(dataSewing) != 1 {
			t.Fatalf("Expected 1 record with status 'sewing', got %d", len(dataSewing))
		}
		if dataSewing[0].(map[string]interface{})["customer_name"] != "Chief Adeola Balogun" {
			t.Errorf("Expected 'Chief Adeola Balogun', got '%v'", dataSewing[0].(map[string]interface{})["customer_name"])
		}
		if dataSewing[0].(map[string]interface{})["status"] != "sewing" {
			t.Errorf("Expected status 'sewing', got '%v'", dataSewing[0].(map[string]interface{})["status"])
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 07: Combined Search and Status Filter
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 07: Combined Search and Status Filter", func(t *testing.T) {
		// Combined: search "Adeleke" + status "ready" -> should MATCH
		wMatch, resMatch := getMeasurements(tokenA, "Adeleke", "ready", 1, 10)
		if wMatch.Code != http.StatusOK {
			t.Fatalf("Combined search match failed: %d", wMatch.Code)
		}
		dataMatch := resMatch["data"].([]interface{})
		if len(dataMatch) != 1 {
			t.Fatalf("Expected 1 match, got %d", len(dataMatch))
		}

		// Combined: search "Adeleke" + status "delivered" -> should NOT MATCH (status is ready)
		wNoMatch, resNoMatch := getMeasurements(tokenA, "Adeleke", "delivered", 1, 10)
		if wNoMatch.Code != http.StatusOK {
			t.Fatalf("Combined search query failed: %d", wNoMatch.Code)
		}
		dataNoMatch := resNoMatch["data"].([]interface{})
		if len(dataNoMatch) != 0 {
			t.Fatalf("Expected 0 results for mismatched status, got %d", len(dataNoMatch))
		}
		if resNoMatch["total"].(float64) != 0 {
			t.Errorf("Expected total=0, got %v", resNoMatch["total"])
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 08: Pagination Across Multiple Pages (Stable, No Duplicates)
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 08: Pagination Metadata and No Duplicates", func(t *testing.T) {
		// Fetch Page 1 (limit 10)
		wP1, resP1 := getMeasurements(tokenA, "", "", 1, 10)
		if wP1.Code != http.StatusOK {
			t.Fatalf("Page 1 failed: %d", wP1.Code)
		}
		p1Data := resP1["data"].([]interface{})
		if len(p1Data) != 10 {
			t.Fatalf("Expected 10 items on page 1, got %d", len(p1Data))
		}
		if resP1["total"].(float64) != 25 {
			t.Errorf("Expected total=25, got %v", resP1["total"])
		}
		if resP1["totalPages"].(float64) != 3 {
			t.Errorf("Expected totalPages=3, got %v", resP1["totalPages"])
		}
		if resP1["hasMore"].(bool) != true {
			t.Errorf("Expected hasMore=true on page 1")
		}

		// Fetch Page 2 (limit 10)
		wP2, resP2 := getMeasurements(tokenA, "", "", 2, 10)
		if wP2.Code != http.StatusOK {
			t.Fatalf("Page 2 failed: %d", wP2.Code)
		}
		p2Data := resP2["data"].([]interface{})
		if len(p2Data) != 10 {
			t.Fatalf("Expected 10 items on page 2, got %d", len(p2Data))
		}
		if resP2["hasMore"].(bool) != true {
			t.Errorf("Expected hasMore=true on page 2")
		}

		// Fetch Page 3 (limit 10)
		wP3, resP3 := getMeasurements(tokenA, "", "", 3, 10)
		if wP3.Code != http.StatusOK {
			t.Fatalf("Page 3 failed: %d", wP3.Code)
		}
		p3Data := resP3["data"].([]interface{})
		if len(p3Data) != 5 {
			t.Fatalf("Expected 5 items on page 3 (25 total), got %d", len(p3Data))
		}
		if resP3["hasMore"].(bool) != false {
			t.Errorf("Expected hasMore=false on page 3")
		}

		// Verify zero duplicate IDs across pages
		seenIDs := make(map[string]bool)
		for _, item := range p1Data {
			id := item.(map[string]interface{})["id"].(string)
			if seenIDs[id] {
				t.Fatalf("Duplicate ID found on page 1: %s", id)
			}
			seenIDs[id] = true
		}
		for _, item := range p2Data {
			id := item.(map[string]interface{})["id"].(string)
			if seenIDs[id] {
				t.Fatalf("Duplicate ID across page 1 and page 2: %s", id)
			}
			seenIDs[id] = true
		}
		for _, item := range p3Data {
			id := item.(map[string]interface{})["id"].(string)
			if seenIDs[id] {
				t.Fatalf("Duplicate ID on page 3: %s", id)
			}
			seenIDs[id] = true
		}
		if len(seenIDs) != 25 {
			t.Errorf("Expected exactly 25 unique IDs collected across all pages, got %d", len(seenIDs))
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 09 & 10: Query and Filter Resets
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 09 & 10: Query and Filter Reset Returns Full Set", func(t *testing.T) {
		// Reset search query and filter to empty
		wReset, resReset := getMeasurements(tokenA, "", "", 1, 10)
		if wReset.Code != http.StatusOK {
			t.Fatalf("Reset query failed: %d", wReset.Code)
		}
		if resReset["total"].(float64) != 25 {
			t.Errorf("Expected reset total to be 25, got %v", resReset["total"])
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 11: Strict Multi-Tenant Isolation
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 11: Tenant Isolation In Search", func(t *testing.T) {
		// Tailor B has 1 order for "Chief Adeola Balogun"
		// Tailor A has 1 order for "Chief Adeola Balogun"
		// When Tailor A searches for "Adeola", they must NEVER see Tailor B's order ("Counterfeit Agbada")
		wA, resA := getMeasurements(tokenA, "Adeola", "", 1, 10)
		if wA.Code != http.StatusOK {
			t.Fatalf("Tailor A query failed: %d", wA.Code)
		}
		dataA := resA["data"].([]interface{})
		if len(dataA) != 1 {
			t.Fatalf("Tailor A expected 1 order, got %d", len(dataA))
		}
		if dataA[0].(map[string]interface{})["garment"] != "Royal Brocade Agbada" {
			t.Errorf("Tailor A saw unexpected garment: %v", dataA[0].(map[string]interface{})["garment"])
		}

		// When Tailor B searches for "Adeola", they only see their own order ("Counterfeit Agbada")
		wB, resB := getMeasurements(tokenB, "Adeola", "", 1, 10)
		if wB.Code != http.StatusOK {
			t.Fatalf("Tailor B query failed: %d", wB.Code)
		}
		dataB := resB["data"].([]interface{})
		if len(dataB) != 1 {
			t.Fatalf("Tailor B expected 1 order, got %d", len(dataB))
		}
		if dataB[0].(map[string]interface{})["garment"] != "Counterfeit Agbada" {
			t.Errorf("Tailor B saw unexpected garment: %v", dataB[0].(map[string]interface{})["garment"])
		}

		// Tailor B searching for "Senator Adeleke" must get 0 results
		wBNone, resBNone := getMeasurements(tokenB, "Senator Adeleke", "", 1, 10)
		if wBNone.Code != http.StatusOK {
			t.Fatalf("Tailor B query failed: %d", wBNone.Code)
		}
		if resBNone["total"].(float64) != 0 {
			t.Errorf("Tailor B was able to find Tailor A's client! Total: %v", resBNone["total"])
		}
		if len(resBNone["data"].([]interface{})) != 0 {
			t.Errorf("Tailor B received Tailor A's records!")
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 14: Empty Results
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 14: Nonexistent Query Returns Clean Empty List", func(t *testing.T) {
		wEmpty, resEmpty := getMeasurements(tokenA, "NonExistentClientXYZ12345", "", 1, 10)
		if wEmpty.Code != http.StatusOK {
			t.Fatalf("Empty search failed: %d", wEmpty.Code)
		}
		if resEmpty["total"].(float64) != 0 {
			t.Errorf("Expected total=0, got %v", resEmpty["total"])
		}
		data := resEmpty["data"].([]interface{})
		if len(data) != 0 {
			t.Errorf("Expected empty data array, got %d items", len(data))
		}
	})
}
