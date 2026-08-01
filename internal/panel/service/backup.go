package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

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
	ErrScheduleAlreadyExists = errors.New("backup schedule already exists for this VM")
	ErrBackupInProgress      = errors.New("backup already in progress for this VM")
	ErrStorageUploadFailed   = errors.New("storage upload failed")
)

// BackupService handles backup-related business operations
type BackupService struct {
	db              *gorm.DB
	backupRepo      *repository.BackupRepository
	scheduleRepo    *repository.BackupScheduleRepository
	vmRepo          *repository.VMRepository
	nodeRepo        *repository.NodeRepository
	riverClient     *river.Client[pgx.Tx]
	storageClient   *storage.Client
	logger          *slog.Logger
	cron            *cron.Cron
	cronParser      cron.Parser
	scheduleMutex   sync.RWMutex
	scheduleEntries map[string]cron.EntryID
}

// NewBackupService creates a new BackupService instance
func NewBackupService(
	db *gorm.DB,
	backupRepo *repository.BackupRepository,
	scheduleRepo *repository.BackupScheduleRepository,
	vmRepo *repository.VMRepository,
	nodeRepo *repository.NodeRepository,
	riverClient *river.Client[pgx.Tx],
	storageClient *storage.Client,
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
		db:              db,
		backupRepo:      backupRepo,
		scheduleRepo:    scheduleRepo,
		vmRepo:          vmRepo,
		nodeRepo:        nodeRepo,
		riverClient:     riverClient,
		storageClient:   storageClient,
		logger:          logger,
		cron:            cron.New(cron.WithParser(cronParser)),
		cronParser:      cronParser,
		scheduleEntries: make(map[string]cron.EntryID),
	}
}

// Start starts the cron scheduler
func (s *BackupService) Start() error {
	s.cron.Start()

	// Load and schedule all active backup schedules
	ctx := context.Background()
	schedules, err := s.scheduleRepo.ListActive(ctx)
	if err != nil {
		s.logger.Error("failed to load active backup schedules", "error", err)
		return err
	}

	for _, schedule := range schedules {
		if err := s.ScheduleBackup(ctx, &schedule); err != nil {
			s.logger.Error("failed to schedule backup",
				"schedule_id", schedule.ID,
				"vm_id", schedule.VMID,
				"error", err,
			)
		}
	}

	s.logger.Info("backup service started", "schedules_loaded", len(schedules))
	return nil
}

// Stop stops the cron scheduler
func (s *BackupService) Stop() {
	s.cron.Stop()
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

	// Determine storage provider
	storageProvider := req.StorageProvider
	if storageProvider == "" {
		if s.storageClient != nil {
			storageProvider = string(s.storageClient.GetProvider())
		} else {
			storageProvider = "s3"
		}
	}

	// Determine compression
	compression := req.Compression
	if compression == "" {
		compression = "gzip"
	}

	// Generate storage path
	timestamp := time.Now().UTC().Format("20060102_150405")
	storagePath := fmt.Sprintf("backups/%s/%s_%s.%s", vm.ID, vm.ID, timestamp, backupFileSuffix(compression))

	// Create backup record
	backup := &models.Backup{
		VMID:            req.VMID,
		StorageProvider: storageProvider,
		StoragePath:     storagePath,
		BackupType:      models.BackupTypeManual,
		Status:          models.BackupStatusPending,
		Compression:     compression,
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

// DeleteBackup deletes a backup and removes it from storage
func (s *BackupService) DeleteBackup(ctx context.Context, backupID string) error {
	backup, err := s.backupRepo.GetByID(ctx, backupID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrBackupNotFound
		}
		return fmt.Errorf("failed to get backup: %w", err)
	}

	// Delete from storage if backup is completed
	if backup.Status == models.BackupStatusCompleted && s.storageClient != nil {
		if err := s.storageClient.Delete(ctx, backup.StoragePath); err != nil {
			s.logger.WarnContext(ctx, "failed to delete backup from storage",
				"backup_id", backupID,
				"storage_path", backup.StoragePath,
				"error", err,
			)
			// Continue to delete the record even if storage deletion fails
		}
	}

	// Delete backup record
	if err := s.backupRepo.Delete(ctx, backupID); err != nil {
		return fmt.Errorf("failed to delete backup: %w", err)
	}

	s.logger.InfoContext(ctx, "backup deleted",
		"backup_id", backupID,
		"vm_id", backup.VMID,
	)

	return nil
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

	if s.storageClient != nil {
		reader, err := s.storageClient.Download(ctx, backup.StoragePath)
		if err != nil {
			return nil, fmt.Errorf("failed to download backup for restore: %w", err)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(reader, 1))
		_ = reader.Close()
	}

	// Create restore job
	params := map[string]interface{}{
		"backup_id":    backup.ID,
		"storage_path": backup.StoragePath,
		"compression":  backup.Compression,
		"provider":     backup.StorageProvider,
	}
	paramsJSON, _ := json.Marshal(params)

	job := queue.VMOperationJob{
		VMID:      vm.ID,
		Operation: queue.VMOpRebuild,
		NodeID:    vm.NodeID,
		Params:    paramsJSON,
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

	// Validate VM exists
	if _, err := s.vmRepo.GetByID(ctx, req.VMID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrVMNotFound
		}
		return nil, fmt.Errorf("failed to get VM: %w", err)
	}

	// Determine storage provider
	storageProvider := req.StorageProvider
	if storageProvider == "" {
		if s.storageClient != nil {
			storageProvider = string(s.storageClient.GetProvider())
		} else {
			storageProvider = "s3"
		}
	}

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

		if err := s.scheduleRepo.Update(ctx, existingSchedule); err != nil {
			return nil, fmt.Errorf("failed to update schedule: %w", err)
		}
		schedule = existingSchedule

		// Remove old cron entry
		s.UnscheduleBackup(req.VMID)
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

		if err := s.scheduleRepo.Create(ctx, newSchedule); err != nil {
			return nil, fmt.Errorf("failed to create schedule: %w", err)
		}
		schedule = newSchedule
	}

	// Schedule the backup in cron
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
	// Unschedule from cron first
	s.UnscheduleBackup(vmID)

	// Delete from database
	if err := s.scheduleRepo.DeleteByVMID(ctx, vmID); err != nil {
		return fmt.Errorf("failed to delete schedule: %w", err)
	}

	s.logger.InfoContext(ctx, "backup schedule deleted",
		"vm_id", vmID,
	)

	return nil
}

// ============================================================================
// Cron Scheduling
// ============================================================================

// ScheduleBackup adds a backup schedule to the cron
func (s *BackupService) ScheduleBackup(ctx context.Context, schedule *models.BackupSchedule) error {
	s.scheduleMutex.Lock()
	defer s.scheduleMutex.Unlock()

	// Drop any existing entry for this VM first, so a reschedule (update) doesn't
	// leak a cron entry that keeps firing the old expression.
	if old, exists := s.scheduleEntries[schedule.VMID]; exists {
		s.cron.Remove(old)
		delete(s.scheduleEntries, schedule.VMID)
	}

	// Parse cron expression
	entryID, err := s.cron.AddFunc(schedule.Schedule, func() {
		s.executeScheduledBackup(schedule.ID)
	})
	if err != nil {
		return fmt.Errorf("failed to add cron job: %w", err)
	}

	s.scheduleEntries[schedule.VMID] = entryID

	// Calculate next run time
	nextRun := s.cron.Entry(entryID).Next
	if err := s.scheduleRepo.UpdateNextRun(ctx, schedule.ID, nextRun); err != nil {
		s.logger.Error("failed to update next run time",
			"schedule_id", schedule.ID,
			"error", err,
		)
	}

	s.logger.Info("backup scheduled",
		"schedule_id", schedule.ID,
		"vm_id", schedule.VMID,
		"schedule", schedule.Schedule,
		"next_run", nextRun,
	)

	return nil
}

// UnscheduleBackup removes a backup schedule from the cron
func (s *BackupService) UnscheduleBackup(vmID string) {
	s.scheduleMutex.Lock()
	defer s.scheduleMutex.Unlock()

	if entryID, exists := s.scheduleEntries[vmID]; exists {
		s.cron.Remove(entryID)
		delete(s.scheduleEntries, vmID)
		s.logger.Info("backup unscheduled", "vm_id", vmID)
	}
}

// executeScheduledBackup is called by cron to execute a scheduled backup
func (s *BackupService) executeScheduledBackup(scheduleID string) {
	ctx := context.Background()

	// Get schedule details
	schedule, err := s.scheduleRepo.GetByID(ctx, scheduleID)
	if err != nil {
		s.logger.Error("failed to get schedule for execution",
			"schedule_id", scheduleID,
			"error", err,
		)
		return
	}

	// Check if schedule is still active
	if !schedule.IsActive() {
		s.logger.Info("skipping inactive schedule",
			"schedule_id", scheduleID,
			"status", schedule.Status,
		)
		return
	}

	// Validate VM exists
	vm, err := s.vmRepo.GetByID(ctx, schedule.VMID)
	if err != nil {
		s.logger.Error("failed to get VM for scheduled backup",
			"schedule_id", scheduleID,
			"vm_id", schedule.VMID,
			"error", err,
		)
		return
	}

	// Generate storage path
	timestamp := time.Now().UTC().Format("20060102_150405")
	storagePath := fmt.Sprintf("backups/%s/scheduled_%s.%s", vm.ID, timestamp, backupFileSuffix(schedule.Compression))

	// Create backup record
	backup := &models.Backup{
		VMID:            schedule.VMID,
		StorageProvider: schedule.StorageProvider,
		StoragePath:     storagePath,
		BackupType:      models.BackupTypeSchedule,
		Status:          models.BackupStatusPending,
		Compression:     schedule.Compression,
	}

	if err := s.backupRepo.Create(ctx, backup); err != nil {
		if isBackupInProgressViolation(err) {
			s.logger.Info("skipping scheduled backup; one is already active for this VM",
				"schedule_id", scheduleID,
				"vm_id", schedule.VMID,
			)
			return
		}
		s.logger.Error("failed to create backup record",
			"schedule_id", scheduleID,
			"vm_id", schedule.VMID,
			"error", err,
		)
		return
	}

	// Enqueue backup job
	job := queue.BackupJob{
		VMID:            backup.VMID,
		BackupType:      queue.BackupTypeFull,
		StorageProvider: backup.StorageProvider,
		Destination:     backup.StoragePath,
		Compression:     backup.Compression,
		BackupID:        backup.ID,
	}

	result, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		s.logger.Error("failed to enqueue scheduled backup job",
			"backup_id", backup.ID,
			"schedule_id", scheduleID,
			"error", err,
		)
		// Update backup status to failed
		s.backupRepo.UpdateStatus(ctx, backup.ID, models.BackupStatusFailed)
		return
	}

	// Update schedule last run
	now := time.Now()
	if err := s.scheduleRepo.UpdateLastRun(ctx, scheduleID, now, &backup.ID); err != nil {
		s.logger.Error("failed to update last run time",
			"schedule_id", scheduleID,
			"error", err,
		)
	}

	// Calculate and update next run
	s.scheduleMutex.RLock()
	if entryID, exists := s.scheduleEntries[schedule.VMID]; exists {
		nextRun := s.cron.Entry(entryID).Next
		s.scheduleRepo.UpdateNextRun(ctx, scheduleID, nextRun)
	}
	s.scheduleMutex.RUnlock()

	s.logger.Info("scheduled backup job enqueued",
		"backup_id", backup.ID,
		"schedule_id", scheduleID,
		"vm_id", schedule.VMID,
		"job_id", result.Job.ID,
	)

	// Apply retention policy
	if err := s.applyRetentionPolicy(ctx, schedule, backup); err != nil {
		s.logger.Error("failed to apply retention policy",
			"schedule_id", scheduleID,
			"vm_id", schedule.VMID,
			"error", err,
		)
	}
}

// applyRetentionPolicy applies the retention policy for scheduled backups
func (s *BackupService) applyRetentionPolicy(ctx context.Context, schedule *models.BackupSchedule, latestBackup *models.Backup) error {
	policy := schedule.RetentionPolicy

	// If no retention policy, keep all backups
	if policy.KeepLast == 0 && policy.KeepDaily == 0 && policy.KeepWeekly == 0 && policy.KeepMonthly == 0 {
		return nil
	}

	// Get all completed backups for this VM
	backups, err := s.backupRepo.ListByVMID(ctx, schedule.VMID, 0, 0)
	if err != nil {
		return fmt.Errorf("failed to list backups: %w", err)
	}

	// Filter completed backups
	var completedBackups []models.Backup
	for _, b := range backups {
		if b.Status == models.BackupStatusCompleted && b.BackupType == models.BackupTypeSchedule {
			completedBackups = append(completedBackups, b)
		}
	}

	// Apply keep_last policy
	if policy.KeepLast > 0 && len(completedBackups) > policy.KeepLast {
		toDelete := completedBackups[policy.KeepLast:]
		for _, b := range toDelete {
			// Check if this backup is protected by other retention rules
			if s.shouldKeepBackup(b, completedBackups, policy) {
				continue
			}

			if err := s.DeleteBackup(ctx, b.ID); err != nil {
				s.logger.Error("failed to delete backup per retention policy",
					"backup_id", b.ID,
					"error", err,
				)
			}
		}
	}

	return nil
}

// shouldKeepBackup determines if a backup should be kept based on retention policy
func (s *BackupService) shouldKeepBackup(backup models.Backup, allBackups []models.Backup, policy models.BackupRetentionPolicy) bool {
	backupTime := backup.CreatedAt
	now := time.Now()

	// Check daily retention
	if policy.KeepDaily > 0 {
		daysSince := int(now.Sub(backupTime).Hours() / 24)
		if daysSince <= policy.KeepDaily {
			// Check if this is the only backup for this day
			backupDay := backupTime.Truncate(24 * time.Hour)
			isOnlyDaily := true
			for _, b := range allBackups {
				if b.ID != backup.ID && b.CreatedAt.Truncate(24*time.Hour).Equal(backupDay) {
					// There's another backup from the same day, keep the newer one
					if b.CreatedAt.After(backupTime) {
						return false
					}
					isOnlyDaily = false
					break
				}
			}
			if isOnlyDaily {
				return true
			}
		}
	}

	return false
}

// ============================================================================
// Storage Operations
// ============================================================================

// GetBackupDownloadURL generates a presigned URL for downloading a backup
func (s *BackupService) GetBackupDownloadURL(ctx context.Context, backupID string, expiration time.Duration) (string, error) {
	if s.storageClient == nil {
		return "", errors.New("storage client not configured")
	}

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

	url, err := s.storageClient.GeneratePresignedURL(ctx, backup.StoragePath, expiration)
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

func backupFileSuffix(compression string) string {
	switch compression {
	case "gzip":
		return "tar.gz"
	case "zstd":
		return "tar.zst"
	default:
		return "tar"
	}
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
