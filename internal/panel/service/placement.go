package service

import (
	"context"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// diskHeadroomBytes is kept free on a pool over and above the disk being
// provisioned.
//
// Disks are thin: a 40 GB qcow2 occupies far less on day one and grows toward
// its declared size as the guest writes. Placing a VM that only fits while it is
// empty means the pool fills later, under load, with no operator involved —
// which is the failure this margin exists to prevent. It is deliberately a flat
// reserve rather than a percentage: what matters is having room to move (delete
// a volume, take a backup) at the moment things go wrong, and that need does not
// scale with pool size.
const diskHeadroomBytes = 20 * 1024 * 1024 * 1024 // 20 GiB

// PrimaryPool returns the pool a node provisions new VM disks into.
//
// The pool marked primary wins; otherwise the pool covering the node's default
// image directory; otherwise the one with the most free space. Returning nil is
// normal on a node whose pools have not been synced yet, and callers must treat
// it as "let the node decide" rather than as an error — refusing to create VMs
// because the panel has not seen a pool yet would be worse than provisioning
// where the node has always provisioned.
func PrimaryPool(ctx context.Context, db *gorm.DB, nodeID string) *models.StoragePool {
	if db == nil || nodeID == "" {
		return nil
	}
	var pools []models.StoragePool
	if err := db.WithContext(ctx).
		Where("node_id = ? AND status = ?", nodeID, "online").
		Find(&pools).Error; err != nil || len(pools) == 0 {
		return nil
	}

	var fallback *models.StoragePool
	for i := range pools {
		if pools[i].IsPrimary {
			return &pools[i]
		}
		if pools[i].Path == DefaultNodeImageDir {
			fallback = &pools[i]
		}
	}
	if fallback != nil {
		return fallback
	}

	roomiest := &pools[0]
	for i := range pools {
		if pools[i].AvailableSpace > roomiest.AvailableSpace {
			roomiest = &pools[i]
		}
	}
	return roomiest
}

// DefaultNodeImageDir mirrors the agent's fallback image directory. Kept here so
// the panel can recognise the pool that covers it without the agent having to
// announce it.
const DefaultNodeImageDir = "/var/lib/libvirt/images"

// PoolFits reports whether a pool has room for a disk of the given size, plus
// headroom.
//
// An unknown pool (nil) fits: the panel not having synced a node's storage is
// not evidence that the node is full, and treating it as full would stop orders
// on a healthy node. A pool reporting no total capacity is treated the same way
// — that is a missing measurement, not a full disk.
func PoolFits(pool *models.StoragePool, diskGB int) bool {
	if pool == nil || pool.TotalSpace <= 0 {
		return true
	}
	need := int64(diskGB)*1024*1024*1024 + diskHeadroomBytes
	return pool.AvailableSpace >= need
}
