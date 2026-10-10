package database

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	RedisClient *redis.Client
	redisMu     sync.RWMutex
)

// SanitizeRedisURI removes passwords/auth tokens from URIs for safe logging
func SanitizeRedisURI(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "[redacted-uri]"
	}
	if u.User != nil {
		u.User = url.UserPassword(u.User.Username(), "******")
	}
	return u.String()
}

// ConnectRedis initializes the Redis connection adhering to environment precedence:
// 1. NEW_REDIS_UR
// 2. NEW_REDIS_URI
// 3. REDIS_URI
// 4. Default: localhost:6379
// Never logs raw passwords or credentials.
func ConnectRedis() *redis.Client {
	redisMu.Lock()
	defer redisMu.Unlock()

	redisURL := ""
	source := ""

	if val := os.Getenv("NEW_REDIS_UR"); strings.TrimSpace(val) != "" {
		redisURL = strings.TrimSpace(val)
		source = "NEW_REDIS_UR"
	} else if val := os.Getenv("NEW_REDIS_URI"); strings.TrimSpace(val) != "" {
		redisURL = strings.TrimSpace(val)
		source = "NEW_REDIS_URI"
	} else if val := os.Getenv("REDIS_URI"); strings.TrimSpace(val) != "" {
		redisURL = strings.TrimSpace(val)
		source = "REDIS_URI"
	}

	var opt *redis.Options
	var err error

	if redisURL != "" && (strings.HasPrefix(redisURL, "redis://") || strings.HasPrefix(redisURL, "rediss://")) {
		opt, err = redis.ParseURL(redisURL)
		if err != nil {
			log.Printf("❌ Failed to parse Redis configuration from %s: %v", source, err)
			RedisClient = nil
			return nil
		}
		log.Printf("ℹ️ Attempting Redis connection via %s (target: %s, TLS: %t)", 
			source, opt.Addr, opt.TLSConfig != nil)
	} else {
		if redisURL == "" {
			redisURL = "localhost:6379"
			source = "default (localhost:6379)"
			log.Println("⚠️ No Redis URI found in environment (checked NEW_REDIS_UR, NEW_REDIS_URI, REDIS_URI), defaulting to localhost:6379")
		} else {
			log.Printf("ℹ️ Connecting to Redis host:port from %s", source)
		}
		opt = &redis.Options{
			Addr:     redisURL,
			Password: "",
			DB:       0,
		}
	}

	// Bounded timeouts to prevent request stalls during network partitions
	opt.DialTimeout = 2 * time.Second
	opt.ReadTimeout = 1 * time.Second
	opt.WriteTimeout = 1 * time.Second
	opt.MaxRetries = 2

	client := redis.NewClient(opt)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Ping the Redis server to verify connection
	_, err = client.Ping(ctx).Result()
	if err != nil {
		log.Printf("⚠️ Redis connection failed via %s: %v. Degrading to MongoDB quota fallback.", source, err)
		RedisClient = nil
		return nil
	}

	RedisClient = client
	fmt.Printf("✅ Redis connected successfully via %s\n", source)
	return RedisClient
}

// IsRedisHealthy performs a bounded, thread-safe ping to determine if Redis is operational.
// Max latency bounded to 500ms to guarantee zero UI lag during outages.
func IsRedisHealthy() bool {
	redisMu.RLock()
	client := RedisClient
	redisMu.RUnlock()

	if client == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err := client.Ping(ctx).Err()
	return err == nil
}

// GetRedisKey generates a consistent key for caching
func GetRedisKey(prefix, id string) string {
	return fmt.Sprintf("%s:%s", prefix, id)
}

