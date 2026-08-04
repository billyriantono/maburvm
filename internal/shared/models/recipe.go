package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Recipe is a user's saved first-boot script (first-boot recipes).
// A recipe is selectable when creating a VM; its Script is injected as the
// per-instance cloud-init user-data so it runs once on first boot.
type Recipe struct {
	ID          string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID      string         `json:"user_id" gorm:"type:uuid;not null;index" validate:"required,uuid"`
	Name        string         `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	Description string         `json:"description" gorm:"type:varchar(500);not null;default:''" validate:"max=500"`
	Script      string         `json:"script" gorm:"type:text;not null" validate:"required"`
	CreatedAt   time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt   time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for Recipe.
func (Recipe) TableName() string { return "recipes" }

// BeforeCreate hook for Recipe.
func (r *Recipe) BeforeCreate(tx *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	return nil
}
