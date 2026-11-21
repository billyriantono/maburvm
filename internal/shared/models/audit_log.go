package models

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AuditLog represents an audit log entry
type AuditLog struct {
	ID             string          `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID         *string         `json:"user_id" gorm:"type:uuid" validate:"omitempty,uuid"`
	Action         string          `json:"action" gorm:"type:varchar(255);not null" validate:"required,max=255"`
	ResourceType   string          `json:"resource_type" gorm:"type:varchar(50)" validate:"omitempty,max=50"`
	ResourceID     *string         `json:"resource_id" gorm:"type:uuid" validate:"omitempty,uuid"`
	IPAddress      string          `json:"ip_address" gorm:"type:inet" validate:"omitempty,ip"`
	UserAgent      string          `json:"user_agent" gorm:"type:varchar(500)" validate:"omitempty,max=500"`
	Details        map[string]any  `json:"details" gorm:"type:jsonb;serializer:json" validate:"-"`
	BeforeSnapshot *map[string]any `json:"before_snapshot" gorm:"type:jsonb;serializer:json" validate:"-"`
	AfterSnapshot  *map[string]any `json:"after_snapshot" gorm:"type:jsonb;serializer:json" validate:"-"`
	CreatedAt      time.Time       `json:"created_at" gorm:"not null;default:NOW()"`

	// UserEmail and ResourceName are filled for display and are not columns. The
	// stored record keeps UUIDs on purpose — they stay correct after a rename and
	// after the thing they point at is deleted — but a page of truncated UUIDs
	// tells the person reading it nothing.
	UserEmail    string `json:"user_email,omitempty" gorm:"-"`
	ResourceName string `json:"resource_name,omitempty" gorm:"-"`
}

// TableName specifies the table name for AuditLog
func (AuditLog) TableName() string {
	return "audit_logs"
}

type auditWritesKey struct{}

// TrackAuditWrites returns a context under which creating an AuditLog sets the
// flag, so the request audit middleware can skip its generic row when a
// handler already recorded the action in its own words.
func TrackAuditWrites(ctx context.Context) (context.Context, *atomic.Bool) {
	written := new(atomic.Bool)
	return context.WithValue(ctx, auditWritesKey{}, written), written
}

// BeforeCreate hook for AuditLog
func (a *AuditLog) BeforeCreate(tx *gorm.DB) error {
	if written, ok := tx.Statement.Context.Value(auditWritesKey{}).(*atomic.Bool); ok {
		written.Store(true)
	}
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	if a.Details == nil {
		a.Details = make(map[string]any)
	}
	if imp, ok := tx.Statement.Context.Value(impersonatorKey{}).(Impersonator); ok {
		a.Details["impersonator_id"], a.Details["impersonator_email"] = imp.ID, imp.Email
	}
	return nil
}

type impersonatorKey struct{}

// Impersonator is the staff account behind a request made while viewing the
// panel as a customer. Every audit row written under it names both.
type Impersonator struct{ ID, Email string }

// WithImpersonator marks ctx as a request made by staff viewing as a customer.
func WithImpersonator(ctx context.Context, imp Impersonator) context.Context {
	return context.WithValue(ctx, impersonatorKey{}, imp)
}

// ImpersonatorFrom returns the staff account behind ctx, if any.
func ImpersonatorFrom(ctx context.Context) (Impersonator, bool) {
	imp, ok := ctx.Value(impersonatorKey{}).(Impersonator)
	return imp, ok
}

// GetID returns the UUID representation of ID
func (a *AuditLog) GetID() uuid.UUID {
	id, _ := uuid.Parse(a.ID)
	return id
}

// GetUserID returns the UUID representation of UserID if set
func (a *AuditLog) GetUserID() *uuid.UUID {
	if a.UserID == nil {
		return nil
	}
	id, _ := uuid.Parse(*a.UserID)
	return &id
}

// Validate validates the AuditLog struct
func (a *AuditLog) Validate() ValidationErrors {
	return ValidateStruct(a)
}
