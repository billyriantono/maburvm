package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// StoragePool represents a storage pool on a node
type StoragePool struct {
	ID             string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name           string         `json:"name" gorm:"type:varchar(100);not null;uniqueIndex:idx_storage_pool_name_node" validate:"required,max=100"`
	Type           string         `json:"type" gorm:"type:varchar(50);not null"`                     // dir, lvm, zfs, etc.
	Status         string         `json:"status" gorm:"type:varchar(20);not null;default:'offline'"` // online, offline, degraded
	TotalSpace     int64          `json:"total_space" gorm:"not null;default:0"`                     // bytes
	UsedSpace      int64          `json:"used_space" gorm:"not null;default:0"`                      // bytes
	AvailableSpace int64          `json:"available_space" gorm:"not null;default:0"`                 // bytes
	Path           string         `json:"path" gorm:"type:varchar(255);not null"`                    // filesystem path
	NodeID         string         `json:"node_id" gorm:"type:uuid;not null;uniqueIndex:idx_storage_pool_name_node;index"`
	CreatedAt      time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt      time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt      gorm.DeletedAt `json:"-" gorm:"index"`

	// Relations
	Node *Node `json:"node,omitempty" gorm:"foreignKey:NodeID"`
}

// TableName specifies the table name for StoragePool
func (StoragePool) TableName() string {
	return "storage_pools"
}

// BeforeCreate hook for StoragePool
func (p *StoragePool) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	return nil
}

// StorageVolume represents a storage volume (disk image) in a pool
type StorageVolume struct {
	ID        string         `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name      string         `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	PoolID    string         `json:"pool_id" gorm:"type:uuid;not null;index"`
	VMID      *string        `json:"vm_id,omitempty" gorm:"type:uuid;index"`
	Size      int64          `json:"size" gorm:"not null"` // bytes
	Format    string         `json:"format" gorm:"type:varchar(20);not null;default:'qcow2'"`
	Path      string         `json:"path" gorm:"type:varchar(255);not null"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`

	// Relations
	Pool *StoragePool `json:"pool,omitempty" gorm:"foreignKey:PoolID"`
}

// TableName specifies the table name for StorageVolume
func (StorageVolume) TableName() string {
	return "storage_volumes"
}

// BeforeCreate hook for StorageVolume
func (v *StorageVolume) BeforeCreate(tx *gorm.DB) error {
	if v.ID == "" {
		v.ID = uuid.New().String()
	}
	return nil
}
