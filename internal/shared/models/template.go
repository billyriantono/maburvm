package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// OSTemplate represents an operating system template for VM provisioning
type OSTemplate struct {
	ID          string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name        string         `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	Version     string         `json:"version" gorm:"type:varchar(50);not null" validate:"required,max=50"`
	ImagePath   string         `json:"image_path" gorm:"type:varchar(255);not null" validate:"required"`
	IsActive    bool           `json:"is_active" gorm:"default:true"`
	Description string         `json:"description" gorm:"type:text"`
	CreatedAt   time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt   time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for OSTemplate
func (OSTemplate) TableName() string {
	return "os_templates"
}

// BeforeCreate hook for OSTemplate
func (t *OSTemplate) BeforeCreate(tx *gorm.DB) error {
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	return nil
}

// GetID returns the UUID representation of ID
func (t *OSTemplate) GetID() uuid.UUID {
	id, _ := uuid.Parse(t.ID)
	return id
}

// Validate validates the OSTemplate struct
func (t *OSTemplate) Validate() ValidationErrors {
	return ValidateStruct(t)
}
