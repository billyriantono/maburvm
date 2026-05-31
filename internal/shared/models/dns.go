package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DNSZone is an authoritative forward DNS zone (e.g. example.com).
type DNSZone struct {
	ID          string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name        string         `json:"name" gorm:"type:varchar(253);not null;uniqueIndex" validate:"required"` // example.com
	TTL         int            `json:"ttl" gorm:"not null;default:3600"`
	PrimaryNS   string         `json:"primary_ns" gorm:"type:varchar(253);not null;default:''"`  // ns1.example.com.
	AdminEmail  string         `json:"admin_email" gorm:"type:varchar(253);not null;default:''"` // hostmaster@example.com
	Description string         `json:"description,omitempty" gorm:"type:text"`
	CreatedAt   time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt   time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt   gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for DNSZone.
func (DNSZone) TableName() string { return "dns_zones" }

// BeforeCreate hook for DNSZone.
func (z *DNSZone) BeforeCreate(tx *gorm.DB) error {
	if z.ID == "" {
		z.ID = uuid.New().String()
	}
	return nil
}

// DNSRecord is a single resource record within a zone.
type DNSRecord struct {
	ID        string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	ZoneID    string         `json:"zone_id" gorm:"type:uuid;not null;index" validate:"required,uuid"`
	Name      string         `json:"name" gorm:"type:varchar(253);not null"` // "@" for apex, or a subdomain label
	Type      string         `json:"type" gorm:"type:varchar(10);not null"`  // A, AAAA, CNAME, MX, TXT, NS, SRV
	Content   string         `json:"content" gorm:"type:text;not null"`      // record value
	TTL       int            `json:"ttl" gorm:"not null;default:3600"`
	Priority  int            `json:"priority" gorm:"not null;default:0"` // MX/SRV priority
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for DNSRecord.
func (DNSRecord) TableName() string { return "dns_records" }

// BeforeCreate hook for DNSRecord.
func (r *DNSRecord) BeforeCreate(tx *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	return nil
}
