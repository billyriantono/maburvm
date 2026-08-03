package queue

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
	vmimport "github.com/maburvm/panel/internal/agent/import"
	panelclient "github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	sharedstorage "github.com/maburvm/panel/internal/shared/storage"
	"github.com/riverqueue/river"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
func (c *AgentClient) GetClient(nodeID, nodeAddress string) (pb.NodeAgentClient, error) {
	// Cache by node ID (the pin identity), not the address, so a node keeps its
	// pinned connection even if its address representation varies.
	c.mu.RLock()
	client, exists := c.clients[nodeID]
	c.mu.RUnlock()

	if exists {
		return client, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if client, exists := c.clients[nodeID]; exists {
		return client, nil
	}

	// Create new connection
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	// Pin the agent's self-signed cert (TOFU-then-verify) via the process-wide pin
	// store — the SAME primitive the panel-client path uses. This carries the
	// node's bearer token + VM secrets, so a bare InsecureSkipVerify would let an
	// on-path attacker impersonate a node and steal them.
	creds := panelclient.NodeTLSCredentials(nodeID, nodeAddress)
	conn, err := grpc.DialContext(ctx, nodeAddress+":50051",
		grpc.WithTransportCredentials(creds),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to agent at %s: %w", nodeAddress, err)
	}

	client = pb.NewNodeAgentClient(conn)
	c.clients[nodeID] = client
	c.connections[nodeID] = conn

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
	NetworkRepo  *repository.NetworkRepository
	IPAMRepo     *repository.IPAMRepository
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

// agentAuthContext attaches the node's auth token and id to the outgoing gRPC
// context so the agent's auth interceptor accepts the call. Without this every
// queued VM command is rejected as Unauthenticated.
func agentAuthContext(ctx context.Context, node *models.Node) context.Context {
	if node == nil || node.Token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+node.Token, "x-node-id", node.ID)
}

// markBackupFailed records a failed backup status (best-effort).
func markBackupFailed(ctx context.Context, backupID, msg string) {
	if backupID == "" || globalWorkerContext == nil {
		return
	}
	_ = globalWorkerContext.DB.WithContext(ctx).
		Model(&models.Backup{}).
		Where("id = ?", backupID).
		Updates(map[string]interface{}{
			"status":        models.BackupStatusFailed,
			"error_message": msg,
		}).Error
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
	client, err := w.agentClient.GetClient(node.ID, node.IPAddress)
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

	// Handle network config operations separately (use ApplyNetworkConfig RPC)
	if job.Args.Operation == VMOpConfigureNetwork {
		return w.handleConfigureNetwork(ctx, client, node, vm, job)
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
	case VMOpRebuild:
		command = pb.VMCommandType_VM_COMMAND_TYPE_REBUILD
	case VMOpResize:
		command = pb.VMCommandType_VM_COMMAND_TYPE_RESIZE
	case VMOpResetPassword:
		command = pb.VMCommandType_VM_COMMAND_TYPE_RESET_PASSWORD
	case VMOpAttachISO:
		command = pb.VMCommandType_VM_COMMAND_TYPE_ATTACH_ISO
	case VMOpDetachISO:
		command = pb.VMCommandType_VM_COMMAND_TYPE_DETACH_ISO
	default:
		return fmt.Errorf("unsupported operation: %s", job.Args.Operation)
	}

	// Build VM config from params
	var vmConfig *pb.VMConfig
	needsConfig := job.Args.Operation == VMOpCreate || job.Args.Operation == VMOpRebuild ||
		job.Args.Operation == VMOpResize || job.Args.Operation == VMOpResetPassword ||
		job.Args.Operation == VMOpAttachISO
	if len(job.Args.Params) > 0 && needsConfig {
		var params struct {
			Hostname      string            `json:"hostname"`
			ImagePath     string            `json:"image_path"`
			RootPassword  string            `json:"root_password"`
			VNCPort       int               `json:"vnc_port"`
			VNCPassword   string            `json:"vnc_password"`
			IPAddress     string            `json:"ip_address"`
			Gateway       string            `json:"gateway"`
			Netmask       int               `json:"netmask"`
			Bridge        string            `json:"bridge"`
			VLANID        int               `json:"vlan_id"`
			BandwidthMbps int               `json:"bandwidth_mbps"`
			MACAddress    string            `json:"mac_address"`
			CPUModel      string            `json:"cpu_model"`
			SSHPublicKey  string            `json:"ssh_public_key"`
			UserData      string            `json:"user_data"`
			Metadata      map[string]string `json:"metadata"`
		}

		if err := json.Unmarshal(job.Args.Params, &params); err != nil {
			w.logger.ErrorContext(ctx, "failed to parse VM params",
				"error", err,
			)
		} else {
			metadata := params.Metadata
			if metadata == nil {
				metadata = map[string]string{}
			}
			// The proto VMConfig has no dedicated VNC-port / VLAN fields, so carry
			// them to the agent through the metadata map.
			metadata["vnc_port"] = strconv.Itoa(params.VNCPort)
			metadata["vlan_id"] = strconv.Itoa(params.VLANID)
			if params.Hostname != "" {
				metadata["hostname"] = params.Hostname
			}
			// CPU model (empty → the agent defaults to a portable, migratable model).
			if params.CPUModel != "" {
				metadata["cpu_model"] = params.CPUModel
			}

			iopsLimit := int32(0)
			if vm.Resources.IOPS != nil {
				iopsLimit = int32(*vm.Resources.IOPS)
			}
			swapMb := int64(0)
			if vm.Resources.Swap != nil {
				swapMb = int64(*vm.Resources.Swap)
			}

			vmConfig = &pb.VMConfig{
				Resources: &pb.VMResources{
					Vcpus:                int32(vm.Resources.CPU),
					MemoryMb:             int64(vm.Resources.RAM),
					DiskGb:               int64(vm.Resources.Disk),
					SwapMb:               swapMb,
					IopsLimit:            iopsLimit,
					NetworkBandwidthMbps: int32(params.BandwidthMbps),
				},
				ImageId:      params.ImagePath,
				SshPublicKey: params.SSHPublicKey,
				UserData:     params.UserData,
				Metadata:     metadata,
				VncEnabled:   true,
				VncPassword:  params.VNCPassword,
				RootPassword: params.RootPassword,
				NetworkConfig: &pb.VMNetworkConfig{
					Interfaces: []*pb.NetworkInterface{
						{
							Name:       "eth0",
							Type:       pb.NetworkInterfaceType_NETWORK_INTERFACE_TYPE_BRIDGE,
							BridgeName: params.Bridge,
							MacAddress: params.MACAddress,
							IpAddress:  params.IPAddress,
							Netmask:    int32(params.Netmask),
							Gateway:    params.Gateway,
							UseDhcp:    params.IPAddress == "",
						},
					},
					BandwidthLimits: &pb.BandwidthLimit{
						IngressRateMbps: int32(params.BandwidthMbps),
						EgressRateMbps:  int32(params.BandwidthMbps),
					},
				},
			}
		}
	}

	// For START, carry just the pool's current bridge so the agent can self-heal
	// a stale NIC <source bridge> before booting. We deliberately don't build a
	// full config — starting an already-defined domain needs no image/resources.
	if job.Args.Operation == VMOpStart && len(job.Args.Params) > 0 {
		var p struct {
			Bridge string `json:"bridge"`
		}
		if err := json.Unmarshal(job.Args.Params, &p); err == nil && p.Bridge != "" {
			vmConfig = &pb.VMConfig{
				NetworkConfig: &pb.VMNetworkConfig{
					Interfaces: []*pb.NetworkInterface{{
						Name:       "eth0",
						Type:       pb.NetworkInterfaceType_NETWORK_INTERFACE_TYPE_BRIDGE,
						BridgeName: p.Bridge,
					}},
				},
			}
		}
	}

	// Track a delete as a multi-step operation so the UI can show progress and
	// whether it actually finished (delete = destroy on host → release IP/network
	// → remove records). Best-effort: never blocks the real work.
	var deleteOpID string
	var opDB *gorm.DB
	if globalWorkerContext != nil {
		opDB = globalWorkerContext.DB
	}
	if job.Args.Operation == VMOpDelete {
		deleteOpID = startVMOperation(ctx, opDB, vm.ID, "delete", "Destroying VM & disk on host", 3)
	}

	// Execute VM command via gRPC
	req := &pb.VMCommandRequest{
		VmId:           vm.ID,
		Command:        command,
		Config:         vmConfig,
		TimeoutSeconds: 300, // 5 minutes
	}

	// Attach the node's auth token + id; the agent's auth interceptor requires a
	// Bearer token over TLS, otherwise the call is rejected as Unauthenticated.
	resp, err := client.ExecuteVMCommand(agentAuthContext(ctx, node), req)

	// Idempotent delete: if the domain is already gone on the node, that IS the
	// desired end state of a delete — treat it as success and proceed to release
	// the IP/network and remove the DB row, so orphaned records can be cleaned up
	// instead of failing forever with "domain not found".
	if job.Args.Operation == VMOpDelete {
		reason := ""
		if err != nil {
			reason = err.Error()
		} else if resp != nil && !resp.Success {
			reason = resp.Error.GetMessage() + " " + resp.GetMessage()
		}
		if reason != "" && isDomainNotFound(reason) {
			w.logger.WarnContext(ctx, "delete: domain already gone, cleaning up records", "vm_id", vm.ID)
			err = nil
			resp = &pb.VMCommandResponse{Success: true}
		}
	}

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
		failVMOperation(ctx, opDB, deleteOpID, err.Error())

		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return river.JobCancel(fmt.Errorf("non-retryable error executing VM command: %w", err))
	}

	if !resp.Success {
		// The agent reports the reason in either the structured Error or the
		// top-level Message; prefer whichever is populated so the real cause
		// surfaces instead of an empty "VM command failed:".
		detail := resp.Error.GetMessage()
		if detail == "" {
			detail = resp.GetMessage()
		}
		if detail == "" {
			detail = "agent reported failure without a message"
		}
		w.logger.ErrorContext(ctx, "VM command failed",
			"vm_id", vm.ID,
			"error_code", resp.Error.GetCode(),
			"error_message", detail,
		)

		// Update VM status to error
		vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusError)
		failVMOperation(ctx, opDB, deleteOpID, detail)

		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("VM command failed: %s", detail)
	}

	// Update VM status based on operation
	var newStatus models.VMStatus
	switch job.Args.Operation {
	case VMOpCreate:
		// The agent boots the VM as part of create (and announces its IP via
		// gratuitous ARP), so a freshly provisioned VM comes up running — as
		// VirtFusion/Virtualizor do — instead of sitting 'stopped'. If the agent's
		// start actually failed, the periodic status reconciler corrects this to
		// 'stopped' within a minute.
		newStatus = models.VMStatusRunning
	case VMOpStart:
		newStatus = models.VMStatusRunning
	case VMOpStop:
		newStatus = models.VMStatusStopped
	case VMOpRestart:
		newStatus = models.VMStatusRunning
	case VMOpDelete:
		// The agent confirmed the domain is destroyed. Now (and only now) it is
		// safe to release the VM's IP/network allocation and remove its DB row.
		// Doing this before agent confirmation risked handing a still-live IP to
		// the next VM. On cleanup failure we return an error so the job retries
		// rather than leaving the IP leaked and the row orphaned.
		stepVMOperation(ctx, opDB, deleteOpID, 2, "Releasing IP & network")
		if err := w.cleanupDeletedVM(ctx, vm.ID); err != nil {
			w.logger.ErrorContext(ctx, "failed to clean up deleted VM records",
				"vm_id", vm.ID, "error", err)
			failVMOperation(ctx, opDB, deleteOpID, err.Error())
			if w.metrics != nil {
				w.metrics.RecordJobFailed()
			}
			return fmt.Errorf("failed to clean up deleted VM records: %w", err)
		}
		completeVMOperation(ctx, opDB, deleteOpID, "VM deleted")
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

// cleanupDeletedVM releases a destroyed VM's IP/network allocation and removes
// its database row. It runs in a single transaction so the resources are freed
// atomically after the agent confirms the hypervisor domain is gone.
func (w *VMOperationWorker) cleanupDeletedVM(ctx context.Context, vmID string) error {
	if globalWorkerContext == nil {
		return fmt.Errorf("worker context not initialized")
	}
	db := globalWorkerContext.DB
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if ipam := globalWorkerContext.IPAMRepo; ipam != nil {
			if err := ipam.WithDB(tx).ReleaseAddressesByVMID(ctx, vmID); err != nil {
				return fmt.Errorf("release IPs: %w", err)
			}
		}
		if net := globalWorkerContext.NetworkRepo; net != nil {
			if err := net.WithDB(tx).DeleteByVMID(ctx, vmID); err != nil {
				return fmt.Errorf("delete network rows: %w", err)
			}
		}
		// Release pending disk-quota reservations for this VM so a confirmed worker
		// deletion also clears orphan capacity that would otherwise over-count.
		if res := repository.NewDiskQuotaReservationRepository(db); res != nil {
			if err := res.WithDB(tx).DeleteByVMIDTx(ctx, tx, vmID); err != nil {
				return fmt.Errorf("release disk reservations: %w", err)
			}
		}
		if err := globalWorkerContext.VMRepo.WithDB(tx).Delete(ctx, vmID); err != nil {
			return fmt.Errorf("delete VM row: %w", err)
		}
		return nil
	})
}

// handleConfigureNetwork dispatches a ConfigureNetwork operation to the agent
// via the ApplyNetworkConfig gRPC method.
func (w *VMOperationWorker) handleConfigureNetwork(ctx context.Context, client pb.NodeAgentClient, node *models.Node, vm *models.VM, job *river.Job[VMOperationJob]) error {
	var params struct {
		IPAddress      string `json:"ip_address"`
		BandwidthLimit int64  `json:"bandwidth_limit"`
		VLANID         *int   `json:"vlan_id,omitempty"`
		AntiSpoofing   bool   `json:"anti_spoofing"`
		FirewallRules  []struct {
			Direction string `json:"direction"`
			Action    string `json:"action"`
			Protocol  string `json:"protocol"`
			SourceIP  string `json:"source_ip"`
			PortRange string `json:"port_range"`
			Priority  int    `json:"priority"`
		} `json:"firewall_rules,omitempty"`
		PortForwards []struct {
			ExternalPort int    `json:"external_port"`
			InternalPort int    `json:"internal_port"`
			Protocol     string `json:"protocol"`
			SourceIP     string `json:"source_ip"`
		} `json:"port_forwards,omitempty"`
	}

	if err := json.Unmarshal(job.Args.Params, &params); err != nil {
		return fmt.Errorf("failed to parse network config params: %w", err)
	}

	// Build proto firewall rules
	var fwRules []*pb.FirewallRule
	for _, r := range params.FirewallRules {
		fwRules = append(fwRules, &pb.FirewallRule{
			Direction:  modelDirectionToProto(r.Direction),
			Action:     modelActionToProto(r.Action),
			Protocol:   r.Protocol,
			SourceCidr: r.SourceIP,
			DestPort:   r.PortRange,
			Priority:   int32(r.Priority),
		})
	}

	// Build network interface with anti-spoofing flag + VLAN tag.
	var vlanID int32
	if params.VLANID != nil {
		vlanID = int32(*params.VLANID)
	}
	iface := &pb.NetworkInterface{
		Name:         "eth0",
		Type:         pb.NetworkInterfaceType_NETWORK_INTERFACE_TYPE_BRIDGE,
		IpAddress:    params.IPAddress,
		AntiSpoofing: params.AntiSpoofing,
		VlanId:       vlanID,
	}

	var pfRules []*pb.PortForward
	for _, pf := range params.PortForwards {
		pfRules = append(pfRules, &pb.PortForward{
			ExternalPort: int32(pf.ExternalPort),
			InternalPort: int32(pf.InternalPort),
			Protocol:     pf.Protocol,
			SourceCidr:   pf.SourceIP,
		})
	}

	req := &pb.NetworkConfigRequest{
		VmId: vm.ID,
		Config: &pb.VMNetworkConfig{
			Interfaces:    []*pb.NetworkInterface{iface},
			FirewallRules: fwRules,
			PortForwards:  pfRules,
			BandwidthLimits: &pb.BandwidthLimit{
				IngressRateMbps: int32(params.BandwidthLimit),
				EgressRateMbps:  int32(params.BandwidthLimit),
			},
		},
		ReplaceAll: true,
	}

	resp, err := client.ApplyNetworkConfig(agentAuthContext(ctx, node), req)
	if err != nil {
		w.logger.ErrorContext(ctx, "failed to apply network config",
			"error", err,
			"vm_id", vm.ID,
		)
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("failed to apply network config: %w", err)
	}

	if !resp.Success {
		errMsg := "agent reported failure"
		if resp.Error != nil {
			errMsg = resp.Error.Message
		}
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		return fmt.Errorf("network config failed: %s", errMsg)
	}

	w.logger.InfoContext(ctx, "network config applied successfully",
		"vm_id", vm.ID,
		"anti_spoofing", params.AntiSpoofing,
	)

	if w.metrics != nil {
		w.metrics.RecordJobProcessed("configure_network", time.Since(time.Now()))
	}

	return nil
}

// modelDirectionToProto converts model direction string to proto enum
func modelDirectionToProto(d string) pb.FirewallDirection {
	switch d {
	case "inbound":
		return pb.FirewallDirection_FIREWALL_DIRECTION_INBOUND
	case "outbound":
		return pb.FirewallDirection_FIREWALL_DIRECTION_OUTBOUND
	default:
		return pb.FirewallDirection_FIREWALL_DIRECTION_INBOUND
	}
}

// modelActionToProto converts model action string to proto enum
func modelActionToProto(a string) pb.FirewallAction {
	switch a {
	case "allow":
		return pb.FirewallAction_FIREWALL_ACTION_ALLOW
	case "deny":
		return pb.FirewallAction_FIREWALL_ACTION_DENY
	case "reject":
		return pb.FirewallAction_FIREWALL_ACTION_REJECT
	default:
		return pb.FirewallAction_FIREWALL_ACTION_DENY
	}
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

	// Drive the node's agent to actually download + checksum the template image
	// into its local cache (idempotent). This replaces the previous simulated sleep.
	if globalWorkerContext == nil || globalWorkerContext.AgentClient == nil {
		w.updateSyncStatus(ctx, template.ID, nodeID, "error", "agent client unavailable")
		return fmt.Errorf("agent client unavailable")
	}
	client, err := globalWorkerContext.AgentClient.GetClient(node.ID, node.IPAddress)
	if err != nil {
		w.updateSyncStatus(ctx, template.ID, nodeID, "error", err.Error())
		return fmt.Errorf("failed to connect to node agent: %w", err)
	}

	// Template images can be large; allow a generous timeout for the download.
	syncCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	resp, err := client.SyncTemplate(agentAuthContext(syncCtx, node), &pb.SyncTemplateRequest{
		ImageUrl: template.ImagePath,
	})
	if err != nil {
		w.updateSyncStatus(ctx, template.ID, nodeID, "error", err.Error())
		return fmt.Errorf("template sync RPC failed: %w", err)
	}
	if !resp.Success {
		msg := "unknown error"
		if resp.Error != nil {
			msg = resp.Error.Message
		}
		w.updateSyncStatus(ctx, template.ID, nodeID, "error", msg)
		return fmt.Errorf("template sync failed on node %s: %s", nodeID, msg)
	}

	w.logger.InfoContext(ctx, "template cached on node",
		"template_id", template.ID,
		"node_id", nodeID,
		"local_path", resp.LocalPath,
		"size_bytes", resp.SizeBytes,
		"checksum", resp.Checksum,
	)
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

// RestoreWorker restores a VM disk from a backup via the agent RestoreDisk RPC.
type RestoreWorker struct {
	river.WorkerDefaults[RestoreJob]
	logger      *slog.Logger
	agentClient *AgentClient
	metrics     *MetricsCollector
}

func NewRestoreWorker(logger *slog.Logger) *RestoreWorker {
	w := &RestoreWorker{logger: logger}
	if globalWorkerContext != nil {
		w.agentClient = globalWorkerContext.AgentClient
		w.metrics = globalWorkerContext.Metrics
	}
	return w
}

func (w *RestoreWorker) Work(ctx context.Context, job *river.Job[RestoreJob]) error {
	if globalWorkerContext == nil {
		return fmt.Errorf("worker context not initialized")
	}
	vm, err := globalWorkerContext.VMRepo.GetByID(ctx, job.Args.VMID)
	if err != nil {
		return fmt.Errorf("failed to get VM: %w", err)
	}
	node, err := globalWorkerContext.NodeRepo.GetByID(ctx, job.Args.NodeID)
	if err != nil {
		return fmt.Errorf("failed to get node: %w", err)
	}
	if node.Status != models.NodeStatusActive {
		return fmt.Errorf("node %s is not active (status: %s)", node.ID, node.Status)
	}
	client, err := w.agentClient.GetClient(node.ID, node.IPAddress)
	if err != nil {
		return err
	}

	resp, err := client.RestoreDisk(agentAuthContext(ctx, node), &pb.RestoreDiskRequest{
		VmId:             vm.ID,
		SourceKey:        job.Args.SourceKey,
		ExpectedChecksum: job.Args.Checksum,
	})
	if err != nil {
		if IsRetryableError(err) {
			return fmt.Errorf("retryable error restoring disk: %w", err)
		}
		return river.JobCancel(fmt.Errorf("non-retryable error restoring disk: %w", err))
	}
	if !resp.Success {
		msg := "disk restore failed"
		if resp.Error != nil {
			msg = resp.Error.GetMessage()
		}
		return river.JobCancel(fmt.Errorf("disk restore failed: %s", msg))
	}

	w.logger.InfoContext(ctx, "restore completed",
		"vm_id", vm.ID, "backup_id", job.Args.BackupID, "bytes", resp.BytesRestored)
	return nil
}

// Timeout overrides River's 1-minute default: a disk export + object-storage
// upload of a multi-GB image routinely exceeds a minute, and letting the job ctx
// cancel mid-transfer both fails the backup and orphans the qemu-img process
// (which then holds the disk lock and blocks retries).
func (w *RestoreWorker) Timeout(*river.Job[RestoreJob]) time.Duration { return 60 * time.Minute }

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

// Timeout overrides River's 1-minute default for the same reason as
// RestoreWorker: a disk export + upload of a multi-GB image exceeds a minute and
// must not be cancelled mid-transfer (which orphans qemu-img and locks the disk).
func (w *BackupWorker) Timeout(*river.Job[BackupJob]) time.Duration { return 60 * time.Minute }

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
	client, err := globalWorkerContext.AgentClient.GetClient(node.ID, node.IPAddress)
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

	// Full-disk backup (the only type real flows enqueue) exports the qcow2
	// directly via BackupDisk below. We deliberately DO NOT take a libvirt
	// snapshot first: imported Virtualizor domains have no snapshot metadata and
	// CreateSnapshot hangs/errors on them, and the retryable-error path here would
	// then loop through River's default 25 attempts while the backup row sits stuck
	// at in_progress/0 B forever. A direct qcow2 export is crash-consistent and is
	// exactly what the working RestoreDisk path expects.
	// ponytail: crash-consistent copy of a running disk; add fs-quiesce only if a
	// restored image ever shows dirty-fs corruption in the field.
	var snapshotName string
	if job.Args.BackupType == BackupTypeSnapshot {
		req := &pb.SnapshotRequest{
			VmId:        vm.ID,
			Operation:   pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_CREATE,
			Name:        fmt.Sprintf("backup_%s_%d", job.Args.BackupType, time.Now().Unix()),
			Description: fmt.Sprintf("Backup job %s for VM %s", jobID, vm.ID),
			Quiesce:     true,
		}

		resp, err := client.CreateSnapshot(agentAuthContext(ctx, node), req)
		if err != nil {
			w.logger.ErrorContext(ctx, "failed to create backup snapshot",
				"error", err,
				"vm_id", vm.ID,
			)

			// Only keep retrying while there are attempts left; on the final
			// attempt mark the row failed so it never lingers in_progress.
			if IsRetryableError(err) && job.Attempt < job.MaxAttempts {
				if w.metrics != nil {
					w.metrics.RecordJobRetried()
				}
				return fmt.Errorf("retryable error creating snapshot: %w", err)
			}

			if w.metrics != nil {
				w.metrics.RecordJobFailed()
			}
			markBackupFailed(ctx, job.Args.BackupID, err.Error())
			return river.JobCancel(fmt.Errorf("error creating snapshot: %w", err))
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
			markBackupFailed(ctx, job.Args.BackupID, resp.Error.GetMessage())
			return fmt.Errorf("snapshot creation failed: %s", resp.Error.GetMessage())
		}

		// Reap the snapshot this attempt created, on every return path. Without
		// this, each River retry leaks a host snapshot (fresh name per attempt)
		// and even successful backups leave it behind. The agent already
		// implements SNAPSHOT_OPERATION_TYPE_DELETE, so no agent redeploy needed.
		snapshotName = resp.Snapshot.GetSnapshotId()
		defer func() {
			reapCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, delErr := client.CreateSnapshot(agentAuthContext(reapCtx, node), &pb.SnapshotRequest{
				VmId:       vm.ID,
				Operation:  pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_DELETE,
				SnapshotId: snapshotName,
			}); delErr != nil {
				w.logger.WarnContext(ctx, "failed to reap backup snapshot", "vm_id", vm.ID, "snapshot", snapshotName, "error", delErr)
			}
		}()
	}

	// Export the actual disk image and upload it to object storage VIA THE AGENT
	// (the agent has the disk file and storage credentials). This replaces the
	// old manifest-only archive, which contained no disk data.
	backupResp, err := client.BackupDisk(agentAuthContext(ctx, node), &pb.BackupDiskRequest{
		VmId:           vm.ID,
		DestinationKey: job.Args.Destination,
		Compress:       true,
	})
	if err != nil {
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		markBackupFailed(ctx, job.Args.BackupID, err.Error())
		return fmt.Errorf("failed to back up disk: %w", err)
	}
	if !backupResp.Success {
		msg := "disk backup failed"
		if backupResp.Error != nil {
			msg = backupResp.Error.GetMessage()
		}
		if w.metrics != nil {
			w.metrics.RecordJobFailed()
		}
		markBackupFailed(ctx, job.Args.BackupID, msg)
		return fmt.Errorf("disk backup failed: %s", msg)
	}

	if job.Args.BackupID != "" {
		completedAt := time.Now()
		if err := globalWorkerContext.DB.WithContext(ctx).
			Model(&models.Backup{}).
			Where("id = ?", job.Args.BackupID).
			Updates(map[string]interface{}{
				"status":       models.BackupStatusCompleted,
				"size":         backupResp.SizeBytes,
				"checksum":     backupResp.Checksum,
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
		"snapshot_id", snapshotName,
		"backup_type", job.Args.BackupType,
		"backup_id", job.Args.BackupID,
		"backup_size", backupResp.SizeBytes,
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

	// Endpoint selection must NOT silently fall through to a lower-priority var
	// when a higher-priority one is set but whitespace-contaminated. Pass the raw
	// value (no trim) so shared normalization rejects whitespace-only/padded
	// endpoints fail-closed instead of reinterpreting them as the SDK default or
	// as an empty slot that drops to S3_ENDPOINT.
	endpoint := firstSetRaw(os.Getenv("STORAGE_ENDPOINT"), os.Getenv("S3_ENDPOINT"))

	// Path-style addressing is the historical production default (MinIO). Only
	// explicitly opt out when the operator sets the flag, to avoid silently
	// breaking existing MinIO callers that omit it.
	usePathStyle := true
	if v, err := boolEnv("STORAGE_USE_PATH_STYLE", "S3_USE_PATH_STYLE"); err != nil {
		return nil, fmt.Errorf("invalid STORAGE_USE_PATH_STYLE/S3_USE_PATH_STYLE: %w", err)
	} else if envSet("STORAGE_USE_PATH_STYLE", "S3_USE_PATH_STYLE") {
		usePathStyle = v
	}

	forceHTTP := false
	if v, err := boolEnv("STORAGE_FORCE_HTTP", "S3_FORCE_HTTP"); err != nil {
		return nil, fmt.Errorf("invalid STORAGE_FORCE_HTTP/S3_FORCE_HTTP: %w", err)
	} else if envSet("STORAGE_FORCE_HTTP", "S3_FORCE_HTTP") {
		forceHTTP = v
	}

	config := &sharedstorage.Config{
		Endpoint:     endpoint,
		AccessKey:    firstNonEmpty(os.Getenv("STORAGE_ACCESS_KEY"), os.Getenv("S3_ACCESS_KEY")),
		SecretKey:    firstNonEmpty(os.Getenv("STORAGE_SECRET_KEY"), os.Getenv("S3_SECRET_KEY")),
		Bucket:       firstNonEmpty(os.Getenv("STORAGE_BUCKET"), os.Getenv("S3_BUCKET")),
		Region:       firstNonEmpty(os.Getenv("STORAGE_REGION"), os.Getenv("S3_REGION"), "us-east-1"),
		Provider:     resolvedProvider,
		ForceHTTP:    forceHTTP,
		UsePathStyle: usePathStyle,
	}

	if config.AccessKey == "" || config.SecretKey == "" || config.Bucket == "" {
		return nil, fmt.Errorf("missing object storage configuration for backup upload")
	}

	return sharedstorage.NewClient(config)
}

// boolEnv reads a boolean storage flag, consulting the given env var names in
// order. Precedence is STORAGE_* before S3_* (first hit wins). A malformed
// explicit value fails closed (error) rather than being coerced.
func boolEnv(keys ...string) (bool, error) {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return strconv.ParseBool(strings.TrimSpace(v))
		}
	}
	return false, nil
}

// envSet reports whether any of the given env vars is set (non-empty). Used to
// distinguish "flag omitted" from "flag explicitly set to false".
func envSet(keys ...string) bool {
	for _, k := range keys {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// firstSetRaw returns the first non-empty (raw, untrimmed) value. Unlike
// firstNonEmpty it does NOT strip surrounding whitespace, so a whitespace-only
// or padded value is treated as "set" and passed through to shared normalization
// for consistent fail-closed handling of the endpoint.
func firstSetRaw(values ...string) string {
	for _, value := range values {
		if value != "" {
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
	client, err := globalWorkerContext.AgentClient.GetClient(node.ID, node.IPAddress)
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

	// Parse metadata if available - declare early so it's available in switch
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

	switch job.Args.Source {
	case ImportSourceVirtualizor:
		w.logger.InfoContext(ctx, "importing from Virtualizor",
			"source_id", job.Args.SourceID,
			"config_path", job.Args.ConfigPath,
			"disk_path", job.Args.DiskPath,
		)

		// 1. Parse Virtualizor XML config
		candidate, err := vmimport.ParseVirtualizorDomainXML(job.Args.ConfigPath)
		if err != nil {
			w.logger.ErrorContext(ctx, "failed to parse Virtualizor XML config",
				"error", err,
				"config_path", job.Args.ConfigPath,
			)
			if w.metrics != nil {
				w.metrics.RecordJobFailed()
			}
			return fmt.Errorf("failed to parse Virtualizor XML: %w", err)
		}

		w.logger.InfoContext(ctx, "parsed Virtualizor config",
			"vm_name", candidate.Name,
			"uuid", candidate.UUID,
			"cpu", candidate.CPU,
			"memory_mb", candidate.Memory,
			"disk_count", len(candidate.Disks),
			"network_count", len(candidate.Networks),
		)

		// 2. Extract VM metadata from parsed config
		metadata.Hostname = candidate.Name
		metadata.CPU = candidate.CPU
		metadata.RAM = candidate.Memory

		// Calculate total disk size from candidate disks
		totalDiskGB := int(candidate.GetTotalDiskSize())
		if totalDiskGB == 0 {
			totalDiskGB = 20 // Default if can't determine
		}
		metadata.Disk = totalDiskGB

		// 3. Import each disk image via agent gRPC
		for i, disk := range candidate.Disks {
			if disk.SourceFile == "" {
				continue
			}

			diskFormat := disk.GetDiskFormatWithFallback()
			if diskFormat == "" {
				diskFormat = "qcow2" // Default format
			}

			// Determine target path for the disk
			targetPath := fmt.Sprintf("/var/lib/libvirt/images/%s-disk%d.%s",
				candidate.UUID, i, diskFormat)

			// Determine action from metadata or default to copy
			action := "copy" // Default action

			w.logger.InfoContext(ctx, "importing disk via agent",
				"vm_id", job.Args.SourceID,
				"source_path", disk.SourceFile,
				"target_path", targetPath,
				"format", diskFormat,
				"action", action,
			)

			// Send disk import command to agent via gRPC
			req := &pb.DiskImportRequest{
				VmId:       job.Args.SourceID,
				SourcePath: disk.SourceFile,
				TargetPath: targetPath,
				Format:     diskFormat,
				Action:     action,
			}

			resp, err := client.ImportDisk(agentAuthContext(ctx, node), req)
			if err != nil {
				w.logger.ErrorContext(ctx, "failed to import disk via agent",
					"error", err,
					"source_path", disk.SourceFile,
				)
				if w.metrics != nil {
					w.metrics.RecordJobFailed()
				}
				return fmt.Errorf("failed to import disk %s: %w", disk.SourceFile, err)
			}

			if !resp.Success {
				w.logger.ErrorContext(ctx, "disk import failed",
					"error_message", resp.Error.GetMessage(),
					"source_path", disk.SourceFile,
				)
				if w.metrics != nil {
					w.metrics.RecordJobFailed()
				}
				return fmt.Errorf("disk import failed for %s: %s", disk.SourceFile, resp.Error.GetMessage())
			}

			w.logger.InfoContext(ctx, "disk imported successfully",
				"imported_path", resp.ImportedPath,
				"size_bytes", resp.SizeBytes,
			)
		}

		// 4. Update VM record with network configuration from parsed XML
		// Networks are extracted from the candidate and will be configured
		// when the VM is started

	case ImportSourceManual:
		w.logger.InfoContext(ctx, "performing manual import",
			"source_id", job.Args.SourceID,
		)
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
	client, err := globalWorkerContext.AgentClient.GetClient(node.ID, node.IPAddress)
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

	resp, err := client.ApplyNetworkConfig(agentAuthContext(ctx, node), req)
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
		return river.JobCancel(fmt.Errorf("non-retryable error applying network config: %w", err))
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
	client, err := w.agentClient.GetClient(node.ID, node.IPAddress)
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
		// The agent looks up the libvirt snapshot by SnapshotId, but libvirt
		// created it under the user-given Name — so send the Name (fall back to
		// the DB id only if Name is missing on an older job).
		snapshotID = job.Args.Name
		if snapshotID == "" {
			snapshotID = job.Args.SnapshotID
		}
	case SnapshotOpDelete:
		operation = pb.SnapshotOperationType_SNAPSHOT_OPERATION_TYPE_DELETE
		snapshotID = job.Args.Name
		if snapshotID == "" {
			snapshotID = job.Args.SnapshotID
		}
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

	resp, err := client.CreateSnapshot(agentAuthContext(ctx, node), req)
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
		return river.JobCancel(fmt.Errorf("non-retryable error executing snapshot command: %w", err))
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

	// Persist snapshot lifecycle state. The row is created 'pending' and nothing
	// else moves it — without this, every snapshot stays pending forever and
	// restore (which requires 'complete') can never run.
	if globalWorkerContext.DB != nil && job.Args.SnapshotID != "" {
		switch job.Args.Operation {
		case SnapshotOpCreate:
			if err := globalWorkerContext.DB.WithContext(ctx).
				Model(&models.Snapshot{}).Where("id = ?", job.Args.SnapshotID).
				Update("status", models.SnapshotStatusComplete).Error; err != nil {
				w.logger.ErrorContext(ctx, "failed to mark snapshot complete", "error", err, "snapshot_id", job.Args.SnapshotID)
			}
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
