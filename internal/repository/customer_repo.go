package repository

import (
	"context"
	"time"

	"github.com/emman/Tailor-Backend/internal/database"
	"github.com/emman/Tailor-Backend/internal/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

type CustomerRepository struct {
	collection *mongo.Collection
}

func NewCustomerRepository() *CustomerRepository {
	return &CustomerRepository{
		collection: database.GetCollection("customers"),
	}
}

func (r *CustomerRepository) Create(ctx context.Context, customer *models.Customer) error {
	customer.CreatedAt = time.Now()
	result, err := r.collection.InsertOne(ctx, customer)
	if err != nil {
		return err
	}
	customer.ID = result.InsertedID.(primitive.ObjectID)
	return nil
}

func (r *CustomerRepository) FindByName(ctx context.Context, name string) (*models.Customer, error) {
	var customer models.Customer
	err := r.collection.FindOne(ctx, bson.M{"name": name}).Decode(&customer)
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *CustomerRepository) FindByID(ctx context.Context, id primitive.ObjectID) (*models.Customer, error) {
	var customer models.Customer
	err := r.collection.FindOne(ctx, bson.M{"_id": id}).Decode(&customer)
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *CustomerRepository) GetAll(ctx context.Context) ([]models.Customer, error) {
	cursor, err := r.collection.Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var customers []models.Customer
	if err := cursor.All(ctx, &customers); err != nil {
		return nil, err
	}
	return customers, nil
}
