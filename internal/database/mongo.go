package database

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var DB *mongo.Database

func ConnectDB() *mongo.Database {
	mongoURL := os.Getenv("MONGO_URI")
	if mongoURL == "" {
		mongoURL = "mongodb://localhost:27017"
	}

	dbName := os.Getenv("DB_NAME")
	if dbName == "" {
		dbName = "tailor_voice"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURL))
	if err != nil {
		log.Fatalf("❌ Failed to connect to MongoDB: %v", err)
	}

	// Check connection
	err = client.Ping(ctx, nil)
	if err != nil {
		log.Fatalf("❌ MongoDB ping failed: %v", err)
	}

	fmt.Println("✅ Connected to MongoDB")
	DB = client.Database(dbName)
	return DB
}

func GetCollection(collectionName string) *mongo.Collection {
	return DB.Collection(collectionName)
}
