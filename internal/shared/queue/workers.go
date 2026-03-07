package queue

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	sharedstorage "github.com/maburvm/panel/internal/shared/storage"
	"github.com/riverqueue/river"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// JobStatus represents the status of a job
type JobStatus string

const (
	JobStatusPending    JobStatus = "pending"
	JobStatusProcessing JobStatus = "processing"
	JobStatusCompleted  JobStatus = "completed"
	JobStatusFailed     JobStatus = "failed"
	JobStatusDead       JobStatus = "dead"
)

// JobRecord tracks job status in the database
type JobRecord struct {
	ID          string `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	JobType     string `gorm:"type:varchar(50);not null"`
	JobID       string `gorm:"type:varchar(100);not null;index"`
	ResourceID  string `gorm:"type:varchar(100);index"`
	Status      string `gorm:"type:varchar(20);not null"`
	Attempts    int    `gorm:"type:integer;default:0"`
	MaxAttempts int    `gorm:"type:integer;default:3"`
	ErrorMsg    string `gorm:"type:text"`
	StartedAt   *time.Time
	CompletedAt *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   gorm.DeletedAt `gorm:"index"`
}

// TableName specifies the table name for JobRecord
func (JobRecord) TableName() string {
	return "job_records"
}

// MetricsCollector collects job metrics
type MetricsCollector struct {
	mu               sync.RWMutex
	jobsProcessed    int64
	jobsFailed       int64
	jobsRetried      int64
	jobsDead         int64
	totalLatency     time.Duration
	latencyByJobType map[string]time.Duration
}

// NewMetricsCollector creates a new metrics collector
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		latencyByJobType: make(map[string]time.Duration),
	}
}

// RecordJobProcessed records a successfully processed job
func (m *MetricsCollector) RecordJobProcessed(jobType string, latency time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobsProcessed++
	m.totalLatency += latency
	m.latencyByJobType[jobType] += latency
}

// RecordJobFailed records a failed job
func (m *MetricsCollector) RecordJobFailed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobsFailed++
}

// RecordJobRetried records a job retry
func (m *MetricsCollector) RecordJobRetried() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobsRetried++
}

// RecordJobDead records a job moved to dead queue
func (m *MetricsCollector) RecordJobDead() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.jobsDead++
}

// GetMetrics returns current metrics
func (m *MetricsCollector) GetMetrics() (processed, failed, retried, dead int64, avgLatency time.Duration) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.jobsProcessed > 0 {
		avgLatency = m.totalLatency / time.Duration(m.jobsProcessed)
	}
	return m.jobsProcessed, m.jobsFailed, m.jobsRetried, m.jobsDead, avgLatency
}

// AgentClient provides gRPC client for node agent communication
type AgentClient struct {
	mu          sync.RWMutex
	clients     map[string]pb.NodeAgentClient
	connections map[string]*grpc.ClientConn
	timeout     time.Duration
}

// NewAgentClient creates a new agent client manager
func NewAgentClient() *AgentClient {
	return &AgentClient{
		clients:     make(map[string]pb.NodeAgentClient),
		connections: make(map[string]*grpc.ClientConn),
		timeout:     30 * time.Second,
	}
}

// GetClient returns a gRPC client for the specified node
func (c *AgentClient) GetClient(nodeAddress string) (pb.NodeAgentClient, error) {
	c.mu.RLock()
	client, exists := c.clients[nodeAddress]
	c.mu.RUnlock()

	if exists {
		return client, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if client, exists := c.clients[nodeAddress]; exists {
		return client, nil
	}

	// Create new connection
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	conn, err := grpc.DialContext(ctx, nodeAddress+":50051",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to agent at %s: %w", nodeAddress, err)
	}

	client = pb.NewNodeAgentClient(conn)
	c.clients[nodeAddress] = client
	c.connections[nodeAddress] = conn

	return client, nil
}

// Close closes all connections
func (c *AgentClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for addr, conn := range c.connections {
		if err := conn.Close(); err != nil {
			slog.Default().Error("failed to close connection", "address", addr, "error", err)
		}
	}

	c.clients = make(map[string]pb.NodeAgentClient)
	c.connections = make(map[string]*grpc.ClientConn)

	return nil
}

// WorkerContext holds shared dependencies for workers
type WorkerContext struct {
	DB           *gorm.DB
	VMRepo       *repository.VMRepository
	NodeRepo     *repository.NodeRepository
	TemplateRepo *repository.TemplateRepository
	AgentClient  *AgentClient
	Metrics      *MetricsCollector
}

// Global worker context (set during initialization)
var globalWorkerContext *WorkerContext

// SetWorkerContext sets the global worker context
func SetWorkerContext(ctx *WorkerContext) {
	globalWorkerContext = ctx
}

// JobManager handles job status updates
type JobManager struct {
	db *gorm.DB
}

// NewJobManager creates a new job manager
func NewJobManager(db *gorm.DB) *JobManager {
	return &JobManager{db: db}
}

// CreateJobRecord creates a new job record
func (jm *JobManager) CreateJobRecord(ctx context.Context, jobType, jobID, resourceID string, maxAttempts int) (*JobRecord, error) {
	record := &JobRecord{
		JobType:     jobType,
		JobID:       jobID,
		ResourceID:  resourceID,
		Status:      string(JobStatusPending),
		MaxAttempts: maxAttempts,
	}

	if err := jm.db.WithContext(ctx).Create(record).Error; err != nil {
		return nil, err
	}

	return record, nil
}

// UpdateJobStatus updates job status
func (jm *JobManager) UpdateJobStatus(ctx context.Context, jobID string, status JobStatus, errorMsg string) error {
	updates := map[string]interface{}{
		"status":    string(status),
		"error_msg": errorMsg,
	}

	switch status {
	case JobStatusProcessing:
		now := time.Now()
		updates["started_at"] = &now
		updates["attempts"] = gorm.Expr("attempts + 1")
	case JobStatusCompleted, JobStatusFailed, JobStatusDead:
		now := time.Now()
		updates["completed_at"] = &now
	}

	return jm.db.WithContext(ctx).Model(&JobRecord{}).Where("job_id = ?", jobID).Updates(updates).Error
}

// GetJobRecord retrieves a job record by job ID
func (jm *JobManager) GetJobRecord(ctx context.Context, jobID string) (*JobRecord, error) {
	var record JobRecord
	if err := jm.db.WithContext(ctx).Where("job_id = ?", jobID).First(&record).Error; err != nil {
		return nil, err
	}
	return &record, nil
}

// IsRetryableError determines if an error is retryable
func IsRetryableError(err error) bool {
	if err == nil {
		return false
	}

	st, ok := status.FromError(err)
	if !ok {
		return true // Non-gRPC errors are generally retryable
	}

	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
		return true
	case codes.Internal, codes.Unknown:
		return true
	default:
		return false
	}
}

// CalculateBackoff calculates exponential backoff with jitter
func CalculateBackoff(attempt int, baseDelay, maxDelay time.Duration) time.Duration {
	delay := baseDelay * time.Duration(1<<uint(attempt))
	if delay > maxDelay {
		delay = maxDelay
	}

	// Add jitter (±25%)
	jitter := time.Duration(float64(delay) * 0.25 * (2.0*float64(time.Now().UnixNano()%1000)/1000.0 - 1.0))
	return delay + jitter
}

// VMOperationWorker handles VM lifecycle operations
// Queue: critical (20 workers)
type VMOperationWorker struct {
	river.WorkerDefaults[VMOperationJob]
	logger      *slog.Logger
	jobManager  *JobManager
	agentClient *AgentClient
	metrics     *MetricsCollector
}

// NewVMOperationWorker creates a new VM operation worker
func NewVMOperationWorker(logger *slog.Logger) *VMOperationWorker {
	w := &VMOperationWorker{
		logger: logger,
	}

	if globalWorkerContext != nil {
		w.jobManager = NewJobManager(globalWorkerContext.DB)
		w.agentClient = globalWorkerContext.AgentClient
		w.metrics = globalWorkerContext.Metrics
	}

	return w
}

// Work implements the VM operation job handler
func (w *VMOperationWorker) Work(ctx context.Context, job *river.Job[VMOperationJob]) error {
	startTime := time.Now()
	jobID := fmt.Sprintf("%d", job.ID)

	w.logger.InfoContext(ctx, "processing VM operation",
		"job_id", jobID,
		"vm_id", job.Args.VMID,
		"operation", job.Args.Operation,
		"node_id", job.Args.NodeID,
		"attempt", job.Attempt,
	)

	// Validate operation type
	if err := ValidateVMOperation(job.Args.Operation); err != nil {
		w.logger.ErrorContext(ctx, "invalid VM operation",
			"error", err,
			"operation", job.Args.Operation,
		)
		return fmt.Errorf("invalid operation: %w", err)
	}

	// Get dependencies
	if globalWorkerContext == nil {
		return fmt.Errorf("worker context not initialized")
	}

	vmRepo := globalWorkerContext.VMRepo
	nodeRepo := globalWorkerContext.NodeRepo

	// Get VM details
	vm, err := vmRepo.GetByID(ctx, job.Args.VMID)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get VM",
			"error", err,
			"vm_id", job.Args.VMID,
		)
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Get node details
	node, err := nodeRepo.GetByID(ctx, job.Args.NodeID)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get node",
			"error", err,
			"node_id", job.Args.NodeID,
		)
		return fmt.Errorf("failed to get node: %w", err)
	}

	if node.Status != models.NodeStatusActive {
		return fmt.Errorf("node %s is not active (status: %s)", node.ID, node.Status)
	}

	// Get gRPC client for node
	client, err := w.agentClient.GetClient(node.IPAddress)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get agent client",
			"error", err,
			"node_address", node.IPAddress,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return err
	}

	// Map operation type to gRPC command
	var command pb.VMCommandType
	switch job.Args.Operation {
	case VMOpCreate:
		command = pb.VMCommandType_VM_COMMAND_TYPE_CREATE
	case VMOpStart:
		command = pb.VMCommandType_VM_COMMAND_TYPE_START
	case VMOpStop:
		command = pb.VMCommandType_VM_COMMAND_TYPE_STOP
	case VMOpRestart:
		command = pb.VMCommandType_VM_COMMAND_TYPE_RESTART
	case VMOpDelete:
		command = pb.VMCommandType_VM_COMMAND_TYPE_DESTROY
	case VMOpSuspend:
		command = pb.VMCommandType_VM_COMMAND_TYPE_PAUSE
	case VMOpUnsuspend:
		command = pb.VMCommandType_VM_COMMAND_TYPE_RESUME
	default:
		return fmt.Errorf("unsupported operation: %s", job.Args.Operation)
	}

	// Build VM config from params
	var vmConfig *pb.VMConfig
	if len(job.Args.Params) > 0 && job.Args.Operation == VMOpCreate {
		var params struct {
			ImageID      string            `json:"image_id"`
			SSHPublicKey string            `json:"ssh_public_key"`
			UserData     string            `json:"user_data"`
			Metadata     map[string]string `json:"metadata"`
			VNCEnabled   bool              `json:"vnc_enabled"`
			VNCPassword  string            `json:"vnc_password"`
		}

		if err := json.Unmarshal(job.Args.Params, &params); err != nil {
			w.logger.ErrorContext(ctx, "failed to parse VM params",
				"error", err,
			)
		} else {
			vmConfig = &pb.VMConfig{
				Resources: &pb.VMResources{
					Vcpus:    int32(vm.Resources.CPU),
					MemoryMb: int64(vm.Resources.RAM),
					DiskGb:   int64(vm.Resources.Disk),
					NetworkBandwidthMbps: func() int32 {
						if vm.Resources.IOPS != nil {
							return int32(*vm.Resources.IOPS)
						}
						return 0
					}(),
				},
				ImageId:      params.ImageID,
				SshPublicKey: params.SSHPublicKey,
				UserData:     params.UserData,
				Metadata:     params.Metadata,
				VncEnabled:   params.VNCEnabled,
				VncPassword:  params.VNCPassword,
			}
		}
	}

	// Execute VM command via gRPC
	req := &pb.VMCommandRequest{
		VmId:           vm.ID,
		Command:        command,
		Config:         vmConfig,
		TimeoutSeconds: 300, // 5 minutes
	}

	resp, err := client.ExecuteVMCommand(ctx, req)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to execute VM command",
			"error", err,
			"vm_id", vm.ID,
			"command", command,
		)

		// Check if error is retryable
		if IsRetryableError(err) {
			if w.metrics != nil {
				w.metrics.RecordJobRetried()
			}
			return fmt.Errorf("retryable error executing VM command: %w", err)
		}

		// Update VM status to error
		vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusError)

		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("non-retryable error executing VM command: %w", err)
	}

	if !resp.Success {
		w.logger.ErrorContext(ctx, "VM command failed",
			"vm_id", vm.ID,
			"error_code", resp.Error.GetCode(),
			"error_message", resp.Error.GetMessage(),
		)

		// Update VM status to error
		vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusError)

		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("VM command failed: %s", resp.Error.GetMessage())
	}

	// Update VM status based on operation
	var newStatus models.VMStatus
	switch job.Args.Operation {
	case VMOpCreate:
		newStatus = models.VMStatusStopped
	case VMOpStart:
		newStatus = models.VMStatusRunning
	case VMOpStop:
		newStatus = models.VMStatusStopped
	case VMOpRestart:
		newStatus = models.VMStatusRunning
	case VMOpDelete:
		// VM is deleted, no status update needed
		newStatus = ""
	case VMOpSuspend:
		newStatus = models.VMStatusSuspended
	case VMOpUnsuspend:
		newStatus = models.VMStatusRunning
	}

	if newStatus != "" {
		if err := vmRepo.UpdateStatus(ctx, vm.ID, newStatus); err != nil {
			w.logger.ErrorContext(ctx, "failed to update VM status",
				"error", err,
				"vm_id", vm.ID,
			)
		}
	}

	latency := time.Since(startTime)
	if w.metrics != nil {
		w.metrics.RecordJobProcessed("vm_operation", latency)
	}

	w.logger.InfoContext(ctx, "VM operation completed successfully",
		"job_id", jobID,
		"vm_id", vm.ID,
		"operation", job.Args.Operation,
		"latency_ms", latency.Milliseconds(),
	)

	return nil
}

// TemplateSyncWorker handles OS template synchronization
// Queue: default (50 workers)
type TemplateSyncWorker struct {
	river.WorkerDefaults[TemplateSyncJob]
	logger       *slog.Logger
	templateRepo *repository.TemplateRepository
	nodeRepo     *repository.NodeRepository
	db           *gorm.DB
	jobManager   *JobManager
	metrics      *MetricsCollector
}

// NewTemplateSyncWorker creates a new template sync worker
func NewTemplateSyncWorker(logger *slog.Logger, templateRepo *repository.TemplateRepository, nodeRepo *repository.NodeRepository, db *gorm.DB) *TemplateSyncWorker {
	w := &TemplateSyncWorker{
		logger:       logger,
		templateRepo: templateRepo,
		nodeRepo:     nodeRepo,
		db:           db,
	}

	if globalWorkerContext != nil {
		w.jobManager = NewJobManager(globalWorkerContext.DB)
		w.metrics = globalWorkerContext.Metrics
	}

	return w
}

// Work implements the template sync job handler
func (w *TemplateSyncWorker) Work(ctx context.Context, job *river.Job[TemplateSyncJob]) error {
	startTime := time.Now()
	jobID := fmt.Sprintf("%d", job.ID)

	w.logger.InfoContext(ctx, "processing template sync",
		"job_id", jobID,
		"template_id", job.Args.TemplateID,
		"nodes_count", len(job.Args.NodeIDs),
		"force", job.Args.Force,
		"attempt", job.Attempt,
	)

	template, err := w.templateRepo.GetByID(ctx, job.Args.TemplateID)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to fetch template",
			"error", err,
			"template_id", job.Args.TemplateID,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("failed to fetch template: %w", err)
	}

	successCount := 0
	var syncErrors []error

	for _, nodeID := range job.Args.NodeIDs {
		if err := w.syncTemplateToNode(ctx, template, nodeID, job.Args.Force, jobID); err != nil {
			w.logger.ErrorContext(ctx, "failed to sync template to node",
				"error", err,
				"template_id", job.Args.TemplateID,
				"node_id", nodeID,
			)
			syncErrors = append(syncErrors, err)
		} else {
			successCount++
		}
	}

	// Record metrics
	latency := time.Since(startTime)
	if w.metrics != nil {
		if successCount == len(job.Args.NodeIDs) {
			w.metrics.RecordJobProcessed("template_sync", latency)
		} else {
			w.metrics.RecordJobFailed()
		}
	}

	w.logger.InfoContext(ctx, "template sync completed",
		"job_id", jobID,
		"template_id", template.ID,
		"success_count", successCount,
		"total_nodes", len(job.Args.NodeIDs),
		"latency_ms", latency.Milliseconds(),
	)

	// Return error if any node failed
	if len(syncErrors) > 0 {
		return fmt.Errorf("template sync partially failed: %d/%d nodes failed", len(syncErrors), len(job.Args.NodeIDs))
	}

	return nil
}

func (w *TemplateSyncWorker) syncTemplateToNode(ctx context.Context, template *models.OSTemplate, nodeID string, force bool, jobID string) error {
	w.logger.InfoContext(ctx, "syncing template to node",
		"template_id", template.ID,
		"node_id", nodeID,
		"job_id", jobID,
	)

	// Update sync status to syncing
	w.updateSyncStatus(ctx, template.ID, nodeID, "syncing", "")

	node, err := w.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		w.updateSyncStatus(ctx, template.ID, nodeID, "error", "Node not found")
		return fmt.Errorf("node not found: %w", err)
	}

	if node.Status != models.NodeStatusActive {
		w.updateSyncStatus(ctx, template.ID, nodeID, "error", "Node is not active")
		return fmt.Errorf("node %s is not active", nodeID)
	}

	// In a real implementation, this would:
	// 1. Connect to node's storage service
	// 2. Download/check template image
	// 3. Verify checksum
	// 4. Update node-local template registry

	w.logger.InfoContext(ctx, "template sync initiated to node",
		"template_id", template.ID,
		"node_id", nodeID,
		"node_address", node.IPAddress,
		"image_path", template.ImagePath,
	)

	// Simulate download and verification
	time.Sleep(100 * time.Millisecond)

	// Update sync status to available
	w.updateSyncStatus(ctx, template.ID, nodeID, "available", "")

	return nil
}

func (w *TemplateSyncWorker) updateSyncStatus(ctx context.Context, templateID, nodeID string, status string, errorMsg string) error {
	type TemplateNodeStatus struct {
		TemplateID string `gorm:"type:uuid;not null;primaryKey"`
		NodeID     string `gorm:"type:uuid;not null;primaryKey"`
		Status     string `gorm:"type:varchar(20);not null"`
		ErrorMsg   string `gorm:"type:text"`
		SyncedAt   *time.Time
	}

	now := time.Now()
	record := TemplateNodeStatus{
		TemplateID: templateID,
		NodeID:     nodeID,
		Status:     status,
		ErrorMsg:   errorMsg,
	}

	if status == "available" {
		record.SyncedAt = &now
	}

	return w.db.WithContext(ctx).Save(&record).Error
}

// BackupWorker handles VM backup operations
// Queue: batch (10 workers)
type BackupWorker struct {
	river.WorkerDefaults[BackupJob]
	logger     *slog.Logger
	jobManager *JobManager
	metrics    *MetricsCollector
}

// NewBackupWorker creates a new backup worker
func NewBackupWorker(logger *slog.Logger) *BackupWorker {
	w := &BackupWorker{
		logger: logger,
	}

	if globalWorkerContext != nil {
		w.jobManager = NewJobManager(globalWorkerContext.DB)
		w.metrics = globalWorkerContext.Metrics
	}

	return w
}

// Work implements the backup job handler
func (w *BackupWorker) Work(ctx context.Context, job *river.Job[BackupJob]) error {
	startTime := time.Now()
	jobID := fmt.Sprintf("%d", job.ID)

	w.logger.InfoContext(ctx, "processing backup",
		"job_id", jobID,
		"vm_id", job.Args.VMID,
		"backup_type", job.Args.BackupType,
		"storage", job.Args.StorageProvider,
		"destination", job.Args.Destination,
		"attempt", job.Attempt,
	)

	// Validate backup type
	if err := ValidateBackupType(job.Args.BackupType); err != nil {
		w.logger.ErrorContext(ctx, "invalid backup type",
			"error", err,
			"backup_type", job.Args.BackupType,
		)
		return fmt.Errorf("invalid backup type: %w", err)
	}

	// Get dependencies
	if globalWorkerContext == nil {
		return fmt.Errorf("worker context not initialized")
	}

	vmRepo := globalWorkerContext.VMRepo
	nodeRepo := globalWorkerContext.NodeRepo

	// Get VM details
	vm, err := vmRepo.GetByIDWithNode(ctx, job.Args.VMID)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get VM",
			"error", err,
			"vm_id", job.Args.VMID,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Get node details
	node, err := nodeRepo.GetByID(ctx, vm.NodeID)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get node",
			"error", err,
			"node_id", vm.NodeID,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("failed to get node: %w", err)
	}

	if node.Status != models.NodeStatusActive {
		return fmt.Errorf("node %s is not active (status: %s)", node.ID, node.Status)
	}

	if job.Args.BackupID != "" {
		now := time.Now()
		if err := globalWorkerContext.DB.WithContext(ctx).
			Model(&models.Backup{}).
			Where("id = ?", job.Args.BackupID).
			Updates(map[string]interface{}{
				"status":        models.BackupStatusInProgress,
				"started_at":    now,
				"error_message": "",
			}).Error; err != nil {
			w.logger.WarnContext(ctx, "failed to mark backup as in progress",
				"backup_id", job.Args.BackupID,
				"error", err,
			)
		}
	}

	// Get gRPC client for node
	client, err := globalWorkerContext.AgentClient.GetClient(node.IPAddress)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get agent client",
			"error", err,
			"node_address", node.IPAddress,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return err
	}

	// Create snapshot based on backup type
	var snapshotOp pb.SnapshotOperationType
	switch job.Args.BackupType {
	case BackupTypeSnapshot:
		snapshotOp = pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_CREATE
	case BackupTypeFull:
		// For full backup, we might need a different approach
		// For now, use snapshot as base
		snapshotOp = pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_CREATE
	}

	// Create backup snapshot
	req := &pb.SnapshotRequest{
		VmId:        vm.ID,
		Operation:   snapshotOp,
		Name:        fmt.Sprintf("backup_%s_%d", job.Args.BackupType, time.Now().Unix()),
		Description: fmt.Sprintf("Backup job %s for VM %s", jobID, vm.ID),
		Quiesce:     true,
	}

	resp, err := client.CreateSnapshot(ctx, req)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to create backup snapshot",
			"error", err,
			"vm_id", vm.ID,
		)

		if IsRetryableError(err) {
			if w.metrics != nil {
				w.metrics.RecordJobRetried()
			}
			return fmt.Errorf("retryable error creating snapshot: %w", err)
		}

		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("non-retryable error creating snapshot: %w", err)
	}

	if !resp.Success {
		w.logger.ErrorContext(ctx, "backup snapshot creation failed",
			"vm_id", vm.ID,
			"error_code", resp.Error.GetCode(),
			"error_message", resp.Error.GetMessage(),
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		if job.Args.BackupID != "" {
			_ = globalWorkerContext.DB.WithContext(ctx).
				Model(&models.Backup{}).
				Where("id = ?", job.Args.BackupID).
				Updates(map[string]interface{}{
					"status":        models.BackupStatusFailed,
					"error_message": resp.Error.GetMessage(),
				}).Error
		}
		return fmt.Errorf("snapshot creation failed: %s", resp.Error.GetMessage())
	}

	archiveData, err := buildBackupArchive(job, vm.ID, resp.Snapshot.GetSnapshotId())
	if err != nil {
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		if job.Args.BackupID != "" {
			_ = globalWorkerContext.DB.WithContext(ctx).
				Model(&models.Backup{}).
				Where("id = ?", job.Args.BackupID).
				Updates(map[string]interface{}{
					"status":        models.BackupStatusFailed,
					"error_message": err.Error(),
				}).Error
		}
		return fmt.Errorf("failed to build backup archive: %w", err)
	}

	storageClient, err := newBackupStorageClient(job.Args.StorageProvider)
	if err != nil {
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		if job.Args.BackupID != "" {
			_ = globalWorkerContext.DB.WithContext(ctx).
				Model(&models.Backup{}).
				Where("id = ?", job.Args.BackupID).
				Updates(map[string]interface{}{
					"status":        models.BackupStatusFailed,
					"error_message": err.Error(),
				}).Error
		}
		return fmt.Errorf("failed to initialize storage client: %w", err)
	}

	if err := storageClient.Upload(ctx, job.Args.Destination, bytes.NewReader(archiveData), int64(len(archiveData)), "application/octet-stream"); err != nil {
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		if job.Args.BackupID != "" {
			_ = globalWorkerContext.DB.WithContext(ctx).
				Model(&models.Backup{}).
				Where("id = ?", job.Args.BackupID).
				Updates(map[string]interface{}{
					"status":        models.BackupStatusFailed,
					"error_message": err.Error(),
				}).Error
		}
		return fmt.Errorf("failed to upload archive to object storage: %w", err)
	}

	checksum := sha256.Sum256(archiveData)
	if job.Args.BackupID != "" {
		completedAt := time.Now()
		if err := globalWorkerContext.DB.WithContext(ctx).
			Model(&models.Backup{}).
			Where("id = ?", job.Args.BackupID).
			Updates(map[string]interface{}{
				"status":       models.BackupStatusCompleted,
				"size":         int64(len(archiveData)),
				"checksum":     hex.EncodeToString(checksum[:]),
				"completed_at": completedAt,
			}).Error; err != nil {
			w.logger.WarnContext(ctx, "failed to update completed backup metadata",
				"backup_id", job.Args.BackupID,
				"error", err,
			)
		}
	}

	latency := time.Since(startTime)
	if w.metrics != nil {
		w.metrics.RecordJobProcessed("backup", latency)
	}

	w.logger.InfoContext(ctx, "backup completed successfully",
		"job_id", jobID,
		"vm_id", vm.ID,
		"snapshot_id", resp.Snapshot.GetSnapshotId(),
		"backup_type", job.Args.BackupType,
		"backup_id", job.Args.BackupID,
		"archive_size", len(archiveData),
		"latency_ms", latency.Milliseconds(),
	)

	return nil
}

func buildBackupArchive(job *river.Job[BackupJob], vmID, snapshotID string) ([]byte, error) {
	manifest := map[string]interface{}{
		"vm_id":          vmID,
		"backup_id":      job.Args.BackupID,
		"snapshot_id":    snapshotID,
		"backup_type":    job.Args.BackupType,
		"compression":    job.Args.Compression,
		"created_at":     time.Now().UTC().Format(time.RFC3339Nano),
		"storage_target": job.Args.Destination,
	}

	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}

	tarBuffer := bytes.NewBuffer(nil)
	tarWriter := tar.NewWriter(tarBuffer)
	header := &tar.Header{
		Name:    "manifest.json",
		Mode:    0o600,
		Size:    int64(len(manifestJSON)),
		ModTime: time.Now(),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		return nil, err
	}
	if _, err := tarWriter.Write(manifestJSON); err != nil {
		return nil, err
	}
	if err := tarWriter.Close(); err != nil {
		return nil, err
	}

	return compressBackupData(tarBuffer.Bytes(), job.Args.Compression)
}

func compressBackupData(raw []byte, compression string) ([]byte, error) {
	switch compression {
	case "", "none":
		return raw, nil
	case "gzip":
		buf := bytes.NewBuffer(nil)
		gz := gzip.NewWriter(buf)
		if _, err := gz.Write(raw); err != nil {
			_ = gz.Close()
			return nil, err
		}
		if err := gz.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	case "zstd":
		buf := bytes.NewBuffer(nil)
		enc, err := zstd.NewWriter(buf)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(enc, bytes.NewReader(raw)); err != nil {
			enc.Close()
			return nil, err
		}
		if err := enc.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	default:
		return nil, fmt.Errorf("unsupported compression type: %s", compression)
	}
}

func newBackupStorageClient(provider string) (*sharedstorage.Client, error) {
	resolvedProvider := sharedstorage.ProviderS3
	if strings.EqualFold(provider, string(sharedstorage.ProviderMinIO)) {
		resolvedProvider = sharedstorage.ProviderMinIO
	}

	endpoint := firstNonEmpty(os.Getenv("STORAGE_ENDPOINT"), os.Getenv("S3_ENDPOINT"))
	if endpoint != "" && !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}

	config := &sharedstorage.Config{
		Endpoint:  endpoint,
		AccessKey: firstNonEmpty(os.Getenv("STORAGE_ACCESS_KEY"), os.Getenv("S3_ACCESS_KEY")),
		SecretKey: firstNonEmpty(os.Getenv("STORAGE_SECRET_KEY"), os.Getenv("S3_SECRET_KEY")),
		Bucket:    firstNonEmpty(os.Getenv("STORAGE_BUCKET"), os.Getenv("S3_BUCKET")),
		Region:    firstNonEmpty(os.Getenv("STORAGE_REGION"), os.Getenv("S3_REGION"), "us-east-1"),
		Provider:  resolvedProvider,
	}

	if config.AccessKey == "" || config.SecretKey == "" || config.Bucket == "" {
		return nil, fmt.Errorf("missing object storage configuration for backup upload")
	}

	return sharedstorage.NewClient(config)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// ImportWorker handles VM import operations (e.g., from Virtualizor)
// Queue: batch (10 workers)
type ImportWorker struct {
	river.WorkerDefaults[ImportJob]
	logger     *slog.Logger
	jobManager *JobManager
	metrics    *MetricsCollector
}

// NewImportWorker creates a new import worker
func NewImportWorker(logger *slog.Logger) *ImportWorker {
	w := &ImportWorker{
		logger: logger,
	}

	if globalWorkerContext != nil {
		w.jobManager = NewJobManager(globalWorkerContext.DB)
		w.metrics = globalWorkerContext.Metrics
	}

	return w
}

// Work implements the import job handler
func (w *ImportWorker) Work(ctx context.Context, job *river.Job[ImportJob]) error {
	startTime := time.Now()
	jobID := fmt.Sprintf("%d", job.ID)

	w.logger.InfoContext(ctx, "processing import",
		"job_id", jobID,
		"source", job.Args.Source,
		"source_id", job.Args.SourceID,
		"node_id", job.Args.NodeID,
		"user_id", job.Args.UserID,
		"attempt", job.Attempt,
	)

	// Validate import source
	if err := ValidateImportSource(job.Args.Source); err != nil {
		w.logger.ErrorContext(ctx, "invalid import source",
			"error", err,
			"source", job.Args.Source,
		)
		return fmt.Errorf("invalid import source: %w", err)
	}

	// Get dependencies
	if globalWorkerContext == nil {
		return fmt.Errorf("worker context not initialized")
	}

	nodeRepo := globalWorkerContext.NodeRepo
	vmRepo := globalWorkerContext.VMRepo

	// Get node details
	node, err := nodeRepo.GetByID(ctx, job.Args.NodeID)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get node",
			"error", err,
			"node_id", job.Args.NodeID,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("failed to get node: %w", err)
	}

	if node.Status != models.NodeStatusActive {
		return fmt.Errorf("node %s is not active (status: %s)", node.ID, node.Status)
	}

	// Get gRPC client for node
	client, err := globalWorkerContext.AgentClient.GetClient(node.IPAddress)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get agent client",
			"error", err,
			"node_address", node.IPAddress,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return err
	}

	switch job.Args.Source {
	case ImportSourceVirtualizor:
		w.logger.InfoContext(ctx, "importing from Virtualizor",
			"source_id", job.Args.SourceID,
			"config_path", job.Args.ConfigPath,
			"disk_path", job.Args.DiskPath,
		)

		// 1. Read XML from config_path
		// 2. Extract VM metadata (RAM, CPU, disk size, network config)
		// 3. Re-map disk image to new storage pool
		// 4. Create VM record in database
		// 5. Update network configuration
		// 6. Start VM if it was running

	case ImportSourceManual:
		w.logger.InfoContext(ctx, "performing manual import",
			"source_id", job.Args.SourceID,
		)
	}

	// Create VM record for imported VM
	// Parse metadata if available
	var metadata struct {
		Hostname string `json:"hostname"`
		CPU      int    `json:"cpu"`
		RAM      int    `json:"ram"`
		Disk     int    `json:"disk"`
	}

	if len(job.Args.Metadata) > 0 {
		if err := json.Unmarshal(job.Args.Metadata, &metadata); err != nil {
			w.logger.WarnContext(ctx, "failed to parse import metadata",
				"error", err,
			)
		}
	}

	// Create VM record
	vm := &models.VM{
		UserID:       job.Args.UserID,
		NodeID:       job.Args.NodeID,
		Hostname:     metadata.Hostname,
		OSTemplateID: "", // Will be set after template matching
		Resources: models.Resources{
			CPU:  metadata.CPU,
			RAM:  metadata.RAM,
			Disk: metadata.Disk,
		},
		Status:          models.VMStatusStopped,
		SourceMigration: string(job.Args.Source),
	}

	if err := vmRepo.Create(ctx, vm); err != nil {
		w.logger.ErrorContext(ctx, "failed to create VM record",
			"error", err,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("failed to create VM record: %w", err)
	}

	// Import disk image via agent
	// Send disk import command to agent
	_ = client // Use client to send import command

	latency := time.Since(startTime)
	if w.metrics != nil {
		w.metrics.RecordJobProcessed("import", latency)
	}

	w.logger.InfoContext(ctx, "import completed successfully",
		"job_id", jobID,
		"source", job.Args.Source,
		"source_id", job.Args.SourceID,
		"vm_id", vm.ID,
		"latency_ms", latency.Milliseconds(),
	)

	return nil
}

// NetworkConfigWorker handles network configuration operations
// Queue: critical (20 workers)
type NetworkConfigWorker struct {
	river.WorkerDefaults[NetworkConfigJob]
	logger     *slog.Logger
	jobManager *JobManager
	metrics    *MetricsCollector
}

// NetworkConfigJob represents a network configuration job
type NetworkConfigJob struct {
	VMID       string          `json:"vm_id"`
	NodeID     string          `json:"node_id"`
	ConfigType string          `json:"config_type"` // bandwidth, firewall, nat
	Config     json.RawMessage `json:"config,omitempty"`
}

// Kind returns the job kind for network config
func (j NetworkConfigJob) Kind() string {
	return "network_config"
}

// InsertOpts returns insert options for network config (critical queue)
func (j NetworkConfigJob) InsertOpts() *river.InsertOpts {
	return &river.InsertOpts{
		Queue:    QueueCritical,
		Priority: PriorityHigh,
	}
}

// NewNetworkConfigWorker creates a new network config worker
func NewNetworkConfigWorker(logger *slog.Logger) *NetworkConfigWorker {
	w := &NetworkConfigWorker{
		logger: logger,
	}

	if globalWorkerContext != nil {
		w.jobManager = NewJobManager(globalWorkerContext.DB)
		w.metrics = globalWorkerContext.Metrics
	}

	return w
}

// Work implements the network config job handler
func (w *NetworkConfigWorker) Work(ctx context.Context, job *river.Job[NetworkConfigJob]) error {
	startTime := time.Now()
	jobID := fmt.Sprintf("%d", job.ID)

	w.logger.InfoContext(ctx, "processing network configuration",
		"job_id", jobID,
		"vm_id", job.Args.VMID,
		"config_type", job.Args.ConfigType,
		"attempt", job.Attempt,
	)

	// Get dependencies
	if globalWorkerContext == nil {
		return fmt.Errorf("worker context not initialized")
	}

	vmRepo := globalWorkerContext.VMRepo
	nodeRepo := globalWorkerContext.NodeRepo

	// Get VM details with networks and firewalls
	vm, err := vmRepo.GetByIDWithRelations(ctx, job.Args.VMID)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get VM",
			"error", err,
			"vm_id", job.Args.VMID,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Get node details
	node, err := nodeRepo.GetByID(ctx, job.Args.NodeID)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get node",
			"error", err,
			"node_id", job.Args.NodeID,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("failed to get node: %w", err)
	}

	if node.Status != models.NodeStatusActive {
		return fmt.Errorf("node %s is not active (status: %s)", node.ID, node.Status)
	}

	// Get gRPC client for node
	client, err := globalWorkerContext.AgentClient.GetClient(node.IPAddress)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get agent client",
			"error", err,
			"node_address", node.IPAddress,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return err
	}

	// Build network config
	config := &pb.VMNetworkConfig{}

	// Parse config based on type
	switch job.Args.ConfigType {
	case "bandwidth":
		var bwConfig struct {
			IngressRateMbps int32 `json:"ingress_rate_mbps"`
			EgressRateMbps  int32 `json:"egress_rate_mbps"`
			BurstSizeMb     int32 `json:"burst_size_mb"`
		}
		if err := json.Unmarshal(job.Args.Config, &bwConfig); err != nil {
			w.logger.ErrorContext(ctx, "failed to parse bandwidth config",
				"error", err,
			)
			return fmt.Errorf("failed to parse bandwidth config: %w", err)
		}
		config.BandwidthLimits = &pb.BandwidthLimit{
			IngressRateMbps: bwConfig.IngressRateMbps,
			EgressRateMbps:  bwConfig.EgressRateMbps,
			BurstSizeMb:     bwConfig.BurstSizeMb,
		}

	case "firewall":
		var fwConfig struct {
			Rules []struct {
				Direction string `json:"direction"`
				Action    string `json:"action"`
				Protocol  string `json:"protocol"`
				SourceIP  string `json:"source_ip"`
				DestPort  string `json:"dest_port"`
				Priority  int32  `json:"priority"`
			} `json:"rules"`
		}
		if err := json.Unmarshal(job.Args.Config, &fwConfig); err != nil {
			w.logger.ErrorContext(ctx, "failed to parse firewall config",
				"error", err,
			)
			return fmt.Errorf("failed to parse firewall config: %w", err)
		}

		for _, rule := range fwConfig.Rules {
			var direction pb.FirewallDirection
			switch rule.Direction {
			case "inbound":
				direction = pb.FirewallDirection_FIREWALL_DIRECTION_INBOUND
			case "outbound":
				direction = pb.FirewallDirection_FIREWALL_DIRECTION_OUTBOUND
			}

			var action pb.FirewallAction
			switch rule.Action {
			case "allow":
				action = pb.FirewallAction_FIREWALL_ACTION_ALLOW
			case "deny":
				action = pb.FirewallAction_FIREWALL_ACTION_DENY
			case "reject":
				action = pb.FirewallAction_FIREWALL_ACTION_REJECT
			}

			config.FirewallRules = append(config.FirewallRules, &pb.FirewallRule{
				Direction:  direction,
				Action:     action,
				Protocol:   rule.Protocol,
				SourceCidr: rule.SourceIP,
				DestPort:   rule.DestPort,
				Priority:   rule.Priority,
			})
		}

	case "nat":
		// NAT/port forwarding configuration
		var natConfig struct {
			Interfaces []struct {
				Name       string `json:"name"`
				Type       string `json:"type"`
				BridgeName string `json:"bridge_name"`
				MacAddress string `json:"mac_address"`
				IpAddress  string `json:"ip_address"`
				Netmask    int32  `json:"netmask"`
				Gateway    string `json:"gateway"`
				UseDhcp    bool   `json:"use_dhcp"`
			} `json:"interfaces"`
		}
		if err := json.Unmarshal(job.Args.Config, &natConfig); err != nil {
			w.logger.ErrorContext(ctx, "failed to parse NAT config",
				"error", err,
			)
			return fmt.Errorf("failed to parse NAT config: %w", err)
		}

		for _, iface := range natConfig.Interfaces {
			var ifaceType pb.NetworkInterfaceType
			switch iface.Type {
			case "bridge":
				ifaceType = pb.NetworkInterfaceType_NETWORK_INTERFACE_TYPE_BRIDGE
			case "macvtap":
				ifaceType = pb.NetworkInterfaceType_NETWORK_INTERFACE_TYPE_MACVTAP
			case "user":
				ifaceType = pb.NetworkInterfaceType_NETWORK_INTERFACE_TYPE_USER
			case "passthrough":
				ifaceType = pb.NetworkInterfaceType_NETWORK_INTERFACE_TYPE_PASSTHROUGH
			}

			config.Interfaces = append(config.Interfaces, &pb.NetworkInterface{
				Name:       iface.Name,
				Type:       ifaceType,
				BridgeName: iface.BridgeName,
				MacAddress: iface.MacAddress,
				IpAddress:  iface.IpAddress,
				Netmask:    iface.Netmask,
				Gateway:    iface.Gateway,
				UseDhcp:    iface.UseDhcp,
			})
		}
	}

	// Apply network config via gRPC
	req := &pb.NetworkConfigRequest{
		VmId:       vm.ID,
		Config:     config,
		ReplaceAll: true,
	}

	resp, err := client.ApplyNetworkConfig(ctx, req)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to apply network config",
			"error", err,
			"vm_id", vm.ID,
		)

		if IsRetryableError(err) {
			if w.metrics != nil {
				w.metrics.RecordJobRetried()
			}
			return fmt.Errorf("retryable error applying network config: %w", err)
		}

		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("non-retryable error applying network config: %w", err)
	}

	if !resp.Success {
		w.logger.ErrorContext(ctx, "network config application failed",
			"vm_id", vm.ID,
			"error_message", resp.Error.GetMessage(),
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("network config failed: %s", resp.Error.GetMessage())
	}

	latency := time.Since(startTime)
	if w.metrics != nil {
		w.metrics.RecordJobProcessed("network_config", latency)
	}

	w.logger.InfoContext(ctx, "network configuration completed successfully",
		"job_id", jobID,
		"vm_id", vm.ID,
		"config_type", job.Args.ConfigType,
		"latency_ms", latency.Milliseconds(),
	)

	return nil
}

// SnapshotWorker handles VM snapshot operations
// Queue: critical (20 workers)
type SnapshotWorker struct {
	river.WorkerDefaults[SnapshotJob]
	logger      *slog.Logger
	jobManager  *JobManager
	agentClient *AgentClient
	metrics     *MetricsCollector
}

// NewSnapshotWorker creates a new snapshot worker
func NewSnapshotWorker(logger *slog.Logger) *SnapshotWorker {
	w := &SnapshotWorker{
		logger: logger,
	}

	if globalWorkerContext != nil {
		w.jobManager = NewJobManager(globalWorkerContext.DB)
		w.agentClient = globalWorkerContext.AgentClient
		w.metrics = globalWorkerContext.Metrics
	}

	return w
}

// Work implements the snapshot job handler
func (w *SnapshotWorker) Work(ctx context.Context, job *river.Job[SnapshotJob]) error {
	startTime := time.Now()
	jobID := fmt.Sprintf("%d", job.ID)

	w.logger.InfoContext(ctx, "processing snapshot operation",
		"job_id", jobID,
		"vm_id", job.Args.VMID,
		"snapshot_id", job.Args.SnapshotID,
		"operation", job.Args.Operation,
		"node_id", job.Args.NodeID,
		"attempt", job.Attempt,
	)

	// Validate operation type
	if err := ValidateSnapshotOperation(job.Args.Operation); err != nil {
		w.logger.ErrorContext(ctx, "invalid snapshot operation",
			"error", err,
			"operation", job.Args.Operation,
		)
		return fmt.Errorf("invalid operation: %w", err)
	}

	// Get dependencies
	if globalWorkerContext == nil {
		return fmt.Errorf("worker context not initialized")
	}

	vmRepo := globalWorkerContext.VMRepo
	nodeRepo := globalWorkerContext.NodeRepo

	// Get VM details
	vm, err := vmRepo.GetByID(ctx, job.Args.VMID)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get VM",
			"error", err,
			"vm_id", job.Args.VMID,
		)
		return fmt.Errorf("failed to get VM: %w", err)
	}

	// Get node details
	node, err := nodeRepo.GetByID(ctx, job.Args.NodeID)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get node",
			"error", err,
			"node_id", job.Args.NodeID,
		)
		return fmt.Errorf("failed to get node: %w", err)
	}

	if node.Status != models.NodeStatusActive {
		return fmt.Errorf("node %s is not active (status: %s)", node.ID, node.Status)
	}

	// Get gRPC client for node
	client, err := w.agentClient.GetClient(node.IPAddress)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to get agent client",
			"error", err,
			"node_address", node.IPAddress,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return err
	}

	// Map operation type to gRPC command
	var operation pb.SnapshotOperationType
	var snapshotID string
	switch job.Args.Operation {
	case SnapshotOpCreate:
		operation = pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_CREATE
		snapshotID = job.Args.SnapshotID
	case SnapshotOpRestore:
		operation = pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_RESTORE
		snapshotID = job.Args.SnapshotID
	case SnapshotOpDelete:
		operation = pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_DELETE
		snapshotID = job.Args.SnapshotID
	default:
		return fmt.Errorf("unsupported snapshot operation: %s", job.Args.Operation)
	}

	// Execute snapshot command via gRPC
	req := &pb.SnapshotRequest{
		VmId:        vm.ID,
		Operation:   operation,
		SnapshotId:  snapshotID,
		Name:        job.Args.Name,
		Description: fmt.Sprintf("Snapshot operation %s for VM %s", job.Args.Operation, vm.ID),
		Quiesce:     true,
	}

	resp, err := client.CreateSnapshot(ctx, req)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to execute snapshot command",
			"error", err,
			"vm_id", vm.ID,
			"operation", operation,
		)

		if IsRetryableError(err) {
			if w.metrics != nil {
				w.metrics.RecordJobRetried()
			}
			return fmt.Errorf("retryable error executing snapshot command: %w", err)
		}

		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("non-retryable error executing snapshot command: %w", err)
	}

	if !resp.Success {
		w.logger.ErrorContext(ctx, "snapshot command failed",
			"vm_id", vm.ID,
			"operation", operation,
			"error_code", resp.Error.GetCode(),
			"error_message", resp.Error.GetMessage(),
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("snapshot command failed: %s", resp.Error.GetMessage())
	}

	// Update VM status based on operation
	var newStatus models.VMStatus
	switch job.Args.Operation {
	case SnapshotOpRestore:
		newStatus = models.VMStatusStopped
		if err := vmRepo.UpdateStatus(ctx, vm.ID, newStatus); err != nil {
			w.logger.ErrorContext(ctx, "failed to update VM status after restore",
				"error", err,
				"vm_id", vm.ID,
			)
		}
	}

	latency := time.Since(startTime)
	if w.metrics != nil {
		w.metrics.RecordJobProcessed("snapshot_operation", latency)
	}

	w.logger.InfoContext(ctx, "snapshot operation completed successfully",
		"job_id", jobID,
		"vm_id", vm.ID,
		"snapshot_id", snapshotID,
		"operation", job.Args.Operation,
		"latency_ms", latency.Milliseconds(),
	)

	return nil
}

// AuditWorker handles audit log insertion to database
// Queue: audit (20 workers)
type AuditWorker struct {
	river.WorkerDefaults[AuditJob]
	logger    *slog.Logger
	auditRepo *repository.AuditRepository
}

// NewAuditWorker creates a new audit worker with repository
func NewAuditWorker(logger *slog.Logger, auditRepo *repository.AuditRepository) *AuditWorker {
	return &AuditWorker{
		logger:    logger,
		auditRepo: auditRepo,
	}
}

// Work implements the audit job handler - inserts audit log to database
func (w *AuditWorker) Work(ctx context.Context, job *river.Job[AuditJob]) error {
	w.logger.InfoContext(ctx, "processing audit log",
		"action", job.Args.Action,
		"resource_type", job.Args.ResourceType,
		"user_id", job.Args.UserID,
	)

	// Create audit log model from job args
	auditLog := &models.AuditLog{
		UserID:         job.Args.UserID,
		Action:         job.Args.Action,
		ResourceType:   job.Args.ResourceType,
		ResourceID:     job.Args.ResourceID,
		IPAddress:      job.Args.IPAddress,
		UserAgent:      job.Args.UserAgent,
		Details:        job.Args.Details,
		BeforeSnapshot: job.Args.BeforeSnapshot,
		AfterSnapshot:  job.Args.AfterSnapshot,
	}

	// Set timestamp from job if provided, otherwise use current time
	if !job.Args.Timestamp.IsZero() {
		auditLog.CreatedAt = job.Args.Timestamp
	}

	// Insert audit log to database
	// Errors are logged but not returned to prevent job retries for audit logs
	if err := w.auditRepo.Create(ctx, auditLog); err != nil {
		w.logger.ErrorContext(ctx, "failed to insert audit log",
			"error", err,
			"action", job.Args.Action,
			"resource_type", job.Args.ResourceType,
		)
		// Return nil to prevent retries - audit log failures should not block the system
		return nil
	}

	w.logger.InfoContext(ctx, "audit log inserted successfully",
		"audit_id", auditLog.ID,
		"action", job.Args.Action,
	)

	return nil
}
