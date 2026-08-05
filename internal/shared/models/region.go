package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Region is the location a customer picks when ordering: a city, holding one or
// more nodes.
//
// With one node per region, the node-scoped resources (VPCs, floating IPs)
// coincide exactly with region boundaries, so a customer sees the behaviour they
// expect from a region. A second node in the same region breaks that coincidence
// — see proposals/002-regions.md before adding one.
type Region struct {
	ID   string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Slug string `json:"slug" gorm:"type:varchar(64);not null" validate:"required,max=64"`
	Name string `json:"name" gorm:"type:varchar(128);not null" validate:"required,max=128"`
	// Country is ISO 3166-1 alpha-2 and drives the flag shown to customers. It is
	// stored rather than guessed from the name, so renaming a city never changes
	// which flag appears.
	Country string `json:"country" gorm:"type:char(2);not null;default:'ID'" validate:"required,len=2"`
	// Enabled hides a region from ordering without deleting it or its nodes —
	// what an operator needs when a site is full or under maintenance.
	Enabled   bool           `json:"enabled" gorm:"not null;default:true"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`

	// NodeCount is filled for listings so the UI can hide regions with no
	// capacity; it is not a column.
	NodeCount int `json:"node_count" gorm:"-"`
}

func (Region) TableName() string { return "regions" }

func (r *Region) BeforeCreate(tx *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	if r.Country == "" {
		r.Country = "ID"
	}
	return nil
}

func (r *Region) Validate() ValidationErrors { return ValidateStruct(r) }
