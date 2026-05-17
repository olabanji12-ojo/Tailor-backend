package database

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var RedisClient *redis.Client

// ConnectRedis initializes the Redis connection
func ConnectRedis() *redis.Client {
	redisURL := os.Getenv("REDIS_URI")
	var opt *redis.Options
	var err error

	if redisURL != "" && (strings.HasPrefix(redisURL, "redis://") || strings.HasPrefix(redisURL, "rediss://")) {
		// If it's a full URI (like Upstash), parse it to extract credentials
		opt, err = redis.ParseURL(redisURL)
		if err != nil {
			log.Printf("❌ Failed to parse Redis URI: %v", err)
			return nil
		}
	} else {
		// Fallback for simple host:port
		if redisURL == "" {
			redisURL = "localhost:6379"
			log.Println("⚠️ REDIS_URI not found in .env, defaulting to localhost:6379")
		}
		opt = &redis.Options{
			Addr:     redisURL,
			Password: "", // empty default
			DB:       0,  // default DB
		}
	}

	client := redis.NewClient(opt)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Ping the Redis server to verify connection
	_, err = client.Ping(ctx).Result()
	if err != nil {
		log.Printf("❌ Redis connection failed: %v. Caching will degrade gracefully.", err)
		return nil
	}

	RedisClient = client
	fmt.Println("✅ Redis connected successfully")
	return RedisClient
}

// GetRedisKey generates a consistent key for caching
func GetRedisKey(prefix, id string) string {
	return fmt.Sprintf("%s:%s", prefix, id)
}
