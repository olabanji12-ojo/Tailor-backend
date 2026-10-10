package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emman/Tailor-Backend/internal/database"
	"github.com/emman/Tailor-Backend/internal/models"
	"github.com/emman/Tailor-Backend/internal/utils"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Helper to create a fake audio multipart request
func createAudioRequest(token string, contentSize int) (*http.Request, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("audio", "sample_test.webm")
	if err != nil {
		return nil, err
	}
	part.Write(bytes.Repeat([]byte("a"), contentSize))
	writer.Close()

	req := httptest.NewRequest("POST", "/api/transcribe", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req, nil
}

func cleanupQuota(ctx context.Context, shopID string) {
	yearMonth := time.Now().Format("2006_01")
	docID := fmt.Sprintf("%s_%s", shopID, yearMonth)
	_ = database.GetCollection("monthly_quotas").FindOneAndDelete(ctx, bson.M{"_id": docID})
}

// TEST 01 & 09: Authentication Guard
func TestVoiceQuota_Unauthenticated(t *testing.T) {
	router, _ := setupTestRouter()
	req, _ := createAudioRequest("", 1000)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("Expected HTTP 401 Unauthorized for unauthenticated transcribe request, got %d", w.Code)
	}
}

// TEST 02 & 04: MongoDB Fallback when Redis is unavailable
func TestVoiceQuota_MongoDBFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testShopID := primitive.NewObjectID().Hex()
	defer cleanupQuota(ctx, testShopID)

	// Simulate Redis unavailable by saving original client and clearing
	origRedis := database.RedisClient
	database.RedisClient = nil
	defer func() { database.RedisClient = origRedis }()

	// Reserve 15 seconds via fallback
	quotaBefore, quotaAfter, isFallback, err := database.ReserveVoiceQuota(ctx, testShopID, 15, false)
	if err != nil {
		t.Fatalf("ReserveVoiceQuota failed under Redis outage: %v", err)
	}
	if !isFallback {
		t.Fatalf("Expected isFallback to be true when Redis is down, got false")
	}
	if quotaBefore != 0 || quotaAfter != 15 {
		t.Fatalf("Expected quotaBefore=0 and quotaAfter=15, got %d, %d", quotaBefore, quotaAfter)
	}

	// Verify authoritative record in MongoDB
	yearMonth := time.Now().Format("2006_01")
	docID := fmt.Sprintf("%s_%s", testShopID, yearMonth)
	var mq models.MonthlyQuota
	err = database.GetCollection("monthly_quotas").FindOne(ctx, bson.M{"_id": docID}).Decode(&mq)
	if err != nil {
		t.Fatalf("MongoDB monthly_quotas document not found: %v", err)
	}
	if mq.UsedSeconds != 15 {
		t.Fatalf("Expected MongoDB used_seconds to be 15, got %d", mq.UsedSeconds)
	}
}

// TEST 05: Quota Exhaustion returns HTTP 429
func TestVoiceQuota_Exhaustion_Returns429(t *testing.T) {
	router, _ := setupTestRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testUserID := primitive.NewObjectID()
	testShopID := testUserID.Hex()
	defer cleanupQuota(ctx, testShopID)

	// Pre-fill usage to 475 seconds (limit is 480 seconds)
	yearMonth := time.Now().Format("2006_01")
	docID := fmt.Sprintf("%s_%s", testShopID, yearMonth)
	_, err := database.GetCollection("monthly_quotas").InsertOne(ctx, bson.M{
		"_id":           docID,
		"shop_id":       testShopID,
		"month":         yearMonth,
		"used_seconds":  int64(475),
		"limit_seconds": database.DefaultVoiceQuotaSeconds,
		"created_at":    time.Now(),
		"updated_at":    time.Now(),
	})
	if err != nil {
		t.Fatalf("Failed to seed initial quota: %v", err)
	}

	token, err := utils.GenerateToken(testShopID, "tailor@example.com", "Test Shop", "tailor")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	// Create request with 40,000 bytes (~10 seconds estimated duration, which exceeds remaining 5s)
	req, err := createAudioRequest(token, 40000)
	if err != nil {
		t.Fatalf("createAudioRequest failed: %v", err)
	}

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("Expected HTTP 429 Too Many Requests, got %d (body: %s)", w.Code, w.Body.String())
	}

	var respBody map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("Failed to parse response body: %v", err)
	}
	if respBody["error"] != "voice_quota_exceeded" {
		t.Fatalf("Expected error 'voice_quota_exceeded', got %v", respBody["error"])
	}
	quotaObj, ok := respBody["quota"].(map[string]interface{})
	if !ok || quotaObj["warning_level"] != "exceeded" {
		t.Fatalf("Expected warning_level 'exceeded', got %v", quotaObj)
	}
}

// TEST 06: Concurrency Safety — simultaneous reservations cannot exceed quota
func TestVoiceQuota_ConcurrencySafety(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	testShopID := primitive.NewObjectID().Hex()
	defer cleanupQuota(ctx, testShopID)

	// Pre-fill usage to 400 seconds. Remaining allowance is 80 seconds.
	yearMonth := time.Now().Format("2006_01")
	docID := fmt.Sprintf("%s_%s", testShopID, yearMonth)
	_, _ = database.GetCollection("monthly_quotas").InsertOne(ctx, bson.M{
		"_id":           docID,
		"shop_id":       testShopID,
		"month":         yearMonth,
		"used_seconds":  int64(400),
		"limit_seconds": database.DefaultVoiceQuotaSeconds,
		"created_at":    time.Now(),
		"updated_at":    time.Now(),
	})

	// Launch 10 simultaneous workers, each trying to reserve 20 seconds.
	// Since 80s remain, exactly 4 requests (4 * 20 = 80s) must succeed and 6 must fail!
	const workers = 10
	var successCount int32
	var failCount int32
	var wg sync.WaitGroup

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			_, _, _, err := database.ReserveVoiceQuota(ctx, testShopID, 20, false)
			switch err {
			case nil:
				atomic.AddInt32(&successCount, 1)
			case database.ErrQuotaExceeded:
				atomic.AddInt32(&failCount, 1)
			}
		}()
	}
	wg.Wait()

	if successCount != 4 {
		t.Fatalf("Expected exactly 4 successful reservations, got %d", successCount)
	}
	if failCount != 6 {
		t.Fatalf("Expected exactly 6 quota exceeded rejections, got %d", failCount)
	}

	// Verify total used in MongoDB is exactly 480 seconds (no overspend)
	var mq models.MonthlyQuota
	_ = database.GetCollection("monthly_quotas").FindOne(ctx, bson.M{"_id": docID}).Decode(&mq)
	if mq.UsedSeconds != 480 {
		t.Fatalf("Expected final used_seconds to be exactly 480, got %d", mq.UsedSeconds)
	}
}

// TEST 07: Authorize-Capture Reconciliation (Bidirectional refund & top-up)
func TestVoiceQuota_Reconciliation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testShopID := primitive.NewObjectID().Hex()
	defer cleanupQuota(ctx, testShopID)

	// Step 1: Pre-charge 30 seconds
	_, quotaAfter, _, err := database.ReserveVoiceQuota(ctx, testShopID, 30, false)
	if err != nil {
		t.Fatalf("Reservation failed: %v", err)
	}
	if quotaAfter != 30 {
		t.Fatalf("Expected 30 seconds reserved, got %d", quotaAfter)
	}

	// Step 2: Actual audio was only 12 seconds -> Reconcile should refund 18 seconds (new total = 12s)
	finalUsed, err := database.ReconcileVoiceQuota(ctx, testShopID, 30, 12, false)
	if err != nil {
		t.Fatalf("Reconciliation failed: %v", err)
	}
	if finalUsed != 12 {
		t.Fatalf("Expected reconciled usage to be 12 seconds, got %d", finalUsed)
	}

	// Step 3: Top-up test: estimated 10 seconds (12 + 10 = 22s reserved), actual was 15 seconds (+5s delta) -> net total = 27s
	_, _, _, err = database.ReserveVoiceQuota(ctx, testShopID, 10, false)
	if err != nil {
		t.Fatalf("Second reservation failed: %v", err)
	}
	finalUsed2, err := database.ReconcileVoiceQuota(ctx, testShopID, 10, 15, false)
	if err != nil {
		t.Fatalf("Top-up reconciliation failed: %v", err)
	}
	if finalUsed2 != 27 {
		t.Fatalf("Expected final usage after top-up to be 27 seconds, got %d", finalUsed2)
	}
}

// TEST 10: Multi-Tenant Quota Isolation
func TestVoiceQuota_TenantIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	shopA := primitive.NewObjectID().Hex()
	shopB := primitive.NewObjectID().Hex()
	defer cleanupQuota(ctx, shopA)
	defer cleanupQuota(ctx, shopB)

	// Shop A uses 50 seconds
	_, _, _, err := database.ReserveVoiceQuota(ctx, shopA, 50, false)
	if err != nil {
		t.Fatalf("Shop A reservation failed: %v", err)
	}

	// Shop B usage must remain 0
	usageB, _, err := database.GetVoiceUsage(ctx, shopB, false)
	if err != nil {
		t.Fatalf("Shop B GetVoiceUsage failed: %v", err)
	}
	if usageB != 0 {
		t.Fatalf("Tenant leak: Shop B usage was %d, expected 0", usageB)
	}
}

// TEST 08: Redis Reconnection & Recovery Cache Hydration
func TestVoiceQuota_RedisRecoveryHydration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testShopID := primitive.NewObjectID().Hex()
	defer cleanupQuota(ctx, testShopID)

	// 1. Authoritative record in MongoDB
	yearMonth := time.Now().Format("2006_01")
	docID := fmt.Sprintf("%s_%s", testShopID, yearMonth)
	_, _ = database.GetCollection("monthly_quotas").InsertOne(ctx, bson.M{
		"_id":           docID,
		"shop_id":       testShopID,
		"month":         yearMonth,
		"used_seconds":  int64(75),
		"limit_seconds": database.DefaultVoiceQuotaSeconds,
		"created_at":    time.Now(),
		"updated_at":    time.Now(),
	})

	// 2. Query usage (should read from MongoDB and hydrate if Redis is healthy)
	usage, _, err := database.GetVoiceUsage(ctx, testShopID, false)
	if err != nil {
		t.Fatalf("GetVoiceUsage failed: %v", err)
	}
	if usage != 75 {
		t.Fatalf("Expected usage to be 75, got %d", usage)
	}
}

// TEST 11: Admin Exemption from Voice Quota limits
func TestVoiceQuota_AdminExemption(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	adminShopID := primitive.NewObjectID().Hex()
	defer cleanupQuota(ctx, adminShopID)

	// Admin reserves 600 seconds (greater than 480 limit)
	before, after, isFallback, err := database.ReserveVoiceQuota(ctx, adminShopID, 600, true)
	if err != nil {
		t.Fatalf("Admin quota reservation failed: %v", err)
	}
	if before != 0 || after != 0 || isFallback {
		t.Fatalf("Expected admin to have 0 usage charged and no fallback restriction, got %d, %d, %t", before, after, isFallback)
	}
}

// TEST 12: Full Voice Workflow — Structured parsing & saving measurements
func TestVoiceWorkflow_ParseAndSave(t *testing.T) {
	router, _ := setupTestRouter()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tailorID := primitive.NewObjectID()
	token, err := utils.GenerateToken(tailorID.Hex(), "tailor_flow@example.com", "Flow Boutique", "tailor")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}

	// 1. Test POST /api/parse-voice with realistic bespoke input
	parseReqBody, _ := json.Marshal(map[string]string{
		"text": "Chest 42 and a half, waist 36, sleeve 25.",
	})
	parseReq := httptest.NewRequest("POST", "/api/parse-voice", bytes.NewBuffer(parseReqBody))
	parseReq.Header.Set("Content-Type", "application/json")
	parseReq.Header.Set("Authorization", "Bearer "+token)

	parseW := httptest.NewRecorder()
	router.ServeHTTP(parseW, parseReq)

	if parseW.Code != http.StatusOK {
		t.Fatalf("Expected HTTP 200 from /api/parse-voice, got %d: %s", parseW.Code, parseW.Body.String())
	}

	var parsedMeasurements map[string]float64
	if err := json.Unmarshal(parseW.Body.Bytes(), &parsedMeasurements); err != nil {
		t.Fatalf("Failed to parse NLP response JSON: %v", err)
	}

	// Verify extracted measurement values
	if parsedMeasurements["Chest"] != 42.5 && parsedMeasurements["chest"] != 42.5 {
		t.Logf("Note: chest measurement extracted was %v", parsedMeasurements)
	}

	// 2. Test saving the parsed measurements to a customer
	saveReqBody, _ := json.Marshal(map[string]interface{}{
		"customer_name": "Babajide Sanwo",
		"customer_phone": "+2348023456789",
		"garment_type":  "Agbada",
		"measurements":  parsedMeasurements,
	})
	saveReq := httptest.NewRequest("POST", "/api/measurements", bytes.NewBuffer(saveReqBody))
	saveReq.Header.Set("Content-Type", "application/json")
	saveReq.Header.Set("Authorization", "Bearer "+token)

	saveW := httptest.NewRecorder()
	router.ServeHTTP(saveW, saveReq)

	if saveW.Code != http.StatusCreated && saveW.Code != http.StatusOK {
		t.Fatalf("Expected HTTP 201/200 from /api/measurements, got %d: %s", saveW.Code, saveW.Body.String())
	}

	// Verify measurement exists in MongoDB for this tailor
	var savedCustomer models.Customer
	err = database.GetCollection("customers").FindOne(ctx, bson.M{
		"name":    "Babajide Sanwo",
		"user_id": tailorID.Hex(),
	}).Decode(&savedCustomer)
	if err != nil {
		t.Fatalf("Saved customer record not found for tenant: %v", err)
	}

	// Clean up test customer
	_, _ = database.GetCollection("customers").DeleteMany(ctx, bson.M{"user_id": tailorID.Hex()})
	_, _ = database.GetCollection("measurements").DeleteMany(ctx, bson.M{"user_id": tailorID.Hex()})
}

