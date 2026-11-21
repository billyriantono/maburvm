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
	ID          string     `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID        string     `json:"vm_id" gorm:"type:uuid;not null;index:idx_bw_vm_period,unique" validate:"required,uuid"`
	NodeID      string     `json:"node_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	PeriodStart time.Time  `json:"period_start" gorm:"type:date;not null;index:idx_bw_vm_period,unique"`
	PeriodEnd   time.Time  `json:"period_end" gorm:"type:date;not null"`
	RxBytes     int64      `json:"rx_bytes" gorm:"type:bigint;default:0"`    // Total received bytes this period
	TxBytes     int64      `json:"tx_bytes" gorm:"type:bigint;default:0"`    // Total transmitted bytes this period
	TotalBytes  int64      `json:"total_bytes" gorm:"type:bigint;default:0"` // RxBytes + TxBytes
	QuotaBytes  int64      `json:"quota_bytes" gorm:"type:bigint;default:0"` // Quota in bytes (0 = unlimited)
	Exceeded    bool       `json:"exceeded" gorm:"type:boolean;default:false"`
	BlockedAt   *time.Time `json:"blocked_at,omitempty" gorm:"type:timestamptz"`
	// WarnedPercent is the highest usage warning sent to the owner this period.
	// Read-only here; the collector writes it by table name.
	WarnedPercent  int            `json:"warned_percent" gorm:"column:warned_percent;->"`
	LastReportedAt time.Time      `json:"last_reported_at" gorm:"type:timestamptz;not null;default:NOW()"`
	CreatedAt      time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt      time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt      gorm.DeletedAt `json:"-" gorm:"index"`
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

// Which direction of a VM's traffic counts toward its transfer allowance.
// "out" is what the VM sends, which is what clouds bill.
const (
	TransferCountBoth = "both"
	TransferCountOut  = "out"
	TransferCountIn   = "in"
)

// ValidTransferCount reports whether s is one of the TransferCount values.
func ValidTransferCount(s string) bool {
	return s == TransferCountBoth || s == TransferCountOut || s == TransferCountIn
}

// CountedBytes is the part of a VM's traffic that counts toward its allowance.
// rx is what the VM received and tx what it sent: libvirt reports interface
// counters from the guest's side.
func CountedBytes(counts string, rx, tx int64) int64 {
	switch counts {
	case TransferCountOut:
		return tx
	case TransferCountIn:
		return rx
	default:
		return rx + tx
	}
}

// MaxTransferResetDay is the last day an allowance may restart on, so every
// month has it.
const MaxTransferResetDay = 28

// TransferPeriod is the allowance period containing now for an allowance that
// restarts on resetDay (1-28; anything else means the 1st), in UTC: start is
// the most recent reset at midnight, end the last second before the next.
func TransferPeriod(now time.Time, resetDay int) (start, end time.Time) {
	if resetDay < 1 || resetDay > MaxTransferResetDay {
		resetDay = 1
	}
	now = now.UTC()
	start = time.Date(now.Year(), now.Month(), resetDay, 0, 0, 0, 0, time.UTC)
	if start.After(now) {
		start = start.AddDate(0, -1, 0)
	}
	return start, start.AddDate(0, 1, 0).Add(-time.Second)
}
