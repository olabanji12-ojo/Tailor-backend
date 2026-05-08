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

func (r *MeasurementRepository) Update(ctx context.Context, id primitive.ObjectID, updates map[string]interface{}) error {
	_, err := r.collection.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": updates})
	return err
}

func (r *MeasurementRepository) Delete(ctx context.Context, id primitive.ObjectID) error {
	_, err := r.collection.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (r *MeasurementRepository) GetAll(ctx context.Context, shopID string, limit, offset int64) ([]models.Measurement, int64, error) {
	opts := options.Find().SetSort(bson.M{"date": -1})
	if limit > 0 {
		opts.SetLimit(limit)
		opts.SetSkip(offset)
	}

	filter := bson.M{}
	if shopID != "" {
		filter = bson.M{"shop_id": shopID}
	}

	total, _ := r.collection.CountDocuments(ctx, filter)

	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(ctx)

	var measurements []models.Measurement
	if err := cursor.All(ctx, &measurements); err != nil {
		return nil, 0, err
	}
	return measurements, total, nil
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
