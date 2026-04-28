package repository

import (
	"context"
	"time"

	"github.com/emman/Tailor-Backend/internal/database"
	"github.com/emman/Tailor-Backend/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type MeasurementRepository struct {
	collection *mongo.Collection
}

func NewMeasurementRepository() *MeasurementRepository {
	return &MeasurementRepository{
		collection: database.GetCollection("measurements"),
	}
}

func (r *MeasurementRepository) Save(ctx context.Context, m *models.Measurement) error {
	m.Date = time.Now()
	result, err := r.collection.InsertOne(ctx, m)
	if err != nil {
		return err
	}
	m.ID = result.InsertedID.(primitive.ObjectID)
	return nil
}

func (r *MeasurementRepository) GetAll(ctx context.Context) ([]models.Measurement, error) {
	opts := options.Find().SetSort(bson.M{"date": -1}) // Newest first
	cursor, err := r.collection.Find(ctx, bson.M{}, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var measurements []models.Measurement
	if err := cursor.All(ctx, &measurements); err != nil {
		return nil, err
	}
	return measurements, nil
}

func (r *MeasurementRepository) GetByCustomerID(ctx context.Context, customerID primitive.ObjectID) ([]models.Measurement, error) {
	cursor, err := r.collection.Find(ctx, bson.M{"customer_id": customerID})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var measurements []models.Measurement
	if err := cursor.All(ctx, &measurements); err != nil {
		return nil, err
	}
	return measurements, nil
}
