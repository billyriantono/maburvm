package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Storage tiers: a label on storage pools (nvme, hdd). A plan naming a tier
// puts its VMs' disks only in pools of that tier; staff may name a pool.

// ErrInvalidStorageTier refuses a tier label that is not a short name.
var ErrInvalidStorageTier = errors.New("a storage tier is 1 to 64 letters, digits, dots, dashes or underscores, starting with a letter or digit")

// ErrStoragePoolNotFound is a pool id that names no pool.
var ErrStoragePoolNotFound = errors.New("storage pool not found")

// ErrNoTierPool is a node with no online pool of the tier an order needs.
var ErrNoTierPool = errors.New("no online storage pool of the tier this VM needs")

// NormalizeStorageTier lower-cases and checks a tier; "" is no tier.
func NormalizeStorageTier(t string) (string, error) {
	t, err := NormalizeNodeGroup(t)
	if err != nil {
		return "", ErrInvalidStorageTier
	}
	return t, nil
}

// planStorageTier is the tier a plan puts disks in; "" for none or no plan.
func planStorageTier(ctx context.Context, db *gorm.DB, planID string) string {
	if planID == "" {
		return ""
	}
	var t string
	if err := db.WithContext(ctx).Table("plans").Select("storage_tier").Where("id = ?", planID).Scan(&t).Error; err != nil {
		return "" // a database without the column has no tiers
	}
	return t
}

// NodeDiskPool is the pool a new disk on a node goes in: for a tier, the best
// online pool of that tier (primary first, then the roomiest); without one,
// the pool the node provisions into (PrimaryPool, nil being "let the node
// decide").
func NodeDiskPool(ctx context.Context, db *gorm.DB, nodeID, tier string) (*models.StoragePool, error) {
	if tier == "" {
		return PrimaryPool(ctx, db, nodeID), nil
	}
	var pools []models.StoragePool
	if err := db.WithContext(ctx).Where("node_id = ? AND status = ? AND tier = ?", nodeID, "online", tier).Find(&pools).Error; err != nil {
		return nil, fmt.Errorf("failed to list storage pools: %w", err)
	}
	if p := pickPool(pools); p != nil {
		return p, nil
	}
	return nil, fmt.Errorf("%w (%q) on this node", ErrNoTierPool, tier)
}

// chosenPool is a pool staff named for a new VM: it must be on the VM's node
// and online. A shared pool's id names that node's member of it.
func chosenPool(ctx context.Context, db *gorm.DB, poolID, nodeID string) (*models.StoragePool, error) {
	var p models.StoragePool
	err := db.WithContext(ctx).Where("id = ? AND node_id = ?", poolID, nodeID).First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).
			Where("shared_pool_id = ? AND node_id = ?", poolID, nodeID).First(&p).Error == nil {
			err = nil
		}
	}
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: storage pool %s is not on the VM's node", ErrInvalidStoragePool, poolID)
		}
		return nil, err
	}
	if p.Status != "online" {
		return nil, fmt.Errorf("%w: storage pool %s is %s", ErrInvalidStoragePool, p.Name, p.Status)
	}
	return &p, nil
}

// SetPoolTier labels a pool with a tier ("" clears it).
func SetPoolTier(ctx context.Context, db *gorm.DB, poolID, tier string) (*models.StoragePool, error) {
	tier, err := NormalizeStorageTier(tier)
	if err != nil {
		return nil, err
	}
	res := db.WithContext(ctx).Table("storage_pools").Where("id = ? AND deleted_at IS NULL", poolID).Update("tier", tier)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrStoragePoolNotFound
	}
	var p models.StoragePool
	if err := db.WithContext(ctx).Where("id = ?", poolID).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// StorageTiers lists the tiers pools are labelled with, for pickers.
func StorageTiers(ctx context.Context, db *gorm.DB) ([]string, error) {
	var out []string
	err := db.WithContext(ctx).Table("storage_pools").Where("tier <> '' AND deleted_at IS NULL").
		Distinct("tier").Order("tier").Pluck("tier", &out).Error
	return out, err
}
