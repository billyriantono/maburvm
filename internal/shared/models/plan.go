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

// Outbound mail policies of a plan.
const (
	SMTPAllowed = "allowed"
	SMTPBlocked = "blocked"
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
	DataQuotaGB       int64  `json:"data_quota_gb" gorm:"type:bigint;default:0" validate:"omitempty,min=0"`
	OverQuotaPolicy   string `json:"over_quota_policy" gorm:"type:varchar(20);default:'throttle'" validate:"omitempty,oneof=throttle overage suspend"`
	ThrottleSpeedMbps int    `json:"throttle_speed_mbps" gorm:"default:0" validate:"omitempty,min=0"` // speed after quota when policy=throttle
	// SMTPPolicy decides whether new VMs on the plan may send mail out on
	// port 25 (SMTPAllowed or SMTPBlocked). Changing it leaves existing VMs as
	// they are.
	SMTPPolicy string `json:"smtp_policy" gorm:"column:smtp_policy;type:varchar(16);not null;default:'allowed'" validate:"omitempty,oneof=allowed blocked"`
	// Limits are the CPU and disk caps a VM gets when it is ordered on, or
	// moved to, the plan. Changing them leaves existing VMs as they are.
	Limits VMLimits `json:"limits" gorm:"column:limits;type:jsonb;serializer:json;not null;default:'{}'"`
	// Display price shown to customers; nil = not shown. Charging is the
	// billing system's job, not the panel's.
	PriceMonthly *float64 `json:"price_monthly" gorm:"column:price_monthly;type:numeric(14,4)"`
	PriceHourly  *float64 `json:"price_hourly" gorm:"column:price_hourly;type:numeric(14,4)"`
	Currency     string   `json:"currency" gorm:"column:currency;type:varchar(3);not null;default:''"`
	// Per-VM allowances, read from the plan whenever one is used, so a change
	// reaches every VM on the plan at once. nil = no limit, 0 = none.
	MaxSnapshots *int `json:"max_snapshots" gorm:"column:max_snapshots"`
	MaxBackups   *int `json:"max_backups" gorm:"column:max_backups"`
	MaxExtraIPv4 *int `json:"max_extra_ipv4" gorm:"column:max_extra_ipv4"`
	MaxExtraIPv6 *int `json:"max_extra_ipv6" gorm:"column:max_extra_ipv6"`
	// DefaultFirewallGroupID is the group VMs created on the plan get instead
	// of the built-in default rules; nil keeps those. Existing VMs keep theirs.
	DefaultFirewallGroupID *string `json:"default_firewall_group_id" gorm:"column:default_firewall_group_id;type:uuid"`
	// NetworkType is PlanNetworkPublic or PlanNetworkNAT. A NAT VM gets
	// NATPorts public ports on its node and may route NATHostnames hostnames.
	NetworkType  string         `json:"network_type" gorm:"column:network_type;type:varchar(8);not null;default:'public'"`
	NATPorts     int            `json:"nat_ports" gorm:"column:nat_ports;not null;default:0"`
	NATHostnames int            `json:"nat_hostnames" gorm:"column:nat_hostnames;not null;default:0"`
	Description  string         `json:"description,omitempty" gorm:"type:text"`
	IsActive     bool           `json:"is_active" gorm:"default:true"`
	CreatedAt    time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt    time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
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
