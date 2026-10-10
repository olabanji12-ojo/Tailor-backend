package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Customer struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	Name        string             `bson:"name" json:"name"`
	Phone       string             `bson:"phone,omitempty" json:"phone,omitempty"`
	Email       string             `bson:"email,omitempty" json:"email,omitempty"`
	ShopID      string             `bson:"shop_id,omitempty" json:"shop_id,omitempty"`
	UserID      string             `bson:"user_id,omitempty" json:"user_id,omitempty"`
	Gender      string             `bson:"gender,omitempty" json:"gender,omitempty"`
	ClientPhoto string             `bson:"client_photo,omitempty" json:"client_photo,omitempty"`
	CreatedAt   time.Time          `bson:"created_at" json:"created_at"`
	UpdatedAt   time.Time          `bson:"updated_at,omitempty" json:"updated_at,omitempty"`
}

type Measurement struct {
	ID           primitive.ObjectID `bson:"_id,omitempty" json:"id,omitempty"`
	CustomerID   primitive.ObjectID `bson:"customer_id" json:"customer_id"`
	Date         time.Time              `bson:"date" json:"date"`
	Data         map[string]interface{} `json:"data" bson:"data"`
	Transcript   string             `json:"transcript" bson:"transcript"`
	Unit         string             `json:"unit" bson:"unit"`
	ShopID       string             `json:"shop_id,omitempty" bson:"shop_id,omitempty"`
	UserID       string             `json:"user_id,omitempty" bson:"user_id,omitempty"`
	StylePhotos  []string           `json:"style_photos,omitempty" bson:"style_photos,omitempty"`
	ClothPhotos  []string           `json:"cloth_photos,omitempty" bson:"cloth_photos,omitempty"`
	Gender       string             `json:"gender,omitempty" bson:"gender,omitempty"`
	Garment      string             `json:"garment,omitempty" bson:"garment,omitempty"`
	DeliveryDate string             `json:"delivery_date,omitempty" bson:"delivery_date,omitempty"`
	ReminderDate string             `json:"reminder_date,omitempty" bson:"reminder_date,omitempty"`
	TotalCost    float64            `json:"total_cost,omitempty" bson:"total_cost,omitempty"`
	AmountPaid   float64            `json:"amount_paid,omitempty" bson:"amount_paid,omitempty"`
	DesignNotes   string                 `json:"design_notes,omitempty" bson:"design_notes,omitempty"`
	ClientPhoto   string                 `json:"client_photo,omitempty" bson:"client_photo,omitempty"`
	Status        string                 `json:"status,omitempty" bson:"status,omitempty"`
	StatusHistory []StatusHistoryEntry   `json:"status_history,omitempty" bson:"status_history,omitempty"`
	Payments      []PaymentRecord        `json:"payments,omitempty" bson:"payments,omitempty"`
	ClientOperationId string             `json:"client_operation_id,omitempty" bson:"client_operation_id,omitempty"`
	CreatedAt     time.Time              `json:"created_at" bson:"created_at"`
}

type PaymentRecord struct {
	ID             primitive.ObjectID `bson:"_id" json:"id"`
	Amount         float64            `bson:"amount" json:"amount"`
	Method         string             `bson:"method" json:"method"` // "cash", "bank_transfer", "pos", "other"
	Date           time.Time          `bson:"date" json:"date"`
	Note           string             `bson:"note,omitempty" json:"note,omitempty"`
	IdempotencyKey string             `bson:"idempotency_key,omitempty" json:"idempotency_key,omitempty"`
	CreatedBy      string             `bson:"created_by" json:"created_by"`
	CreatedAt      time.Time          `bson:"created_at" json:"created_at"`
}

type PaymentRequest struct {
	Amount         float64 `json:"amount"`
	Method         string  `json:"method"`
	Date           string  `json:"date,omitempty"`
	Note           string  `json:"note,omitempty"`
	IdempotencyKey string  `json:"idempotency_key,omitempty"`
}

type FinancialSummary struct {
	TotalCost     float64         `json:"total_cost"`
	AmountPaid    float64         `json:"amount_paid"`
	TotalPaid     float64         `json:"total_paid"`
	Balance       float64         `json:"balance"`
	PaymentStatus string          `json:"payment_status"`
	Payments      []PaymentRecord `json:"payments"`
}

type AnalyticsSummary struct {
	TotalRevenuePipeline float64        `json:"total_revenue_pipeline"`
	TotalRevenuePaid     float64        `json:"total_revenue_paid"`
	TotalOutstandingDebt float64        `json:"total_outstanding_debt"`
	ActiveJobsCount      int64          `json:"active_jobs_count"`
	CompletedJobsCount   int64          `json:"completed_jobs_count"`
	OverdueJobsCount     int64          `json:"overdue_jobs_count"`
	DebtorsCount         int64          `json:"debtors_count"`
	Debtors              []DebtorRecord `json:"debtors"`
}

type DebtorRecord struct {
	ID            primitive.ObjectID `json:"id" bson:"_id"`
	CustomerID    primitive.ObjectID `json:"customer_id" bson:"customer_id"`
	CustomerName  string             `json:"customer_name" bson:"customer_name"`
	CustomerPhone string             `json:"customer_phone,omitempty" bson:"customer_phone"`
	Garment       string             `json:"garment" bson:"garment"`
	TotalCost     float64            `json:"total_cost" bson:"total_cost"`
	AmountPaid    float64            `json:"amount_paid" bson:"amount_paid"`
	Balance       float64            `json:"balance" bson:"balance"`
	DeliveryDate  string             `json:"delivery_date,omitempty" bson:"delivery_date"`
	ReminderDate  string             `json:"reminder_date,omitempty" bson:"reminder_date"`
	Status        string             `json:"status,omitempty" bson:"status"`
}

type StatusHistoryEntry struct {
	PreviousStatus string    `json:"previous_status" bson:"previous_status"`
	NewStatus      string    `json:"new_status"      bson:"new_status"`
	Timestamp      time.Time `json:"timestamp"       bson:"timestamp"`
	UpdatedBy      string    `json:"updated_by"      bson:"updated_by"`
}

type StatusUpdateRequest struct {
	Status string `json:"status"`
}

type MeasurementRequest struct {
	CustomerID    string                 `json:"customer_id"`
	CustomerName  string                 `json:"customer_name"`
	CustomerPhone string                 `json:"customer_phone,omitempty"`
	Data          map[string]interface{} `json:"data"`
	Measurements  map[string]interface{} `json:"measurements,omitempty"`
	Transcript    string                 `json:"transcript"`
	Unit          string                 `json:"unit"`
	ShopID        string                 `json:"shop_id"`
	StylePhotos   []string               `json:"style_photos"`
	ClothPhotos   []string               `json:"cloth_photos"`
	Gender        string                 `json:"gender"`
	Garment       string                 `json:"garment"`
	DeliveryDate  string                 `json:"delivery_date"`
	ReminderDate  string                 `json:"reminder_date"`
	TotalCost     float64                `json:"total_cost"`
	AmountPaid    float64                `json:"amount_paid"`
	DesignNotes   string                 `json:"design_notes"`
	ClientPhoto   string                 `json:"client_photo"`
	Status        string                 `json:"status,omitempty"`
	ClientOperationId string             `json:"client_operation_id,omitempty"`
}

type MeasurementResponse struct {
	ID            primitive.ObjectID     `json:"id"`
	CustomerID    primitive.ObjectID     `json:"customer_id"`
	CustomerName  string                 `json:"customer_name"`
	CustomerPhone string                 `json:"customer_phone,omitempty"`
	Date          time.Time              `json:"date"`
	Data          map[string]interface{} `json:"data"`
	Transcript    string                 `json:"transcript"`
	Unit          string                 `json:"unit"`
	ShopID        string                 `json:"shop_id,omitempty"`
	UserID        string                 `json:"user_id,omitempty"`
	StylePhotos   []string               `json:"style_photos,omitempty"`
	ClothPhotos   []string               `json:"cloth_photos,omitempty"`
	Gender        string                 `json:"gender,omitempty"`
	Garment       string                 `json:"garment,omitempty"`
	DeliveryDate  string                 `json:"delivery_date,omitempty"`
	ReminderDate  string                 `json:"reminder_date,omitempty"`
	TotalCost     float64                `json:"total_cost,omitempty"`
	AmountPaid    float64                `json:"amount_paid,omitempty"`
	DesignNotes   string                 `json:"design_notes,omitempty"`
	ClientPhoto   string                 `json:"client_photo,omitempty"`
	Status        string                 `json:"status,omitempty"`
	StatusHistory []StatusHistoryEntry   `json:"status_history,omitempty"`
	Payments      []PaymentRecord        `json:"payments,omitempty"`
}

type CustomerUpdateRequest struct {
	Name  string `json:"name,omitempty"`
	Phone string `json:"phone,omitempty"`
	Email string `json:"email,omitempty"`
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

// MonthlyQuota tracks voice transcription usage per boutique per calendar month in MongoDB.
// This serves as the authoritative, concurrency-safe persistence store for voice quotas.
type MonthlyQuota struct {
	ID           string    `bson:"_id"            json:"id"`            // format: "<shop_id>_<YYYY_MM>"
	ShopID       string    `bson:"shop_id"        json:"shop_id"`
	Month        string    `bson:"month"          json:"month"`          // format: "2026_10"
	UsedSeconds  int64     `bson:"used_seconds"   json:"used_seconds"`
	LimitSeconds int64     `bson:"limit_seconds"  json:"limit_seconds"`
	UpdatedAt    time.Time `bson:"updated_at"     json:"updated_at"`
}

