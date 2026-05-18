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
	ShopID    string             `bson:"shop_id,omitempty" json:"shop_id,omitempty"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
}

type Measurement struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	CustomerID   primitive.ObjectID `bson:"customer_id" json:"customer_id"`
	Date         time.Time              `bson:"date" json:"date"`
	Data         map[string]interface{} `json:"data" bson:"data"`
	Transcript   string             `json:"transcript" bson:"transcript"`
	Unit         string             `json:"unit" bson:"unit"`
	ShopID       string             `json:"shop_id,omitempty" bson:"shop_id,omitempty"`
	StylePhotos  []string           `json:"style_photos,omitempty" bson:"style_photos,omitempty"`
	ClothPhotos  []string           `json:"cloth_photos,omitempty" bson:"cloth_photos,omitempty"`
	Gender       string             `json:"gender,omitempty" bson:"gender,omitempty"`
	Garment      string             `json:"garment,omitempty" bson:"garment,omitempty"`
	DeliveryDate string             `json:"delivery_date,omitempty" bson:"delivery_date,omitempty"`
	ReminderDate string             `json:"reminder_date,omitempty" bson:"reminder_date,omitempty"`
	TotalCost    float64            `json:"total_cost,omitempty" bson:"total_cost,omitempty"`
	AmountPaid   float64            `json:"amount_paid,omitempty" bson:"amount_paid,omitempty"`
	DesignNotes  string             `json:"design_notes,omitempty" bson:"design_notes,omitempty"`
	ClientPhoto  string             `json:"client_photo,omitempty" bson:"client_photo,omitempty"`
	CreatedAt    time.Time          `json:"created_at" bson:"created_at"`
}

type MeasurementRequest struct {
	CustomerID   string             `json:"customer_id"`
	CustomerName string                 `json:"customer_name"`
	Data         map[string]interface{} `json:"data"`
	Transcript   string             `json:"transcript"`
	Unit         string             `json:"unit"`
	ShopID       string             `json:"shop_id"`
	StylePhotos  []string           `json:"style_photos"`
	ClothPhotos  []string           `json:"cloth_photos"`
	Gender       string             `json:"gender"`
	Garment      string             `json:"garment"`
	DeliveryDate string             `json:"delivery_date"`
	ReminderDate string             `json:"reminder_date"`
	TotalCost    float64            `json:"total_cost"`
	AmountPaid   float64            `json:"amount_paid"`
	DesignNotes  string             `json:"design_notes"`
	ClientPhoto  string             `json:"client_photo"`
}

type MeasurementResponse struct {
	ID           primitive.ObjectID `json:"id"`
	CustomerID   primitive.ObjectID `json:"customer_id"`
	CustomerName string             `json:"customer_name"`
	Date         time.Time              `json:"date"`
	Data         map[string]interface{} `json:"data"`
	Transcript   string             `json:"transcript"`
	Unit         string             `json:"unit"`
	ShopID       string             `json:"shop_id,omitempty"`
	StylePhotos  []string           `json:"style_photos,omitempty"`
	ClothPhotos  []string           `json:"cloth_photos,omitempty"`
	Gender       string             `json:"gender,omitempty"`
	Garment      string             `json:"garment,omitempty"`
	DeliveryDate string             `json:"delivery_date,omitempty"`
	ReminderDate string             `json:"reminder_date,omitempty"`
	TotalCost    float64            `json:"total_cost,omitempty"`
	AmountPaid   float64            `json:"amount_paid,omitempty"`
	DesignNotes  string             `json:"design_notes,omitempty"`
	ClientPhoto  string             `json:"client_photo,omitempty"`
}
type User struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	Email     string             `bson:"email" json:"email"`
	Password  string             `bson:"password" json:"-"`
	ShopName  string             `bson:"shop_name" json:"shop_name"`
	Role      string             `bson:"role" json:"role"`
	CreatedAt time.Time          `bson:"created_at" json:"created_at"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type SignupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	ShopName string `json:"shop_name"`
}

type AuthResponse struct {
	Token    string `json:"token"`
	User     User   `json:"user"`
}

// VoiceEvent is an immutable receipt for every transcription attempt.
// Implements Event Sourcing — Redis holds the fast counter,
// MongoDB holds the truth. One document = one recording attempt.
type VoiceEvent struct {
	ID               primitive.ObjectID `bson:"_id,omitempty"          json:"id,omitempty"`
	ShopID           string             `bson:"shop_id"                json:"shop_id"`
	Timestamp        time.Time          `bson:"timestamp"              json:"timestamp"`
	FileSizeBytes    int64              `bson:"file_size_bytes"        json:"file_size_bytes"`
	EstimatedSeconds int64              `bson:"estimated_seconds"      json:"estimated_seconds"`
	ActualSeconds    int64              `bson:"actual_seconds"         json:"actual_seconds"`
	// Outcome: "success" | "timeout" | "empty" | "quota_exceeded" | "unavailable"
	Outcome     string `bson:"outcome"      json:"outcome"`
	QuotaBefore int64  `bson:"quota_before" json:"quota_before"`
	QuotaAfter  int64  `bson:"quota_after"  json:"quota_after"`
}
