// Package service provides business logic for import operations
package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	vmimport "github.com/maburvm/panel/internal/agent/import"
	panelclient "github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

var (
	// ErrImportFailed is returned when import operation fails
	ErrImportFailed = errors.New("import operation failed")
	// ErrVMAlreadyExists is returned when trying to import a VM with existing UUID
	ErrVMAlreadyExists = errors.New("VM with this UUID already exists")
	// ErrInvalidXMLPath is returned when XML path is invalid
	ErrInvalidXMLPath = errors.New("invalid XML configuration path")
	// ErrNoVMsFound is returned when no VMs are found to import
	ErrNoVMsFound = errors.New("no VMs found to import")
)

// Default paths for Virtualizor VM XML files
const (
	DefaultLibvirtQemuPath   = "/etc/libvirt/qemu/"
	DefaultLibvirtImagesPath = "/var/lib/libvirt/images/"
	DefaultVirtualizorVMPath = "/var/virtualizor/"
)

// ImportService handles VM import operations from external sources
// like Virtualizor
// API endpoint: POST /api/nodes/:id/import/virtualizor
type ImportService struct {
	db           *gorm.DB
	vmRepo       *repository.VMRepository
	nodeRepo     *repository.NodeRepository
	templateRepo *repository.TemplateRepository
	networkRepo  *repository.NetworkRepository
	ipamService  *IPAMService
	riverClient  *river.Client[pgx.Tx]
	logger       *slog.Logger
	grpcConns    map[string]*grpc.ClientConn
	connMutex    sync.RWMutex
}

// NewImportService creates a new ImportService instance
func NewImportService(
	db *gorm.DB,
	vmRepo *repository.VMRepository,
	nodeRepo *repository.NodeRepository,
	templateRepo *repository.TemplateRepository,
	networkRepo *repository.NetworkRepository,
	riverClient *river.Client[pgx.Tx],
	logger *slog.Logger,
) *ImportService {
	return &ImportService{
		db:           db,
		vmRepo:       vmRepo,
		nodeRepo:     nodeRepo,
		templateRepo: templateRepo,
		networkRepo:  networkRepo,
		ipamService:  NewIPAMService(db, repository.NewIPAMRepository(db)),
		riverClient:  riverClient,
		logger:       logger,
		grpcConns:    make(map[string]*grpc.ClientConn),
	}
}

// ImportVirtualizorRequest contains parameters for importing VMs from Virtualizor
// Scans node for Virtualizor VM XML files from common paths:
//   - /etc/libvirt/qemu/
//   - /var/lib/libvirt/images/
//   - /var/virtualizor/
//
// Allows custom path specification via CustomPath field
type ImportVirtualizorRequest struct {
	NodeID       string     `json:"node_id" validate:"required,uuid"`
	UserID       string     `json:"user_id" validate:"required,uuid"`        // Owner for imported VMs
	OSTemplateID string     `json:"os_template_id" validate:"required,uuid"` // Template to associate with VMs
	CustomPath   string     `json:"custom_path,omitempty"`                   // Optional: custom path to scan
	StoragePool  string     `json:"storage_pool,omitempty"`                  // Target storage pool for disk images
	DiskAction   DiskAction `json:"disk_action,omitempty"`                   // How to handle disk images
	VMUUIDs      []string   `json:"vm_uuids,omitempty"`                      // Optional: only import these VMs (empty = all)
}

// DiskAction represents how to handle disk images during import
type DiskAction string

const (
	// DiskActionSymlink creates symlinks to original disk images
	DiskActionSymlink DiskAction = "symlink"
	// DiskActionCopy copies disk images to new location
	DiskActionCopy DiskAction = "copy"
	// DiskActionMove moves disk images to new location
	DiskActionMove DiskAction = "move"
)

// ImportResult represents the result of importing a single VM
type ImportResult struct {
	VMID     string `json:"vm_id,omitempty"`
	SourceID string `json:"source_id"` // Original VM UUID
	Name     string `json:"name"`
	Status   string `json:"status"`            // "imported", "skipped", "error"
	Message  string `json:"message,omitempty"` // Error message or skip reason
	JobID    int64  `json:"job_id,omitempty"`  // Enqueued job ID
}

// ImportVirtualizorResponse contains the complete import report
type ImportVirtualizorResponse struct {
	NodeID       string         `json:"node_id"`
	TotalFound   int            `json:"total_found"`
	SuccessCount int            `json:"success_count"`
	SkippedCount int            `json:"skipped_count"`
	ErrorCount   int            `json:"error_count"`
	Results      []ImportResult `json:"results"`
	CompletedAt  time.Time      `json:"completed_at"`
	DurationMs   int64          `json:"duration_ms"`
}

// DiscoveredVM represents a VM discovered during scanning
type DiscoveredVM struct {
	XMLPath   string                    `json:"xml_path"`
	Candidate *vmimport.ImportCandidate `json:"candidate"`
	Valid     bool                      `json:"valid"`
	Error     string                    `json:"error,omitempty"`
}

// ImportVirtualizor scans a node for Virtualizor VMs and imports them
// - Scans for XML files in common Virtualizor paths
// - Parses each XML to extract VM metadata (Task 28 integration)
// - Checks for conflicts (UUID already exists)
// - Creates VM records with source_migration = "virtualizor"
// - Re-maps disk paths to new storage location
// - Enqueues ImportJob for each VM
// Returns import report with success count, skipped count, and errors
func (s *ImportService) ImportVirtualizor(ctx context.Context, req *ImportVirtualizorRequest) (*ImportVirtualizorResponse, error) {
	startTime := time.Now()

	// Validate node exists and is active
	node, err := s.nodeRepo.GetByID(ctx, req.NodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	if node.Status != models.NodeStatusActive {
		return nil, fmt.Errorf("node is not active (status: %s)", node.Status)
	}

	// Validate OS template exists
	_, err = s.templateRepo.GetByID(ctx, req.OSTemplateID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTemplateNotFound
		}
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	// Scan the target node via the agent (libvirt on the node) — the only place
	// the source VMs actually live. (There is no panel-side filesystem fallback:
	// the panel host isn't the node, so scanning its filesystem finds nothing.)
	discoveredVMs, err := s.ListImportableVMs(ctx, req.NodeID, req.CustomPath)
	if err != nil {
		return nil, fmt.Errorf("failed to scan node %s for importable VMs: %w", req.NodeID, err)
	}

	if len(discoveredVMs) == 0 {
		return nil, ErrNoVMsFound
	}

	// Filter by VM UUIDs if specified
	if len(req.VMUUIDs) > 0 {
		uuidSet := make(map[string]bool, len(req.VMUUIDs))
		for _, u := range req.VMUUIDs {
			uuidSet[u] = true
		}
		var filtered []DiscoveredVM
		for _, vm := range discoveredVMs {
			if vm.Candidate != nil && uuidSet[vm.Candidate.UUID] {
				filtered = append(filtered, vm)
			}
		}
		discoveredVMs = filtered
		if len(discoveredVMs) == 0 {
			return nil, ErrNoVMsFound
		}
	}

	s.logger.InfoContext(ctx, "discovered Virtualizor VMs",
		"node_id", req.NodeID,
		"count", len(discoveredVMs),
	)

	// Process each discovered VM
	response := &ImportVirtualizorResponse{
		NodeID:      req.NodeID,
		TotalFound:  len(discoveredVMs),
		Results:     make([]ImportResult, 0, len(discoveredVMs)),
		CompletedAt: time.Now(),
	}

	for _, discovered := range discoveredVMs {
		result := s.processDiscoveredVM(ctx, req, discovered)
		response.Results = append(response.Results, result)

		switch result.Status {
		case "imported":
			response.SuccessCount++
		case "skipped":
			response.SkippedCount++
		case "error":
			response.ErrorCount++
		}
	}

	response.DurationMs = time.Since(startTime).Milliseconds()

	s.logger.InfoContext(ctx, "Virtualizor import completed",
		"node_id", req.NodeID,
		"total", response.TotalFound,
		"success", response.SuccessCount,
		"skipped", response.SkippedCount,
		"errors", response.ErrorCount,
		"duration_ms", response.DurationMs,
	)

	return response, nil
}

// processDiscoveredVM processes a single discovered VM
// - Checks for UUID conflicts
// - Creates VM record with source_migration = "virtualizor"
// - Re-maps disk paths
// - Enqueues ImportJob
func (s *ImportService) assignImportedIPAM(ctx context.Context, vmID, nodeID, ip string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return s.ipamService.AssignImportedAddressInTx(ctx, tx, vmID, nodeID, ip)
	})
}

func (s *ImportService) processDiscoveredVM(ctx context.Context, req *ImportVirtualizorRequest, discovered DiscoveredVM) ImportResult {
	candidate := discovered.Candidate

	result := ImportResult{
		SourceID: candidate.UUID,
		Name:     candidate.Name,
	}

	// Check for UUID conflict. If the VM was already imported, still sync runtime status.
	existingVM, err := s.vmRepo.GetByID(ctx, candidate.UUID)
	if err == nil && existingVM != nil {
		result.Status = "skipped"
		result.Message = fmt.Sprintf("VM with UUID %s already exists (hostname: %s)", candidate.UUID, existingVM.Hostname)
		if newStatus, ok := mapImportedRuntimeStatus(candidate.Status); ok && existingVM.Status != newStatus &&
			existingVM.Status != models.VMStatusDeleting && existingVM.Status != models.VMStatusCreating {
			if updateErr := s.vmRepo.UpdateStatus(ctx, existingVM.ID, newStatus); updateErr != nil {
				result.Status = "error"
				result.Message = fmt.Sprintf("failed to sync existing VM status: %v", updateErr)
				return result
			}
			result.Message += fmt.Sprintf("; status updated to %s", newStatus)
		}
		return result
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		result.Status = "error"
		result.Message = fmt.Sprintf("failed to check existing VM UUID: %v", err)
		return result
	}

	// Determine hostname - prefer Virtualizor metadata hostname over libvirt domain name
	hostname := candidate.Name
	if candidate.Metadata != nil && candidate.Metadata.Hostname != "" {
		hostname = candidate.Metadata.Hostname
	}

	// Check for hostname conflict
	hostnameExists, err := s.vmRepo.HostnameExists(ctx, hostname)
	if err != nil {
		result.Status = "error"
		result.Message = fmt.Sprintf("failed to check hostname: %v", err)
		return result
	}
	if hostnameExists {
		result.Status = "skipped"
		result.Message = fmt.Sprintf("hostname %s already exists", hostname)
		return result
	}

	// Generate VNC credentials (8-char alnum — VNC-safe, see generateVNCPassword)
	vncPort := 5900 + int(time.Now().UnixNano()%100)
	vncPassword := generateVNCPassword()

	// Calculate disk size from discovered disks
	totalDiskGB := int(candidate.GetTotalDiskSize())
	if totalDiskGB == 0 {
		totalDiskGB = 20 // Default if can't determine
	}

	// Create VM record
	vm := &models.VM{
		ID:           candidate.UUID, // Use original UUID for tracking
		UserID:       req.UserID,
		NodeID:       req.NodeID,
		Hostname:     hostname,
		OSTemplateID: req.OSTemplateID,
		Resources: models.Resources{
			CPU:  candidate.CPU,
			RAM:  candidate.Memory,
			Disk: totalDiskGB,
		},
		Status:          defaultImportedRuntimeStatus(candidate.Status),
		SourceMigration: string(queue.ImportSourceVirtualizor),
		VNCPort:         &vncPort,
		VNCPassword:     vncPassword,
	}

	// Create VM in database
	if err := s.vmRepo.Create(ctx, vm); err != nil {
		result.Status = "error"
		result.Message = fmt.Sprintf("failed to create VM record: %v", err)
		return result
	}

	result.VMID = vm.ID

	// Create network records for discovered IPs
	for _, net := range candidate.Networks {
		if net.IPConfig != nil && net.IPConfig.Address != "" {
			// Skip private/internal IPs (Docker bridges, etc.)
			ip := net.IPConfig.Address
			if strings.HasPrefix(ip, "172.") || strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") || ip == "127.0.0.1" {
				continue
			}
			networkRecord := &models.Network{
				ID:        uuid.New().String(),
				VMID:      vm.ID,
				IPAddress: ip,
			}
			if err := s.networkRepo.Create(ctx, networkRecord); err != nil {
				s.logger.WarnContext(ctx, "failed to create network record during import",
					"vm_id", vm.ID,
					"ip_address", ip,
					"error", err,
				)
				continue
			}
			if err := s.assignImportedIPAM(ctx, vm.ID, req.NodeID, ip); err != nil {
				s.logger.WarnContext(ctx, "failed to record imported IP in IPAM",
					"vm_id", vm.ID,
					"ip_address", ip,
					"error", err,
				)
			}
		}
	}

	var diskMappings []DiskMapping
	var updatedXMLPath string

	// Only remap disk artifacts if we have a local XML path (filesystem-based import)
	// For gRPC-based imports, disks are already on the remote node
	if discovered.XMLPath != "" {
		diskMappings, updatedXMLPath, err = s.remapDiskArtifacts(discovered.XMLPath, candidate, req)
		if err != nil {
			_ = s.vmRepo.Delete(ctx, vm.ID)
			result.Status = "error"
			result.Message = fmt.Sprintf("failed remapping disk artifacts: %v", err)
			return result
		}
	}

	// Prepare metadata for the import job
	metadata := map[string]interface{}{
		"original_xml_path": discovered.XMLPath,
		"updated_xml_path":  updatedXMLPath,
		"disk_mappings":     diskMappings,
		"source_type":       "virtualizor",
		"source_migration":  "virtualizor",
		"original_name":     candidate.Name,
		"original_uuid":     candidate.UUID,
		"original_status":   candidate.Status,
		"original_metadata": candidate.Metadata,
		"networks":          candidate.Networks,
		"vnc_config": map[string]interface{}{
			"port":     vncPort,
			"password": vncPassword,
		},
	}

	// If candidate had VNC config, preserve the original port info
	if candidate.VNC != nil {
		metadata["original_vnc"] = map[string]interface{}{
			"port":      candidate.VNC.Port,
			"auto_port": candidate.VNC.AutoPort,
			"listen":    candidate.VNC.Listen,
		}
	}

	metadataJSON, _ := json.Marshal(metadata)

	// gRPC scan imports are already defined and running on the target node.
	// Importing them into MaburVM only needs the DB row; no disk/XML job is required.
	if discovered.XMLPath == "" {
		result.Status = "imported"
		result.Message = "VM imported from existing node state"
		s.logger.InfoContext(ctx, "VM imported without background job",
			"vm_id", vm.ID,
			"name", candidate.Name,
			"source_uuid", candidate.UUID,
			"source_status", candidate.Status,
		)
		_ = metadataJSON // keep metadata construction validated for future persistence
		return result
	}

	if s.riverClient == nil {
		result.Status = "imported"
		result.Message = "VM record imported; background import queue is disabled"
		s.logger.WarnContext(ctx, "import queue disabled; skipping background import job",
			"vm_id", vm.ID,
			"name", candidate.Name,
			"source_uuid", candidate.UUID,
		)
		return result
	}

	// Enqueue import job for filesystem-based imports.
	primaryDisk := candidate.GetPrimaryDisk()
	diskPath := ""
	if primaryDisk != nil {
		diskPath = primaryDisk.SourceFile
	}

	job := queue.ImportJob{
		Source:     queue.ImportSourceVirtualizor,
		SourceID:   candidate.UUID,
		NodeID:     req.NodeID,
		UserID:     req.UserID,
		ConfigPath: updatedXMLPath,
		DiskPath:   diskPath,
		Metadata:   metadataJSON,
	}

	jobResult, err := s.riverClient.Insert(ctx, job, nil)
	if err != nil {
		// Job enqueue failed, but VM record was created.
		result.Status = "error"
		result.Message = fmt.Sprintf("failed to enqueue import job: %v", err)
		return result
	}

	result.JobID = jobResult.Job.ID
	result.Status = "imported"

	s.logger.InfoContext(ctx, "VM import processed",
		"vm_id", vm.ID,
		"name", candidate.Name,
		"source_uuid", candidate.UUID,
		"job_id", jobResult.Job.ID,
	)

	return result
}

// remapDiskPaths prepares disk path mappings for the import job
// Re-maps disk image to new storage location based on DiskAction:
//   - symlink: Create symlinks to original disk images
//   - copy: Copy disk images to new location
//   - move: Move disk images to new location
func (s *ImportService) remapDiskPaths(candidate *vmimport.ImportCandidate, req *ImportVirtualizorRequest) []DiskMapping {
	var mappings []DiskMapping

	diskAction := req.DiskAction
	if diskAction == "" {
		diskAction = DiskActionSymlink // Default action
	}

	storagePool := s.resolveStoragePool(req)

	for i, disk := range candidate.Disks {
		diskFormat := disk.GetDiskFormatWithFallback()
		if diskFormat == "" {
			diskFormat = "qcow2"
		}

		newPath := filepath.Join(storagePool, fmt.Sprintf("%s-disk%d.%s",
			candidate.UUID,
			i,
			diskFormat,
		))

		mappings = append(mappings, DiskMapping{
			OriginalPath: disk.SourceFile,
			NewPath:      newPath,
			Format:       diskFormat,
			Device:       disk.Device,
			Bus:          disk.Bus,
			Action:       string(diskAction),
		})
	}

	return mappings
}

func (s *ImportService) remapDiskArtifacts(xmlPath string, candidate *vmimport.ImportCandidate, req *ImportVirtualizorRequest) ([]DiskMapping, string, error) {
	diskMappings := s.remapDiskPaths(candidate, req)
	for _, mapping := range diskMappings {
		if err := s.applyDiskMapping(mapping); err != nil {
			return nil, "", err
		}
	}

	xmlBytes, err := os.ReadFile(xmlPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read domain XML: %w", err)
	}

	updatedXML, err := s.UpdateDomainXML(xmlBytes, diskMappings)
	if err != nil {
		return nil, "", err
	}

	updatedXMLPath := filepath.Join(s.resolveStoragePool(req), fmt.Sprintf("%s-domain.xml", candidate.UUID))
	if err := os.MkdirAll(filepath.Dir(updatedXMLPath), 0755); err != nil {
		return nil, "", fmt.Errorf("failed to prepare XML target directory: %w", err)
	}

	if err := os.WriteFile(updatedXMLPath, updatedXML, 0644); err != nil {
		return nil, "", fmt.Errorf("failed writing updated XML: %w", err)
	}

	return diskMappings, updatedXMLPath, nil
}

func (s *ImportService) applyDiskMapping(mapping DiskMapping) error {
	if mapping.OriginalPath == "" || mapping.NewPath == "" {
		return fmt.Errorf("invalid disk mapping: original=%q new=%q", mapping.OriginalPath, mapping.NewPath)
	}

	if mapping.OriginalPath == mapping.NewPath {
		return nil
	}

	if _, err := os.Stat(mapping.OriginalPath); err != nil {
		return fmt.Errorf("failed to access source disk image: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(mapping.NewPath), 0755); err != nil {
		return fmt.Errorf("failed to create disk destination directory: %w", err)
	}

	if _, err := os.Stat(mapping.NewPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect destination disk path: %w", err)
	}

	switch mapping.Action {
	case string(DiskActionSymlink):
		if err := os.Symlink(mapping.OriginalPath, mapping.NewPath); err != nil {
			return fmt.Errorf("failed to create disk symlink: %w", err)
		}
	case string(DiskActionCopy):
		if err := copyFile(mapping.OriginalPath, mapping.NewPath); err != nil {
			return fmt.Errorf("failed to copy disk image: %w", err)
		}
	case string(DiskActionMove):
		if err := os.Rename(mapping.OriginalPath, mapping.NewPath); err != nil {
			return fmt.Errorf("failed to move disk image: %w", err)
		}
	default:
		return fmt.Errorf("unsupported disk mapping action %q", mapping.Action)
	}

	return nil
}

func copyFile(sourcePath, destinationPath string) error {
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destinationFile, err := os.Create(destinationPath)
	if err != nil {
		return err
	}
	defer destinationFile.Close()

	if _, err := io.Copy(destinationFile, sourceFile); err != nil {
		return err
	}

	return destinationFile.Sync()
}

func (s *ImportService) resolveStoragePool(req *ImportVirtualizorRequest) string {
	if req.StoragePool != "" {
		return req.StoragePool
	}

	return DefaultLibvirtImagesPath
}

// DiskMapping represents a disk path remapping
type DiskMapping struct {
	OriginalPath string `json:"original_path"`
	NewPath      string `json:"new_path"`
	Format       string `json:"format"`
	Device       string `json:"device"`
	Bus          string `json:"bus"`
	Action       string `json:"action"` // symlink, copy, move
}

// generateRandomPassword generates a random password
func (s *ImportService) generateRandomPassword(length int) string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	password := make([]byte, length)
	randomBytes := make([]byte, length)
	if _, err := rand.Read(randomBytes); err != nil {
		for i := range password {
			password[i] = charset[time.Now().UnixNano()%int64(len(charset))]
		}
		return string(password)
	}

	for i := range password {
		password[i] = charset[int(randomBytes[i])%len(charset)]
	}

	return string(password)
}

// UpdateDomainXML updates the domain XML with new paths
// This prepares the XML configuration with remapped disk paths
func (s *ImportService) UpdateDomainXML(originalXML []byte, mappings []DiskMapping) ([]byte, error) {
	var domain vmimport.LibvirtDomain
	if err := xml.Unmarshal(originalXML, &domain); err != nil {
		return nil, fmt.Errorf("failed to unmarshal XML: %w", err)
	}

	// Create mapping lookup
	pathMap := make(map[string]string)
	for _, m := range mappings {
		pathMap[m.OriginalPath] = m.NewPath
	}

	// Update disk sources
	for i := range domain.Devices.Disks {
		disk := &domain.Devices.Disks[i]
		if disk.Source != nil && disk.Source.File != "" {
			if newPath, ok := pathMap[disk.Source.File]; ok {
				disk.Source.File = newPath
			}
		}
	}

	// Add metadata to indicate this is an imported VM
	if domain.Metadata.Virtualizor == nil {
		domain.Metadata.Virtualizor = &vmimport.VirtualizorMetadataXML{}
	}

	// Marshal back to XML
	updatedXML, err := xml.MarshalIndent(domain, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal updated XML: %w", err)
	}

	return updatedXML, nil
}

// GetImportStatus retrieves the status of an import job from River
func (s *ImportService) GetImportStatus(ctx context.Context, jobID int64) (*ImportJobStatus, error) {
	job, err := s.riverClient.JobGet(ctx, jobID)
	if err != nil {
		return nil, fmt.Errorf("failed to get job status: %w", err)
	}

	status := &ImportJobStatus{
		JobID:       job.ID,
		State:       string(job.State),
		Attempt:     job.Attempt,
		MaxAttempts: job.MaxAttempts,
		CreatedAt:   job.CreatedAt,
		AttemptedAt: job.AttemptedAt,
	}

	if job.FinalizedAt != nil {
		status.FinalizedAt = job.FinalizedAt
	}

	// Decode job args to get import-specific info
	var importJob queue.ImportJob
	if err := json.Unmarshal(job.EncodedArgs, &importJob); err == nil {
		status.Source = string(importJob.Source)
		status.SourceID = importJob.SourceID
		status.NodeID = importJob.NodeID
	}

	// Map River job states to user-friendly status
	switch job.State {
	case "available", "scheduled", "retryable":
		status.Status = "pending"
	case "running":
		status.Status = "processing"
	case "completed":
		status.Status = "completed"
	case "discarded", "cancelled":
		status.Status = "failed"
	default:
		status.Status = string(job.State)
	}

	// Include errors if present
	if len(job.Errors) > 0 {
		lastErr := job.Errors[len(job.Errors)-1]
		status.Error = lastErr.Error
	}

	return status, nil
}

// ImportJobStatus represents the status of an import job
type ImportJobStatus struct {
	JobID       int64      `json:"job_id"`
	Status      string     `json:"status"`    // pending, processing, completed, failed
	State       string     `json:"state"`     // Raw River state
	Source      string     `json:"source"`    // e.g., "virtualizor"
	SourceID    string     `json:"source_id"` // Original VM UUID
	NodeID      string     `json:"node_id"`
	Attempt     int        `json:"attempt"`
	MaxAttempts int        `json:"max_attempts"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	AttemptedAt *time.Time `json:"attempted_at,omitempty"`
	FinalizedAt *time.Time `json:"finalized_at,omitempty"`
}

// ListImportableVMs scans a node via agent gRPC and returns VMs that can be imported
func (s *ImportService) ListImportableVMs(ctx context.Context, nodeID string, customPath string) ([]DiscoveredVM, error) {
	node, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	// Connect to agent via gRPC
	client, err := s.getAgentClient(ctx, node.ID, node.IPAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to agent: %w", err)
	}

	// Create authenticated context with node token
	authCtx := s.createAuthContext(ctx, node.Token)

	// Call ScanVMs on agent
	resp, err := client.ScanVMs(authCtx, &pb.ScanVMsRequest{ScanPath: customPath})
	if err != nil {
		return nil, fmt.Errorf("failed to scan VMs on agent: %w", err)
	}

	if !resp.Success {
		errMsg := "scan failed"
		if resp.Error != nil {
			errMsg = resp.Error.Message
		}
		return nil, fmt.Errorf("agent scan failed: %s", errMsg)
	}

	// Convert agent response to DiscoveredVM list
	var discovered []DiscoveredVM
	for _, vm := range resp.Vms {
		candidate := &vmimport.ImportCandidate{
			Name:       vm.Name,
			UUID:       vm.Uuid,
			CPU:        int(vm.Cpu),
			Memory:     int(vm.MemoryMb),
			Status:     vm.Status,
			SourcePath: vm.XmlPath,
			SourceType: "virtualizor",
		}

		// Set hostname from metadata
		if vm.Hostname != "" && vm.Hostname != vm.Name {
			candidate.Metadata = &vmimport.VirtualizorMetadata{
				Hostname: vm.Hostname,
			}
		}

		// Set VNC info
		if vm.VncPort > 0 {
			candidate.VNC = &vmimport.VNCInfo{
				Port:     int(vm.VncPort),
				Password: vm.VncPassword,
			}
		}

		// Set disks
		for _, d := range vm.Disks {
			candidate.Disks = append(candidate.Disks, vmimport.DiskInfo{
				SourceFile: d.SourceFile,
				Format:     d.Format,
				Device:     d.Device,
				Bus:        d.Bus,
				TargetDev:  d.TargetDev,
				Size:       d.SizeBytes,
			})
		}

		// Set networks
		for _, n := range vm.Networks {
			netInfo := vmimport.NetworkInfo{
				MACAddress: n.MacAddress,
				Bridge:     n.Bridge,
				Model:      n.Model,
			}
			if n.IpAddress != "" {
				netInfo.IPConfig = &vmimport.IPInfo{
					Address: n.IpAddress,
					Family:  "ipv4",
				}
			}
			candidate.Networks = append(candidate.Networks, netInfo)
		}

		discovered = append(discovered, DiscoveredVM{
			XMLPath:   "", // Empty for gRPC-based imports - disks are already on remote node
			Candidate: candidate,
			Valid:     true,
		})
	}

	return discovered, nil
}

// getAgentClient creates a gRPC client to the agent
func (s *ImportService) getAgentClient(ctx context.Context, nodeID, nodeIP string) (pb.NodeAgentClient, error) {
	address := fmt.Sprintf("%s:50051", nodeIP)

	s.connMutex.RLock()
	conn, exists := s.grpcConns[address]
	s.connMutex.RUnlock()

	if exists {
		return pb.NewNodeAgentClient(conn), nil
	}

	s.connMutex.Lock()
	defer s.connMutex.Unlock()

	tlsCreds := panelclient.NodeTLSCredentials(nodeID, nodeIP)
	connCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(connCtx, address,
		grpc.WithTransportCredentials(tlsCreds),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to dial agent at %s: %w", address, err)
	}

	s.grpcConns[address] = conn
	return pb.NewNodeAgentClient(conn), nil
}

// createAuthContext creates a context with authentication metadata for agent gRPC calls
func (s *ImportService) createAuthContext(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	md := metadata.New(map[string]string{
		"authorization": "Bearer " + token,
	})
	return metadata.NewOutgoingContext(ctx, md)
}

// SyncNodeVMs syncs VM info (hostname + IP) from agent for all VMs on a node
// This updates existing VMs that were imported before guest-agent enrichment was added
func (s *ImportService) SyncNodeVMs(ctx context.Context, nodeID string) ([]SyncResult, error) {
	node, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	// Connect to agent
	client, err := s.getAgentClient(ctx, node.ID, node.IPAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to agent: %w", err)
	}

	authCtx := s.createAuthContext(ctx, node.Token)

	// Scan VMs on agent
	resp, err := client.ScanVMs(authCtx, &pb.ScanVMsRequest{})
	if err != nil {
		return nil, fmt.Errorf("failed to scan VMs on agent: %w", err)
	}

	if !resp.Success {
		errMsg := "scan failed"
		if resp.Error != nil {
			errMsg = resp.Error.Message
		}
		return nil, fmt.Errorf("agent scan failed: %s", errMsg)
	}

	var results []SyncResult

	for _, scannedVM := range resp.Vms {
		result := SyncResult{
			UUID:     scannedVM.Uuid,
			Name:     scannedVM.Name,
			Hostname: scannedVM.Hostname,
			VMStatus: scannedVM.Status,
		}

		// Find existing VM in DB by UUID
		existingVM, err := s.vmRepo.GetByID(ctx, scannedVM.Uuid)
		if err != nil {
			result.Status = "skipped"
			result.Message = "VM not in database"
			results = append(results, result)
			continue
		}

		updated := false

		// Update hostname if guest-agent returned a real one (not just libvirt name)
		if scannedVM.Hostname != "" && scannedVM.Hostname != scannedVM.Name && scannedVM.Hostname != existingVM.Hostname {
			existingVM.Hostname = scannedVM.Hostname
			updated = true
			result.Message = fmt.Sprintf("hostname updated to %s", scannedVM.Hostname)
		}

		// Update runtime status reported by libvirt on the node.
		if newStatus, ok := mapImportedRuntimeStatus(scannedVM.Status); ok && existingVM.Status != newStatus {
			existingVM.Status = newStatus
			updated = true
			if result.Message != "" {
				result.Message += "; "
			}
			result.Message += fmt.Sprintf("status updated to %s", newStatus)
		}

		if updated {
			if err := s.vmRepo.Update(ctx, existingVM); err != nil {
				result.Status = "error"
				result.Message = fmt.Sprintf("failed to update VM: %v", err)
				results = append(results, result)
				continue
			}
		}

		// Sync network/IP records
		for _, net := range scannedVM.Networks {
			if net.IpAddress == "" {
				continue
			}
			ip := net.IpAddress
			// Skip private IPs
			if strings.HasPrefix(ip, "172.") || strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") || ip == "127.0.0.1" {
				continue
			}

			// Check if this IP already exists for this VM
			existingNets, _ := s.networkRepo.ListByVMID(ctx, existingVM.ID)
			found := false
			for _, en := range existingNets {
				if en.IPAddress == ip {
					found = true
					break
				}
			}
			if !found {
				networkRecord := &models.Network{
					ID:        uuid.New().String(),
					VMID:      existingVM.ID,
					IPAddress: ip,
				}
				if err := s.networkRepo.Create(ctx, networkRecord); err != nil {
					s.logger.WarnContext(ctx, "failed to create network record during sync",
						"vm_id", existingVM.ID,
						"ip_address", ip,
						"error", err,
					)
				} else {
					if result.Message != "" {
						result.Message += "; "
					}
					result.Message += fmt.Sprintf("IP %s added", ip)
					updated = true
				}
			}
		}

		if updated {
			result.Status = "updated"
		} else {
			result.Status = "unchanged"
		}
		results = append(results, result)
	}

	return results, nil
}

// SyncResult represents the result of syncing a single VM
type SyncResult struct {
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	VMStatus string `json:"vm_status,omitempty"`
	Status   string `json:"status"` // "updated", "unchanged", "skipped", "error"
	Message  string `json:"message,omitempty"`
}

func defaultImportedRuntimeStatus(status string) models.VMStatus {
	if mappedStatus, ok := mapImportedRuntimeStatus(status); ok {
		return mappedStatus
	}
	return models.VMStatusStopped
}

func mapImportedRuntimeStatus(status string) (models.VMStatus, bool) {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running":
		return models.VMStatusRunning, true
	case "paused", "suspended", "pmsuspended":
		return models.VMStatusSuspended, true
	case "crashed", "error":
		return models.VMStatusError, true
	case "stopped", "shutoff", "shutdown":
		return models.VMStatusStopped, true
	default:
		return "", false
	}
}

func (s *ImportService) GetVMByID(ctx context.Context, vmID string) (*models.VM, error) {
	return s.vmRepo.GetByID(ctx, vmID)
}

// GetVMByHostname retrieves a VM by hostname for conflict checking
func (s *ImportService) GetVMByHostname(ctx context.Context, hostname string) (*models.VM, error) {
	return s.vmRepo.GetByHostname(ctx, hostname)
}

// ValidateImportRequest validates an import request before processing
func (s *ImportService) ValidateImportRequest(ctx context.Context, req *ImportVirtualizorRequest) error {
	if req.NodeID == "" {
		return fmt.Errorf("node_id is required")
	}
	if req.UserID == "" {
		return fmt.Errorf("user_id is required")
	}
	if req.OSTemplateID == "" {
		return fmt.Errorf("os_template_id is required")
	}

	// Validate UUID formats
	if _, err := uuid.Parse(req.NodeID); err != nil {
		return fmt.Errorf("invalid node_id format: %w", err)
	}
	if _, err := uuid.Parse(req.UserID); err != nil {
		return fmt.Errorf("invalid user_id format: %w", err)
	}
	if _, err := uuid.Parse(req.OSTemplateID); err != nil {
		return fmt.Errorf("invalid os_template_id format: %w", err)
	}

	// Validate disk action
	if req.DiskAction != "" {
		switch req.DiskAction {
		case DiskActionSymlink, DiskActionCopy, DiskActionMove:
			// Valid
		default:
			return fmt.Errorf("invalid disk_action: %s", req.DiskAction)
		}
	}

	return nil
}
