package models

import "time"

// NodeMetricSample is one timestamped point of a node's resource usage,
// persisted by the metrics collector so the UI can render historical trends.
// Explicit column names are pinned so the GORM model, the SQL migration, and
// test schemas agree exactly (avoids GORM acronym-casing surprises).
type NodeMetricSample struct {
	ID                   uint64    `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	NodeID               string    `json:"node_id" gorm:"column:node_id;type:uuid;not null;index"`
	CPUUsage             float64   `json:"cpu_usage" gorm:"column:cpu_usage;not null;default:0"`
	MemoryUsage          float64   `json:"memory_usage" gorm:"column:memory_usage;not null;default:0"`
	DiskUsage            float64   `json:"disk_usage" gorm:"column:disk_usage;not null;default:0"`
	NetworkRxBytesPerSec float64   `json:"network_rx_bytes_per_sec" gorm:"column:network_rx_bytes_per_sec;not null;default:0"`
	NetworkTxBytesPerSec float64   `json:"network_tx_bytes_per_sec" gorm:"column:network_tx_bytes_per_sec;not null;default:0"`
	VMCount              int       `json:"vm_count" gorm:"column:vm_count;not null;default:0"`
	Status               string    `json:"status" gorm:"column:status;type:varchar(20);not null;default:''"`
	RecordedAt           time.Time `json:"recorded_at" gorm:"column:recorded_at;not null;index"`
}

// TableName specifies the table name for NodeMetricSample.
func (NodeMetricSample) TableName() string { return "node_metrics" }
