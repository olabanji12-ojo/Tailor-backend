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

// BuildTenantFilter builds a backward-compatible tenant filter:
// 1. Matches records with user_id == userID (immutable primary ownership)
// 2. OR matches legacy records where shop_id == shopName AND user_id is empty/null.
func BuildTenantFilter(userID, shopName string) bson.M {
	return bson.M{
		"$or": []bson.M{
			{"user_id": userID},
			{
				"shop_id": shopName,
				"user_id": bson.M{"$in": []interface{}{nil, ""}},
			},
		},
	}
}

type CustomerRepository struct {
	collection *mongo.Collection
}

func NewCustomerRepository() *CustomerRepository {
	return &CustomerRepository{
		collection: database.GetCollection("customers"),
	}
}

func (r *CustomerRepository) Create(ctx context.Context, customer *models.Customer) error {
	now := time.Now()
	customer.CreatedAt = now
	customer.UpdatedAt = now
	result, err := r.collection.InsertOne(ctx, customer)
	if err != nil {
		return err
	}
	customer.ID = result.InsertedID.(primitive.ObjectID)
	return nil
}

func (r *CustomerRepository) FindByName(ctx context.Context, name string, userID, shopName string) (*models.Customer, error) {
	filter := bson.M{
		"name": name,
		"$or": []bson.M{
			{"user_id": userID},
			{
				"shop_id": shopName,
				"user_id": bson.M{"$in": []interface{}{nil, ""}},
			},
		},
	}
	var customer models.Customer
	err := r.collection.FindOne(ctx, filter).Decode(&customer)
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *CustomerRepository) FindByID(ctx context.Context, id primitive.ObjectID, userID, shopName string) (*models.Customer, error) {
	filter := bson.M{
		"_id": id,
		"$or": []bson.M{
			{"user_id": userID},
			{
				"shop_id": shopName,
				"user_id": bson.M{"$in": []interface{}{nil, ""}},
			},
		},
	}
	var customer models.Customer
	err := r.collection.FindOne(ctx, filter).Decode(&customer)
	if err != nil {
		return nil, err
	}
	return &customer, nil
}

func (r *CustomerRepository) GetAll(ctx context.Context, userID, shopName string) ([]models.Customer, error) {
	filter := BuildTenantFilter(userID, shopName)
	cursor, err := r.collection.Find(ctx, filter)
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

func (r *CustomerRepository) Search(ctx context.Context, userID, shopName string, query string, limit int64) ([]models.Customer, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}

	tenantFilter := BuildTenantFilter(userID, shopName)
	var filter bson.M
	if query != "" {
		filter = bson.M{
			"$and": []bson.M{
				tenantFilter,
				{
					"$or": []bson.M{
						{"name": bson.M{"$regex": primitive.Regex{Pattern: query, Options: "i"}}},
						{"phone": bson.M{"$regex": primitive.Regex{Pattern: query, Options: "i"}}},
					},
				},
			},
		}
	} else {
		filter = tenantFilter
	}

	opts := options.Find().
		SetLimit(limit).
		SetSort(bson.D{{Key: "updated_at", Value: -1}, {Key: "created_at", Value: -1}})

	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	customers := make([]models.Customer, 0)
	if err := cursor.All(ctx, &customers); err != nil {
		return nil, err
	}
	return customers, nil
}

func (r *CustomerRepository) Update(ctx context.Context, id primitive.ObjectID, userID, shopName string, updates bson.M) (int64, error) {
	updates["updated_at"] = time.Now()
	// Stamp immutable user_id to lock down ownership permanently
	updates["user_id"] = userID
	delete(updates, "_id")
	delete(updates, "shop_id")

	filter := bson.M{
		"_id": id,
		"$or": []bson.M{
			{"user_id": userID},
			{
				"shop_id": shopName,
				"user_id": bson.M{"$in": []interface{}{nil, ""}},
			},
		},
	}
	res, err := r.collection.UpdateOne(ctx, filter, bson.M{"$set": updates})
	if err != nil {
		return 0, err
	}
	return res.MatchedCount, nil
}

func (r *CustomerRepository) UpdatePhone(ctx context.Context, id primitive.ObjectID, userID, shopName, phone string) (int64, error) {
	return r.Update(ctx, id, userID, shopName, bson.M{"phone": phone})
}
