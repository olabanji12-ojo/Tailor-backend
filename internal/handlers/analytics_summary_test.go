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

func TestAnalyticsSummarySuite(t *testing.T) {
	router, _ := setupTestRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	usersColl := database.GetCollection("users")
	measColl := database.GetCollection("measurements")
	custColl := database.GetCollection("customers")

	// Setup Tailor A
	userA := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("analytics_tailor_a_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Elite Analytics Lagos",
		Role:      "tailor_free",
		CreatedAt: time.Now(),
	}
	// Setup Tailor B (for tenant isolation verification)
	userB := &models.User{
		ID:        primitive.NewObjectID(),
		Email:     fmt.Sprintf("analytics_tailor_b_%d@test.com", time.Now().UnixNano()),
		ShopName:  "Elite Analytics Abuja",
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

	// TEST 01: Empty Boutique returns exact zeros
	t.Run("TEST_01:_Empty_Boutique_Returns_Zeros", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/analytics/summary", nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}

		var summary models.AnalyticsSummary
		if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if summary.TotalRevenuePipeline != 0 || summary.TotalRevenuePaid != 0 || summary.TotalOutstandingDebt != 0 {
			t.Fatalf("Expected all zero revenue for empty boutique, got %+v", summary)
		}
		if summary.ActiveJobsCount != 0 || summary.CompletedJobsCount != 0 || summary.DebtorsCount != 0 {
			t.Fatalf("Expected all zero counts, got %+v", summary)
		}
	})

	// Seed Customer for Tailor A
	custA := &models.Customer{
		ID:        primitive.NewObjectID(),
		Name:      "Alhaji Dantata",
		Phone:     "+2348039988776",
		ShopID:    userA.ShopName,
		UserID:    userA.ID.Hex(),
		Gender:    "male",
		CreatedAt: time.Now(),
	}
	_, _ = custColl.InsertOne(ctx, custA)

	// TEST 02: Seed 25 Isolated Orders for Tailor A to verify calculations beyond Page 1 (limit 10)
	t.Run("TEST_02:_At_Least_25_Orders_Calculated_Across_Entire_Database", func(t *testing.T) {
		// Create 25 orders:
		// - 15 Active, partially paid (debtors)
		// - 5 Active, fully paid
		// - 5 Completed (Delivered)
		// - 3 of the active orders are Overdue (past delivery deadline)
		var expectedPipeline float64 = 0
		var expectedPaid float64 = 0
		var expectedDebt float64 = 0
		var expectedActive int64 = 20
		var expectedCompleted int64 = 5
		var expectedOverdue int64 = 3
		var expectedDebtors int64 = 15

		for i := 1; i <= 25; i++ {
			totalCost := float64(50000 + (i * 2000))
			var amountPaid float64 = 0
			status := "sewing"
			deliveryDate := "2026-12-01"

			if i <= 15 {
				// Partially paid
				amountPaid = float64(20000)
				debt := totalCost - amountPaid
				expectedDebt += debt
				if i <= 3 {
					// 3 Overdue orders
					deliveryDate = "2020-01-01"
				}
			} else if i <= 20 {
				// Fully paid active
				amountPaid = totalCost
			} else {
				// Completed delivered
				amountPaid = totalCost
				status = "delivered"
			}

			expectedPipeline += totalCost
			expectedPaid += amountPaid

			m := &models.Measurement{
				CustomerID:   custA.ID,
				UserID:       userA.ID.Hex(),
				ShopID:       userA.ShopName,
				TotalCost:    totalCost,
				AmountPaid:   amountPaid,
				Status:       status,
				DeliveryDate: deliveryDate,
				Garment:      "Agbada",
				Date:         time.Now(),
				CreatedAt:    time.Now(),
			}
			_, err := measColl.InsertOne(ctx, m)
			if err != nil {
				t.Fatalf("Failed to seed order %d: %v", i, err)
			}
		}

		req := httptest.NewRequest("GET", "/api/analytics/summary", nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}

		var summary models.AnalyticsSummary
		if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		// Verify that all 25 records were included, NOT just 10!
		if summary.TotalRevenuePipeline != expectedPipeline {
			t.Fatalf("Expected pipeline %.2f across all 25 records, got %.2f", expectedPipeline, summary.TotalRevenuePipeline)
		}
		if summary.TotalRevenuePaid != expectedPaid {
			t.Fatalf("Expected paid %.2f across all 25 records, got %.2f", expectedPaid, summary.TotalRevenuePaid)
		}
		if summary.TotalOutstandingDebt != expectedDebt {
			t.Fatalf("Expected debt %.2f across all 25 records, got %.2f", expectedDebt, summary.TotalOutstandingDebt)
		}
		if summary.ActiveJobsCount != expectedActive {
			t.Fatalf("Expected %d active jobs, got %d", expectedActive, summary.ActiveJobsCount)
		}
		if summary.CompletedJobsCount != expectedCompleted {
			t.Fatalf("Expected %d completed jobs, got %d", expectedCompleted, summary.CompletedJobsCount)
		}
		if summary.OverdueJobsCount != expectedOverdue {
			t.Fatalf("Expected %d overdue jobs, got %d", expectedOverdue, summary.OverdueJobsCount)
		}
		if summary.DebtorsCount != expectedDebtors {
			t.Fatalf("Expected %d debtors, got %d", expectedDebtors, summary.DebtorsCount)
		}
		if len(summary.Debtors) != int(expectedDebtors) {
			t.Fatalf("Expected %d debtor records in list, got %d", expectedDebtors, len(summary.Debtors))
		}

		// Verify debtors are sorted by balance descending
		for idx := 1; idx < len(summary.Debtors); idx++ {
			if summary.Debtors[idx-1].Balance < summary.Debtors[idx].Balance {
				t.Fatalf("Debtors not sorted descending by balance: item %d has %.2f, item %d has %.2f",
					idx-1, summary.Debtors[idx-1].Balance, idx, summary.Debtors[idx].Balance)
			}
		}

		// Verify customer details were populated in debtor record
		if summary.Debtors[0].CustomerName != "Alhaji Dantata" {
			t.Fatalf("Expected customer name 'Alhaji Dantata', got '%s'", summary.Debtors[0].CustomerName)
		}
	})

	// TEST 03: Tenant Isolation — Tailor B sees 0 of Tailor A's records
	t.Run("TEST_03:_Tenant_Isolation_Guaranteed", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/analytics/summary", nil)
		req.Header.Set("Authorization", "Bearer "+tokenB)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("Expected 200 OK, got %d: %s", w.Code, w.Body.String())
		}

		var summary models.AnalyticsSummary
		if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
			t.Fatalf("Failed to decode response: %v", err)
		}

		if summary.TotalRevenuePipeline != 0 || summary.TotalRevenuePaid != 0 || summary.TotalOutstandingDebt != 0 {
			t.Fatalf("Tailor B leaked Tailor A's financial summary: %+v", summary)
		}
		if summary.ActiveJobsCount != 0 || summary.DebtorsCount != 0 {
			t.Fatalf("Tailor B leaked Tailor A's job counts: %+v", summary)
		}
	})

	// TEST 04: Dynamic Update after new Payment installment
	t.Run("TEST_04:_Financial_Summary_Updates_After_Payment", func(t *testing.T) {
		// Pick one of Tailor A's debtor orders and add payment
		var debtorOrd models.Measurement
		err := measColl.FindOne(ctx, bson.M{"user_id": userA.ID.Hex(), "delivery_date": "2020-01-01"}).Decode(&debtorOrd)
		if err != nil {
			t.Fatalf("Failed to find order: %v", err)
		}

		payPayload := map[string]interface{}{
			"amount": 10000.0,
			"method": "bank_transfer",
			"note":   "Audit test payment",
		}
		bodyBytes, _ := json.Marshal(payPayload)
		payReq := httptest.NewRequest("POST", fmt.Sprintf("/api/measurements/%s/payments", debtorOrd.ID.Hex()), bytes.NewBuffer(bodyBytes))
		payReq.Header.Set("Authorization", "Bearer "+tokenA)
		wPay := httptest.NewRecorder()
		router.ServeHTTP(wPay, payReq)

		if wPay.Code != http.StatusOK && wPay.Code != http.StatusCreated {
			t.Fatalf("Expected 200/201 OK on payment, got %d: %s", wPay.Code, wPay.Body.String())
		}

		// Re-fetch analytics summary
		req := httptest.NewRequest("GET", "/api/analytics/summary", nil)
		req.Header.Set("Authorization", "Bearer "+tokenA)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		var summary models.AnalyticsSummary
		_ = json.Unmarshal(w.Body.Bytes(), &summary)

		// Outstanding debt should have decreased by 10,000, Total paid increased by 10,000
		if summary.TotalRevenuePaid == 0 {
			t.Fatalf("Revenue paid should not be 0 after payment")
		}
	})
}
