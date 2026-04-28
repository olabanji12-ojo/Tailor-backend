package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Customer struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	Name      string             `bson:"name" json:"name"`
	Phone     string             `bson:"phone,omitempty" json:"phone,omitempty"`
	Email     string             `bson:"email,omitempty" json:"email,omitempty"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
}

type Measurement struct {
	ID         primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	CustomerID primitive.ObjectID `bson:"customer_id" json:"customer_id"`
	Date       time.Time          `bson:"date" json:"date"`
	Data       map[string]float64 `bson:"data" json:"data"`
	Transcript string             `bson:"transcript" json:"transcript"`
	Unit       string             `bson:"unit" json:"unit"` // "in" or "cm"
}

type MeasurementRequest struct {
	CustomerName string             `json:"customer_name"`
	Data         map[string]float64 `json:"data"`
	Transcript   string             `json:"transcript"`
	Unit         string             `json:"unit"`
}

type MeasurementResponse struct {
	ID           primitive.ObjectID `json:"id"`
	CustomerID   primitive.ObjectID `json:"customer_id"`
	CustomerName string             `json:"customer_name"`
	Date         time.Time          `json:"date"`
	Data         map[string]float64 `json:"data"`
	Transcript   string             `json:"transcript"`
	Unit         string             `json:"unit"`
}
