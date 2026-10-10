package database

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/emman/Tailor-Backend/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// DefaultVoiceQuotaSeconds is the monthly voice transcription limit per boutique (8 minutes = 480 seconds)
const DefaultVoiceQuotaSeconds int64 = 480

var (
	ErrQuotaExceeded           = errors.New("voice_quota_exceeded")
	ErrQuotaServiceUnavailable = errors.New("voice_service_unavailable")
)

// NextMonthResetDate returns a human-friendly string indicating when the quota resets
func NextMonthResetDate() string {
	now := time.Now()
	first := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, now.Location())
	return first.Format("January 2, 2006")
}

func getMonthKey(shopID, yearMonth string) string {
	return fmt.Sprintf("voice:seconds:%s:%s", shopID, yearMonth)
}

func getDocID(shopID, yearMonth string) string {
	return fmt.Sprintf("%s_%s", shopID, yearMonth)
}

// ReserveVoiceQuota performs an atomic, concurrency-safe reservation of estimated seconds.
// It uses MongoDB monthly_quotas as the authoritative persistence layer and syncs with Redis (L1 cache).
// If Redis is unreachable, it automatically activates the MongoDB fallback without dropping the request.
// If both MongoDB and Redis are unreachable, it returns ErrQuotaServiceUnavailable.
func ReserveVoiceQuota(ctx context.Context, shopID string, estimatedSeconds int64, isAdmin bool) (quotaBefore int64, quotaAfter int64, isFallback bool, err error) {
	if isAdmin {
		return 0, 0, false, nil
	}

	yearMonth := time.Now().Format("2006_01")
	docID := getDocID(shopID, yearMonth)
	redisKey := getMonthKey(shopID, yearMonth)

	coll := GetCollection("monthly_quotas")
	if coll == nil {
		log.Printf("❌ Quota Error: MongoDB monthly_quotas collection unavailable for shop %s", shopID)
		return 0, 0, false, ErrQuotaServiceUnavailable
	}

	// 1. Ensure the monthly quota document exists (idempotent upsert)
	ensureCtx, ensureCancel := context.WithTimeout(ctx, 3*time.Second)
	defer ensureCancel()

	_, err = coll.UpdateOne(
		ensureCtx,
		bson.M{"_id": docID},
		bson.M{
			"$setOnInsert": bson.M{
				"_id":           docID,
				"shop_id":       shopID,
				"month":         yearMonth,
				"used_seconds":  int64(0),
				"limit_seconds": DefaultVoiceQuotaSeconds,
				"created_at":    time.Now(),
			},
		},
		options.Update().SetUpsert(true),
	)
	if err != nil {
		log.Printf("❌ Quota Error: Failed to initialize monthly quota doc for shop %s: %v", shopID, err)
		return 0, 0, false, ErrQuotaServiceUnavailable
	}

	// 2. Concurrency-Safe Atomic Reservation via FindOneAndUpdate
	// Only increments if used_seconds <= limit - estimatedSeconds
	reserveCtx, reserveCancel := context.WithTimeout(ctx, 3*time.Second)
	defer reserveCancel()

	filter := bson.M{
		"_id":          docID,
		"used_seconds": bson.M{"$lte": DefaultVoiceQuotaSeconds - estimatedSeconds},
	}
	update := bson.M{
		"$inc": bson.M{"used_seconds": estimatedSeconds},
		"$set": bson.M{"updated_at": time.Now()},
	}

	var updated models.MonthlyQuota
	err = coll.FindOneAndUpdate(
		reserveCtx,
		filter,
		update,
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&updated)

	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			// Quota exceeded: fetch current usage for accurate response metadata
			var current models.MonthlyQuota
			_ = coll.FindOne(reserveCtx, bson.M{"_id": docID}).Decode(&current)
			log.Printf("⛔ Quota Exceeded for shop %s: current=%ds, requested=%ds, limit=%ds",
				shopID, current.UsedSeconds, estimatedSeconds, DefaultVoiceQuotaSeconds)
			return current.UsedSeconds, current.UsedSeconds, false, ErrQuotaExceeded
		}
		log.Printf("❌ Quota Error: FindOneAndUpdate failed for shop %s: %v", shopID, err)
		return 0, 0, false, ErrQuotaServiceUnavailable
	}

	quotaAfter = updated.UsedSeconds
	quotaBefore = updated.UsedSeconds - estimatedSeconds

	// 3. Redis L1 Cache Synchronization
	redisHealthy := IsRedisHealthy()
	if redisHealthy && RedisClient != nil {
		rCtx, rCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer rCancel()
		if rErr := RedisClient.Set(rCtx, redisKey, quotaAfter, 45*24*time.Hour).Err(); rErr != nil {
			log.Printf("⚠️ Redis cache update failed: %v. Fallback persistence in MongoDB remains safe.", rErr)
		}
	} else {
		isFallback = true
		log.Printf("ℹ️ Redis unavailable. Voice quota reserved directly in MongoDB fallback for shop %s (used=%ds)", shopID, quotaAfter)
	}

	return quotaBefore, quotaAfter, isFallback, nil
}

// ReconcileVoiceQuota applies the Authorize-Capture adjustment once Whisper returns the actual audio duration.
// delta = actualSeconds - estimatedSeconds. If delta is negative, the tailor receives a refund.
func ReconcileVoiceQuota(ctx context.Context, shopID string, estimatedSeconds, actualSeconds int64, isAdmin bool) (finalUsed int64, err error) {
	if isAdmin {
		return 0, nil
	}

	yearMonth := time.Now().Format("2006_01")
	docID := getDocID(shopID, yearMonth)
	redisKey := getMonthKey(shopID, yearMonth)
	delta := actualSeconds - estimatedSeconds

	coll := GetCollection("monthly_quotas")
	if coll == nil {
		log.Printf("❌ Reconcile Error: MongoDB monthly_quotas unavailable for shop %s", shopID)
		return 0, ErrQuotaServiceUnavailable
	}

	if delta != 0 {
		recCtx, recCancel := context.WithTimeout(ctx, 3*time.Second)
		defer recCancel()

		filter := bson.M{"_id": docID}
		update := bson.M{
			"$inc": bson.M{"used_seconds": delta},
			"$set": bson.M{"updated_at": time.Now()},
		}

		var updated models.MonthlyQuota
		err = coll.FindOneAndUpdate(
			recCtx,
			filter,
			update,
			options.FindOneAndUpdate().SetReturnDocument(options.After),
		).Decode(&updated)

		if err == nil {
			finalUsed = updated.UsedSeconds
			if finalUsed < 0 {
				// Floor guard: usage cannot fall below 0
				_, _ = coll.UpdateOne(recCtx, bson.M{"_id": docID}, bson.M{"$set": bson.M{"used_seconds": int64(0)}})
				finalUsed = 0
			}
		} else {
			log.Printf("⚠️ Reconcile MongoDB update failed for shop %s: %v", shopID, err)
		}
	} else {
		// No adjustment needed, query current
		var current models.MonthlyQuota
		_ = coll.FindOne(ctx, bson.M{"_id": docID}).Decode(&current)
		finalUsed = current.UsedSeconds
	}

	// Sync reconciled value to Redis
	if IsRedisHealthy() && RedisClient != nil {
		rCtx, rCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer rCancel()
		_ = RedisClient.Set(rCtx, redisKey, finalUsed, 45*24*time.Hour).Err()
	}

	return finalUsed, nil
}

// GetVoiceUsage returns current monthly usage, consulting Redis first if healthy, with MongoDB fallback
func GetVoiceUsage(ctx context.Context, shopID string, isAdmin bool) (usedSeconds int64, isFallback bool, err error) {
	if isAdmin {
		return 0, false, nil
	}

	yearMonth := time.Now().Format("2006_01")
	docID := getDocID(shopID, yearMonth)
	redisKey := getMonthKey(shopID, yearMonth)

	// Try Redis first if healthy
	if IsRedisHealthy() && RedisClient != nil {
		rCtx, rCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer rCancel()
		val, rErr := RedisClient.Get(rCtx, redisKey).Int64()
		if rErr == nil {
			return val, false, nil
		}
	}

	// Fallback to MongoDB
	coll := GetCollection("monthly_quotas")
	if coll == nil {
		return 0, true, ErrQuotaServiceUnavailable
	}

	mCtx, mCancel := context.WithTimeout(ctx, 3*time.Second)
	defer mCancel()

	var mq models.MonthlyQuota
	err = coll.FindOne(mCtx, bson.M{"_id": docID}).Decode(&mq)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return 0, !IsRedisHealthy(), nil
		}
		return 0, true, err
	}

	// Backfill Redis if healthy
	if IsRedisHealthy() && RedisClient != nil {
		rCtx, rCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer rCancel()
		_ = RedisClient.Set(rCtx, redisKey, mq.UsedSeconds, 45*24*time.Hour).Err()
	}

	return mq.UsedSeconds, !IsRedisHealthy(), nil
}

// BuildVoiceQuotaResponse formats a standardized quota payload for client responses
func BuildVoiceQuotaResponse(usedSeconds int64, isAdmin bool) map[string]interface{} {
	remaining := DefaultVoiceQuotaSeconds - usedSeconds
	if remaining < 0 {
		remaining = 0
	}
	level := "none"
	if !isAdmin {
		ratio := float64(usedSeconds) / float64(DefaultVoiceQuotaSeconds)
		switch {
		case usedSeconds >= DefaultVoiceQuotaSeconds:
			level = "exceeded"
		case ratio >= 0.95:
			level = "urgent"
		case ratio >= 0.80:
			level = "warning"
		}
	}
	return map[string]interface{}{
		"used_seconds":      usedSeconds,
		"limit_seconds":     DefaultVoiceQuotaSeconds,
		"remaining_seconds": remaining,
		"warning_level":     level,
		"resets_on":         NextMonthResetDate(),
		"is_admin":          isAdmin,
	}
}
