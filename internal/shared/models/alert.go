package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// What an alert is about.
const (
	AlertKindNodeOffline  = "node_offline"
	AlertKindNodeSilent   = "node_silent"
	AlertKindStoragePool  = "storage_pool"
	AlertKindBackupFailed = "backup_failed"
	AlertKindJobFailed    = "job_failed"
	AlertKindFirewallOff  = "firewall_off"
	AlertKindNetworkDrift = "network_drift"
	AlertKindVMCrash      = "vm_crash"
	AlertKindAbuse        = "abuse"
)

const (
	AlertSeverityCritical = "critical"
	AlertSeverityWarning  = "warning"
)

// An alert about a lasting condition is firing until the condition has stayed
// clear, then resolved. One about something that happened once is an event.
const (
	AlertStateFiring   = "firing"
	AlertStateResolved = "resolved"
	AlertStateEvent    = "event"
)

// Alert is one episode of a condition operators are told about, or one event.
// The firing row of a key is the alert's whole state, which is what lets it
// survive a panel restart; closed rows are the history.
type Alert struct {
	ID           string    `json:"id" gorm:"column:id;primaryKey"`
	Key          string    `json:"key" gorm:"column:alert_key"`
	Kind         string    `json:"kind" gorm:"column:kind"`
	Severity     string    `json:"severity" gorm:"column:severity"`
	State        string    `json:"state" gorm:"column:state"`
	Title        string    `json:"title" gorm:"column:title"`
	Detail       string    `json:"detail" gorm:"column:detail"`
	ResourceType string    `json:"resource_type" gorm:"column:resource_type"`
	ResourceID   string    `json:"resource_id" gorm:"column:resource_id"`
	ResourceName string    `json:"resource_name" gorm:"column:resource_name"`
	FiredAt      time.Time `json:"fired_at" gorm:"column:fired_at"`
	// ClearSince is when the condition was last seen clear while the alert was
	// still open; it resolves once that has lasted the settle time.
	ClearSince *time.Time `json:"clear_since" gorm:"column:clear_since"`
	ResolvedAt *time.Time `json:"resolved_at" gorm:"column:resolved_at"`
	// NotifiedAt is when telling operators was settled, sent or given up;
	// nil is still to do. Delivered says whether anything actually went out.
	NotifiedAt         *time.Time `json:"notified_at" gorm:"column:notified_at"`
	RecoveryNotifiedAt *time.Time `json:"recovery_notified_at" gorm:"column:recovery_notified_at"`
	Delivered          bool       `json:"delivered" gorm:"column:delivered"`
	Attempts           int        `json:"-" gorm:"column:attempts"`
	// Delivery is the outcome in words, for the history page.
	Delivery string `json:"delivery" gorm:"column:delivery"`
}

// TableName specifies the table name for Alert.
func (Alert) TableName() string { return "alerts" }

// BeforeCreate hook for Alert.
func (a *Alert) BeforeCreate(*gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	return nil
}
