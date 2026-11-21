package service

import (
	"context"
	"fmt"
	"path"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
		Find(&pools).Error; err != nil {
		return nil
	}
	return pickPool(pools)
}

// pickPool applies PrimaryPool's order of preference to a node's pools.
func pickPool(pools []models.StoragePool) *models.StoragePool {
	if len(pools) == 0 {
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

// VMPool returns the pool a VM's disks live in: the one recorded when it was
// created, else the pool its node provisions into. nil means the panel knows
// no pool on that node.
func VMPool(ctx context.Context, db *gorm.DB, vmID, nodeID string) *models.StoragePool {
	if db == nil {
		return nil
	}
	var id *string
	if err := db.WithContext(ctx).Raw("SELECT storage_pool_id FROM vms WHERE id = ?", vmID).Scan(&id).Error; err == nil && id != nil && *id != "" {
		var pool models.StoragePool
		// A pool on another node is a stale record from before a migration.
		if db.WithContext(ctx).Where("id = ? AND node_id = ?", *id, nodeID).First(&pool).Error == nil {
			return &pool
		}
	}
	return PrimaryPool(ctx, db, nodeID)
}

// PoolFits reports whether a pool's real free space can hold a disk of the
// given size in full, plus headroom. This is the floor under every admission:
// no overcommit ratio lets a disk in that could not be written to the end.
//
// An unknown pool (nil) fits: the panel not having synced a node's storage is
// not evidence that the node is full, and treating it as full would stop orders
// on a healthy node. A pool reporting no total capacity is treated the same way
// — that is a missing measurement, not a full disk.
func PoolFits(pool *models.StoragePool, diskGB int) bool {
	if pool == nil || pool.TotalSpace <= 0 {
		return true
	}
	return pool.AvailableSpace >= int64(diskGB)*bytesPerGB+diskHeadroomBytes
}

// PoolWithinRatio reports whether a pool that has already promised
// allocatedBytes of virtual disk may promise addGB more, under an allocation
// ratio. Free space cannot answer this: disks are thin, so the pool looks just
// as empty to the next order as it did to this one.
//
// A ratio of 0 or less means no cap. An unmeasured pool is within any ratio,
// for the reason PoolFits gives.
func PoolWithinRatio(pool *models.StoragePool, allocatedBytes int64, addGB int, ratio float64) bool {
	if pool == nil || pool.TotalSpace <= 0 || ratio <= 0 {
		return true
	}
	return float64(allocatedBytes+int64(addGB)*bytesPerGB) <= float64(pool.TotalSpace)*ratio
}

// PoolAllocations returns, per storage pool id, the bytes of virtual disk the
// panel has promised there: VM system disks, extra disks, extra disks being
// attached, and volumes. An empty nodeID covers every node.
//
// A system disk counts against the pool recorded on its VM. A VM with none
// recorded (imported, or moved between nodes) counts against the pool its node
// provisions into. An extra disk counts against the pool whose directory holds
// it.
//
// ponytail: counts what the panel knows. Domains on a node that the panel does
// not manage are not counted; have the agent report virtual sizes per pool if
// that gap matters.
func PoolAllocations(ctx context.Context, db *gorm.DB, nodeID string) (map[string]int64, error) {
	onNode := func(q *gorm.DB, column string) *gorm.DB {
		if nodeID != "" {
			return q.Where(column+" = ?", nodeID)
		}
		return q
	}

	var pools []models.StoragePool
	if err := onNode(db.WithContext(ctx), "node_id").Find(&pools).Error; err != nil {
		return nil, fmt.Errorf("failed to list storage pools: %w", err)
	}
	byNode := map[string][]models.StoragePool{}
	out := make(map[string]int64, len(pools))
	for _, p := range pools {
		byNode[p.NodeID] = append(byNode[p.NodeID], p)
		out[p.ID] = 0
	}

	var vms []struct {
		ID            string
		NodeID        string
		Resources     models.Resources `gorm:"serializer:json"`
		StoragePoolID *string
	}
	if err := onNode(db.WithContext(ctx).Table("vms").
		Select("id, node_id, resources, storage_pool_id").
		Where("deleted_at IS NULL"), "node_id").Find(&vms).Error; err != nil {
		return nil, fmt.Errorf("failed to list VM disks: %w", err)
	}

	vmPool := make(map[string]string, len(vms))
	vmNode := make(map[string]string, len(vms))
	for _, vm := range vms {
		poolID := ""
		if vm.StoragePoolID != nil {
			for _, p := range byNode[vm.NodeID] {
				if p.ID == *vm.StoragePoolID {
					poolID = p.ID
				}
			}
		}
		if poolID == "" {
			if p := pickPool(byNode[vm.NodeID]); p != nil {
				poolID = p.ID
			}
		}
		vmPool[vm.ID] = poolID
		vmNode[vm.ID] = vm.NodeID
		if poolID != "" {
			out[poolID] += int64(vm.Resources.Disk) * bytesPerGB
		}
	}

	var disks []struct {
		VMID   string `gorm:"column:vm_id"`
		SizeGB int    `gorm:"column:size_gb"`
		Path   string
	}
	if err := db.WithContext(ctx).Table("vm_disks").Select("vm_id, size_gb, path").
		Where("deleted_at IS NULL").Find(&disks).Error; err != nil {
		return nil, fmt.Errorf("failed to list extra disks: %w", err)
	}
	for _, d := range disks {
		poolID, known := vmPool[d.VMID]
		if !known {
			continue
		}
		dir := path.Dir(d.Path)
		for _, p := range byNode[vmNode[d.VMID]] {
			if path.Clean(p.Path) == dir {
				poolID = p.ID
			}
		}
		if poolID != "" {
			out[poolID] += int64(d.SizeGB) * bytesPerGB
		}
	}

	// An attach in flight has no disk row yet, only its reservation.
	var pending []struct {
		VMID   string `gorm:"column:vm_id"`
		SizeGB int    `gorm:"column:size_gb"`
	}
	if err := db.WithContext(ctx).Table("disk_quota_reservations").Select("vm_id, size_gb").
		Where("status = ?", models.DiskQuotaReservationPending).Find(&pending).Error; err != nil {
		return nil, fmt.Errorf("failed to list disk reservations: %w", err)
	}
	for _, r := range pending {
		if poolID := vmPool[r.VMID]; poolID != "" {
			out[poolID] += int64(r.SizeGB) * bytesPerGB
		}
	}

	var volumes []models.StorageVolume
	if err := db.WithContext(ctx).Select("pool_id, size").Find(&volumes).Error; err != nil {
		return nil, fmt.Errorf("failed to list storage volumes: %w", err)
	}
	for _, v := range volumes {
		if _, ok := out[v.PoolID]; ok {
			out[v.PoolID] += v.Size
		}
	}
	return out, nil
}

// AdmitPoolDisk checks that a pool can take addGB more gigabytes of disk: the
// whole disk must fit in real free space with headroom, and, when the operator
// has set an allocation ratio, everything promised on the pool must stay
// within capacity times that ratio.
//
// Call it in the transaction that records the new disk. The pool row is locked
// there, so two orders cannot both be admitted against the same room.
func AdmitPoolDisk(ctx context.Context, tx *gorm.DB, pool *models.StoragePool, addGB int) error {
	if pool == nil || addGB <= 0 {
		return nil
	}
	if !PoolFits(pool, addGB) {
		return fmt.Errorf("%w: storage pool %s has %d GB free, and a %d GB disk needs that much plus %d GB kept free",
			ErrNodeCapacityExceeded, pool.Name, pool.AvailableSpace/bytesPerGB, addGB, diskHeadroomBytes/bytesPerGB)
	}
	return admitPoolRatio(ctx, tx, pool, addGB)
}

// admitPoolRatio is the allocation-ratio half of AdmitPoolDisk. With addGB 0 it
// re-checks a pool after the new disk has been recorded.
func admitPoolRatio(ctx context.Context, tx *gorm.DB, pool *models.StoragePool, addGB int) error {
	if pool == nil || pool.TotalSpace <= 0 {
		return nil
	}
	ratio := DiskAllocationRatio(ctx, tx)
	if ratio <= 0 {
		return nil
	}
	var locked models.StoragePool
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate}).
		Select("id").Where("id = ?", pool.ID).First(&locked).Error; err != nil {
		return fmt.Errorf("failed to lock storage pool: %w", err)
	}
	allocated, err := PoolAllocations(ctx, tx, pool.NodeID)
	if err != nil {
		return err
	}
	if !PoolWithinRatio(pool, allocated[pool.ID], addGB, ratio) {
		return fmt.Errorf("%w: storage pool %s would have %d GB of disk allocated, over its limit of %d GB (%.2fx its capacity)",
			ErrNodeCapacityExceeded, pool.Name,
			allocated[pool.ID]/bytesPerGB+int64(addGB), int64(float64(pool.TotalSpace)*ratio)/bytesPerGB, ratio)
	}
	return nil
}
