package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"github.com/maburvm/panel/internal/shared/storage"
)

var (
	ErrBackupNotFound        = errors.New("backup not found")
	ErrScheduleNotFound      = errors.New("backup schedule not found")
	ErrInvalidCronExpression = errors.New("invalid cron expression")
	ErrInvalidRetention      = errors.New("invalid retention policy")
	ErrInvalidCompression    = errors.New("compression must be gzip, zstd or none")
	ErrScheduleAlreadyExists = errors.New("backup schedule already exists for this VM")
	ErrBackupInProgress      = errors.New("backup already in progress for this VM")
	ErrStorageUploadFailed   = errors.New("storage upload failed")
	// ErrBackupFileKept is returned when a backup's file could not be removed
	// from storage. The backup is kept, so the delete can be tried again.
	ErrBackupFileKept = errors.New("the backup's file could not be removed from storage, so the backup was kept")
	// ErrBackupHasDependents refuses deleting a backup newer incremental
	// backups are built on: they could no longer be restored.
	ErrBackupHasDependents = errors.New("newer incremental backups are built on this backup")
	// ErrInvalidFullEvery refuses a full-backup interval out of range.
	ErrInvalidFullEvery = fmt.Errorf("full_every_days must be between 0 and %d", models.MaxFullEveryDays)
)

// BackupService handles backup-related business operations
type BackupService struct {
	db            *gorm.DB
	backupRepo    *repository.BackupRepository
	scheduleRepo  *repository.BackupScheduleRepository
	vmRepo        *repository.VMRepository
	nodeRepo      *repository.NodeRepository
	riverClient   *river.Client[pgx.Tx]
	dest          *BackupDestinations
	objectStore   backupObjectStore // replaces the destination's client in tests
	vmService     *VMService
	logger        *slog.Logger
	cronParser    cron.Parser
	stop          chan struct{}
	lastRetention time.Time // touched by the scheduler loop only
	// fire replaces executeScheduledBackup in tests.
	fire func(ctx context.Context, schedule *models.BackupSchedule, now time.Time)
}

// NewBackupService creates a new BackupService instance. dest is where backups
// are kept; nil means only what each node has in its own environment file.
func NewBackupService(
	db *gorm.DB,
	backupRepo *repository.BackupRepository,
	scheduleRepo *repository.BackupScheduleRepository,
	vmRepo *repository.VMRepository,
	nodeRepo *repository.NodeRepository,
	riverClient *river.Client[pgx.Tx],
	dest *BackupDestinations,
	logger *slog.Logger,
) *BackupService {
	cronParser := cron.NewParser(
		cron.SecondOptional |
			cron.Minute |
			cron.Hour |
			cron.Dom |
			cron.Month |
			cron.Dow |
			cron.Descriptor,
	)

	return &BackupService{
		db:           db,
		backupRepo:   backupRepo,
		scheduleRepo: scheduleRepo,
		vmRepo:       vmRepo,
		nodeRepo:     nodeRepo,
		riverClient:  riverClient,
		dest:         dest,
		logger:       logger,
		cronParser:   cronParser,
	}
}

// Start starts the backup scheduler. Schedules live in the database (their
// next run included), so a restart loses none and runs what it missed once.
func (s *BackupService) Start() error {
	n, err := s.scheduleRepo.CountByStatus(context.Background(), models.BackupScheduleStatusActive)
	if err != nil {
		s.logger.Error("failed to load active backup schedules", "error", err)
		return err
	}
	s.stop = make(chan struct{})
	go s.run(s.stop)
	s.logger.Info("backup service started", "schedules", n)
	return nil
}

// Stop stops the scheduler.
func (s *BackupService) Stop() {
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	s.logger.Info("backup service stopped")
}

// ============================================================================
// Create Backup (Manual)
// ============================================================================

// CreateBackupRequest contains parameters for creating a backup
type CreateBackupRequest struct {
	VMID            string `json:"vm_id" validate:"required,uuid"`
	StorageProvider string `json:"storage_provider,omitempty" validate:"omitempty,max=100"`
	Compression     string `json:"compression,omitempty" validate:"omitempty,oneof=gzip zstd none"`
}

// CreateBackupResponse contains the created backup and job information
type CreateBackupResponse struct {
	Backup *models.Backup `json:"backup"`
	JobID  int64          `json:"job_id"`
}

// GetVMOwner returns the user ID owning the given VM, for per-resource
// authorization of VM-scoped backup endpoints. Returns ErrVMNotFound if absent.
func (s *BackupService) GetVMOwner(ctx context.Context, vmID string) (string, error) {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrVMNotFound
		}
		return "", fmt.Errorf("failed to get VM: %w", err)
	}
	return vm.UserID, nil
}

// CreateBackup creates a manual backup and enqueues a backup job
func (s *BackupService) CreateBackup(ctx context.Context, req *CreateBackupRequest) (*CreateBackupResponse, error) {
	// Validate VM exists
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// Concurrent-backup guard is enforced atomically by the
	// ux_backups_active_per_vm partial unique index (see backupRepo.Create below),
	// which covers both this path and the scheduled path with no TOCTOU window.

	kept, err := keptBackups(ctx, s.db, vm.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to count the VM's backups: %w", err)
	}
	if err := checkVMAllowance(ctx, s.db, vm, kept, planMaxBackups, "backup", "backups", "Delete one to make another."); err != nil {
		return nil, err
	}

	// Determine compression
	if !validCompression(req.Compression) {
		return nil, ErrInvalidCompression
	}
	compression := req.Compression
	if compression == "" {
		compression = "gzip"
	}

	// A destination saved but unreadable stops here: sending nothing would put
	// the backup in the node's own storage without anyone choosing that.
	dest, err := s.dest.Resolve(ctx, "")
	if err != nil {
		return nil, err
	}

	// Generate storage path
	timestamp := time.Now().UTC().Format("20060102_150405")
	storagePath := dest.Key(fmt.Sprintf("backups/%s/%s_%s.qcow2", vm.ID, vm.ID, timestamp))

	// Create backup record. Where the file ends up is the worker's to record,
	// once the node has said where it put it. A manual backup is always a
	// full one: it is kept until deleted, and nothing else depends on it.
	backup := &models.Backup{
		VMID:            req.VMID,
		StorageProvider: models.BackupStoreDestination,
		StoragePath:     storagePath,
		BackupType:      models.BackupTypeManual,
		Status:          models.BackupStatusPending,
		Compression:     compression,
		Mode:            models.BackupModeFull,
	}
	if dest != nil {
		backup.StorageProvider, backup.DestinationID = dest.Kind, &dest.ID
	}

	if err := s.backupRepo.Create(ctx, backup); err != nil {
		if isBackupInProgressViolation(err) {
			return nil, ErrBackupInProgress
		}
		return nil, fmt.Errorf("failed to create backup record: %w", err)
	}

	// Enqueue backup job
	job := queue.BackupJob{
		VMID:            backup.VMID,
		BackupType:      queue.BackupTypeFull,
		StorageProvider: backup.StorageProvider,
		Destination:     backup.StoragePath,
		Compression:     backup.Compression,
		BackupID:        backup.ID,
		DestinationID:   strVal(backup.DestinationID),
	}

	result, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		// Rollback backup creation on job enqueue failure
		if delErr := s.backupRepo.Delete(ctx, backup.ID); delErr != nil {
			s.logger.ErrorContext(ctx, "failed to rollback backup creation after job enqueue failure",
				"backup_id", backup.ID, "error", delErr)
		}
		return nil, fmt.Errorf("failed to enqueue backup job: %w", err)
	}

	s.logger.InfoContext(ctx, "backup job enqueued",
		"backup_id", backup.ID,
		"vm_id", backup.VMID,
		"job_id", result.Job.ID,
	)

	return &CreateBackupResponse{
		Backup: backup,
		JobID:  result.Job.ID,
	}, nil
}

// ============================================================================
// List Backups
// ============================================================================

// ListBackupsRequest contains filtering and pagination parameters
type ListBackupsRequest struct {
	VMID            string              `json:"vm_id,omitempty" validate:"omitempty,uuid"`
	Status          models.BackupStatus `json:"status,omitempty" validate:"omitempty,oneof=pending in_progress completed failed"`
	StorageProvider string              `json:"storage_provider,omitempty" validate:"omitempty,max=100"`
	Limit           int                 `json:"limit,omitempty" validate:"omitempty,min=1,max=100"`
	Offset          int                 `json:"offset,omitempty" validate:"omitempty,min=0"`
}

// ListBackupsResponse contains the list of backups and pagination info
type ListBackupsResponse struct {
	Backups []models.Backup `json:"backups"`
	Total   int64           `json:"total"`
	Limit   int             `json:"limit"`
	Offset  int             `json:"offset"`
	HasMore bool            `json:"has_more"`
}

// ListBackups retrieves backups with filtering and pagination
func (s *BackupService) ListBackups(ctx context.Context, req *ListBackupsRequest) (*ListBackupsResponse, error) {
	limit := req.Limit
	if limit == 0 {
		limit = 20
	}

	var backups []models.Backup
	var total int64
	var err error

	// Apply filters
	switch {
	case req.VMID != "":
		backups, err = s.backupRepo.ListByVMID(ctx, req.VMID, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list backups by VM: %w", err)
		}
		total, err = s.backupRepo.CountByVMID(ctx, req.VMID)
	case req.Status != "":
		backups, err = s.backupRepo.ListByStatus(ctx, req.Status, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list backups by status: %w", err)
		}
		total, err = s.backupRepo.CountByStatus(ctx, req.Status)
	case req.StorageProvider != "":
		backups, err = s.backupRepo.ListByStorageProvider(ctx, req.StorageProvider, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list backups by storage provider: %w", err)
		}
		total, err = s.backupRepo.Count(ctx)
	default:
		backups, err = s.backupRepo.List(ctx, limit, req.Offset)
		if err != nil {
			return nil, fmt.Errorf("failed to list backups: %w", err)
		}
		total, err = s.backupRepo.Count(ctx)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to count backups: %w", err)
	}

	return &ListBackupsResponse{
		Backups: backups,
		Total:   total,
		Limit:   limit,
		Offset:  req.Offset,
		HasMore: int64(req.Offset+limit) < total,
	}, nil
}

// ============================================================================
// Get Backup
// ============================================================================

// GetBackup retrieves a backup by ID
func (s *BackupService) GetBackup(ctx context.Context, backupID string) (*models.Backup, error) {
	backup, err := s.backupRepo.GetByID(ctx, backupID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBackupNotFound
		}
		return nil, fmt.Errorf("failed to get backup: %w", err)
	}
	return backup, nil
}

// ============================================================================
// Delete Backup
// ============================================================================

// DeleteBackup deletes a backup and its file in storage. fileNotDeleted is
// true when the record was removed but no file was: it is not where the panel
// can look (a node's own storage, or a bucket used before the destination was
// changed).
func (s *BackupService) DeleteBackup(ctx context.Context, backupID string) (fileNotDeleted bool, err error) {
	backup, err := s.backupRepo.GetByID(ctx, backupID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, ErrBackupNotFound
		}
		return false, fmt.Errorf("failed to get backup: %w", err)
	}

	all, err := s.vmBackups(ctx, backup.VMID)
	if err != nil {
		return false, err
	}
	if n := dependents(all, backup.ID); n > 0 {
		return false, fmt.Errorf("%w: %d newer backups need it to be restored; delete those first, newest first", ErrBackupHasDependents, n)
	}

	// The file goes first. If it cannot be removed the record stays, so the
	// delete can be tried again: a record removed on its own leaves a file
	// nothing points to any more, kept and paid for indefinitely.
	notDeleted, err := s.removeBackupFile(ctx, backup)
	if err != nil {
		s.logger.WarnContext(ctx, "backup kept: its file could not be removed from storage",
			"backup_id", backupID, "storage_path", backup.StoragePath, "error", err)
		return false, fmt.Errorf("%w: %v", ErrBackupFileKept, err)
	}

	// Delete backup record
	if err := s.backupRepo.Delete(ctx, backupID); err != nil {
		return false, fmt.Errorf("failed to delete backup: %w", err)
	}

	s.logger.InfoContext(ctx, "backup deleted",
		"backup_id", backupID,
		"vm_id", backup.VMID,
		"storage_path", backup.StoragePath,
		"file_not_deleted", notDeleted,
	)

	return notDeleted, nil
}

// vmBackups are a VM's backups that are not deleted, every status.
func (s *BackupService) vmBackups(ctx context.Context, vmID string) ([]models.Backup, error) {
	var all []models.Backup
	if err := s.db.WithContext(ctx).Where("vm_id = ?", vmID).Order("created_at ASC").Find(&all).Error; err != nil {
		return nil, fmt.Errorf("failed to list the VM's backups: %w", err)
	}
	return all, nil
}

// destinationOf is the destination a backup is in. An older backup records
// none: one kept "in the destination" was in the only one there was, now the
// default, and one in a node's own storage is looked for there too, since that
// is often the same bucket. nil means the panel cannot look.
func (s *BackupService) destinationOf(ctx context.Context, backup *models.Backup) (*ResolvedDestination, error) {
	if id := strVal(backup.DestinationID); id != "" {
		return s.dest.Resolve(ctx, id)
	}
	r, err := s.dest.Resolve(ctx, "")
	if err != nil || r == nil || (backup.StorageProvider == models.BackupStoreNode && r.Kind != storage.KindS3) {
		return nil, err
	}
	return r, nil
}

// removeBackupFile deletes a backup's files from its destination. notDeleted
// is true when there was no file there to delete: it is in storage only its
// node knows, or in a bucket the destination pointed at before it was
// changed. The caller says so instead of claiming the file is gone.
func (s *BackupService) removeBackupFile(ctx context.Context, backup *models.Backup) (notDeleted bool, err error) {
	if backup.Status != models.BackupStatusCompleted || backup.StoragePath == "" {
		return false, nil // never uploaded
	}
	var client backupObjectStore = s.objectStore
	if client == nil {
		r, err := s.destinationOf(ctx, backup)
		if err != nil {
			return false, err
		}
		if r == nil {
			if backup.StorageProvider == models.BackupStoreNode {
				return true, nil
			}
			return false, errors.New("no backup destination is set; add one under System settings, Backup")
		}
		if r.Kind == storage.KindPath {
			return s.removeOnNodes(ctx, r, backup)
		}
		st, err := r.Open(ctx)
		if err != nil {
			return false, err
		}
		defer st.Close()
		client = st
	}
	// A delete of a file that is not there succeeds on S3, so look first: only
	// a file that was found can be said to have been deleted. Every disk of
	// the backup goes; the first file is the one the backup is known by.
	for i, key := range backup.StorageKeys() {
		found, err := client.Exists(ctx, key)
		if err != nil {
			return false, err
		}
		if !found {
			if i == 0 {
				notDeleted = true
			}
			continue
		}
		if err := client.Delete(ctx, key); err != nil {
			return false, err
		}
	}
	return notDeleted, nil
}

// panelStore connects the panel to a destination it can reach itself (not a
// directory on the nodes); done closes it.
func (s *BackupService) panelStore(ctx context.Context, r *ResolvedDestination) (backupObjectStore, func(), error) {
	if s.objectStore != nil {
		return s.objectStore, func() {}, nil
	}
	st, err := r.Open(ctx)
	if err != nil {
		return nil, nil, err
	}
	return st, func() { st.Close() }, nil
}

// removeOnNodes deletes a backup kept in a directory mounted on the nodes,
// through the node that wrote it or the VM's node now. Another node may not
// see that directory, and "not found" there would say nothing.
func (s *BackupService) removeOnNodes(ctx context.Context, r *ResolvedDestination, backup *models.Backup) (bool, error) {
	writer := strVal(backup.NodeID)
	if writer != "" && s.nodeRepo != nil {
		if _, err := s.nodeRepo.GetByID(ctx, writer); errors.Is(err, gorm.ErrRecordNotFound) {
			writer = "" // removed from the panel, its own disks with it
		}
	}
	var others []string
	if vm, err := s.vmRepo.GetByID(ctx, backup.VMID); err == nil && vm.NodeID != "" && vm.NodeID != writer {
		others = append(others, vm.NodeID)
	}
	keys := backup.StorageKeys()
	missing, err := s.dest.DeleteOnNodes(ctx, r, writer, others, keys)
	if err != nil {
		return false, err
	}
	for _, k := range missing {
		if k == keys[0] {
			return true, nil
		}
	}
	return false, nil
}

// backupObjectStore is the part of the storage client deleting a backup uses.
type backupObjectStore interface {
	Exists(ctx context.Context, key string) (bool, error)
	Delete(ctx context.Context, key string) error
}

// ============================================================================
// Restore Backup
// ============================================================================

// RestoreBackupRequest contains parameters for restoring a backup
type RestoreBackupRequest struct {
	VMID     string `json:"vm_id" validate:"required,uuid"`
	BackupID string `json:"backup_id" validate:"required,uuid"`
}

// RestoreBackupResponse contains the restore job information
type RestoreBackupResponse struct {
	VMID     string `json:"vm_id"`
	BackupID string `json:"backup_id"`
	JobID    int64  `json:"job_id"`
}

// RestoreBackup initiates a restore from a backup
func (s *BackupService) RestoreBackup(ctx context.Context, req *RestoreBackupRequest) (*RestoreBackupResponse, error) {
	// Validate VM exists
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// Validate backup exists
	backup, err := s.backupRepo.GetByID(ctx, req.BackupID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrBackupNotFound
		}
		return nil, fmt.Errorf("failed to get backup: %w", err)
	}

	// Validate backup is completed
	if backup.Status != models.BackupStatusCompleted {
		return nil, fmt.Errorf("backup is not completed (status: %s)", backup.Status)
	}

	// VM must be stopped for restore
	if vm.Status == models.VMStatusRunning {
		return nil, fmt.Errorf("VM must be stopped before restoring from backup")
	}

	if backup.Checksum == "" {
		return nil, fmt.Errorf("backup has no recorded checksum; cannot safely restore")
	}
	_, layers, err := s.restoreChain(ctx, backup)
	if err != nil {
		return nil, err
	}

	job := queue.RestoreJob{
		VMID:          vm.ID,
		NodeID:        vm.NodeID,
		BackupID:      backup.ID,
		SourceKey:     backup.StoragePath,
		Checksum:      backup.Checksum,
		NodeStore:     backup.StorageProvider == models.BackupStoreNode,
		Disks:         backup.Disks,
		DestinationID: strVal(backup.DestinationID),
		Layers:        layers,
	}

	result, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to enqueue restore job: %w", err)
	}

	s.logger.InfoContext(ctx, "restore job enqueued",
		"vm_id", vm.ID,
		"backup_id", backup.ID,
		"job_id", result.Job.ID,
	)

	return &RestoreBackupResponse{
		VMID:     vm.ID,
		BackupID: backup.ID,
		JobID:    result.Job.ID,
	}, nil
}

// restoreChain is what restoring a backup needs: the backups it is built on,
// the full one first and it last, and for an incremental one the images under
// each of its disks. Every backup of the chain must still be there.
func (s *BackupService) restoreChain(ctx context.Context, backup *models.Backup) ([]models.Backup, map[string][]models.BackupDisk, error) {
	if backup.Mode != models.BackupModeIncremental {
		return []models.Backup{*backup}, nil, nil
	}
	all, err := s.vmBackups(ctx, backup.VMID)
	if err != nil {
		return nil, nil, err
	}
	byID := map[string]*models.Backup{}
	for i := range all {
		byID[all[i].ID] = &all[i]
	}
	chain, err := chainOf(backup, byID)
	if err != nil {
		return nil, nil, fmt.Errorf("this incremental backup cannot be restored: %w", err)
	}
	layers, err := restoreLayers(chain)
	if err != nil {
		return nil, nil, fmt.Errorf("this incremental backup cannot be restored: %w", err)
	}
	return chain, layers, nil
}

// ============================================================================
// Backup Schedule Management
// ============================================================================

// ConfigureScheduleRequest contains parameters for configuring a backup schedule
type ConfigureScheduleRequest struct {
	VMID            string                        `json:"vm_id" validate:"required,uuid"`
	Schedule        string                        `json:"schedule" validate:"required"` // Cron expression
	StorageProvider string                        `json:"storage_provider,omitempty" validate:"omitempty,max=100"`
	Compression     string                        `json:"compression,omitempty" validate:"omitempty,oneof=gzip zstd none"`
	RetentionPolicy *models.BackupRetentionPolicy `json:"retention_policy,omitempty"`
	// FullEveryDays, when set, makes scheduled backups incremental with a
	// full one at least that often; 0 makes every one full.
	FullEveryDays *int `json:"full_every_days,omitempty"`
}

// ConfigureScheduleResponse contains the configured schedule
type ConfigureScheduleResponse struct {
	Schedule *models.BackupSchedule `json:"schedule"`
}

// ConfigureSchedule creates or updates a backup schedule for a VM
func (s *BackupService) ConfigureSchedule(ctx context.Context, req *ConfigureScheduleRequest) (*ConfigureScheduleResponse, error) {
	// Validate cron expression
	if _, err := s.cronParser.Parse(req.Schedule); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCronExpression, err)
	}
	if req.RetentionPolicy != nil {
		if err := validRetention(*req.RetentionPolicy); err != nil {
			return nil, err
		}
	}
	if !validCompression(req.Compression) {
		return nil, ErrInvalidCompression
	}
	if req.FullEveryDays != nil && (*req.FullEveryDays < 0 || *req.FullEveryDays > models.MaxFullEveryDays) {
		return nil, ErrInvalidFullEvery
	}

	// Validate VM exists
	vm, err := s.vmRepo.GetByID(ctx, req.VMID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}
	// A plan with no backups has nothing for a schedule to keep. With some,
	// scheduled backups rotate within the allowance (see executeScheduledBackup).
	if err := checkVMAllowance(ctx, s.db, vm, 0, planMaxBackups, "backup", "backups", ""); err != nil {
		return nil, err
	}

	storageProvider := models.BackupStoreDestination

	// Determine compression
	compression := req.Compression
	if compression == "" {
		compression = "gzip"
	}

	// Check if schedule already exists
	existingSchedule, err := s.scheduleRepo.GetByVMID(ctx, req.VMID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("failed to check existing schedule: %w", err)
	}

	var schedule *models.BackupSchedule

	if existingSchedule != nil {
		// Update existing schedule
		existingSchedule.Schedule = req.Schedule
		existingSchedule.StorageProvider = storageProvider
		existingSchedule.Compression = compression
		if req.RetentionPolicy != nil {
			existingSchedule.RetentionPolicy = *req.RetentionPolicy
		}
		if req.FullEveryDays != nil {
			existingSchedule.FullEveryDays = *req.FullEveryDays
		}

		if err := s.scheduleRepo.Update(ctx, existingSchedule); err != nil {
			return nil, fmt.Errorf("failed to update schedule: %w", err)
		}
		schedule = existingSchedule
	} else {
		// Create new schedule
		newSchedule := &models.BackupSchedule{
			VMID:            req.VMID,
			Schedule:        req.Schedule,
			Status:          models.BackupScheduleStatusActive,
			StorageProvider: storageProvider,
			Compression:     compression,
		}
		if req.RetentionPolicy != nil {
			newSchedule.RetentionPolicy = *req.RetentionPolicy
		}
		if req.FullEveryDays != nil {
			newSchedule.FullEveryDays = *req.FullEveryDays
		}

		if err := s.scheduleRepo.Create(ctx, newSchedule); err != nil {
			return nil, fmt.Errorf("failed to create schedule: %w", err)
		}
		schedule = newSchedule
	}

	// From now on: changing a schedule does not run it at once.
	if err := s.ScheduleBackup(ctx, schedule); err != nil {
		s.logger.ErrorContext(ctx, "failed to schedule backup",
			"schedule_id", schedule.ID,
			"error", err,
		)
	}

	s.logger.InfoContext(ctx, "backup schedule configured",
		"schedule_id", schedule.ID,
		"vm_id", schedule.VMID,
		"schedule", schedule.Schedule,
	)

	return &ConfigureScheduleResponse{
		Schedule: schedule,
	}, nil
}

// GetSchedule retrieves the backup schedule for a VM
func (s *BackupService) GetSchedule(ctx context.Context, vmID string) (*models.BackupSchedule, error) {
	schedule, err := s.scheduleRepo.GetByVMID(ctx, vmID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrScheduleNotFound
		}
		return nil, fmt.Errorf("failed to get schedule: %w", err)
	}
	return schedule, nil
}

// DeleteSchedule deletes the backup schedule for a VM
func (s *BackupService) DeleteSchedule(ctx context.Context, vmID string) error {
	if err := s.scheduleRepo.DeleteByVMID(ctx, vmID); err != nil {
		return fmt.Errorf("failed to delete schedule: %w", err)
	}

	s.logger.InfoContext(ctx, "backup schedule deleted",
		"vm_id", vmID,
	)

	return nil
}

// ============================================================================
// Scheduling
// ============================================================================

// scheduleTick is how often the scheduler looks for due backups.
const scheduleTick = time.Minute

// retentionEvery is how often old scheduled backups are pruned.
const retentionEvery = time.Hour

// run is the scheduler loop. Its first pass, at start, is the catch-up: a
// schedule whose run fell while the panel was down is due, and runs once.
func (s *BackupService) run(stop <-chan struct{}) {
	t := time.NewTicker(scheduleTick)
	defer t.Stop()
	for {
		s.RunDue(context.Background(), time.Now())
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// ScheduleBackup records when a schedule next runs, from now on.
func (s *BackupService) ScheduleBackup(ctx context.Context, schedule *models.BackupSchedule) error {
	next, err := s.nextRun(schedule.Schedule, time.Now())
	if err != nil {
		return err
	}
	if err := s.scheduleRepo.UpdateNextRun(ctx, schedule.ID, next); err != nil {
		return fmt.Errorf("failed to save the next run: %w", err)
	}
	schedule.NextRunAt = &next
	s.logger.Info("backup scheduled", "schedule_id", schedule.ID, "vm_id", schedule.VMID, "schedule", schedule.Schedule, "next_run", next)
	return nil
}

// nextRun is the schedule's first run after now.
func (s *BackupService) nextRun(expr string, now time.Time) (time.Time, error) {
	sched, err := s.cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: %v", ErrInvalidCronExpression, err)
	}
	return sched.Next(now), nil
}

// RunDue starts every active schedule that is due at now, once each, and
// prunes old backups once an hour. Runs missed while the panel was down are
// caught up by one backup, not one per missed run. Outside the backup window
// nothing starts: due schedules wait for it to open.
func (s *BackupService) RunDue(ctx context.Context, now time.Time) {
	schedules, err := s.scheduleRepo.ListActive(ctx)
	if err != nil {
		s.logger.Error("failed to load backup schedules", "error", err)
		return
	}
	inWindow := queue.LoadBackupPolicy(ctx, s.db).InWindow(now)
	fire := s.executeScheduledBackup
	if s.fire != nil {
		fire = s.fire
	}
	for i := range schedules {
		sc := &schedules[i]
		next, err := s.nextRun(sc.Schedule, now)
		if err != nil {
			s.logger.Error("backup schedule has an invalid expression", "schedule_id", sc.ID, "schedule", sc.Schedule, "error", err)
			continue
		}
		if sc.NextRunAt == nil || sc.NextRunAt.IsZero() {
			_ = s.scheduleRepo.UpdateNextRun(ctx, sc.ID, next)
			continue
		}
		if sc.NextRunAt.After(now) || !inWindow {
			continue
		}
		claimed, err := s.scheduleRepo.ClaimRun(ctx, sc.ID, now, next)
		if err != nil || !claimed {
			continue
		}
		fire(ctx, sc, now)
	}
	if now.Sub(s.lastRetention) >= retentionEvery {
		s.lastRetention = now
		for i := range schedules {
			if err := s.applyRetentionPolicy(ctx, &schedules[i], now); err != nil {
				s.logger.Error("failed to apply retention policy", "schedule_id", schedules[i].ID, "vm_id", schedules[i].VMID, "error", err)
			}
		}
	}
}

// executeScheduledBackup makes one scheduled backup of the schedule's VM.
func (s *BackupService) executeScheduledBackup(ctx context.Context, schedule *models.BackupSchedule, now time.Time) {
	vm, err := s.vmRepo.GetByID(ctx, schedule.VMID)
	if err != nil {
		s.logger.Error("failed to get VM for scheduled backup", "schedule_id", schedule.ID, "vm_id", schedule.VMID, "error", err)
		return
	}
	if ok, why, err := s.scheduledBackupAllowed(ctx, vm); !ok {
		s.logger.Warn("skipping scheduled backup", "schedule_id", schedule.ID, "vm_id", vm.ID, "reason", why, "error", err)
		return
	}

	dest, err := s.dest.Resolve(ctx, "")
	if err != nil {
		s.logger.Error("skipping scheduled backup; the backup destination cannot be read", "schedule_id", schedule.ID, "vm_id", vm.ID, "error", err)
		return
	}
	storagePath := dest.Key(fmt.Sprintf("backups/%s/scheduled_%s.qcow2", vm.ID, now.UTC().Format("20060102_150405")))
	backup := &models.Backup{
		ID:              uuid.NewString(),
		VMID:            schedule.VMID,
		StorageProvider: models.BackupStoreDestination,
		StoragePath:     storagePath,
		BackupType:      models.BackupTypeSchedule,
		Status:          models.BackupStatusPending,
		Compression:     schedule.Compression,
		Mode:            models.BackupModeFull,
	}
	job := queue.BackupJob{
		VMID:        backup.VMID,
		BackupType:  queue.BackupTypeFull,
		Destination: backup.StoragePath,
		Compression: backup.Compression,
		BackupID:    backup.ID,
		Scheduled:   true,
	}
	if dest != nil {
		backup.StorageProvider, backup.DestinationID, job.DestinationID = dest.Kind, &dest.ID, dest.ID
	}
	if schedule.FullEveryDays > 0 {
		// Each backup leaves a checkpoint for the next to build on; the
		// parent is recorded now, so nothing prunes it while this one runs.
		job.Checkpoint = models.BackupCheckpointPrefix + backup.ID
		d, derr := s.chainDecision(ctx, schedule, vm, job.DestinationID, now)
		if derr != nil {
			s.logger.Warn("taking a full backup; the chain could not be read", "schedule_id", schedule.ID, "vm_id", vm.ID, "error", derr)
		} else if d.ParentID != "" {
			backup.Mode, backup.ParentID = models.BackupModeIncremental, &d.ParentID
			job.ParentID, job.IncrementalFrom, job.ChainID = d.ParentID, d.From, d.ChainID
		}
	}
	job.StorageProvider = backup.StorageProvider
	if err := s.backupRepo.Create(ctx, backup); err != nil {
		if isBackupInProgressViolation(err) {
			s.logger.Info("skipping scheduled backup; one is already active for this VM", "schedule_id", schedule.ID, "vm_id", schedule.VMID)
			return
		}
		s.logger.Error("failed to create backup record", "schedule_id", schedule.ID, "vm_id", schedule.VMID, "error", err)
		return
	}

	result, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		s.logger.Error("failed to enqueue scheduled backup job", "backup_id", backup.ID, "schedule_id", schedule.ID, "error", err)
		_ = s.backupRepo.UpdateStatus(ctx, backup.ID, models.BackupStatusFailed)
		return
	}
	if err := s.scheduleRepo.UpdateLastRun(ctx, schedule.ID, now, &backup.ID); err != nil {
		s.logger.Error("failed to update last run time", "schedule_id", schedule.ID, "error", err)
	}
	s.logger.Info("scheduled backup job enqueued", "backup_id", backup.ID, "schedule_id", schedule.ID, "vm_id", schedule.VMID, "job_id", result.Job.ID)
}

// chainDecision says what the schedule's next backup builds on.
func (s *BackupService) chainDecision(ctx context.Context, schedule *models.BackupSchedule, vm *models.VM, destinationID string, now time.Time) (chainDecision, error) {
	all, err := s.vmBackups(ctx, vm.ID)
	if err != nil {
		return chainDecision{}, err
	}
	in := chainInput{Now: now, FullEveryDays: schedule.FullEveryDays, DestinationID: destinationID, NodeID: vm.NodeID,
		ResetAt: schedule.ChainResetAt, Backups: all}
	if plan, perr := vmPlan(ctx, s.db, vm); perr == nil && plan != nil {
		in.Allowance = plan.MaxBackups
	}
	for _, b := range all {
		if b.Status == models.BackupStatusCompleted && b.BackupType == models.BackupTypeManual {
			in.ManualKept++
		}
	}
	return decideChain(in), nil
}

// applyRetentionPolicy deletes the schedule's backups its policy no longer
// keeps. Manual backups are never touched, nor any backup a kept incremental
// one is built on.
func (s *BackupService) applyRetentionPolicy(ctx context.Context, schedule *models.BackupSchedule, now time.Time) error {
	if schedule.RetentionPolicy == (models.BackupRetentionPolicy{}) {
		return s.pruneOverAllowance(ctx, schedule.VMID)
	}
	all, err := s.vmBackups(ctx, schedule.VMID)
	if err != nil {
		return err
	}
	var scheduled []models.Backup
	for _, b := range all {
		if b.Status == models.BackupStatusCompleted && b.BackupType == models.BackupTypeSchedule {
			scheduled = append(scheduled, b)
		}
	}
	pruneChains(all, backupsToPrune(scheduled, schedule.RetentionPolicy, now), func(b models.Backup) error {
		_, err := s.DeleteBackup(ctx, b.ID)
		if err != nil {
			s.logger.Error("failed to delete backup per retention policy", "backup_id", b.ID, "error", err)
		}
		return err
	})
	return s.pruneOverAllowance(ctx, schedule.VMID)
}

func planMaxBackups(p *models.Plan) *int { return p.MaxBackups }

// scheduledBackupAllowed reports whether a scheduled backup may start under
// the VM's plan: below its allowance, or at it with a completed scheduled
// backup for the new one to replace (pruneOverAllowance removes it once the
// new one exists). Manual backups are never removed to make room.
func (s *BackupService) scheduledBackupAllowed(ctx context.Context, vm *models.VM) (bool, string, error) {
	vmID := vm.ID
	plan, err := vmPlan(ctx, s.db, vm)
	if err != nil || plan == nil || plan.MaxBackups == nil {
		return err == nil, "", err
	}
	if *plan.MaxBackups == 0 {
		return false, "the VM's plan includes no backups", nil
	}
	kept, err := keptBackups(ctx, s.db, vmID)
	if err != nil || kept < int64(*plan.MaxBackups) {
		return err == nil, "", err
	}
	var replaceable int64
	if err := s.db.WithContext(ctx).Model(&models.Backup{}).
		Where("vm_id = ? AND status = ? AND backup_type = ?", vmID, models.BackupStatusCompleted, models.BackupTypeSchedule).
		Count(&replaceable).Error; err != nil {
		return false, "", err
	}
	if replaceable == 0 {
		return false, fmt.Sprintf("the VM's plan keeps %d backups and the VM holds that many manual ones", *plan.MaxBackups), nil
	}
	return true, "", nil
}

// pruneOverAllowance removes the oldest completed scheduled backups while the
// VM holds more than its plan keeps. Manual backups, the newest scheduled one
// and anything a remaining backup is built on stay, and a plan with no backups
// removes nothing: its schedules only stop making new ones.
func (s *BackupService) pruneOverAllowance(ctx context.Context, vmID string) error {
	vm, err := s.vmRepo.GetByID(ctx, vmID)
	if err != nil {
		return nil // a VM gone has nothing to prune; its schedule goes with it
	}
	plan, err := vmPlan(ctx, s.db, vm)
	if err != nil || plan == nil || plan.MaxBackups == nil || *plan.MaxBackups == 0 {
		return err
	}
	// Completed only: a run still going may fail, and must not cost the
	// backup it was meant to replace.
	var kept int64
	if err := s.db.WithContext(ctx).Model(&models.Backup{}).
		Where("vm_id = ? AND status = ?", vmID, models.BackupStatusCompleted).Count(&kept).Error; err != nil {
		return err
	}
	excess := kept - int64(*plan.MaxBackups)
	if excess <= 0 {
		return nil
	}
	all, err := s.vmBackups(ctx, vmID)
	if err != nil {
		return err
	}
	failed := map[string]bool{}
	for excess > 0 {
		next := nextOverAllowance(all, failed)
		if next == nil {
			return nil
		}
		if _, err := s.DeleteBackup(ctx, next.ID); err != nil {
			s.logger.Error("failed to delete a backup over the plan's allowance", "backup_id", next.ID, "error", err)
			failed[next.ID] = true
			continue
		}
		excess--
		for i := range all {
			if all[i].ID == next.ID {
				all = append(all[:i], all[i+1:]...)
				break
			}
		}
	}
	return nil
}

// backupsToPrune picks, from one VM's completed scheduled backups, those the
// policy no longer keeps. A backup is kept when it is among the last KeepLast,
// or the newest of one of the last KeepDaily days (KeepWeekly weeks,
// KeepMonthly months) that have a backup, and is not older than MaxAgeDays.
// With no keep rule, only age removes backups. The newest is always kept.
func backupsToPrune(backups []models.Backup, p models.BackupRetentionPolicy, now time.Time) []models.Backup {
	if p == (models.BackupRetentionPolicy{}) || len(backups) == 0 {
		return nil
	}
	sorted := append([]models.Backup(nil), backups...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].CreatedAt.After(sorted[j].CreatedAt) })

	countRules := p.KeepLast > 0 || p.KeepDaily > 0 || p.KeepWeekly > 0 || p.KeepMonthly > 0
	keep := map[string]bool{}
	for i := 0; i < p.KeepLast && i < len(sorted); i++ {
		keep[sorted[i].ID] = true
	}
	newestPer := func(n int, period func(time.Time) string) {
		seen := map[string]bool{}
		for _, b := range sorted {
			k := period(b.CreatedAt.In(now.Location()))
			if seen[k] {
				continue
			}
			if len(seen) == n {
				return
			}
			seen[k] = true
			keep[b.ID] = true
		}
	}
	newestPer(p.KeepDaily, func(t time.Time) string { return t.Format("2006-01-02") })
	newestPer(p.KeepWeekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprintf("%d-%02d", y, w) })
	newestPer(p.KeepMonthly, func(t time.Time) string { return t.Format("2006-01") })

	maxAge := time.Duration(p.MaxAgeDays) * 24 * time.Hour
	var prune []models.Backup
	for _, b := range sorted[1:] {
		if (countRules && !keep[b.ID]) || (maxAge > 0 && now.Sub(b.CreatedAt) > maxAge) {
			prune = append(prune, b)
		}
	}
	return prune
}

// validCompression accepts what the node can export with; empty is the default.
func validCompression(c string) bool {
	return c == "" || c == "gzip" || c == "zstd" || c == "none"
}

// validRetention checks a retention policy an owner sent.
func validRetention(p models.BackupRetentionPolicy) error {
	for _, f := range []struct {
		name     string
		val, max int
	}{{"keep_last", p.KeepLast, 365}, {"keep_daily", p.KeepDaily, 365}, {"keep_weekly", p.KeepWeekly, 104},
		{"keep_monthly", p.KeepMonthly, 120}, {"max_age_days", p.MaxAgeDays, 3650}} {
		if f.val < 0 || f.val > f.max {
			return fmt.Errorf("%w: %s must be between 0 and %d", ErrInvalidRetention, f.name, f.max)
		}
	}
	return nil
}

// ============================================================================
// Storage Operations
// ============================================================================

// GetBackupDownloadURL generates a presigned URL for downloading a backup
func (s *BackupService) GetBackupDownloadURL(ctx context.Context, backupID string, expiration time.Duration) (string, error) {
	backup, err := s.backupRepo.GetByID(ctx, backupID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrBackupNotFound
		}
		return "", fmt.Errorf("failed to get backup: %w", err)
	}

	if backup.Status != models.BackupStatusCompleted {
		return "", fmt.Errorf("backup is not completed")
	}
	if backup.Mode == models.BackupModeIncremental {
		return "", errors.New("an incremental backup holds only changes; download its full backup instead")
	}
	r, err := s.destinationOf(ctx, backup)
	if err != nil {
		return "", err
	}
	if r == nil || r.Kind != storage.KindS3 {
		return "", errors.New("download links are offered for backups in an S3 destination only")
	}
	client, err := r.Target.S3.Client()
	if err != nil {
		return "", err
	}

	url, err := client.GeneratePresignedURL(ctx, backup.StoragePath, expiration)
	if err != nil {
		return "", fmt.Errorf("failed to generate download URL: %w", err)
	}

	return url, nil
}

// GetBackupStats returns statistics about backups for a VM
func (s *BackupService) GetBackupStats(ctx context.Context, vmID string) (*BackupStats, error) {
	// Get total backup count
	totalCount, err := s.backupRepo.CountByVMID(ctx, vmID)
	if err != nil {
		return nil, fmt.Errorf("failed to count backups: %w", err)
	}

	// Get total backup size
	totalSize, err := s.backupRepo.GetTotalSizeByVMID(ctx, vmID)
	if err != nil {
		return nil, fmt.Errorf("failed to get total backup size: %w", err)
	}

	// Get count by status
	pendingCount, _ := s.backupRepo.CountByStatus(ctx, models.BackupStatusPending)
	inProgressCount, _ := s.backupRepo.CountByStatus(ctx, models.BackupStatusInProgress)
	completedCount, _ := s.backupRepo.CountByStatus(ctx, models.BackupStatusCompleted)
	failedCount, _ := s.backupRepo.CountByStatus(ctx, models.BackupStatusFailed)

	return &BackupStats{
		VMID:            vmID,
		TotalCount:      totalCount,
		TotalSize:       totalSize,
		PendingCount:    pendingCount,
		InProgressCount: inProgressCount,
		CompletedCount:  completedCount,
		FailedCount:     failedCount,
	}, nil
}

// BackupStats holds backup statistics for a VM
type BackupStats struct {
	VMID            string `json:"vm_id"`
	TotalCount      int64  `json:"total_count"`
	TotalSize       int64  `json:"total_size"`
	PendingCount    int64  `json:"pending_count"`
	InProgressCount int64  `json:"in_progress_count"`
	CompletedCount  int64  `json:"completed_count"`
	FailedCount     int64  `json:"failed_count"`
}

// isBackupInProgressViolation reports whether err is the active-backup uniqueness
// violation from ux_backups_active_per_vm. String-based (like isQuotaUniqueViolation)
// so it also matches under the SQLite test driver, whose UNIQUE errors carry the
// table name rather than a Postgres SQLSTATE.
func isBackupInProgressViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "ux_backups_active_per_vm") ||
		(strings.Contains(msg, "UNIQUE") && strings.Contains(msg, "backups"))
}
