package queue

import (
	"context"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// MigrationJob moves one VM to another node. Its record (vm_migrations) holds
// the plan and the progress; the job only names it.
type MigrationJob struct {
	MigrationID string `json:"migration_id" river:"unique"`
	// VMID names the VM in the alert a failed move raises.
	VMID string `json:"vm_id"`
}

func (MigrationJob) Kind() string { return "vm_migration" }

// A migration resumes where it was after a panel restart (the node keeps
// copying meanwhile), so attempts are for restarts, not for retrying a move
// that failed: a failure is final and recorded.
func (MigrationJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueBatch, Priority: PriorityHigh, MaxAttempts: 10, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// DrainJob moves every VM it can off a node.
type DrainJob struct {
	DrainID string `json:"drain_id" river:"unique"`
}

func (DrainJob) Kind() string { return "node_drain" }

func (DrainJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueBatch, Priority: PriorityHigh, MaxAttempts: 10, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// MigrationHook and DrainHook run the jobs. The panel sets them: placement,
// network config and alerts live in its services, which this package cannot
// import.
var (
	MigrationHook func(ctx context.Context, migrationID string) error
	DrainHook     func(ctx context.Context, drainID string) error
)

type migrationWorker struct {
	river.WorkerDefaults[MigrationJob]
}

func (*migrationWorker) Work(ctx context.Context, job *river.Job[MigrationJob]) error {
	if MigrationHook == nil {
		// Taken in the moment between the queue starting and the routes being built.
		return river.JobSnooze(10 * time.Second)
	}
	return MigrationHook(ctx, job.Args.MigrationID)
}

// A large disk across a slow link takes many hours; past this the job ends
// and its next try picks the move up where it is.
func (*migrationWorker) Timeout(*river.Job[MigrationJob]) time.Duration { return 24 * time.Hour }

type drainWorker struct {
	river.WorkerDefaults[DrainJob]
}

func (*drainWorker) Work(ctx context.Context, job *river.Job[DrainJob]) error {
	if DrainHook == nil {
		return river.JobSnooze(10 * time.Second)
	}
	return DrainHook(ctx, job.Args.DrainID)
}

func (*drainWorker) Timeout(*river.Job[DrainJob]) time.Duration { return 72 * time.Hour }

// MigrationActive reports that a VM is being moved between nodes, or recovered
// onto another node from one that went down. A database without the table (an
// older test schema) has no migrations.
func MigrationActive(ctx context.Context, vmID string) bool {
	if globalWorkerContext == nil || globalWorkerContext.DB == nil {
		return false
	}
	if RecoveryActive(ctx, globalWorkerContext.DB, vmID) {
		return true
	}
	var n int64
	err := globalWorkerContext.DB.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).Table("vm_migrations").
		Where("vm_id = ? AND status IN ?", vmID, []string{models.MigrationQueued, models.MigrationRunning, models.MigrationCancelling}).
		Count(&n).Error
	return err == nil && n > 0
}

// RestoreActive reports a restore onto the VM, from a backup or a snapshot,
// whose job is queued or running. Read from the queue, it ends with the job
// however that ends: done, failed, cancelled, or rescued after a panel restart.
// A database without the queue's tables has none.
func RestoreActive(ctx context.Context, db *gorm.DB, vmID string) bool {
	if db == nil {
		return false
	}
	var n int64
	err := db.WithContext(ctx).Session(&gorm.Session{Logger: gormlogger.Discard}).Raw(`SELECT count(*) FROM river_job
		WHERE state IN ('available', 'pending', 'running', 'retryable', 'scheduled') AND args->>'vm_id' = ?
		  AND (kind = ? OR (kind = ? AND args->>'operation' = ?))`,
		vmID, RestoreJob{}.Kind(), SnapshotJob{}.Kind(), string(SnapshotOpRestore)).Scan(&n).Error
	return err == nil && n > 0
}

// currentNode is the node a VM is on now; a job queued before the VM moved
// names the node it left.
func currentNode(vm *models.VM, queuedFor string) string {
	if vm != nil && vm.NodeID != "" {
		return vm.NodeID
	}
	return queuedFor
}

// ApplyVMNetworkConfig pushes a VM's network config, as a configure network job
// carries it, to a node now and waits for the answer. A moved VM gets its host
// rules this way the moment it arrives, rather than whenever a job runs.
func ApplyVMNetworkConfig(ctx context.Context, client pb.NodeAgentClient, node *models.Node, vm *models.VM, params []byte) error {
	w := &VMOperationWorker{logger: slog.Default()}
	job := &river.Job[VMOperationJob]{Args: VMOperationJob{VMID: vm.ID, NodeID: node.ID, Operation: VMOpConfigureNetwork, Params: params}}
	return w.handleConfigureNetwork(ctx, client, node, vm, job)
}

// DiskMoveJob follows one move of a VM's disk to another pool of its node.
// Its record (vm_disk_moves) holds the progress.
type DiskMoveJob struct {
	MoveID string `json:"move_id" river:"unique"`
}

func (DiskMoveJob) Kind() string { return "vm_disk_move" }

func (DiskMoveJob) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueBatch, Priority: PriorityHigh, MaxAttempts: 10, UniqueOpts: river.UniqueOpts{ByArgs: true}}
}

// DiskMoveHook runs a disk move job; the panel sets it.
var DiskMoveHook func(ctx context.Context, moveID string) error

type diskMoveWorker struct {
	river.WorkerDefaults[DiskMoveJob]
}

func (*diskMoveWorker) Work(ctx context.Context, job *river.Job[DiskMoveJob]) error {
	if DiskMoveHook == nil {
		return river.JobSnooze(10 * time.Second)
	}
	return DiskMoveHook(ctx, job.Args.MoveID)
}

// A large disk takes hours; past this the next try follows the node again.
func (*diskMoveWorker) Timeout(*river.Job[DiskMoveJob]) time.Duration { return 24 * time.Hour }
