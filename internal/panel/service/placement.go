package service

import (
	"context"
	"fmt"
	"math"
	"path"
	"sort"

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

// nodeLoad is what a node has promised to VMs, with the ratios that cap it.
type nodeLoad struct {
	Node           models.Node
	AllocatedVCPU  int
	AllocatedRAMMB int64
	RAMRatio       float64 // 0 = no cap
	VCPURatio      float64 // 0 = no cap
}

// ramFits mirrors AdmitNodeRAMTx, so placement never picks a node admission
// then refuses. Unmeasured RAM or no ratio fits.
func (n nodeLoad) ramFits(ramMB int) bool {
	if n.RAMRatio <= 0 || n.Node.MemoryTotalBytes <= 0 {
		return true
	}
	capacityMB := int64(float64(n.Node.MemoryTotalBytes) / (1024 * 1024) * n.RAMRatio)
	return n.AllocatedRAMMB+int64(ramMB) <= capacityMB
}

// vcpuFits is the vCPU half: unmeasured CPUs or no ratio fits.
func (n nodeLoad) vcpuFits(cpu int) bool {
	return vcpuWithinRatio(n.Node.CPUCount, n.AllocatedVCPU, cpu, n.VCPURatio)
}

func vcpuWithinRatio(cpuCount, allocated, add int, ratio float64) bool {
	if ratio <= 0 || cpuCount <= 0 || add <= 0 {
		return true
	}
	return float64(allocated+add) <= float64(cpuCount)*ratio
}

// rankNodes orders candidates best first for a VM of ramMB: least RAM
// allocated relative to the node's physical RAM once the VM is placed, then
// most RAM left, then by name and id so the same state always picks the same
// node. Nodes whose RAM has not been measured go last.
func rankNodes(loads []nodeLoad, ramMB int) []nodeLoad {
	out := append([]nodeLoad(nil), loads...)
	share := func(n nodeLoad) float64 {
		total := float64(n.Node.MemoryTotalBytes) / (1024 * 1024)
		if total <= 0 {
			return math.Inf(1)
		}
		return float64(n.AllocatedRAMMB+int64(ramMB)) / total
	}
	free := func(n nodeLoad) int64 { return n.Node.MemoryTotalBytes/(1024*1024) - n.AllocatedRAMMB }
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if sa, sb := share(a), share(b); sa != sb {
			return sa < sb
		}
		if fa, fb := free(a), free(b); fa != fb {
			return fa > fb
		}
		if a.Node.Name != b.Node.Name {
			return a.Node.Name < b.Node.Name
		}
		return a.Node.ID < b.Node.ID
	})
	return out
}

// nodeAllocations sums the vCPUs and RAM (MB) the panel has promised on each
// of the given nodes. Summed here rather than in SQL so it reads the same on
// every database.
func nodeAllocations(ctx context.Context, db *gorm.DB, nodeIDs []string) (map[string][2]int64, error) {
	out := make(map[string][2]int64, len(nodeIDs))
	if len(nodeIDs) == 0 {
		return out, nil
	}
	var vms []struct {
		NodeID    string
		Resources models.Resources `gorm:"serializer:json"`
	}
	if err := db.WithContext(ctx).Table("vms").Select("node_id, resources").
		Where("deleted_at IS NULL AND node_id IN ?", nodeIDs).Find(&vms).Error; err != nil {
		return nil, fmt.Errorf("failed to sum node allocations: %w", err)
	}
	for _, vm := range vms {
		a := out[vm.NodeID]
		a[0] += int64(vm.Resources.CPU)
		a[1] += int64(vm.Resources.RAM)
		out[vm.NodeID] = a
	}
	return out, nil
}

// loadNodes pairs nodes with what is promised on them and their ratios.
func loadNodes(ctx context.Context, db *gorm.DB, nodes []models.Node) ([]nodeLoad, error) {
	ids := make([]string, len(nodes))
	for i := range nodes {
		ids[i] = nodes[i].ID
	}
	alloc, err := nodeAllocations(ctx, db, ids)
	if err != nil {
		return nil, err
	}
	ramRatio := RAMOvercommitRatio(ctx, db)
	out := make([]nodeLoad, len(nodes))
	for i, n := range nodes {
		out[i] = nodeLoad{
			Node: n, AllocatedVCPU: int(alloc[n.ID][0]), AllocatedRAMMB: alloc[n.ID][1],
			RAMRatio: ramRatio, VCPURatio: VCPUAllocationRatio(ctx, db, n.VCPURatio),
		}
	}
	return out, nil
}

// AdmitNodeVCPU checks that a node may carry addCPU more vCPUs under its vCPU
// ratio. Call it in the transaction that records the change: the node row is
// locked there, so concurrent orders on one node are admitted one at a time.
// No ratio, a node whose CPUs were never measured, or a shrink all pass.
func AdmitNodeVCPU(ctx context.Context, tx *gorm.DB, nodeID string, addCPU int) error {
	if nodeID == "" || addCPU <= 0 {
		return nil
	}
	read := func(q *gorm.DB) (models.Node, error) {
		var node models.Node
		err := q.Where("id = ?", nodeID).First(&node).Error
		return node, err
	}
	node, err := read(tx.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("failed to read node: %w", err)
	}
	// Locked only when there is a cap to keep: with none, nothing changes.
	if VCPUAllocationRatio(ctx, tx, node.VCPURatio) <= 0 || node.CPUCount <= 0 {
		return nil
	}
	if node, err = read(tx.WithContext(ctx).Clauses(clause.Locking{Strength: clause.LockingStrengthUpdate})); err != nil {
		return fmt.Errorf("failed to lock node: %w", err)
	}
	ratio := VCPUAllocationRatio(ctx, tx, node.VCPURatio)
	alloc, err := nodeAllocations(ctx, tx, []string{nodeID})
	if err != nil {
		return err
	}
	allocated := int(alloc[nodeID][0])
	if !vcpuWithinRatio(node.CPUCount, allocated, addCPU, ratio) {
		return fmt.Errorf("%w: node %s would carry %d vCPUs, over its limit of %d (%.2fx its %d CPUs)",
			ErrNodeCapacityExceeded, node.Name, allocated+addCPU, int(float64(node.CPUCount)*ratio), ratio, node.CPUCount)
	}
	return nil
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

// NodeAllocation is what a node has promised to VMs against what it has, with
// the ratios that cap it. A limit of 0 is no cap (no ratio, or not measured).
type NodeAllocation struct {
	VCPUAllocated int     `json:"vcpu_allocated"`
	CPUCount      int     `json:"cpu_count"`
	VCPURatio     float64 `json:"vcpu_ratio"`
	// VCPURatioNode is the node's own ratio; 0 means the panel-wide one applies.
	VCPURatioNode  float64 `json:"vcpu_ratio_node"`
	VCPULimit      int     `json:"vcpu_limit"`
	RAMAllocatedMB int64   `json:"ram_allocated_mb"`
	RAMTotalMB     int64   `json:"ram_total_mb"`
	RAMRatio       float64 `json:"ram_ratio"`
	RAMLimitMB     int64   `json:"ram_limit_mb"`
}

// NodeAllocationOf reports a node's allocation, for its page.
func NodeAllocationOf(ctx context.Context, db *gorm.DB, node models.Node) (*NodeAllocation, error) {
	loads, err := loadNodes(ctx, db, []models.Node{node})
	if err != nil {
		return nil, err
	}
	n := loads[0]
	a := &NodeAllocation{
		VCPUAllocated: n.AllocatedVCPU, CPUCount: node.CPUCount, VCPURatio: n.VCPURatio, VCPURatioNode: node.VCPURatio,
		RAMAllocatedMB: n.AllocatedRAMMB, RAMTotalMB: node.MemoryTotalBytes / (1024 * 1024), RAMRatio: n.RAMRatio,
	}
	if n.VCPURatio > 0 && node.CPUCount > 0 {
		a.VCPULimit = int(float64(node.CPUCount) * n.VCPURatio)
	}
	if n.RAMRatio > 0 && node.MemoryTotalBytes > 0 {
		a.RAMLimitMB = int64(float64(node.MemoryTotalBytes) / (1024 * 1024) * n.RAMRatio)
	}
	return a, nil
}
