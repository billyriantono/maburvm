package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// ErrDiskMoveActive is a disk change refused while a disk of the VM moves.
// It counts as ErrVMMigrating, so every route that refuses changes during a
// move refuses them during this one too.
var ErrDiskMoveActive error = diskMoveError{}

type diskMoveError struct{}

func (diskMoveError) Error() string {
	return "a disk of this VM is being moved to another pool; try again when the move has finished"
}

func (diskMoveError) Is(target error) bool { return target == ErrVMMigrating }

// GuestGrowNote tells the owner what is left to do inside the VM after its
// disk grew: the disk is bigger, the partition and filesystem are not yet.
const GuestGrowNote = "The disk is bigger now. Inside the VM, grow its partition and filesystem to use the space: on Linux growpart then resize2fs or xfs_growfs (cloud images do this at their next boot), on Windows extend the volume in Disk Management."

// DiskResizeResult is a grown disk.
type DiskResizeResult struct {
	Device string `json:"device"`
	SizeGB int    `json:"size_gb"`
	// Live is true when the running VM was given the new size at once.
	Live bool `json:"live"`
	// Queued is true when the change waits for a job (a stopped VM on a node
	// whose agent predates growing one disk).
	Queued bool   `json:"queued"`
	Note   string `json:"note"`
}

// diskOnlyGrowth reports a change of resources that only grows the disk.
func diskOnlyGrowth(old, want models.Resources) bool {
	if want.Disk <= old.Disk {
		return false
	}
	same := want
	same.Disk = old.Disk
	return reflect.DeepEqual(same, old)
}

// diskMoveActive reports a disk move of the VM that has not ended. A database
// without the table has none.
func diskMoveActive(ctx context.Context, db *gorm.DB, vmID string) bool {
	if db == nil {
		return false
	}
	var n int64
	err := db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).Table("vm_disk_moves").Where("vm_id = ? AND status IN ?", vmID, []string{diskMoveQueued, diskMoveRunning}).Count(&n).Error
	return err == nil && n > 0
}

// ResizeDisk grows one disk of a VM to sizeGB, whether it runs or not:
// "vda" is its system disk (its size in the plan sense), any other device one
// of its extra disks. Disks only grow.
func (s *VMService) ResizeDisk(ctx context.Context, vmID, device string, sizeGB int, billed bool) (*DiskResizeResult, error) {
	if err := vmBusy(ctx, s.db, vmID); err != nil {
		return nil, err
	}
	if diskMoveActive(ctx, s.db, vmID) {
		return nil, ErrDiskMoveActive
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	if vm.Status != models.VMStatusRunning && vm.Status != models.VMStatusStopped {
		return nil, fmt.Errorf("the VM is %s; a disk grows while it runs or while it is stopped", vm.Status)
	}
	if device == "" || device == "vda" {
		return s.growSystemDisk(ctx, vm, sizeGB, billed)
	}
	return s.growExtraDisk(ctx, vm, device, sizeGB)
}

func (s *VMService) growSystemDisk(ctx context.Context, vm *models.VM, sizeGB int, billed bool) (*DiskResizeResult, error) {
	oldRes := vm.Resources
	if sizeGB <= oldRes.Disk {
		return nil, fmt.Errorf("disk can only be grown (current %dGB, requested %dGB)", oldRes.Disk, sizeGB)
	}
	newRes := oldRes
	newRes.Disk = sizeGB
	if err := s.validateResources(&newRes); err != nil {
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := s.quotaService.AdmitVMResizeTx(ctx, tx, vm.UserID, oldRes, newRes); err != nil && !billedOverQuota(billed, err) {
			return err
		}
		if err := AdmitPoolDisk(ctx, tx, VMPool(ctx, tx, vm.ID, vm.NodeID), sizeGB-oldRes.Disk); err != nil {
			return err
		}
		return setVMResources(ctx, tx, vm.ID, newRes)
	}); err != nil {
		return nil, err
	}
	revert := func() { _ = setVMResources(context.WithoutCancel(ctx), s.db, vm.ID, oldRes) }
	resp, err := s.resizeOnNode(ctx, vm.NodeID, vm.ID, "vda", sizeGB)
	if status.Code(err) == codes.Unimplemented {
		if vm.Status == models.VMStatusStopped {
			// The resize job grows the disk of a stopped VM on any agent.
			params, _ := json.Marshal(map[string]interface{}{"resources": newRes})
			if _, jerr := s.riverClient.Insert(ctx, queue.VMOperationJob{VMID: vm.ID, Operation: queue.VMOpResize, NodeID: vm.NodeID, Params: params}, nil); jerr != nil {
				revert()
				return nil, fmt.Errorf("failed to enqueue the resize: %w", jerr)
			}
			return &DiskResizeResult{Device: "vda", SizeGB: sizeGB, Queued: true, Note: GuestGrowNote}, nil
		}
		revert()
		return nil, fmt.Errorf("this VM's node needs an agent update before a disk grows while the VM runs; stop the VM to grow it now")
	}
	if err != nil {
		revert()
		return nil, err
	}
	return &DiskResizeResult{Device: "vda", SizeGB: sizeGB, Live: resp.Live, Note: GuestGrowNote}, nil
}

func (s *VMService) growExtraDisk(ctx context.Context, vm *models.VM, device string, sizeGB int) (*DiskResizeResult, error) {
	var disk models.VMDisk
	if err := s.db.WithContext(ctx).Where("vm_id = ? AND device = ?", vm.ID, device).First(&disk).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("disk %s not found on this VM", device)
		}
		return nil, err
	}
	if disk.Lifecycle != models.VMDiskLifecycleAttached {
		return nil, fmt.Errorf("disk %s is being removed", device)
	}
	grow := sizeGB - disk.SizeGB
	if grow <= 0 {
		return nil, fmt.Errorf("disk can only be grown (current %dGB, requested %dGB)", disk.SizeGB, sizeGB)
	}
	pool := DiskPool(ctx, s.db, vm.ID, vm.NodeID, disk.Path)
	if !PoolFits(pool, grow) {
		return nil, fmt.Errorf("%w: storage pool %s has %d GB free, and growing the disk by %d GB needs that much plus %d GB kept free",
			ErrNodeCapacityExceeded, pool.Name, pool.AvailableSpace/bytesPerGB, grow, diskHeadroomBytes/bytesPerGB)
	}
	// The growth is held against the owner's disk quota while the node works,
	// as a new disk is.
	res, err := s.quotaService.ReserveDiskQuota(ctx, vm.UserID, vm.ID, grow)
	if err != nil {
		return nil, err
	}
	release := func() { _ = s.quotaService.ReleaseDiskReservation(context.WithoutCancel(ctx), res.ID) }
	if err := admitPoolRatio(ctx, s.db, pool, 0); err != nil {
		release()
		return nil, err
	}
	resp, err := s.resizeOnNode(ctx, vm.NodeID, vm.ID, device, sizeGB)
	if err != nil {
		release()
		if status.Code(err) == codes.Unimplemented {
			return nil, fmt.Errorf("this VM's node needs an agent update before an extra disk can grow")
		}
		return nil, err
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.VMDisk{}).Where("id = ?", disk.ID).Update("size_gb", sizeGB).Error; err != nil {
			return err
		}
		// The disk's record now counts the growth; its reservation goes.
		return tx.Where("id = ? AND status = ?", res.ID, models.DiskQuotaReservationPending).Delete(&models.DiskQuotaReservation{}).Error
	}); err != nil {
		// The disk is grown on the node; the reservation keeps counting it.
		return nil, fmt.Errorf("disk %s grew on its node but could not be recorded (its quota stays held): %w", device, err)
	}
	return &DiskResizeResult{Device: device, SizeGB: sizeGB, Live: resp.Live, Note: GuestGrowNote}, nil
}

// setVMResources writes a VM's resources and nothing else, through the
// model so the column is serialized as everywhere else.
func setVMResources(ctx context.Context, db *gorm.DB, vmID string, res models.Resources) error {
	return db.WithContext(ctx).Model(&models.VM{ID: vmID}).Select("resources").Updates(&models.VM{Resources: res}).Error
}

// resizeOnNode asks the VM's node to grow one disk.
func (s *VMService) resizeOnNode(ctx context.Context, nodeID, vmID, device string, sizeGB int) (*pb.ResizeVMDiskResponse, error) {
	if err := s.ensureVMNodeActive(ctx, nodeID); err != nil {
		return nil, err
	}
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	actx, err := s.agentAuthContext(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(actx, 5*time.Minute)
	defer cancel()
	resp, err := client.ResizeVMDisk(cctx, &pb.ResizeVMDiskRequest{
		VmId: vmID, Target: device, SizeGb: int64(sizeGB), LvmPools: queue.NodeLVMPools(ctx, s.db, nodeID),
	})
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, fmt.Errorf("the node could not grow disk %s: %s", device, agentErrorMessage(resp.Error))
	}
	return resp, nil
}

// Disk moves between two pools of a VM's node.
const (
	diskMoveQueued    = "queued"
	diskMoveRunning   = "running"
	diskMoveCompleted = "completed"
	diskMoveFailed    = "failed"
	diskMoveCancelled = "cancelled"
)

// VMDiskMove is one move of a VM's disk (vm_disk_moves).
type VMDiskMove struct {
	ID              string     `json:"id" gorm:"type:uuid;primaryKey"`
	VMID            string     `json:"vm_id" gorm:"column:vm_id"`
	NodeID          string     `json:"node_id"`
	Target          string     `json:"target"`
	SizeGB          int        `json:"size_gb" gorm:"column:size_gb"`
	SourcePoolID    *string    `json:"source_pool_id"`
	DestPoolID      string     `json:"dest_pool_id"`
	SourcePath      string     `json:"source_path"`
	DestPath        string     `json:"dest_path"`
	Live            bool       `json:"live"`
	Status          string     `json:"status"`
	Phase           string     `json:"phase"`
	TotalBytes      int64      `json:"total_bytes"`
	DoneBytes       int64      `json:"done_bytes"`
	Error           string     `json:"error"`
	Note            string     `json:"note"`
	CancelRequested bool       `json:"cancel_requested"`
	CreatedBy       *string    `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	FinishedAt      *time.Time `json:"finished_at"`
}

func (VMDiskMove) TableName() string { return "vm_disk_moves" }

// StartDiskMove moves one disk of a VM to another storage pool of its node:
// copied while the VM is off, or block-copied while it runs, then the old
// disk is removed. The destination pool is admitted as a new disk would be.
func (s *VMService) StartDiskMove(ctx context.Context, vmID, device, destPoolID, userID string) (*VMDiskMove, error) {
	if err := vmBusy(ctx, s.db, vmID); err != nil {
		return nil, err
	}
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	if vm.Status != models.VMStatusRunning && vm.Status != models.VMStatusStopped {
		return nil, fmt.Errorf("the VM is %s; a disk moves while the VM runs or while it is stopped", vm.Status)
	}
	if device == "" {
		device = "vda"
	}
	var snaps int64
	s.db.WithContext(ctx).Model(&models.Snapshot{}).Where("vm_id = ?", vm.ID).Count(&snaps)
	if snaps > 0 {
		return nil, fmt.Errorf("the VM has %d snapshots, which cannot move with its disk; delete them first", snaps)
	}
	var source *models.StoragePool
	sizeGB, sourcePath := vm.Resources.Disk, ""
	if device == "vda" {
		source = VMPool(ctx, s.db, vm.ID, vm.NodeID)
	} else {
		var disk models.VMDisk
		if err := s.db.WithContext(ctx).Where("vm_id = ? AND device = ?", vm.ID, device).First(&disk).Error; err != nil {
			return nil, fmt.Errorf("disk %s not found on this VM", device)
		}
		sizeGB, sourcePath = disk.SizeGB, disk.Path
		source = DiskPool(ctx, s.db, vm.ID, vm.NodeID, disk.Path)
	}
	dest, err := chosenPool(ctx, s.db, destPoolID, vm.NodeID)
	if err != nil {
		return nil, err
	}
	if source != nil && source.ID == dest.ID {
		return nil, fmt.Errorf("%w: the disk is already in %s", ErrInvalidStoragePool, dest.Name)
	}
	if dest.Type != "" && dest.Type != PoolTypeDir && !models.IsLVMPoolType(dest.Type) {
		return nil, fmt.Errorf("%w: storage pool %s is %s storage, which holds no VM disks", ErrInvalidStoragePool, dest.Name, dest.Type)
	}
	if err := lvmPoolUsable(dest); err != nil {
		return nil, err
	}

	move := &VMDiskMove{ID: uuid.New().String(), VMID: vm.ID, NodeID: vm.NodeID, Target: device, SizeGB: sizeGB,
		DestPoolID: dest.ID, SourcePath: sourcePath, Status: diskMoveQueued, Phase: "Starting"}
	if source != nil {
		move.SourcePoolID = &source.ID
	}
	if userID != "" {
		move.CreatedBy = &userID
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := AdmitPoolDisk(ctx, tx, dest, sizeGB); err != nil {
			return err
		}
		if err := tx.Create(move).Error; err != nil {
			if strings.Contains(err.Error(), "uniq_vm_disk_moves_active") || strings.Contains(strings.ToLower(err.Error()), "unique") {
				return ErrDiskMoveActive
			}
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}

	fail := func(err error) (*VMDiskMove, error) {
		s.finishDiskMove(context.WithoutCancel(ctx), move, diskMoveFailed, err.Error(), "")
		return nil, err
	}
	if err := s.ensureVMNodeActive(ctx, vm.NodeID); err != nil {
		return fail(err)
	}
	client, err := s.getAgentClient(ctx, vm.NodeID)
	if err != nil {
		return fail(err)
	}
	actx, err := s.agentAuthContext(ctx, vm.NodeID)
	if err != nil {
		return fail(err)
	}
	cctx, cancel := context.WithTimeout(actx, 2*time.Minute)
	defer cancel()
	destType := dest.Type
	if !models.IsLVMPoolType(destType) {
		destType = ""
	}
	resp, err := client.MoveVMDisk(cctx, &pb.MoveVMDiskRequest{VmId: vm.ID, MoveId: move.ID, Target: device,
		DestPoolType: destType, DestPoolPath: dest.Path, LvmPools: queue.NodeLVMPools(ctx, s.db, vm.NodeID)})
	switch {
	case status.Code(err) == codes.Unimplemented:
		return fail(fmt.Errorf("this VM's node needs an agent update before a disk can move between pools"))
	case err != nil:
		return fail(fmt.Errorf("the node did not start the move: %w", err))
	case !resp.Success:
		return fail(errors.New(agentErrorMessage(resp.Error)))
	}
	move.Status, move.Live, move.DestPath, move.Phase = diskMoveRunning, resp.Live, resp.DestPath, "Copying"
	s.db.WithContext(ctx).Model(move).Updates(map[string]interface{}{"status": move.Status, "live": move.Live,
		"dest_path": move.DestPath, "phase": move.Phase, "updated_at": time.Now()})
	if s.riverClient != nil {
		if _, err := s.riverClient.Insert(ctx, queue.DiskMoveJob{MoveID: move.ID}, nil); err != nil {
			s.logger.WarnContext(ctx, "disk move started on its node but its follow-up job was not queued", "move_id", move.ID, "error", err)
		}
	}
	return move, nil
}

// finishDiskMove records how a move ended.
func (s *VMService) finishDiskMove(ctx context.Context, m *VMDiskMove, st, errMsg, note string) {
	now := time.Now()
	m.Status, m.Error, m.FinishedAt = st, errMsg, &now
	if note != "" {
		m.Note = note
	}
	s.db.WithContext(ctx).Model(&VMDiskMove{}).Where("id = ?", m.ID).Updates(map[string]interface{}{
		"status": st, "error": errMsg, "note": m.Note, "finished_at": now, "updated_at": now})
}

// RunDiskMoveJob follows a move on its node until it ends, then points the
// panel's records at the disk's new home.
func (s *VMService) RunDiskMoveJob(ctx context.Context, moveID string) error {
	var m VMDiskMove
	if err := s.db.WithContext(ctx).Where("id = ?", moveID).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // gone with its VM
		}
		return err
	}
	if m.Status != diskMoveRunning && m.Status != diskMoveQueued {
		return nil
	}
	client, err := s.getAgentClient(ctx, m.NodeID)
	if err != nil {
		return err
	}
	actx, err := s.agentAuthContext(ctx, m.NodeID)
	if err != nil {
		return err
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	aborted := false
	for {
		var cancelAsked bool
		s.db.WithContext(ctx).Model(&VMDiskMove{}).Where("id = ?", m.ID).Select("cancel_requested").Scan(&cancelAsked)
		if cancelAsked && !aborted {
			_, _ = client.AbortMigration(actx, &pb.AbortMigrationRequest{VmId: m.VMID, MigrationId: m.ID})
			aborted = true
		}
		prog, err := client.GetMigrationProgress(actx, &pb.GetMigrationProgressRequest{VmId: m.VMID, MigrationId: m.ID})
		if err != nil {
			return fmt.Errorf("the node could not report the move: %w", err)
		}
		switch prog.State {
		case "running":
			cols := map[string]interface{}{"total_bytes": prog.TotalBytes, "done_bytes": prog.ProcessedBytes, "updated_at": time.Now()}
			if prog.Phase != "" {
				cols["phase"] = prog.Phase
			}
			s.db.WithContext(ctx).Model(&VMDiskMove{}).Where("id = ?", m.ID).Updates(cols)
		case "completed":
			return s.settleDiskMove(ctx, &m, client, actx, "")
		case "failed":
			s.finishDiskMove(ctx, &m, diskMoveFailed, prog.Error, "The disk stays where it was.")
			return nil
		case "cancelled":
			s.finishDiskMove(ctx, &m, diskMoveCancelled, prog.Error, "The disk stays where it was.")
			return nil
		default:
			// The agent restarted and forgot the move: where the disk is says
			// how it ended.
			return s.settleDiskMove(ctx, &m, client, actx, "the node restarted while the disk was moving")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// settleDiskMove reads where the disk is now and records it. lost is set when
// the node no longer knows the move.
func (s *VMService) settleDiskMove(ctx context.Context, m *VMDiskMove, client pb.NodeAgentClient, actx context.Context, lost string) error {
	ctx = context.WithoutCancel(ctx)
	info, err := client.GetMigrationInfo(actx, &pb.GetMigrationInfoRequest{VmId: m.VMID})
	if err != nil {
		return fmt.Errorf("the node could not list the VM's disks: %w", err)
	}
	path := ""
	for _, d := range info.Disks {
		if d.Target == m.Target {
			path = d.Path
		}
	}
	var dest models.StoragePool
	if err := s.db.WithContext(ctx).Where("id = ?", m.DestPoolID).First(&dest).Error; err != nil {
		return err
	}
	if path == "" || !PoolHoldsDisk(dest, path) {
		why := lost
		if why == "" {
			why = "the node finished, but the disk is not in the destination pool"
		}
		s.finishDiskMove(ctx, m, diskMoveFailed, why, "The disk stays where it was. A partial copy may be left in the destination pool; the orphaned-disk list shows it once the VM is deleted or moved off this node.")
		return nil
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if m.Target == "vda" {
			if err := tx.Exec("UPDATE vms SET storage_pool_id = ? WHERE id = ?", dest.ID, m.VMID).Error; err != nil {
				return err
			}
		} else if err := tx.Model(&models.VMDisk{}).Where("vm_id = ? AND device = ?", m.VMID, m.Target).Update("path", path).Error; err != nil {
			return err
		}
		return tx.Model(&VMDiskMove{}).Where("id = ?", m.ID).Updates(map[string]interface{}{"dest_path": path, "done_bytes": gorm.Expr("total_bytes")}).Error
	})
	if err != nil {
		return err
	}
	note := ""
	if m.SourcePath != "" && models.IsBlockDevicePath(m.SourcePath) && !models.MaburVMVolumePath(m.SourcePath) {
		note = "The old volume was not made by MaburVM and was kept; delete it under orphaned disks when you no longer need it."
	}
	s.finishDiskMove(ctx, m, diskMoveCompleted, "", note)
	// Backup checkpoints named the old image; the next backup is a full one.
	s.db.WithContext(ctx).Model(&models.BackupSchedule{}).Where("vm_id = ?", m.VMID).Update("chain_reset_at", time.Now())
	return nil
}

// CancelDiskMove asks the VM's running disk move to stop; the disk stays where
// it was.
func (s *VMService) CancelDiskMove(ctx context.Context, vmID string) (*VMDiskMove, error) {
	var m VMDiskMove
	if err := s.db.WithContext(ctx).Where("vm_id = ? AND status IN ?", vmID, []string{diskMoveQueued, diskMoveRunning}).First(&m).Error; err != nil {
		return nil, fmt.Errorf("no disk of this VM is moving")
	}
	if err := s.db.WithContext(ctx).Model(&m).Update("cancel_requested", true).Error; err != nil {
		return nil, err
	}
	m.CancelRequested = true
	// The follow-up job may be gone (never queued, or it gave up while the
	// node was unreachable), which would leave the VM refusing to start for
	// good. Queue it again; one already queued or running makes this a no-op.
	if s.riverClient != nil {
		if _, err := s.riverClient.Insert(ctx, queue.DiskMoveJob{MoveID: m.ID}, nil); err != nil {
			s.logger.WarnContext(ctx, "disk move cancel could not queue its follow-up job", "move_id", m.ID, "error", err)
		}
	}
	return &m, nil
}

// ListDiskMoves returns a VM's recent disk moves, newest first.
func (s *VMService) ListDiskMoves(ctx context.Context, vmID string) ([]VMDiskMove, error) {
	var out []VMDiskMove
	err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("created_at DESC").Limit(20).Find(&out).Error
	return out, err
}

// OrphanDisk is a disk file or logical volume on a node that no VM there and
// no panel record uses.
type OrphanDisk struct {
	NodeID       string `json:"node_id"`
	Path         string `json:"path"`
	Kind         string `json:"kind"`
	SizeBytes    int64  `json:"size_bytes"`
	ModifiedUnix int64  `json:"modified_unix"`
	Reason       string `json:"reason"`
	Pool         string `json:"pool"`
	// MaburVM is true when MaburVM made it (its name, or a volume's tag);
	// deleting one it did not make needs a second confirmation.
	MaburVM bool `json:"maburvm"`
	// VMID is the VM its name says it belonged to; VMHostname and VMDeleted
	// say what the panel knows of that VM.
	VMID       string `json:"vm_id,omitempty"`
	VMHostname string `json:"vm_hostname,omitempty"`
	VMDeleted  bool   `json:"vm_deleted"`
}

// nodePoolsForScan are the pools of a node the orphan finder looks in.
func nodePoolsForScan(ctx context.Context, db *gorm.DB, nodeID string) (dirs []string, lvm []*pb.LVMPool, err error) {
	var pools []models.StoragePool
	if err := db.WithContext(ctx).Where("node_id = ?", nodeID).Find(&pools).Error; err != nil {
		return nil, nil, err
	}
	for _, p := range pools {
		if models.IsLVMPoolType(p.Type) {
			if vg, thin, err := models.SplitLVMPoolPath(p.Type, p.Path); err == nil {
				lvm = append(lvm, &pb.LVMPool{Vg: vg, ThinPool: thin})
			}
		} else if strings.HasPrefix(p.Path, "/") {
			dirs = append(dirs, p.Path)
		}
	}
	dirs = append(dirs, DefaultNodeImageDir)
	return dirs, lvm, nil
}

// systemDiskReasons are the kinds of file a VM in use has; one of a VM the
// panel still has is never offered for deletion.
var systemDiskReasons = map[string]bool{
	"VM system disk": true, "VM data disk": true, "VM setup disc (cloud-init seed)": true, "VM disk volume": true,
}

// ListOrphanDisks asks a node for the disks in its pools no domain uses, and
// leaves out what a panel record refers to.
func (s *VMService) ListOrphanDisks(ctx context.Context, nodeID string) ([]OrphanDisk, error) {
	dirs, lvm, err := nodePoolsForScan(ctx, s.db, nodeID)
	if err != nil {
		return nil, err
	}
	if err := s.ensureVMNodeActive(ctx, nodeID); err != nil {
		return nil, err
	}
	client, err := s.getAgentClient(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	actx, err := s.agentAuthContext(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(actx, 3*time.Minute)
	defer cancel()
	resp, err := client.ListOrphanDisks(cctx, &pb.ListOrphanDisksRequest{DirPools: dirs, LvmPools: lvm})
	if status.Code(err) == codes.Unimplemented {
		return nil, fmt.Errorf("this node needs an agent update before it can list orphaned disks")
	}
	if err != nil {
		return nil, err
	}
	if !resp.Success {
		return nil, errors.New(agentErrorMessage(resp.Error))
	}

	referenced := map[string]bool{}
	var paths []string
	s.db.WithContext(ctx).Model(&models.VMDisk{}).Pluck("path", &paths)
	for _, p := range paths {
		referenced[p] = true
	}
	paths = nil
	s.db.WithContext(ctx).Table("storage_volumes").Where("deleted_at IS NULL").Pluck("path", &paths)
	for _, p := range paths {
		referenced[p] = true
	}
	paths = nil
	s.db.WithContext(ctx).Model(&models.OSTemplate{}).Unscoped().Pluck("image_path", &paths)
	for _, p := range paths {
		referenced[p] = true
	}

	var out []OrphanDisk
	for _, d := range resp.Disks {
		if referenced[d.Path] {
			continue
		}
		o := OrphanDisk{NodeID: nodeID, Path: d.Path, Kind: d.Kind, SizeBytes: d.SizeBytes, ModifiedUnix: d.ModifiedUnix,
			Reason: d.Reason, Pool: d.Pool, MaburVM: d.Maburvm, VMID: d.VmId}
		if o.VMID != "" {
			var vm struct {
				Hostname  string
				NodeID    string
				DeletedAt *time.Time
			}
			if s.db.WithContext(ctx).Table("vms").Select("hostname, node_id, deleted_at").Where("id = ?", o.VMID).Scan(&vm).Error == nil && vm.Hostname != "" {
				o.VMHostname, o.VMDeleted = vm.Hostname, vm.DeletedAt != nil
				// A VM the panel still has on this node may be using its disk in
				// a way the node cannot see yet (a create about to define it).
				// One recorded on another node left this copy behind (a move or
				// migration that did not clean up), so it is listed.
				if !o.VMDeleted && systemDiskReasons[o.Reason] {
					if vm.NodeID == "" || vm.NodeID == nodeID {
						continue
					}
					o.Reason += " left behind: the VM is now on another node"
				}
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// OrphanDeleteRequest names an orphaned disk as the list showed it.
type OrphanDeleteRequest struct {
	NodeID       string `json:"node_id"`
	Path         string `json:"path"`
	SizeBytes    int64  `json:"size_bytes"`
	ModifiedUnix int64  `json:"modified_unix"`
	// Confirm must repeat the path.
	Confirm string `json:"confirm"`
	// AllowForeign confirms deleting a disk MaburVM did not make.
	AllowForeign bool `json:"allow_foreign"`
}

// DeleteOrphanDisk deletes one orphaned disk, after finding it orphaned and
// unchanged again, here and on its node.
func (s *VMService) DeleteOrphanDisk(ctx context.Context, req OrphanDeleteRequest) error {
	if req.Confirm != req.Path || req.Path == "" {
		return fmt.Errorf("%w: confirm by repeating the disk's path", ErrInvalidStoragePool)
	}
	list, err := s.ListOrphanDisks(ctx, req.NodeID)
	if err != nil {
		return err
	}
	var found *OrphanDisk
	for i := range list {
		if list[i].Path == req.Path {
			found = &list[i]
		}
	}
	switch {
	case found == nil:
		return fmt.Errorf("%w: %s is no longer an orphaned disk on this node", ErrInvalidStoragePool, req.Path)
	case found.SizeBytes != req.SizeBytes || found.ModifiedUnix != req.ModifiedUnix:
		return fmt.Errorf("%w: %s changed since it was listed; list the orphaned disks again", ErrInvalidStoragePool, req.Path)
	case !found.MaburVM && !req.AllowForeign:
		return fmt.Errorf("%w: %s was not made by MaburVM; confirm deleting it as well", ErrInvalidStoragePool, req.Path)
	}
	dirs, lvm, err := nodePoolsForScan(ctx, s.db, req.NodeID)
	if err != nil {
		return err
	}
	client, err := s.getAgentClient(ctx, req.NodeID)
	if err != nil {
		return err
	}
	actx, err := s.agentAuthContext(ctx, req.NodeID)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(actx, 3*time.Minute)
	defer cancel()
	resp, err := client.DeleteOrphanDisk(cctx, &pb.DeleteOrphanDiskRequest{Path: req.Path, SizeBytes: req.SizeBytes, ModifiedUnix: req.ModifiedUnix,
		DirPools: dirs, LvmPools: lvm, AllowForeign: req.AllowForeign})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%w: %s", ErrInvalidStoragePool, agentErrorMessage(resp.Error))
	}
	return nil
}
