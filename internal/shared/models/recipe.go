package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Recipe is a saved first-boot script (first-boot recipes).
// A recipe is selectable when creating a VM; its Script is injected as the
// per-instance cloud-init user-data so it runs once on first boot.
//
// A public recipe is one staff published to every customer (the app catalog);
// any other is private to the account that saved it.
type Recipe struct {
	ID          string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID      string `json:"user_id" gorm:"type:uuid;not null;index" validate:"required,uuid"`
	Name        string `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	Description string `json:"description" gorm:"type:varchar(500);not null;default:''" validate:"max=500"`
	Script      string `json:"script" gorm:"type:text;not null" validate:"required"`
	// IsPublic and Inputs are read-only here and written by table name
	// (RecipeService), so a test schema without them keeps working.
	IsPublic  bool           `json:"is_public" gorm:"column:is_public;->"`
	Inputs    []RecipeInput  `json:"inputs" gorm:"column:inputs;type:jsonb;serializer:json;->"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// RecipeInput is one value a customer fills in when ordering with a recipe.
// The script names it as {{key}}.
type RecipeInput struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Default     string `json:"default,omitempty"`
	// Secret inputs are typed into a password field and never shown back.
	Secret bool `json:"secret,omitempty"`
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
