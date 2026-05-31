package models

import "time"

// VMMetricSample is one timestamped point of a VM's resource usage, persisted by
// the metrics collector so the UI can render per-VM historical trends.
// Explicit column names are pinned so the GORM model, the SQL migration, and
// test schemas agree exactly.
type VMMetricSample struct {
	ID                   uint64    `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	VMID                 string    `json:"vm_id" gorm:"column:vm_id;type:uuid;not null;index"`
	CPUUsage             float64   `json:"cpu_usage" gorm:"column:cpu_usage;not null;default:0"`           // percent
	MemoryUsage          float64   `json:"memory_usage" gorm:"column:memory_usage;not null;default:0"`     // percent
	MemoryUsedBytes      int64     `json:"memory_used_bytes" gorm:"column:memory_used_bytes;not null;default:0"`
	DiskReadBytesPerSec  int64     `json:"disk_read_bytes_per_sec" gorm:"column:disk_read_bytes_per_sec;not null;default:0"`
	DiskWriteBytesPerSec int64     `json:"disk_write_bytes_per_sec" gorm:"column:disk_write_bytes_per_sec;not null;default:0"`
	NetworkRxBytesPerSec int64     `json:"network_rx_bytes_per_sec" gorm:"column:network_rx_bytes_per_sec;not null;default:0"`
	NetworkTxBytesPerSec int64     `json:"network_tx_bytes_per_sec" gorm:"column:network_tx_bytes_per_sec;not null;default:0"`
	RecordedAt           time.Time `json:"recorded_at" gorm:"column:recorded_at;not null;index"`
}

// TableName specifies the table name for VMMetricSample.
func (VMMetricSample) TableName() string { return "vm_metrics" }
