// Package service provides business logic for snapshot operations
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

var (
	// ErrSnapshotNotFound is returned when a snapshot is not found
	ErrSnapshotNotFound = errors.New("snapshot not found")
	// ErrSnapshotNameExists is returned when a snapshot name already exists for a VM
	ErrSnapshotNameExists = errors.New("snapshot name already exists for this VM")
	// ErrVMSnapshotLimitReached is returned when the VM has too many snapshots
	ErrVMSnapshotLimitReached = errors.New("VM snapshot limit reached")
	// ErrSnapshotOperationFailed is returned when a snapshot operation fails
	ErrSnapshotOperationFailed = errors.New("snapshot operation failed")
	// ErrSnapshotInProgress is returned when a snapshot operation is already in progress
	ErrSnapshotInProgress = errors.New("snapshot operation already in progress")
)

// MaxSnapshotsPerVM is the maximum number of snapshots allowed per VM
const MaxSnapshotsPerVM = 10

// SnapshotService handles snapshot-related business operations
type SnapshotService struct {
	db           *gorm.DB
	snapshotRepo *repository.SnapshotRepository
	vmRepo       *repository.VMRepository
	nodeRepo     *repository.NodeRepository
	riverClient  *river.Client[pgx.Tx]
	logger       *slog.Logger
}

// NewSnapshotService creates a new SnapshotService instance
func NewSnapshotService(
	db *gorm.DB,
	snapshotRepo *repository.SnapshotRepository,
	vmRepo *repository.VMRepository,
	nodeRepo *repository.NodeRepository,
	riverClient *river.Client[pgx.Tx],
	logger *slog.Logger,
) *SnapshotService {
	return &SnapshotService{
		db:           db,
		snapshotRepo: snapshotRepo,
		vmRepo:       vmRepo,
		nodeRepo:     nodeRepo,
		riverClient:  riverClient,
		logger:       logger,
	}
}

// ============================================================================
// Create Snapshot
// ============================================================================

// CreateSnapshotRequest contains parameters for creating a new snapshot
type CreateSnapshotRequest struct {
	VMID   string `json:"vm_id" validate:"required,uuid"`
	Name   string `json:"name" validate:"required,max=100"`
	UserID string `json:"user_id" validate:"required,uuid"`
}

// CreateSnapshotResponse contains the created snapshot and job information
type CreateSnapshotResponse struct {
	Snapshot *models.Snapshot `json:"snapshot"`
	JobID    int64            `json:"job_id"`
	Status   string           `json:"status"`
}

// CreateSnapshot creates a new VM snapshot and enqueues a creation job
func (s *SnapshotService) CreateSnapshot(ctx context.Context, req *CreateSnapshotRequest) (*CreateSnapshotResponse, error) {
	// Get VM details
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// Verify user owns the VM (or is admin)
	if vm.UserID != req.UserID {
		return nil, fmt.Errorf("unauthorized: VM does not belong to user")
	}

	// Check if snapshot name already exists for this VM
	exists, err := s.snapshotRepo.NameExists(ctx, req.VMID, req.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to check snapshot name: %w", err)
	}
	if exists {
		return nil, ErrSnapshotNameExists
	}

	// Check snapshot limit
	count, err := s.snapshotRepo.CountByVMID(ctx, req.VMID)
	if err != nil {
		return nil, fmt.Errorf("failed to count snapshots: %w", err)
	}
	if count >= MaxSnapshotsPerVM {
		return nil, ErrVMSnapshotLimitReached
	}

	// Check if there's already a pending snapshot operation for this VM
	pendingSnapshots, err := s.snapshotRepo.ListByStatus(ctx, models.SnapshotStatusPending, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to check pending snapshots: %w", err)
	}
	for _, snap := range pendingSnapshots {
		if snap.VMID == req.VMID {
			return nil, ErrSnapshotInProgress
		}
	}

	// Create snapshot record
	snapshot := &models.Snapshot{
		VMID:     req.VMID,
		Name:     req.Name,
		DiskPath: "", // Will be updated by agent
		Status:   models.SnapshotStatusPending,
	}

	if err := s.snapshotRepo.Create(ctx, snapshot); err != nil {
		return nil, fmt.Errorf("failed to create snapshot record: %w", err)
	}

	// Prepare snapshot creation params
	params := map[string]interface{}{
		"snapshot_name": req.Name,
		"vm_hostname":   vm.Hostname,
		"created_at":    time.Now().Format(time.RFC3339),
	}
	paramsJSON, _ := json.Marshal(params)

	// Enqueue snapshot creation job
	job := queue.SnapshotJob{
		VMID:       vm.ID,
		SnapshotID: snapshot.ID,
		Operation:  queue.SnapshotOpCreate,
		NodeID:     vm.NodeID,
		Name:       req.Name,
		Params:     paramsJSON,
	}

	result, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		// Rollback snapshot creation on job enqueue failure
		if delErr := s.snapshotRepo.Delete(ctx, snapshot.ID); delErr != nil {
			s.logger.ErrorContext(ctx, "failed to rollback snapshot creation after job enqueue failure",
				"snapshot_id", snapshot.ID, "error", delErr)
		}
		return nil, fmt.Errorf("failed to enqueue snapshot creation job: %w", err)
	}

	s.logger.InfoContext(ctx, "snapshot creation job enqueued",
		"snapshot_id", snapshot.ID,
		"vm_id", vm.ID,
		"job_id", result.Job.ID,
		"node_id", vm.NodeID,
	)

	return &CreateSnapshotResponse{
		Snapshot: snapshot,
		JobID:    result.Job.ID,
		Status:   "pending",
	}, nil
}

// ============================================================================
// List Snapshots
// ============================================================================

// ListSnapshotsRequest contains filtering and pagination parameters
type ListSnapshotsRequest struct {
	VMID   string `json:"vm_id,omitempty" validate:"omitempty,uuid"`
	UserID string `json:"user_id,omitempty" validate:"omitempty,uuid"`
	Status string `json:"status,omitempty" validate:"omitempty,oneof=pending completed failed"`
	Limit  int    `json:"limit,omitempty" validate:"omitempty,min=1,max=100"`
	Offset int    `json:"offset,omitempty" validate:"omitempty,min=0"`
}

// ListSnapshotsResponse contains the list of snapshots and pagination info
type ListSnapshotsResponse struct {
	Snapshots []models.Snapshot `json:"snapshots"`
	Total     int64             `json:"total"`
	Limit     int               `json:"limit"`
	Offset    int               `json:"offset"`
	HasMore   bool              `json:"has_more"`
}

// ListSnapshots retrieves snapshots with filtering and pagination
func (s *SnapshotService) ListSnapshots(ctx context.Context, req *ListSnapshotsRequest) (*ListSnapshotsResponse, error) {
	// Set default pagination
	limit := req.Limit
	if limit == 0 {
		limit = 20
	}

	var snapshots []models.Snapshot
	var total int64
	var err error

	// Apply filters
	switch {
	case req.VMID != "":
		// Verify user has access to this VM
		vm, err := s.vmRepo.GetByID(ctx, req.VMID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrVMNotFound
			}
			return nil, fmt.Errorf("failed to get VM: %w", err)
		}
		if vm.UserID != req.UserID {
			return nil, fmt.Errorf("unauthorized: VM does not belong to user")
		}

		snapshots, err = s.snapshotRepo.ListByVMID(ctx, req.VMID, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list snapshots by VM: %w", err)
		}
		total, err = s.snapshotRepo.CountByVMID(ctx, req.VMID)
	case req.Status != "":
		status := models.SnapshotStatus(req.Status)
		snapshots, err = s.snapshotRepo.ListByStatus(ctx, status, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list snapshots by status: %w", err)
		}
		total, err = s.snapshotRepo.CountByStatus(ctx, status)
	default:
		snapshots, err = s.snapshotRepo.List(ctx, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list snapshots: %w", err)
		}
		total, err = s.snapshotRepo.Count(ctx)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to count snapshots: %w", err)
	}

	return &ListSnapshotsResponse{
		Snapshots: snapshots,
		Total:     total,
		Limit:     limit,
		Offset:    req.Offset,
		HasMore:   int64(req.Offset+limit) < total,
	}, nil
}

// ============================================================================
// Get Snapshot
// ============================================================================

// GetSnapshot retrieves a snapshot by ID
func (s *SnapshotService) GetSnapshot(ctx context.Context, snapshotID string, userID string) (*models.Snapshot, error) {
	snapshot, err := s.snapshotRepo.GetByID(ctx, snapshotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSnapshotNotFound
		}
		return nil, fmt.Errorf("failed to get snapshot: %w", err)
	}

	// Verify user has access to the VM this snapshot belongs to
	vm, err := s.vmRepo.GetByID(ctx, snapshot.VMID)
	if err != nil {
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	if vm.UserID != userID {
		return nil, fmt.Errorf("unauthorized: snapshot does not belong to user")
	}

	return snapshot, nil
}

// ============================================================================
// Restore Snapshot
// ============================================================================

// RestoreSnapshotRequest contains parameters for restoring a snapshot
type RestoreSnapshotRequest struct {
	SnapshotID string `json:"snapshot_id" validate:"required,uuid"`
	VMID       string `json:"vm_id" validate:"required,uuid"`
	UserID     string `json:"user_id" validate:"required,uuid"`
}

// RestoreSnapshotResponse contains the result of a restore operation
type RestoreSnapshotResponse struct {
	SnapshotID string `json:"snapshot_id"`
	VMID       string `json:"vm_id"`
	Status     string `json:"status"`
	JobID      int64  `json:"job_id"`
	Message    string `json:"message,omitempty"`
}

// RestoreSnapshot restores a VM to a snapshot state
func (s *SnapshotService) RestoreSnapshot(ctx context.Context, req *RestoreSnapshotRequest) (*RestoreSnapshotResponse, error) {
	// Get snapshot details
	snapshot, err := s.snapshotRepo.GetByID(ctx, req.SnapshotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSnapshotNotFound
		}
		return nil, fmt.Errorf("failed to get snapshot: %w", err)
	}

	// Verify snapshot belongs to the specified VM
	if snapshot.VMID != req.VMID {
		return nil, fmt.Errorf("snapshot does not belong to the specified VM")
	}

	// Verify VM exists and user has access
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	if vm.UserID != req.UserID {
		return nil, fmt.Errorf("unauthorized: VM does not belong to user")
	}

	// Verify snapshot is completed
	if snapshot.Status != models.SnapshotStatusComplete {
		return nil, fmt.Errorf("cannot restore from snapshot with status: %s", snapshot.Status)
	}

	// VM must be stopped for restore
	if vm.Status == models.VMStatusRunning {
		return nil, fmt.Errorf("VM must be stopped before restoring snapshot")
	}

	// Prepare restore params
	params := map[string]interface{}{
		"snapshot_name": snapshot.Name,
		"disk_path":     snapshot.DiskPath,
		"restored_at":   time.Now().Format(time.RFC3339),
	}
	paramsJSON, _ := json.Marshal(params)

	// Enqueue snapshot restore job
	job := queue.SnapshotJob{
		VMID:       vm.ID,
		SnapshotID: snapshot.ID,
		Operation:  queue.SnapshotOpRestore,
		NodeID:     vm.NodeID,
		DiskPath:   snapshot.DiskPath,
		Params:     paramsJSON,
	}

	result, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to enqueue snapshot restore job: %w", err)
	}

	s.logger.InfoContext(ctx, "snapshot restore job enqueued",
		"snapshot_id", snapshot.ID,
		"vm_id", vm.ID,
		"job_id", result.Job.ID,
		"node_id", vm.NodeID,
	)

	return &RestoreSnapshotResponse{
		SnapshotID: snapshot.ID,
		VMID:       vm.ID,
		Status:     "pending",
		JobID:      result.Job.ID,
		Message:    "Snapshot restore initiated",
	}, nil
}

// ============================================================================
// Delete Snapshot
// ============================================================================

// DeleteSnapshotRequest contains parameters for deleting a snapshot
type DeleteSnapshotRequest struct {
	SnapshotID string `json:"snapshot_id" validate:"required,uuid"`
	VMID       string `json:"vm_id" validate:"required,uuid"`
	UserID     string `json:"user_id" validate:"required,uuid"`
}

// DeleteSnapshot deletes a snapshot and its backing file
func (s *SnapshotService) DeleteSnapshot(ctx context.Context, req *DeleteSnapshotRequest) error {
	// Get snapshot details
	snapshot, err := s.snapshotRepo.GetByID(ctx, req.SnapshotID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrSnapshotNotFound
		}
		return fmt.Errorf("failed to get snapshot: %w", err)
	}

	// Verify snapshot belongs to the specified VM
	if snapshot.VMID != req.VMID {
		return fmt.Errorf("snapshot does not belong to the specified VM")
	}

	// Verify VM exists and user has access
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrVMNotFound
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	if vm.UserID != req.UserID {
		return fmt.Errorf("unauthorized: VM does not belong to user")
	}

	// Enqueue snapshot delete job
	params := map[string]interface{}{
		"snapshot_name": snapshot.Name,
		"disk_path":     snapshot.DiskPath,
		"deleted_at":    time.Now().Format(time.RFC3339),
	}
	paramsJSON, _ := json.Marshal(params)

	job := queue.SnapshotJob{
		VMID:       vm.ID,
		SnapshotID: snapshot.ID,
		Operation:  queue.SnapshotOpDelete,
		NodeID:     vm.NodeID,
		DiskPath:   snapshot.DiskPath,
		Params:     paramsJSON,
	}

	_, err = s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		return fmt.Errorf("failed to enqueue snapshot delete job: %w", err)
	}

	s.logger.InfoContext(ctx, "snapshot delete job enqueued",
		"snapshot_id", snapshot.ID,
		"vm_id", vm.ID,
		"node_id", vm.NodeID,
	)

	// Delete the snapshot record from database
	// Note: The actual disk file will be deleted by the agent
	if err := s.snapshotRepo.Delete(ctx, snapshot.ID); err != nil {
		return fmt.Errorf("failed to delete snapshot record: %w", err)
	}

	return nil
}

// ============================================================================
// Update Snapshot Status (called by worker)
// ============================================================================

// UpdateSnapshotStatus updates the status of a snapshot
func (s *SnapshotService) UpdateSnapshotStatus(ctx context.Context, snapshotID string, status models.SnapshotStatus, diskPath string) error {
	updates := map[string]interface{}{
		"status": status,
	}
	if diskPath != "" {
		updates["disk_path"] = diskPath
	}

	return s.db.WithContext(ctx).Model(&models.Snapshot{}).Where("id = ?", snapshotID).Updates(updates).Error
}
