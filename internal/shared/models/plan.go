package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Over-quota policies: what happens to a VM once it uses up its plan's monthly
// data quota.
const (
	OverQuotaThrottle = "throttle" // drop speed to ThrottleSpeedMbps until the quota resets
	OverQuotaOverage  = "overage"  // keep full speed, emit a billing webhook to charge the overage
	OverQuotaSuspend  = "suspend"  // stop the VM until the quota resets
)

// Plan is a VPS flavor: a named bundle of resources users pick when creating a
// VM ("Plans").
type Plan struct {
	ID            string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name          string `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	CPU           int    `json:"cpu" gorm:"not null" validate:"required,min=1,max=128"`       // vCPUs
	RAM           int    `json:"ram" gorm:"not null" validate:"required,min=128,max=1048576"` // MB
	Disk          int    `json:"disk" gorm:"not null" validate:"required,min=1,max=1048576"`  // GB
	BandwidthMbps int    `json:"bandwidth_mbps" gorm:"default:0" validate:"omitempty,min=0"`  // 0 = unlimited
	// Monthly DATA quota (GB, 0 = unlimited) and what happens once it is used up.
	DataQuotaGB       int64          `json:"data_quota_gb" gorm:"type:bigint;default:0" validate:"omitempty,min=0"`
	OverQuotaPolicy   string         `json:"over_quota_policy" gorm:"type:varchar(20);default:'throttle'" validate:"omitempty,oneof=throttle overage suspend"`
	ThrottleSpeedMbps int            `json:"throttle_speed_mbps" gorm:"default:0" validate:"omitempty,min=0"` // speed after quota when policy=throttle
	Description       string         `json:"description,omitempty" gorm:"type:text"`
	IsActive          bool           `json:"is_active" gorm:"default:true"`
	CreatedAt         time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt         time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt         gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for Plan.
func (Plan) TableName() string { return "plans" }

// BeforeCreate hook for Plan.
func (p *Plan) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	return nil
}

// Validate validates the Plan struct.
func (p *Plan) Validate() ValidationErrors { return ValidateStruct(p) }
