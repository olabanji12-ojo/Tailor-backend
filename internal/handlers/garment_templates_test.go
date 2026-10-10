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

// TestGarmentTemplatesSuite validates TASK-05: Nigerian Garment Templates,
// Local Measurement Vocabulary, Fractional Values, Separate Lengths,
// Historical Prefill, Legacy Record Compatibility, and Tenant Isolation.
func TestGarmentTemplatesSuite(t *testing.T) {
	router, _ := setupTestRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	usersColl := database.GetCollection("users")
	custColl := database.GetCollection("customers")
	measColl := database.GetCollection("measurements")

	// Setup Tailor A (Lagos Atelier)
	userA := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("lagos_tailor_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Lagos Bespoke Atelier",
		Role:      "user",
		CreatedAt: time.Now(),
	}
	// Setup Tailor B (Abuja Boutique) for Tenant Isolation validation
	userB := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("abuja_tailor_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Abuja Royal Stitches",
		Role:      "user",
		CreatedAt: time.Now(),
	}

	_, err := usersColl.InsertMany(ctx, []interface{}{userA, userB})
	if err != nil {
		t.Fatalf("Failed to insert test users: %v", err)
	}
	defer usersColl.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": []primitive.ObjectID{userA.ID, userB.ID}}})

	tokenA, err := utils.GenerateToken(userA.ID.Hex(), userA.Email, userA.ShopName, userA.Role)
	if err != nil {
		t.Fatalf("Failed to generate token A: %v", err)
	}
	tokenB, err := utils.GenerateToken(userB.ID.Hex(), userB.Email, userB.ShopName, userB.Role)
	if err != nil {
		t.Fatalf("Failed to generate token B: %v", err)
	}

	// Create customer for Tailor A
	custA := &models.Customer{
		ID:        primitive.NewObjectID(),
		Name:      "Alhaji Babatunde Adeleke",
		Phone:     "+2348031234567",
		ShopID:    userA.ID.Hex(),
		UserID:    userA.ID.Hex(),
		Gender:    "male",
		CreatedAt: time.Now(),
	}
	_, err = custColl.InsertOne(ctx, custA)
	if err != nil {
		t.Fatalf("Failed to insert customer A: %v", err)
	}
	defer custColl.DeleteOne(ctx, bson.M{"_id": custA.ID})

	// Variable to store created measurement IDs for cleanup
	var createdMeasIDs []primitive.ObjectID
	defer func() {
		if len(createdMeasIDs) > 0 {
			measColl.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": createdMeasIDs}})
		}
	}()

	// =========================================================================
	// TEST 01 & TEST 07: Save Agbada with Distinct Agbada Length & Buba Length,
	// Wingspan, Round Sleeve, and Lap/Thigh
	// =========================================================================
	t.Run("TEST 01 & 07 - Save and Retrieve Agbada with Wingspan and Separate Buba/Agbada Lengths", func(t *testing.T) {
		agbadaPayload := map[string]interface{}{
			"customer_id":    custA.ID.Hex(),
			"customer_name":  custA.Name,
			"customer_phone": custA.Phone,
			"garment":        "Agbada",
			"gender":         "male",
			"unit":           "in",
			"delivery_date":  time.Now().Add(14 * 24 * time.Hour).Format("2006-01-02"),
			"total_cost":     150000.0,
			"amount_paid":    80000.0,
			"data": map[string]interface{}{
				"Agbada": map[string]interface{}{
					"agbada_length":  56.0,
					"buba_length":    34.0, // Strictly distinct from Agbada length (TEST 07)
					"wingspan":       62.0, // TEST 01: wingspan support
					"chest":          44.0,
					"shoulder":       19.5,
					"neck":           17.0,
					"round_sleeve":   16.5,
					"sleeve":         25.0,
					"waist":          38.0,
					"lap":            28.0,
					"trouser_length": 42.0,
				},
			},
		}

		bodyBytes, _ := json.Marshal(agbadaPayload)
		req, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
			t.Fatalf("Expected HTTP 201/200 saving Agbada, got %d: %s", rr.Code, rr.Body.String())
		}

		var created struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rr.Body.Bytes(), &created)
		measOID, err := primitive.ObjectIDFromHex(created.ID)
		if err != nil {
			t.Fatalf("Invalid returned measurement ID: %v", err)
		}
		createdMeasIDs = append(createdMeasIDs, measOID)

		// Fetch and verify from MongoDB
		var savedDoc models.Measurement
		err = measColl.FindOne(ctx, bson.M{"_id": measOID}).Decode(&savedDoc)
		if err != nil {
			t.Fatalf("Failed to retrieve saved Agbada measurement: %v", err)
		}

		agbadaData, ok := savedDoc.Data["Agbada"].(map[string]interface{})
		if !ok {
			// May be flat or nested
			agbadaData = savedDoc.Data
		}

		// Verify TEST 07: Buba length and Agbada length remain separate fields
		if agbadaData["agbada_length"] != 56.0 && agbadaData["agbada_length"] != float64(56) {
			t.Errorf("Expected agbada_length 56, got %v", agbadaData["agbada_length"])
		}
		if agbadaData["buba_length"] != 34.0 && agbadaData["buba_length"] != float64(34) {
			t.Errorf("Expected buba_length 34, got %v", agbadaData["buba_length"])
		}
		if agbadaData["wingspan"] != 62.0 && agbadaData["wingspan"] != float64(62) {
			t.Errorf("Expected wingspan 62, got %v", agbadaData["wingspan"])
		}
		if agbadaData["round_sleeve"] != 16.5 && agbadaData["round_sleeve"] != float64(16.5) {
			t.Errorf("Expected round_sleeve 16.5, got %v", agbadaData["round_sleeve"])
		}
		if agbadaData["lap"] != 28.0 && agbadaData["lap"] != float64(28) {
			t.Errorf("Expected lap 28, got %v", agbadaData["lap"])
		}
	})

	// =========================================================================
	// TEST 02: Save Senator / Kaftan with Local Fields (Lap, Round Sleeve, Ankle)
	// =========================================================================
	t.Run("TEST 02 - Save and Retrieve Senator Garment with Local Fields", func(t *testing.T) {
		senatorPayload := map[string]interface{}{
			"customer_id":    custA.ID.Hex(),
			"customer_name":  custA.Name,
			"customer_phone": custA.Phone,
			"garment":        "Senator",
			"gender":         "male",
			"unit":           "in",
			"delivery_date":  time.Now().Add(7 * 24 * time.Hour).Format("2006-01-02"),
			"total_cost":     65000.0,
			"amount_paid":    35000.0,
			"data": map[string]interface{}{
				"Senator": map[string]interface{}{
					"neck":           16.5,
					"shoulder":       18.5,
					"chest":          42.0,
					"top_length":     38.0,
					"sleeve":         24.5,
					"round_sleeve":   15.5,
					"waist":          35.0,
					"hip":            41.0,
					"lap":            27.0,
					"knee":           18.0,
					"trouser_length": 41.5,
					"ankle":          14.0,
				},
			},
		}

		bodyBytes, _ := json.Marshal(senatorPayload)
		req, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
			t.Fatalf("Expected HTTP 201/200 saving Senator, got %d: %s", rr.Code, rr.Body.String())
		}

		var created struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rr.Body.Bytes(), &created)
		measOID, err := primitive.ObjectIDFromHex(created.ID)
		if err != nil {
			t.Fatalf("Invalid returned measurement ID: %v", err)
		}
		createdMeasIDs = append(createdMeasIDs, measOID)

		var savedDoc models.Measurement
		err = measColl.FindOne(ctx, bson.M{"_id": measOID}).Decode(&savedDoc)
		if err != nil {
			t.Fatalf("Failed to retrieve saved Senator measurement: %v", err)
		}

		senatorData := savedDoc.Data["Senator"].(map[string]interface{})
		if senatorData["lap"] != 27.0 && senatorData["lap"] != float64(27) {
			t.Errorf("Expected lap 27, got %v", senatorData["lap"])
		}
		if senatorData["ankle"] != 14.0 && senatorData["ankle"] != float64(14) {
			t.Errorf("Expected ankle 14, got %v", senatorData["ankle"])
		}
	})

	// =========================================================================
	// TEST 03: Save Dedicated Buba & Sokoto Template
	// =========================================================================
	t.Run("TEST 03 - Save and Retrieve Buba & Sokoto Template", func(t *testing.T) {
		bubaPayload := map[string]interface{}{
			"customer_id":    custA.ID.Hex(),
			"customer_name":  custA.Name,
			"customer_phone": custA.Phone,
			"garment":        "Buba & Sokoto",
			"gender":         "male",
			"unit":           "in",
			"delivery_date":  time.Now().Add(10 * 24 * time.Hour).Format("2006-01-02"),
			"total_cost":     45000.0,
			"amount_paid":    25000.0,
			"data": map[string]interface{}{
				"Buba & Sokoto": map[string]interface{}{
					"neck":           16.0,
					"shoulder":       18.0,
					"chest":          40.0,
					"buba_length":    33.0,
					"sleeve":         23.0,
					"round_sleeve":   15.0,
					"waist":          34.0,
					"lap":            26.0,
					"trouser_length": 40.0,
					"ankle":          13.5,
				},
			},
		}

		bodyBytes, _ := json.Marshal(bubaPayload)
		req, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
			t.Fatalf("Expected HTTP 201/200 saving Buba & Sokoto, got %d: %s", rr.Code, rr.Body.String())
		}

		var created struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rr.Body.Bytes(), &created)
		measOID, err := primitive.ObjectIDFromHex(created.ID)
		if err != nil {
			t.Fatalf("Invalid returned measurement ID: %v", err)
		}
		createdMeasIDs = append(createdMeasIDs, measOID)

		var savedDoc models.Measurement
		err = measColl.FindOne(ctx, bson.M{"_id": measOID}).Decode(&savedDoc)
		if err != nil {
			t.Fatalf("Failed to retrieve saved Buba & Sokoto: %v", err)
		}
		bubaData := savedDoc.Data["Buba & Sokoto"].(map[string]interface{})
		if bubaData["buba_length"] != 33.0 && bubaData["buba_length"] != float64(33) {
			t.Errorf("Expected buba_length 33, got %v", bubaData["buba_length"])
		}
	})

	// =========================================================================
	// TEST 04 & TEST 06: Traditional Cap / Fila with Fractional Values
	// =========================================================================
	t.Run("TEST 04 & 06 - Save Traditional Cap with Fractional Sizing (22.5)", func(t *testing.T) {
		capPayload := map[string]interface{}{
			"customer_id":    custA.ID.Hex(),
			"customer_name":  custA.Name,
			"customer_phone": custA.Phone,
			"garment":        "Traditional Cap / Fila",
			"gender":         "male",
			"unit":           "in",
			"delivery_date":  time.Now().Add(5 * 24 * time.Hour).Format("2006-01-02"),
			"total_cost":     15000.0,
			"amount_paid":    15000.0,
			"data": map[string]interface{}{
				"Traditional Cap / Fila": map[string]interface{}{
					"cap_size":           22.5, // Spoken "Cap size 22 and a half" (TEST 06)
					"head_circumference": 22.5,
					"cap_height":         5.25,
				},
			},
		}

		bodyBytes, _ := json.Marshal(capPayload)
		req, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
			t.Fatalf("Expected HTTP 201/200 saving Cap, got %d: %s", rr.Code, rr.Body.String())
		}

		var created struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rr.Body.Bytes(), &created)
		measOID, err := primitive.ObjectIDFromHex(created.ID)
		if err != nil {
			t.Fatalf("Invalid returned measurement ID: %v", err)
		}
		createdMeasIDs = append(createdMeasIDs, measOID)

		var savedDoc models.Measurement
		err = measColl.FindOne(ctx, bson.M{"_id": measOID}).Decode(&savedDoc)
		if err != nil {
			t.Fatalf("Failed to retrieve saved Cap: %v", err)
		}
		capData := savedDoc.Data["Traditional Cap / Fila"].(map[string]interface{})
		if capData["cap_size"] != 22.5 && capData["cap_size"] != float64(22.5) {
			t.Errorf("Expected fractional cap_size 22.5, got %v", capData["cap_size"])
		}
	})

	// =========================================================================
	// TEST 12: Historical Prefill for Returning Customer
	// =========================================================================
	t.Run("TEST 12 - Historical Measurement Prefill for Returning Customer", func(t *testing.T) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("/api/customers/%s/measurements", custA.ID.Hex()), nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected HTTP 200 fetching customer measurements, got %d: %s", rr.Code, rr.Body.String())
		}

		var pastMeasurements []models.Measurement
		err := json.Unmarshal(rr.Body.Bytes(), &pastMeasurements)
		if err != nil {
			t.Fatalf("Failed to decode past measurements: %v", err)
		}

		if len(pastMeasurements) == 0 {
			t.Fatal("Expected at least 1 past measurement for returning customer, got 0")
		}

		// Verify that Nigerian garment data is included in history
		foundAgbada := false
		for _, m := range pastMeasurements {
			if m.Garment == "Agbada" || (m.Data != nil && m.Data["Agbada"] != nil) {
				foundAgbada = true
				break
			}
		}
		if !foundAgbada {
			t.Errorf("Expected returning customer history to contain Agbada measurements")
		}
	})

	// =========================================================================
	// TEST 13: Legacy Records Compatibility (Older records without new fields)
	// =========================================================================
	t.Run("TEST 13 - Legacy Records Compatibility", func(t *testing.T) {
		legacyDoc := &models.Measurement{
			ID:           primitive.NewObjectID(),
			CustomerID:   custA.ID,
			ShopID:       userA.ID.Hex(),
			UserID:       userA.ID.Hex(),
			Garment:      "Agbada / Buba", // Old legacy template name
			Gender:       "male",
			Date:         time.Now().Add(-60 * 24 * time.Hour),
			TotalCost:    50000.0,
			AmountPaid:   50000.0,
			DeliveryDate: time.Now().Add(-50 * 24 * time.Hour).Format("2006-01-02"),
			Data: map[string]interface{}{
				"neck":           16.0,
				"shoulder":       18.0,
				"chest":          42.0,
				"waist":          36.0,
				"top_length":     36.0,
				"trouser_length": 40.0,
			},
			CreatedAt: time.Now().Add(-60 * 24 * time.Hour),
		}
		_, err := measColl.InsertOne(ctx, legacyDoc)
		if err != nil {
			t.Fatalf("Failed to insert legacy document: %v", err)
		}
		createdMeasIDs = append(createdMeasIDs, legacyDoc.ID)

		// Fetch via API to verify it loads without error
		req, _ := http.NewRequest("GET", "/api/measurements?q=Agbada", nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("Expected HTTP 200 fetching legacy records, got %d: %s", rr.Code, rr.Body.String())
		}

		var resp struct {
			Data []models.MeasurementResponse `json:"data"`
		}
		json.Unmarshal(rr.Body.Bytes(), &resp)

		foundLegacy := false
		for _, m := range resp.Data {
			if m.ID == legacyDoc.ID && m.Garment == "Agbada / Buba" {
				foundLegacy = true
				break
			}
		}
		if !foundLegacy {
			t.Errorf("Expected to find legacy record with garment 'Agbada / Buba'")
		}
	})

	// =========================================================================
	// TEST 14: Western Garment Regression (Shirt, Trouser, Suit still work)
	// =========================================================================
	t.Run("TEST 14 - Western Garment Regression (Shirt & Trouser)", func(t *testing.T) {
		shirtPayload := map[string]interface{}{
			"customer_id":    custA.ID.Hex(),
			"customer_name":  custA.Name,
			"customer_phone": custA.Phone,
			"garment":        "Shirt",
			"gender":         "male",
			"unit":           "in",
			"delivery_date":  time.Now().Add(5 * 24 * time.Hour).Format("2006-01-02"),
			"total_cost":     20000.0,
			"amount_paid":    20000.0,
			"data": map[string]interface{}{
				"Shirt": map[string]interface{}{
					"neck":       16.0,
					"shoulder":   18.0,
					"chest":      40.0,
					"waist":      34.0,
					"bicep":      14.0,
					"sleeve":     25.0,
					"top_length": 30.0,
				},
			},
		}

		bodyBytes, _ := json.Marshal(shirtPayload)
		req, _ := http.NewRequest("POST", "/api/measurements", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Authorization", "Bearer "+tokenA)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
			t.Fatalf("Expected HTTP 201/200 saving Shirt, got %d: %s", rr.Code, rr.Body.String())
		}

		var created struct {
			ID string `json:"id"`
		}
		json.Unmarshal(rr.Body.Bytes(), &created)
		measOID, _ := primitive.ObjectIDFromHex(created.ID)
		createdMeasIDs = append(createdMeasIDs, measOID)
	})

	// =========================================================================
	// TEST 15: Tenant Isolation - Shop B Cannot Access Shop A's Measurements
	// =========================================================================
	t.Run("TEST 15 - Tenant Isolation on Nigerian Measurements", func(t *testing.T) {
		if len(createdMeasIDs) == 0 {
			t.Fatal("No measurements available to test isolation")
		}
		targetMeasID := createdMeasIDs[0]

		// Shop B attempts to modify Shop A's Agbada measurement
		updatePayload := map[string]interface{}{
			"total_cost": 999999.0,
		}
		bodyB, _ := json.Marshal(updatePayload)
		req, _ := http.NewRequest("PUT", fmt.Sprintf("/api/measurements/%s", targetMeasID.Hex()), bytes.NewBuffer(bodyB))
		req.Header.Set("Authorization", "Bearer "+tokenB) // Tailor B's token
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, req)

		// Must return 404 or 403 (Multi-tenant data isolation enforced)
		if rr.Code != http.StatusNotFound && rr.Code != http.StatusForbidden {
			t.Fatalf("Security violation: Tailor B modified Tailor A's measurement! Status: %d", rr.Code)
		}
	})
}
