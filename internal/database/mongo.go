package database

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var DB *mongo.Database

func init() {
	// Configure Go resolver to use 8.8.8.8 / 1.1.1.1 so mongodb+srv SRV queries succeed
	// even when local router DNS fails to resolve SRV records on Windows.
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			conn, err := d.DialContext(ctx, "udp", "8.8.8.8:53")
			if err != nil {
				return d.DialContext(ctx, "udp", "1.1.1.1:53")
			}
			return conn, nil
		},
	}
}

func ConnectDB() *mongo.Database {
	mongoURL := os.Getenv("MONGO_URI")
	if mongoURL == "" {
		mongoURL = "mongodb://localhost:27017"
	}

	dbName := os.Getenv("DB_NAME")
	if dbName == "" {
		dbName = "tailor_voice"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	opts := options.Client().ApplyURI(mongoURL).SetServerSelectionTimeout(20 * time.Second)
	client, err := mongo.Connect(ctx, opts)
	if err != nil {
		log.Fatalf("❌ Failed to connect to MongoDB: %v", err)
	}

	// Check connection with retry for transient network latency
	var pingErr error
	for attempt := 1; attempt <= 3; attempt++ {
		pingCtx, pCancel := context.WithTimeout(context.Background(), 10*time.Second)
		pingErr = client.Ping(pingCtx, nil)
		pCancel()
		if pingErr == nil {
			break
		}
		time.Sleep(1 * time.Second)
	}
	if pingErr != nil {
		log.Fatalf("❌ MongoDB ping failed: %v", pingErr)
	}

	fmt.Println("✅ Connected to MongoDB")
	DB = client.Database(dbName)
	return DB
}

func GetCollection(collectionName string) *mongo.Collection {
	return DB.Collection(collectionName)
}
