package models

import "time"

// UserQuota caps the resources a user may allocate across all their VMs.
// A zero value for any limit means "unlimited" for that dimension.
type UserQuota struct {
	// Explicit column names are required: GORM's default naming would turn
	// MaxVCPU into "max_v_cpu", which would not match migration 012 ("max_vcpu").
	UserID    string    `json:"user_id" gorm:"column:user_id;type:uuid;primaryKey"`
	MaxVMs    int       `json:"max_vms" gorm:"column:max_vms;not null;default:0"`        // max number of VMs
	MaxVCPU   int       `json:"max_vcpu" gorm:"column:max_vcpu;not null;default:0"`      // total vCPUs across VMs
	MaxRAMMB  int       `json:"max_ram_mb" gorm:"column:max_ram_mb;not null;default:0"`  // total RAM in MB
	MaxDiskGB int       `json:"max_disk_gb" gorm:"column:max_disk_gb;not null;default:0"` // total disk in GB
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at;not null;default:NOW()"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at;not null;default:NOW()"`
}

// TableName specifies the table name for UserQuota.
func (UserQuota) TableName() string { return "user_quotas" }
