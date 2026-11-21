package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// RecoveryJob recovers one VM of a failed node on another node. Its record
// (vm_recoveries) holds the plan and the progress; the job only names it.
type RecoveryJob struct {
	RecoveryID string `json:"recovery_id" river:"unique"`
	// VMID names the VM in the alert a failed recovery raises.
	VMID string `json:"vm_id"`
}

func (RecoveryJob) Kind() string { return "vm_recovery" }

// Attempts are for panel restarts, as for a move: a recovery that fails is
// final and recorded, never retried by the queue.
func (RecoveryJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueBatch, Priority: PriorityHigh, MaxAttempts: 10, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// RecoveryHook runs the job; the panel sets it.
var RecoveryHook func(ctx context.Context, recoveryID string) error

type recoveryWorker struct {
	river.WorkerDefaults[RecoveryJob]
}

func (*recoveryWorker) Work(ctx context.Context, job *river.Job[RecoveryJob]) error {
	if RecoveryHook == nil {
		return river.JobSnooze(10 * time.Second)
	}
	return RecoveryHook(ctx, job.Args.RecoveryID)
}

// A restore downloads a whole disk before the node answers.
func (*recoveryWorker) Timeout(*river.Job[RecoveryJob]) time.Duration { return 2 * diskTransferTimeout }

// RecoveryActive reports that a VM is being recovered from a failed node. A
// database without the table (an older test schema) has no recoveries.
func RecoveryActive(ctx context.Context, db *gorm.DB, vmID string) bool {
	if db == nil {
		return false
	}
	var n int64
	err := db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).Table("vm_recoveries").
		Where("vm_id = ? AND status IN ?", vmID, []string{models.RecoveryQueued, models.RecoveryRunning}).Count(&n).Error
	return err == nil && n > 0
}

// RecoveryCommand is the command that brings a VM back on node from params, a
// create job's params: ADOPT when adopt lists its existing disks, otherwise
// the CREATE a restore as a new VM sends, with the backup destination.
func RecoveryCommand(ctx context.Context, vm *models.VM, params json.RawMessage, adopt []*pb.AdoptDisk) (*pb.VMCommandRequest, error) {
	cc, err := buildCommandConfig(ctx, slog.Default(), vm, VMOpCreate, params)
	if err != nil {
		return nil, err
	}
	if cc.Config == nil {
		return nil, fmt.Errorf("the VM's definition could not be built")
	}
	req := &pb.VMCommandRequest{VmId: vm.ID, Command: pb.VMCommandType_VM_COMMAND_TYPE_CREATE, Config: cc.Config,
		PoolPath: cc.PoolPath, PoolType: cc.PoolType, TimeoutSeconds: int32(diskTransferTimeout.Seconds())}
	if len(adopt) > 0 {
		req.Command, req.AdoptDisks, req.PoolPath, req.PoolType = pb.VMCommandType_VM_COMMAND_TYPE_ADOPT, adopt, "", ""
		return req, nil
	}
	if cc.ObjectStore {
		store, _, err := panelObjectStore(ctx, cc.Destination)
		if err != nil {
			return nil, fmt.Errorf("the backup destination saved in the panel cannot be read: %w", err)
		}
		req.ObjectStore = store
	}
	return req, nil
}

// CreateStillRunning recognises a node's answer that an earlier try of the
// same create is still downloading the disk.
func CreateStillRunning(detail string) bool { return createStillRunning(detail) }

// DiskTransferTimeout is how long a node may take to download a backup.
const DiskTransferTimeout = diskTransferTimeout
