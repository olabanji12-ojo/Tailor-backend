package repository

import (
	"context"
	"math"
	"regexp"
	"sort"
	"strings"
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
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now()
	}
	result, err := r.collection.InsertOne(ctx, m)
	if err != nil {
		return err
	}
	m.ID = result.InsertedID.(primitive.ObjectID)
	return nil
}

func (r *MeasurementRepository) FindByClientOperationId(ctx context.Context, clientOpID, userID string) (*models.Measurement, error) {
	if clientOpID == "" {
		return nil, mongo.ErrNoDocuments
	}
	var m models.Measurement
	err := r.collection.FindOne(ctx, bson.M{
		"client_operation_id": clientOpID,
		"user_id":             userID,
	}).Decode(&m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *MeasurementRepository) Update(ctx context.Context, id primitive.ObjectID, userID, shopName string, updates map[string]interface{}) (int64, error) {
	// Stamp immutable user_id to claim legacy records and protect ownership
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

func (r *MeasurementRepository) UpdateStatus(ctx context.Context, id primitive.ObjectID, userID, shopName, newStatus string, entry models.StatusHistoryEntry) (*models.Measurement, error) {
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
	update := bson.M{
		"$set": bson.M{
			"status":  newStatus,
			"user_id": userID,
		},
		"$push": bson.M{
			"status_history": entry,
		},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var updated models.Measurement
	err := r.collection.FindOneAndUpdate(ctx, filter, update, opts).Decode(&updated)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

func (r *MeasurementRepository) Delete(ctx context.Context, id primitive.ObjectID, userID, shopName string) (int64, error) {
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
	res, err := r.collection.DeleteOne(ctx, filter)
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func (r *MeasurementRepository) FindByID(ctx context.Context, id primitive.ObjectID, userID, shopName string) (*models.Measurement, error) {
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
	var m models.Measurement
	err := r.collection.FindOne(ctx, filter).Decode(&m)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func (r *MeasurementRepository) GetAll(ctx context.Context, userID, shopName string, limit, offset int64) ([]models.Measurement, int64, error) {
	opts := options.Find().SetSort(bson.M{"date": -1})
	if limit > 0 {
		opts.SetLimit(limit)
		opts.SetSkip(offset)
	}

	filter := BuildTenantFilter(userID, shopName)

	total, _ := r.collection.CountDocuments(ctx, filter)

	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(ctx)

	measurements := make([]models.Measurement, 0)
	if err := cursor.All(ctx, &measurements); err != nil {
		return nil, 0, err
	}
	return measurements, total, nil
}

func (r *MeasurementRepository) GetByCustomerID(ctx context.Context, customerID primitive.ObjectID, userID, shopName string) ([]models.Measurement, error) {
	filter := bson.M{
		"customer_id": customerID,
		"$or": []bson.M{
			{"user_id": userID},
			{
				"shop_id": shopName,
				"user_id": bson.M{"$in": []interface{}{nil, ""}},
			},
		},
	}
	opts := options.Find().SetSort(bson.D{{Key: "date", Value: -1}, {Key: "_id", Value: -1}})
	cursor, err := r.collection.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	measurements := make([]models.Measurement, 0)
	if err := cursor.All(ctx, &measurements); err != nil {
		return nil, err
	}
	return measurements, nil
}

// AddPayment records a new installment payment with idempotency protection and atomic balance updating
func (r *MeasurementRepository) AddPayment(ctx context.Context, id primitive.ObjectID, userID, shopName string, payment models.PaymentRecord) (*models.Measurement, error) {
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

	// 1. Fetch current document to check idempotency and ownership
	var current models.Measurement
	err := r.collection.FindOne(ctx, filter).Decode(&current)
	if err != nil {
		return nil, err
	}

	// Idempotency: if payment with this key was already recorded, return current document safely
	if payment.IdempotencyKey != "" {
		for _, existing := range current.Payments {
			if existing.IdempotencyKey == payment.IdempotencyKey {
				return &current, nil
			}
		}
	}

	// Calculate new amount_paid
	newAmountPaid := current.AmountPaid + payment.Amount

	var update bson.M
	if len(current.Payments) == 0 {
		update = bson.M{
			"$set": bson.M{
				"amount_paid": newAmountPaid,
				"user_id":     userID,
				"payments":    []models.PaymentRecord{payment},
			},
		}
	} else {
		update = bson.M{
			"$set": bson.M{
				"amount_paid": newAmountPaid,
				"user_id":     userID,
			},
			"$push": bson.M{
				"payments": payment,
			},
		}
	}

	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var updated models.Measurement
	err = r.collection.FindOneAndUpdate(ctx, filter, update, opts).Decode(&updated)
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// SearchAndPaginate performs tenant-scoped, regex-safe, multi-field archive search and server-side pagination.
// Matches across customer name, customer phone, garment, and production status.
func (r *MeasurementRepository) SearchAndPaginate(
	ctx context.Context,
	userID, shopName string,
	query string,
	statusFilter string,
	limit, offset int64,
) ([]models.MeasurementResponse, int64, error) {
	tenantFilter := BuildTenantFilter(userID, shopName)

	var pipeline mongo.Pipeline

	// 1. Initial tenant filter on measurements
	pipeline = append(pipeline, bson.D{{Key: "$match", Value: tenantFilter}})

	// 2. Production status filter (if provided and not empty/"all")
	normStatus := strings.ToLower(strings.TrimSpace(statusFilter))
	if normStatus != "" && normStatus != "all" {
		switch normStatus {
		case "active":
			pipeline = append(pipeline, bson.D{{
				Key: "$match",
				Value: bson.M{
					"status": bson.M{"$ne": "delivered"},
				},
			}})
		case "needs_review", "needs review":
			pipeline = append(pipeline, bson.D{{
				Key: "$match",
				Value: bson.M{
					"$or": []bson.M{
						{"status": bson.M{"$in": []interface{}{nil, ""}}},
						{"status": bson.M{"$nin": []interface{}{"received", "cutting", "sewing", "fitting", "ready", "delivered"}}},
					},
				},
			}})
		default:
			pipeline = append(pipeline, bson.D{{
				Key: "$match",
				Value: bson.M{"status": normStatus},
			}})
		}
	}

	// 3. Lookup customer details from customers collection
	pipeline = append(pipeline, bson.D{{
		Key: "$lookup",
		Value: bson.M{
			"from":         "customers",
			"localField":   "customer_id",
			"foreignField": "_id",
			"as":           "customer_details",
		},
	}})

	// 4. Search query across customer name, customer phone, and garment
	cleanQ := strings.TrimSpace(query)
	if cleanQ != "" {
		quotedQ := regexp.QuoteMeta(cleanQ)

		searchOr := []bson.M{
			{"garment": bson.M{"$regex": primitive.Regex{Pattern: quotedQ, Options: "i"}}},
			{"customer_details.name": bson.M{"$regex": primitive.Regex{Pattern: quotedQ, Options: "i"}}},
			{"customer_details.phone": bson.M{"$regex": primitive.Regex{Pattern: quotedQ, Options: "i"}}},
		}

		// Normalize phone searches (strip non-digits to handle Nigerian phone number formats: +234 / 080)
		digitsOnly := regexp.MustCompile(`\D`).ReplaceAllString(cleanQ, "")
		if len(digitsOnly) >= 7 {
			searchOr = append(searchOr, bson.M{
				"customer_details.phone": bson.M{"$regex": primitive.Regex{Pattern: digitsOnly, Options: "i"}},
			})
			if strings.HasPrefix(digitsOnly, "234") && len(digitsOnly) > 3 {
				searchOr = append(searchOr, bson.M{
					"customer_details.phone": bson.M{"$regex": primitive.Regex{Pattern: digitsOnly[3:], Options: "i"}},
				})
			} else if strings.HasPrefix(digitsOnly, "0") && len(digitsOnly) > 1 {
				searchOr = append(searchOr, bson.M{
					"customer_details.phone": bson.M{"$regex": primitive.Regex{Pattern: digitsOnly[1:], Options: "i"}},
				})
			}
		}

		pipeline = append(pipeline, bson.D{{
			Key: "$match",
			Value: bson.M{"$or": searchOr},
		}})
	}

	// 5. Facet for total count and deterministic paginated data slice
	pipeline = append(pipeline, bson.D{{
		Key: "$facet",
		Value: bson.M{
			"metadata": []bson.M{
				{"$count": "total"},
			},
			"data": []bson.M{
				{"$sort": bson.D{{Key: "date", Value: -1}, {Key: "_id", Value: -1}}},
				{"$skip": offset},
				{"$limit": limit},
			},
		},
	}})

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(ctx)

	type FacetResult struct {
		Metadata []struct {
			Total int64 `bson:"total"`
		} `bson:"metadata"`
		Data []struct {
			models.Measurement `bson:",inline"`
			CustomerDetails    []models.Customer `bson:"customer_details"`
		} `bson:"data"`
	}

	var results []FacetResult
	if err := cursor.All(ctx, &results); err != nil {
		return nil, 0, err
	}

	if len(results) == 0 {
		return []models.MeasurementResponse{}, 0, nil
	}

	total := int64(0)
	if len(results[0].Metadata) > 0 {
		total = results[0].Metadata[0].Total
	}

	responses := make([]models.MeasurementResponse, 0, len(results[0].Data))
	for _, item := range results[0].Data {
		name := "Unknown"
		phone := ""
		if len(item.CustomerDetails) > 0 {
			name = item.CustomerDetails[0].Name
			phone = item.CustomerDetails[0].Phone
		}

		responses = append(responses, models.MeasurementResponse{
			ID:            item.ID,
			CustomerID:    item.CustomerID,
			CustomerName:  name,
			CustomerPhone: phone,
			Date:          item.Date,
			Data:          item.Data,
			Transcript:    item.Transcript,
			Unit:          item.Unit,
			ShopID:        item.ShopID,
			UserID:        item.UserID,
			StylePhotos:   item.StylePhotos,
			ClothPhotos:   item.ClothPhotos,
			Gender:        item.Gender,
			Garment:       item.Garment,
			DeliveryDate:  item.DeliveryDate,
			ReminderDate:  item.ReminderDate,
			TotalCost:     item.TotalCost,
			AmountPaid:    item.AmountPaid,
			DesignNotes:   item.DesignNotes,
			ClientPhoto:   item.ClientPhoto,
			Status:        item.Status,
			StatusHistory: item.StatusHistory,
			Payments:      item.Payments,
		})
	}

	return responses, total, nil
}

// GetAnalyticsSummary calculates authoritative financial totals, job counts, and debtor rosters across the tenant's entire database.
func (r *MeasurementRepository) GetAnalyticsSummary(ctx context.Context, userID, shopName string) (*models.AnalyticsSummary, error) {
	tenantFilter := BuildTenantFilter(userID, shopName)

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: tenantFilter}},
		bson.D{{Key: "$lookup", Value: bson.M{
			"from":         "customers",
			"localField":   "customer_id",
			"foreignField": "_id",
			"as":           "customer_details",
		}}},
	}

	cursor, err := r.collection.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	type OrderWithCustomer struct {
		models.Measurement `bson:",inline"`
		CustomerDetails    []models.Customer `bson:"customer_details"`
	}

	var orders []OrderWithCustomer
	if err := cursor.All(ctx, &orders); err != nil {
		return nil, err
	}

	summary := &models.AnalyticsSummary{
		Debtors: []models.DebtorRecord{},
	}

	todayStr := time.Now().Format("2006-01-02")

	for _, ord := range orders {
		totalCost := ord.TotalCost
		amountPaid := ord.AmountPaid
		balance := math.Max(0, totalCost-amountPaid)

		summary.TotalRevenuePipeline += totalCost
		summary.TotalRevenuePaid += amountPaid

		if balance > 0 {
			summary.TotalOutstandingDebt += balance
			summary.DebtorsCount++

			name := "Unknown"
			phone := ""
			if len(ord.CustomerDetails) > 0 {
				name = ord.CustomerDetails[0].Name
				phone = ord.CustomerDetails[0].Phone
			}

			summary.Debtors = append(summary.Debtors, models.DebtorRecord{
				ID:            ord.ID,
				CustomerID:    ord.CustomerID,
				CustomerName:  name,
				CustomerPhone: phone,
				Garment:       ord.Garment,
				TotalCost:     totalCost,
				AmountPaid:    amountPaid,
				Balance:       balance,
				DeliveryDate:  ord.DeliveryDate,
				ReminderDate:  ord.ReminderDate,
				Status:        ord.Status,
			})
		}

		orderStatus := strings.ToLower(strings.TrimSpace(ord.Status))
		if orderStatus == "delivered" {
			summary.CompletedJobsCount++
		} else {
			summary.ActiveJobsCount++
			if ord.DeliveryDate != "" {
				delDate := ord.DeliveryDate
				if len(delDate) >= 10 {
					delDate = delDate[:10]
				}
				if delDate < todayStr {
					summary.OverdueJobsCount++
				}
			}
		}
	}

	// Sort Debtors by balance descending
	sort.Slice(summary.Debtors, func(i, j int) bool {
		return summary.Debtors[i].Balance > summary.Debtors[j].Balance
	})

	// Round financial numbers to 2 decimal places
	summary.TotalRevenuePipeline = math.Round(summary.TotalRevenuePipeline*100) / 100
	summary.TotalRevenuePaid = math.Round(summary.TotalRevenuePaid*100) / 100
	summary.TotalOutstandingDebt = math.Round(summary.TotalOutstandingDebt*100) / 100

	return summary, nil
}
