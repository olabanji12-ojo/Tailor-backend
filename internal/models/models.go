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
	Data       map[string]float64 `json:"data" bson:"data"`
	Transcript string             `json:"transcript" bson:"transcript"`
	Unit       string             `json:"unit" bson:"unit"`
	ShopID      string             `json:"shop_id,omitempty" bson:"shop_id,omitempty"`
	StylePhotos []string           `json:"style_photos,omitempty" bson:"style_photos,omitempty"`
	ClothPhotos []string           `json:"cloth_photos,omitempty" bson:"cloth_photos,omitempty"`
	CreatedAt  time.Time          `json:"created_at" bson:"created_at"`
}

type MeasurementRequest struct {
	CustomerID   string             `json:"customer_id"`
	CustomerName string             `json:"customer_name"`
	Data         map[string]float64 `json:"data"`
	Transcript   string             `json:"transcript"`
	Unit         string             `json:"unit"`
	ShopID       string             `json:"shop_id"`
	StylePhotos  []string           `json:"style_photos"`
	ClothPhotos  []string           `json:"cloth_photos"`
}

type MeasurementResponse struct {
	ID           primitive.ObjectID `json:"id"`
	CustomerID   primitive.ObjectID `json:"customer_id"`
	CustomerName string             `json:"customer_name"`
	Date         time.Time          `json:"date"`
	Data         map[string]float64 `json:"data"`
	Transcript   string             `json:"transcript"`
	Unit         string             `json:"unit"`
	ShopID       string             `json:"shop_id,omitempty"`
	StylePhotos  []string           `json:"style_photos,omitempty"`
	ClothPhotos  []string           `json:"cloth_photos,omitempty"`
}
