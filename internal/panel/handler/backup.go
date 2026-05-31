package handler

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// BackupHandler handles HTTP requests for backup management
type BackupHandler struct {
	service *service.BackupService
}

// NewBackupHandler creates a new BackupHandler instance
func NewBackupHandler(service *service.BackupService) *BackupHandler {
	return &BackupHandler{
		service: service,
	}
}

// ============================================================================
// Create Backup
// ============================================================================

// CreateBackupRequest represents a request to create a new backup
type CreateBackupRequest struct {
	StorageProvider string `json:"storage_provider,omitempty"`
	Compression     string `json:"compression,omitempty"`
}

// CreateBackupResponse represents the response after creating a backup
type CreateBackupResponse struct {
	ID              string    `json:"id"`
	VMID            string    `json:"vm_id"`
	StorageProvider string    `json:"storage_provider"`
	BackupType      string    `json:"backup_type"`
	Status          string    `json:"status"`
	Compression     string    `json:"compression"`
	JobID           int64     `json:"job_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// CreateBackup handles POST /api/vms/:id/backups - Create a manual backup
func (h *BackupHandler) CreateBackup(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	var req CreateBackupRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	// Create backup
	createReq := &service.CreateBackupRequest{
		VMID:            vmID,
		StorageProvider: req.StorageProvider,
		Compression:     req.Compression,
	}

	resp, err := h.service.CreateBackup(c.Request().Context(), createReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		case errors.Is(err, service.ErrBackupInProgress):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": "Backup already in progress for this VM",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"message": "Backup creation initiated",
		"data": CreateBackupResponse{
			ID:              resp.Backup.ID,
			VMID:            resp.Backup.VMID,
			StorageProvider: resp.Backup.StorageProvider,
			BackupType:      string(resp.Backup.BackupType),
			Status:          string(resp.Backup.Status),
			Compression:     resp.Backup.Compression,
			JobID:           resp.JobID,
			CreatedAt:       resp.Backup.CreatedAt,
		},
	})
}

// ============================================================================
// List Backups
// ============================================================================

// BackupListItem represents a backup in the list response
type BackupListItem struct {
	ID              string    `json:"id"`
	VMID            string    `json:"vm_id"`
	StorageProvider string    `json:"storage_provider"`
	StoragePath     string    `json:"storage_path"`
	BackupType      string    `json:"backup_type"`
	Status          string    `json:"status"`
	Size            int64     `json:"size"`
	Compression     string    `json:"compression"`
	Checksum        string    `json:"checksum,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// ListBackups handles GET /api/vms/:id/backups - List backups
func (h *BackupHandler) ListBackups(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	// Parse query parameters
	status := c.QueryParam("status")
	limitStr := c.QueryParam("limit")
	offsetStr := c.QueryParam("offset")

	var limit, offset int
	if limitStr != "" {
		limit, _ = strconv.Atoi(limitStr)
	}
	if offsetStr != "" {
		offset, _ = strconv.Atoi(offsetStr)
	}

	// Build request
	listReq := &service.ListBackupsRequest{
		VMID:   vmID,
		Limit:  limit,
		Offset: offset,
	}

	if status != "" {
		listReq.Status = models.BackupStatus(status)
	}

	// List backups
	resp, err := h.service.ListBackups(c.Request().Context(), listReq)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	// Map to response format
	items := make([]BackupListItem, len(resp.Backups))
	for i, backup := range resp.Backups {
		items[i] = BackupListItem{
			ID:              backup.ID,
			VMID:            backup.VMID,
			StorageProvider: backup.StorageProvider,
			StoragePath:     backup.StoragePath,
			BackupType:      string(backup.BackupType),
			Status:          string(backup.Status),
			Size:            backup.Size,
			Compression:     backup.Compression,
			Checksum:        backup.Checksum,
			CreatedAt:       backup.CreatedAt,
		}
		if backup.StartedAt != nil {
			items[i].StartedAt = *backup.StartedAt
		}
		if backup.CompletedAt != nil {
			items[i].CompletedAt = *backup.CompletedAt
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message":  "Backups retrieved successfully",
		"data":     items,
		"total":    resp.Total,
		"limit":    resp.Limit,
		"offset":   resp.Offset,
		"has_more": resp.HasMore,
	})
}

// ============================================================================
// Get Backup
// ============================================================================

// GetBackup handles GET /api/vms/:id/backups/:backup_id - Get backup details
func (h *BackupHandler) GetBackup(c echo.Context) error {
	vmID := c.Param("id")
	backupID := c.Param("backup_id")

	if vmID == "" || backupID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID and Backup ID are required",
		})
	}

	backup, err := h.service.GetBackup(c.Request().Context(), backupID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrBackupNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Backup not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	// Verify backup belongs to the specified VM
	if backup.VMID != vmID {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"error":   "Not Found",
			"message": "Backup not found for this VM",
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup retrieved successfully",
		"data": BackupListItem{
			ID:              backup.ID,
			VMID:            backup.VMID,
			StorageProvider: backup.StorageProvider,
			StoragePath:     backup.StoragePath,
			BackupType:      string(backup.BackupType),
			Status:          string(backup.Status),
			Size:            backup.Size,
			Compression:     backup.Compression,
			Checksum:        backup.Checksum,
			CreatedAt:       backup.CreatedAt,
		},
	})
}

// ============================================================================
// Delete Backup
// ============================================================================

// DeleteBackup handles DELETE /api/vms/:id/backups/:backup_id - Delete backup
func (h *BackupHandler) DeleteBackup(c echo.Context) error {
	vmID := c.Param("id")
	backupID := c.Param("backup_id")

	if vmID == "" || backupID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID and Backup ID are required",
		})
	}

	// Verify backup exists and belongs to the VM
	backup, err := h.service.GetBackup(c.Request().Context(), backupID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrBackupNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Backup not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	if backup.VMID != vmID {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"error":   "Not Found",
			"message": "Backup not found for this VM",
		})
	}

	// Delete backup
	if err := h.service.DeleteBackup(c.Request().Context(), backupID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup deleted successfully",
	})
}

// ============================================================================
// Restore Backup
// ============================================================================

// RestoreBackupRequest represents a request to restore from a backup
type RestoreBackupRequest struct {
	BackupID string `json:"backup_id" validate:"required,uuid"`
}

// RestoreBackupResponse represents the response from a restore operation
type RestoreBackupResponse struct {
	VMID     string `json:"vm_id"`
	BackupID string `json:"backup_id"`
	JobID    int64  `json:"job_id"`
	Status   string `json:"status"`
}

// RestoreBackup handles POST /api/vms/:id/backups/:backup_id/restore - Restore from backup
func (h *BackupHandler) RestoreBackup(c echo.Context) error {
	vmID := c.Param("id")
	backupID := c.Param("backup_id")

	if vmID == "" || backupID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID and Backup ID are required",
		})
	}

	// Verify backup exists and belongs to the VM
	backup, err := h.service.GetBackup(c.Request().Context(), backupID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrBackupNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Backup not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	if backup.VMID != vmID {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"error":   "Not Found",
			"message": "Backup not found for this VM",
		})
	}

	// Initiate restore
	restoreReq := &service.RestoreBackupRequest{
		VMID:     vmID,
		BackupID: backupID,
	}

	resp, err := h.service.RestoreBackup(c.Request().Context(), restoreReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		case errors.Is(err, service.ErrBackupNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Backup not found",
			})
		default:
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error":   "Bad Request",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusAccepted, map[string]interface{}{
		"message": "Restore initiated",
		"data": RestoreBackupResponse{
			VMID:     resp.VMID,
			BackupID: resp.BackupID,
			JobID:    resp.JobID,
			Status:   "pending",
		},
	})
}

// ============================================================================
// Configure Backup Schedule
// ============================================================================

// ConfigureScheduleRequest represents a request to configure backup schedule
type ConfigureScheduleRequest struct {
	Schedule        string                        `json:"schedule" validate:"required"`
	StorageProvider string                        `json:"storage_provider,omitempty"`
	Compression     string                        `json:"compression,omitempty"`
	RetentionPolicy *models.BackupRetentionPolicy `json:"retention_policy,omitempty"`
}

// ConfigureScheduleResponse represents the response after configuring schedule
type ConfigureScheduleResponse struct {
	ID              string                        `json:"id"`
	VMID            string                        `json:"vm_id"`
	Schedule        string                        `json:"schedule"`
	Status          string                        `json:"status"`
	StorageProvider string                        `json:"storage_provider"`
	Compression     string                        `json:"compression"`
	RetentionPolicy *models.BackupRetentionPolicy `json:"retention_policy,omitempty"`
	NextRunAt       time.Time                     `json:"next_run_at,omitempty"`
	LastRunAt       time.Time                     `json:"last_run_at,omitempty"`
	CreatedAt       time.Time                     `json:"created_at"`
	UpdatedAt       time.Time                     `json:"updated_at"`
}

// ConfigureSchedule handles PUT /api/vms/:id/backup-schedule - Configure schedule
func (h *BackupHandler) ConfigureSchedule(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	var req ConfigureScheduleRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	// Validate required fields
	if req.Schedule == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Schedule (cron expression) is required",
		})
	}

	// Configure schedule
	configReq := &service.ConfigureScheduleRequest{
		VMID:            vmID,
		Schedule:        req.Schedule,
		StorageProvider: req.StorageProvider,
		Compression:     req.Compression,
		RetentionPolicy: req.RetentionPolicy,
	}

	resp, err := h.service.ConfigureSchedule(c.Request().Context(), configReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrVMNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		case errors.Is(err, service.ErrInvalidCronExpression):
			return c.JSON(http.StatusBadRequest, map[string]interface{}{
				"error":   "Bad Request",
				"message": err.Error(),
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup schedule configured successfully",
		"data":    mapScheduleResponse(resp.Schedule),
	})
}

// ============================================================================
// Get Backup Schedule
// ============================================================================

// GetSchedule handles GET /api/vms/:id/backup-schedule - Get backup schedule
func (h *BackupHandler) GetSchedule(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	schedule, err := h.service.GetSchedule(c.Request().Context(), vmID)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrScheduleNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Backup schedule not found for this VM",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup schedule retrieved successfully",
		"data":    mapScheduleResponse(schedule),
	})
}

// ListSchedules handles GET /api/vms/:id/backup-schedules - frontend compatibility.
func (h *BackupHandler) ListSchedules(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	schedule, err := h.service.GetSchedule(c.Request().Context(), vmID)
	if err != nil {
		if errors.Is(err, service.ErrScheduleNotFound) {
			return c.JSON(http.StatusOK, map[string]interface{}{
				"message": "Backup schedules retrieved successfully",
				"data":    []ConfigureScheduleResponse{},
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup schedules retrieved successfully",
		"data":    []ConfigureScheduleResponse{mapScheduleResponse(schedule)},
	})
}

// ============================================================================
// Delete Backup Schedule
// ============================================================================

// DeleteSchedule handles DELETE /api/vms/:id/backup-schedule - Delete backup schedule
func (h *BackupHandler) DeleteSchedule(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	if err := h.service.DeleteSchedule(c.Request().Context(), vmID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup schedule deleted successfully",
	})
}

// ============================================================================
// Get Backup Stats
// ============================================================================

// BackupStatsResponse represents backup statistics
type BackupStatsResponse struct {
	VMID            string `json:"vm_id"`
	TotalCount      int64  `json:"total_count"`
	TotalSize       int64  `json:"total_size"`
	PendingCount    int64  `json:"pending_count"`
	InProgressCount int64  `json:"in_progress_count"`
	CompletedCount  int64  `json:"completed_count"`
	FailedCount     int64  `json:"failed_count"`
}

// GetBackupStats handles GET /api/vms/:id/backup-stats - Get backup statistics
func (h *BackupHandler) GetBackupStats(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	stats, err := h.service.GetBackupStats(c.Request().Context(), vmID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Backup statistics retrieved successfully",
		"data": BackupStatsResponse{
			VMID:            stats.VMID,
			TotalCount:      stats.TotalCount,
			TotalSize:       stats.TotalSize,
			PendingCount:    stats.PendingCount,
			InProgressCount: stats.InProgressCount,
			CompletedCount:  stats.CompletedCount,
			FailedCount:     stats.FailedCount,
		},
	})
}

// ============================================================================
// Helper Functions
// ============================================================================

func mapScheduleResponse(schedule *models.BackupSchedule) ConfigureScheduleResponse {
	resp := ConfigureScheduleResponse{
		ID:              schedule.ID,
		VMID:            schedule.VMID,
		Schedule:        schedule.Schedule,
		Status:          string(schedule.Status),
		StorageProvider: schedule.StorageProvider,
		Compression:     schedule.Compression,
		RetentionPolicy: &schedule.RetentionPolicy,
		CreatedAt:       schedule.CreatedAt,
		UpdatedAt:       schedule.UpdatedAt,
	}

	if schedule.NextRunAt != nil {
		resp.NextRunAt = *schedule.NextRunAt
	}
	if schedule.LastRunAt != nil {
		resp.LastRunAt = *schedule.LastRunAt
	}

	return resp
}

// ============================================================================
// Register Routes
// ============================================================================

// RegisterBackupRoutes registers all backup routes with the Echo router
func RegisterBackupRoutes(e *echo.Echo, handler *BackupHandler, db *gorm.DB) {
	// Create backup routes group
	vms := e.Group("/api/v1/vms")

	// Apply authentication middleware
	vms.Use(middleware.RequireAuth(db))

	backups := vms.Group("/:id/backups")
	{
		backups.GET("", handler.ListBackups, middleware.RequirePermission("backup:read"))
		backups.POST("", handler.CreateBackup, middleware.RequirePermission("backup:create"))
		backups.GET("/:backup_id", handler.GetBackup, middleware.RequirePermission("backup:read"))
		backups.DELETE("/:backup_id", handler.DeleteBackup, middleware.RequirePermission("backup:delete"))
		backups.POST("/:backup_id/restore", handler.RestoreBackup, middleware.RequirePermission("backup:update"))
	}

	schedule := vms.Group("/:id/backup-schedule")
	{
		schedule.GET("", handler.GetSchedule, middleware.RequirePermission("backup:read"))
		schedule.PUT("", handler.ConfigureSchedule, middleware.RequirePermission("backup:update"))
		schedule.DELETE("", handler.DeleteSchedule, middleware.RequirePermission("backup:delete"))
	}

	// Compatibility routes for the frontend's plural backup-schedules shape.
	schedules := vms.Group("/:id/backup-schedules")
	{
		schedules.GET("", handler.ListSchedules, middleware.RequirePermission("backup:read"))
		schedules.POST("", handler.ConfigureSchedule, middleware.RequirePermission("backup:update"))
		schedules.PUT("/:schedule_id", handler.ConfigureSchedule, middleware.RequirePermission("backup:update"))
		schedules.DELETE("/:schedule_id", handler.DeleteSchedule, middleware.RequirePermission("backup:delete"))
	}

	// Backup stats route
	vms.GET("/:id/backup-stats", handler.GetBackupStats, middleware.RequirePermission("backup:read"))
}
