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

func TestPaymentLedgerAndCustomerHistorySuite(t *testing.T) {
	router, _ := setupTestRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	usersColl := database.GetCollection("users")
	custColl := database.GetCollection("customers")
	measColl := database.GetCollection("measurements")

	// Create Tailor A
	userA := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("tailor_ledger_a_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Lagos Master Tailors",
		Role:      "tailor_pro",
		CreatedAt: time.Now(),
	}
	// Create Tailor B (for multi-tenant security verification)
	userB := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("tailor_ledger_b_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Abuja Couture Hub",
		Role:      "tailor_pro",
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

	// Helper to send POST /api/measurements/{id}/payments
	recordPayment := func(measID string, payload map[string]interface{}, token string) (*httptest.ResponseRecorder, map[string]interface{}) {
		body, _ := json.Marshal(payload)
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/measurements/%s/payments", measID), bytes.NewBuffer(body))
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

	// Helper to send GET /api/measurements/{id}/payments
	getPaymentHistory := func(measID string, token string) (*httptest.ResponseRecorder, map[string]interface{}) {
		req := httptest.NewRequest("GET", fmt.Sprintf("/api/measurements/%s/payments", measID), nil)
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
	// TEST 01: Initial Order Creation with Initial Deposit
	// ─────────────────────────────────────────────────────────────────
	var orderAID string
	var customerAID string

	t.Run("TEST 01: Create Order with Initial Deposit", func(t *testing.T) {
		createPayload := map[string]interface{}{
			"customer_name":  "Chief John Okoro",
			"customer_phone": "+2348031112233",
			"garment":        "Agbada 3-Piece",
			"gender":         "Male",
			"total_cost":     100000.0,
			"amount_paid":    30000.0, // Initial deposit of 30,000
			"data":           map[string]float64{"chest": 44.0, "length": 58.0},
		}
		body, _ := json.Marshal(createPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201 Created, got %d: %s", w.Code, w.Body.String())
		}

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		orderAID = res["id"].(string)

		// Check MongoDB document
		var savedDoc models.Measurement
		objID, _ := primitive.ObjectIDFromHex(orderAID)
		err := measColl.FindOne(ctx, bson.M{"_id": objID}).Decode(&savedDoc)
		if err != nil {
			t.Fatalf("Could not fetch measurement from DB: %v", err)
		}
		customerAID = savedDoc.CustomerID.Hex()

		if savedDoc.TotalCost != 100000.0 {
			t.Errorf("Expected TotalCost 100000, got %f", savedDoc.TotalCost)
		}
		if savedDoc.AmountPaid != 30000.0 {
			t.Errorf("Expected AmountPaid 30000, got %f", savedDoc.AmountPaid)
		}
		if len(savedDoc.Payments) != 1 {
			t.Fatalf("Expected 1 initial payment in Payments slice, got %d", len(savedDoc.Payments))
		}
		if savedDoc.Payments[0].Amount != 30000.0 {
			t.Errorf("Expected initial payment amount 30000, got %f", savedDoc.Payments[0].Amount)
		}
		if savedDoc.Payments[0].Note != "Initial deposit" {
			t.Errorf("Expected note 'Initial deposit', got %s", savedDoc.Payments[0].Note)
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 02 & 03: Multiple Installments & Balance Calculation
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 02 & 03: Add Multiple Installments and Authoritative Balance Calculation", func(t *testing.T) {
		// Installment 2: 20,000 via Bank Transfer
		w2, res2 := recordPayment(orderAID, map[string]interface{}{
			"amount":          20000.0,
			"method":          "bank_transfer",
			"note":            "Second installment via transfer",
			"idempotency_key": "idemp_test_2",
		}, tokenA)

		if w2.Code != http.StatusOK && w2.Code != http.StatusCreated {
			t.Fatalf("Expected 200/201 for payment 2, got %d: %+v", w2.Code, res2)
		}
		if res2["amount_paid"].(float64) != 50000.0 {
			t.Errorf("Expected amount_paid 50000, got %v", res2["amount_paid"])
		}

		// Installment 3: 15,000 via POS
		w3, res3 := recordPayment(orderAID, map[string]interface{}{
			"amount":          15000.0,
			"method":          "pos",
			"note":            "Third installment at atelier POS",
			"idempotency_key": "idemp_test_3",
		}, tokenA)

		if w3.Code != http.StatusOK && w3.Code != http.StatusCreated {
			t.Fatalf("Expected 200/201 for payment 3, got %d: %+v", w3.Code, res3)
		}
		if res3["amount_paid"].(float64) != 65000.0 {
			t.Errorf("Expected amount_paid 65000, got %v", res3["amount_paid"])
		}

		// Verify GET /api/measurements/{id}/payments financial summary
		wGet, resGet := getPaymentHistory(orderAID, tokenA)
		if wGet.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK from get payments, got %d: %+v", wGet.Code, resGet)
		}

		if resGet["total_cost"].(float64) != 100000.0 {
			t.Errorf("Expected total_cost 100000, got %v", resGet["total_cost"])
		}
		if resGet["amount_paid"].(float64) != 65000.0 {
			t.Errorf("Expected amount_paid 65000, got %v", resGet["amount_paid"])
		}
		if resGet["balance"].(float64) != 35000.0 {
			t.Errorf("Expected authoritative balance 35000, got %v", resGet["balance"])
		}
		if resGet["payment_status"].(string) != "partially_paid" {
			t.Errorf("Expected payment_status 'partially_paid', got %v", resGet["payment_status"])
		}

		payments := resGet["payments"].([]interface{})
		if len(payments) != 3 {
			t.Fatalf("Expected 3 payment records, got %d", len(payments))
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 04: Full Settlement (Balance becomes 0)
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 04: Full Settlement Clears Outstanding Balance", func(t *testing.T) {
		// Final payment of remaining 35,000 (100k total - 65k already paid = 35k)
		wFinal, resFinal := recordPayment(orderAID, map[string]interface{}{
			"amount":          35000.0,
			"method":          "cash",
			"note":            "Final balance settlement",
			"idempotency_key": "idemp_test_final",
		}, tokenA)

		if wFinal.Code != http.StatusOK && wFinal.Code != http.StatusCreated {
			t.Fatalf("Expected 200/201 for final payment, got %d: %+v", wFinal.Code, resFinal)
		}
		if resFinal["amount_paid"].(float64) != 100000.0 {
			t.Errorf("Expected amount_paid 100000, got %v", resFinal["amount_paid"])
		}

		// Verify GET /api/measurements/{id}/payments
		wGet, resGet := getPaymentHistory(orderAID, tokenA)
		if wGet.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK from get payments, got %d: %+v", wGet.Code, resGet)
		}
		if resGet["balance"].(float64) != 0.0 {
			t.Errorf("Expected balance 0.0, got %v", resGet["balance"])
		}
		if resGet["payment_status"].(string) != "fully_paid" {
			t.Errorf("Expected payment_status 'fully_paid', got %v", resGet["payment_status"])
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 05 & 06: Separate Orders and Returning Customer Dossier
	// ─────────────────────────────────────────────────────────────────
	var orderA2ID string
	t.Run("TEST 05 & 06: Separate Orders and Customer History Dossier", func(t *testing.T) {
		// Create a second order for the SAME customer (Chief John Okoro): Senator ₦70,000, ₦40,000 paid
		createPayload := map[string]interface{}{
			"customer_name":  "Chief John Okoro",
			"customer_phone": "+2348031112233",
			"garment":        "Senator Wear 2-Piece",
			"gender":         "Male",
			"total_cost":     70000.0,
			"amount_paid":    40000.0,
			"data":           map[string]float64{"chest": 44.0, "trouser_length": 42.0},
		}
		body, _ := json.Marshal(createPayload)
		req := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tokenA)

		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusCreated {
			t.Fatalf("Expected 201 Created for second order, got %d", w.Code)
		}

		var res map[string]interface{}
		_ = json.Unmarshal(w.Body.Bytes(), &res)
		orderA2ID = res["id"].(string)

		// Verify order 1 and order 2 payments remain strictly independent
		_, res1Get := getPaymentHistory(orderAID, tokenA)
		_, res2Get := getPaymentHistory(orderA2ID, tokenA)

		if res1Get["amount_paid"].(float64) != 100000.0 || res1Get["balance"].(float64) != 0.0 {
			t.Errorf("Order 1 was corrupted by Order 2 creation! Got %+v", res1Get)
		}
		if res2Get["amount_paid"].(float64) != 40000.0 || res2Get["balance"].(float64) != 30000.0 {
			t.Errorf("Order 2 financials incorrect! Got %+v", res2Get)
		}

		// Retrieve Customer History Dossier: GET /api/customers/{customer_id}/measurements
		reqDossier := httptest.NewRequest("GET", fmt.Sprintf("/api/customers/%s/measurements", customerAID), nil)
		reqDossier.Header.Set("Authorization", "Bearer "+tokenA)
		wDossier := httptest.NewRecorder()
		router.ServeHTTP(wDossier, reqDossier)

		if wDossier.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK for dossier, got %d: %s", wDossier.Code, wDossier.Body.String())
		}

		var dossierList []map[string]interface{}
		_ = json.Unmarshal(wDossier.Body.Bytes(), &dossierList)

		if len(dossierList) < 2 {
			t.Fatalf("Expected at least 2 orders in customer dossier, got %d", len(dossierList))
		}

		// Verify order independence in the returned dossier
		foundOrder1 := false
		foundOrder2 := false
		for _, item := range dossierList {
			id, _ := item["id"].(string)
			if id == "" {
				id, _ = item["_id"].(string)
			}
			if id == orderAID {
				foundOrder1 = true
				if item["total_cost"].(float64) != 100000.0 || item["amount_paid"].(float64) != 100000.0 {
					t.Errorf("Dossier order 1 amounts incorrect: %+v", item)
				}
			}
			if id == orderA2ID {
				foundOrder2 = true
				if item["total_cost"].(float64) != 70000.0 || item["amount_paid"].(float64) != 40000.0 {
					t.Errorf("Dossier order 2 amounts incorrect: %+v", item)
				}
			}
		}

		if !foundOrder1 || !foundOrder2 {
			t.Errorf("Customer dossier missing expected orders: found1=%v, found2=%v", foundOrder1, foundOrder2)
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 07: Legacy Financial Record Compatibility
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 07: Legacy Record with positive amount_paid but no Payments slice", func(t *testing.T) {
		// Manually insert legacy measurement directly into MongoDB with amount_paid = 45,000 and nil payments
		legacyID := primitive.NewObjectID()
		legacyDoc := bson.M{
			"_id":           legacyID,
			"customer_id":   primitive.NewObjectID(),
			"customer_name": "Alhaji Bello",
			"shop_id":       userA.ShopName,
			"user_id":       userA.ID.Hex(),
			"total_cost":    90000.0,
			"amount_paid":   45000.0,
			"payments":      nil, // Legacy record with no installment history
			"date":          time.Now().AddDate(0, -2, 0),
			"status":        "cutting",
		}
		_, err := measColl.InsertOne(ctx, legacyDoc)
		if err != nil {
			t.Fatalf("Failed to insert legacy document: %v", err)
		}

		// Call GET /api/measurements/{id}/payments
		wLeg, resLeg := getPaymentHistory(legacyID.Hex(), tokenA)
		if wLeg.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK for legacy record, got %d: %+v", wLeg.Code, resLeg)
		}

		if resLeg["total_cost"].(float64) != 90000.0 {
			t.Errorf("Expected legacy total_cost 90000, got %v", resLeg["total_cost"])
		}
		if resLeg["amount_paid"].(float64) != 45000.0 {
			t.Errorf("Expected legacy amount_paid 45000, got %v", resLeg["amount_paid"])
		}
		if resLeg["balance"].(float64) != 45000.0 {
			t.Errorf("Expected legacy balance 45000, got %v", resLeg["balance"])
		}
		// Notice: payments slice is empty, preserving real historical truth without fabricating entries
		payments := resLeg["payments"].([]interface{})
		if len(payments) != 0 {
			t.Errorf("Expected 0 fabricated payments for legacy record, got %d", len(payments))
		}

		// Now add a subsequent payment of 20,000 to this legacy order
		wAdd, resAdd := recordPayment(legacyID.Hex(), map[string]interface{}{
			"amount": 20000.0,
			"method": "bank_transfer",
			"note":   "Subsequent payment on legacy job",
		}, tokenA)

		if wAdd.Code != http.StatusOK && wAdd.Code != http.StatusCreated {
			t.Fatalf("Expected 200/201 when adding payment to legacy record, got %d: %+v", wAdd.Code, resAdd)
		}
		// Authoritative total received = Legacy 45,000 + New 20,000 = 65,000
		if resAdd["amount_paid"].(float64) != 65000.0 {
			t.Errorf("Expected legacy order total received 65000, got %v", resAdd["amount_paid"])
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 08: Idempotency Key Duplicate Prevention
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 08: Duplicate Request with Same Idempotency Key is Not Re-applied", func(t *testing.T) {
		idempKey := fmt.Sprintf("idemp_repeat_%d", time.Now().UnixNano())

		// First submission
		w1, res1 := recordPayment(orderA2ID, map[string]interface{}{
			"amount":          10000.0,
			"method":          "cash",
			"idempotency_key": idempKey,
			"note":            "Idempotency test payment",
		}, tokenA)

		if w1.Code != http.StatusOK && w1.Code != http.StatusCreated {
			t.Fatalf("Expected 200/201 on first submission, got %d: %+v", w1.Code, res1)
		}
		firstPaid := res1["amount_paid"].(float64)

		// Second duplicate submission with identical idempotency key
		w2, res2 := recordPayment(orderA2ID, map[string]interface{}{
			"amount":          10000.0,
			"method":          "cash",
			"idempotency_key": idempKey,
			"note":            "Duplicate retry payment",
		}, tokenA)

		if w2.Code != http.StatusOK && w2.Code != http.StatusCreated {
			t.Fatalf("Expected 200/201 on retry submission, got %d: %+v", w2.Code, res2)
		}
		secondPaid := res2["amount_paid"].(float64)

		// The amount_paid must NOT have increased twice
		if secondPaid != firstPaid {
			t.Errorf("Duplicate payment was mistakenly counted twice! firstPaid=%f, secondPaid=%f", firstPaid, secondPaid)
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 09: Invalid Payment Rejection (Zero and Negative)
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 09: Zero or Negative Payments Rejected", func(t *testing.T) {
		// Zero amount
		wZero, _ := recordPayment(orderA2ID, map[string]interface{}{
			"amount": 0.0,
			"method": "cash",
		}, tokenA)
		if wZero.Code != http.StatusBadRequest {
			t.Errorf("Expected HTTP 400 for 0 payment, got %d", wZero.Code)
		}

		// Negative amount
		wNeg, _ := recordPayment(orderA2ID, map[string]interface{}{
			"amount": -5000.0,
			"method": "cash",
		}, tokenA)
		if wNeg.Code != http.StatusBadRequest {
			t.Errorf("Expected HTTP 400 for negative payment, got %d", wNeg.Code)
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 10: Overpayment Policy
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 10: Overpayment Enforcement", func(t *testing.T) {
		// Attempting to record 500,000 payment on order with ~20,000 balance
		wOver, resOver := recordPayment(orderA2ID, map[string]interface{}{
			"amount": 500000.0,
			"method": "cash",
		}, tokenA)

		if wOver.Code != http.StatusBadRequest {
			t.Errorf("Expected HTTP 400 rejecting excessive overpayment, got %d: %+v", wOver.Code, resOver)
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 11 & 12: Tenant Isolation Security Enforcement
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 11: Tailor B Cannot Add Payment to Tailor A's Order", func(t *testing.T) {
		wCross, _ := recordPayment(orderAID, map[string]interface{}{
			"amount": 5000.0,
			"method": "cash",
		}, tokenB) // Signed with Tailor B's token

		if wCross.Code != http.StatusNotFound && wCross.Code != http.StatusForbidden {
			t.Errorf("Expected 404 or 403 on cross-tenant payment creation, got %d", wCross.Code)
		}
	})

	t.Run("TEST 12: Tailor B Cannot Access Tailor A's Customer Dossier or Payment History", func(t *testing.T) {
		// Attempt to view payments
		wCrossGet, _ := getPaymentHistory(orderAID, tokenB)
		if wCrossGet.Code != http.StatusNotFound && wCrossGet.Code != http.StatusForbidden {
			t.Errorf("Expected 404 or 403 on cross-tenant get payment history, got %d", wCrossGet.Code)
		}

		// Attempt to view customer measurements dossier
		reqDossier := httptest.NewRequest("GET", fmt.Sprintf("/api/customers/%s/measurements", customerAID), nil)
		reqDossier.Header.Set("Authorization", "Bearer "+tokenB)
		wDossier := httptest.NewRecorder()
		router.ServeHTTP(wDossier, reqDossier)

		if wDossier.Code != http.StatusNotFound && wDossier.Code != http.StatusForbidden {
			t.Errorf("Expected 404 or 403 on cross-tenant customer dossier, got %d", wDossier.Code)
		}
	})

	// ─────────────────────────────────────────────────────────────────
	// TEST 13: Production Status Remains Independent of Payments
	// ─────────────────────────────────────────────────────────────────
	t.Run("TEST 13: Production Status Remains Independent of Payments", func(t *testing.T) {
		var doc models.Measurement
		objID, _ := primitive.ObjectIDFromHex(orderAID)
		_ = measColl.FindOne(ctx, bson.M{"_id": objID}).Decode(&doc)

		// Even though Order AID was fully paid (₦100,000 / ₦100,000 in TEST 04),
		// its production stage should still reflect its tailoring progress independently!
		if doc.Status == "delivered" {
			t.Errorf("Production status was incorrectly auto-marked delivered by payment settlement!")
		}
	})
}
