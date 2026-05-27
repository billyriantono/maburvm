package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// BandwidthUsage tracks cumulative bandwidth consumption per VM per billing period.
// The agent reports raw byte counters from /sys/class/net/<vnet>/statistics/;
// the panel accumulates deltas into this table.
type BandwidthUsage struct {
	ID        string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID      string         `json:"vm_id" gorm:"type:uuid;not null;index:idx_bw_vm_period,unique" validate:"required,uuid"`
	NodeID    string         `json:"node_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	PeriodStart time.Time    `json:"period_start" gorm:"type:date;not null;index:idx_bw_vm_period,unique"`
	PeriodEnd   time.Time    `json:"period_end" gorm:"type:date;not null"`
	RxBytes   int64          `json:"rx_bytes" gorm:"type:bigint;default:0"`   // Total received bytes this period
	TxBytes   int64          `json:"tx_bytes" gorm:"type:bigint;default:0"`   // Total transmitted bytes this period
	TotalBytes int64         `json:"total_bytes" gorm:"type:bigint;default:0"` // RxBytes + TxBytes
	QuotaBytes int64         `json:"quota_bytes" gorm:"type:bigint;default:0"` // Quota in bytes (0 = unlimited)
	Exceeded  bool           `json:"exceeded" gorm:"type:boolean;default:false"`
	BlockedAt *time.Time     `json:"blocked_at,omitempty" gorm:"type:timestamptz"`
	LastReportedAt time.Time `json:"last_reported_at" gorm:"type:timestamptz;not null;default:NOW()"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for BandwidthUsage
func (BandwidthUsage) TableName() string {
	return "bandwidth_usages"
}

// BeforeCreate hook for BandwidthUsage
func (b *BandwidthUsage) BeforeCreate(tx *gorm.DB) error {
	if b.ID == "" {
		b.ID = uuid.New().String()
	}
	return nil
}

// UsedGB returns the total bandwidth used in GB
func (b *BandwidthUsage) UsedGB() float64 {
	return float64(b.TotalBytes) / (1024 * 1024 * 1024)
}

// QuotaGB returns the quota in GB
func (b *BandwidthUsage) QuotaGB() float64 {
	if b.QuotaBytes == 0 {
		return 0
	}
	return float64(b.QuotaBytes) / (1024 * 1024 * 1024)
}

// UsagePercent returns the percentage of quota used (0-100+)
func (b *BandwidthUsage) UsagePercent() float64 {
	if b.QuotaBytes == 0 {
		return 0
	}
	return (float64(b.TotalBytes) / float64(b.QuotaBytes)) * 100
}
