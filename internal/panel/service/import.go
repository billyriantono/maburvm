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
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"gorm.io/gorm"

	vmimport "github.com/maburvm/panel/internal/agent/import"
	"github.com/maburvm/panel/internal/panel/repository"
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
	riverClient  *river.Client[pgx.Tx]
	logger       *slog.Logger
}

// NewImportService creates a new ImportService instance
func NewImportService(
	db *gorm.DB,
	vmRepo *repository.VMRepository,
	nodeRepo *repository.NodeRepository,
	templateRepo *repository.TemplateRepository,
	riverClient *river.Client[pgx.Tx],
	logger *slog.Logger,
) *ImportService {
	return &ImportService{
		db:           db,
		vmRepo:       vmRepo,
		nodeRepo:     nodeRepo,
		templateRepo: templateRepo,
		riverClient:  riverClient,
		logger:       logger,
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

	// Scan for Virtualizor VM XML files
	discoveredVMs, err := s.scanForVirtualizorVMs(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("failed to scan for VMs: %w", err)
	}

	if len(discoveredVMs) == 0 {
		return nil, ErrNoVMsFound
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

// scanForVirtualizorVMs scans the node for Virtualizor VM XML files
// Common paths scanned:
//   - /etc/libvirt/qemu/
//   - /var/lib/libvirt/images/
//   - /var/virtualizor/
//   - Custom path if specified
func (s *ImportService) scanForVirtualizorVMs(ctx context.Context, req *ImportVirtualizorRequest) ([]DiscoveredVM, error) {
	var discovered []DiscoveredVM

	// Build list of paths to scan
	pathsToScan := []string{
		DefaultLibvirtQemuPath,
		DefaultLibvirtImagesPath,
		DefaultVirtualizorVMPath,
	}

	// Add custom path if specified
	if req.CustomPath != "" {
		pathsToScan = append([]string{req.CustomPath}, pathsToScan...)
	}

	// Track discovered UUIDs to avoid duplicates
	seenUUIDs := make(map[string]bool)

	for _, scanPath := range pathsToScan {
		// In a real implementation, this would communicate with the agent
		// to scan files on the node. For now, we simulate local scanning.
		vms, err := s.scanPathForVMs(scanPath, seenUUIDs)
		if err != nil {
			s.logger.WarnContext(ctx, "failed to scan path",
				"path", scanPath,
				"error", err,
			)
			continue
		}
		discovered = append(discovered, vms...)
	}

	return discovered, nil
}

// scanPathForVMs scans a specific path for VM XML files
func (s *ImportService) scanPathForVMs(scanPath string, seenUUIDs map[string]bool) ([]DiscoveredVM, error) {
	var discovered []DiscoveredVM

	// Check if path exists
	info, err := os.Stat(scanPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Path doesn't exist, skip silently
			return discovered, nil
		}
		return nil, err
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", scanPath)
	}

	// Walk directory looking for XML files
	err = filepath.Walk(scanPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Continue walking despite errors
		}

		// Skip directories and non-XML files
		if info.IsDir() || !strings.HasSuffix(strings.ToLower(path), ".xml") {
			return nil
		}

		// Try to parse as Virtualizor domain XML
		candidate, err := vmimport.ParseVirtualizorDomainXML(path)
		if err != nil {
			// Not a valid Virtualizor XML, skip
			return nil
		}

		// Check for duplicate UUID
		if seenUUIDs[candidate.UUID] {
			return nil
		}
		seenUUIDs[candidate.UUID] = true

		discovered = append(discovered, DiscoveredVM{
			XMLPath:   path,
			Candidate: candidate,
			Valid:     true,
		})

		return nil
	})

	if err != nil {
		return nil, err
	}

	return discovered, nil
}

// processDiscoveredVM processes a single discovered VM
// - Checks for UUID conflicts
// - Creates VM record with source_migration = "virtualizor"
// - Re-maps disk paths
// - Enqueues ImportJob
func (s *ImportService) processDiscoveredVM(ctx context.Context, req *ImportVirtualizorRequest, discovered DiscoveredVM) ImportResult {
	candidate := discovered.Candidate

	result := ImportResult{
		SourceID: candidate.UUID,
		Name:     candidate.Name,
	}

	// Check for UUID conflict
	existingVM, err := s.vmRepo.GetByID(ctx, candidate.UUID)
	if err == nil && existingVM != nil {
		result.Status = "skipped"
		result.Message = fmt.Sprintf("VM with UUID %s already exists (hostname: %s)", candidate.UUID, existingVM.Hostname)
		return result
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		result.Status = "error"
		result.Message = fmt.Sprintf("failed to check existing VM UUID: %v", err)
		return result
	}

	// Check for hostname conflict
	hostnameExists, err := s.vmRepo.HostnameExists(ctx, candidate.Name)
	if err != nil {
		result.Status = "error"
		result.Message = fmt.Sprintf("failed to check hostname: %v", err)
		return result
	}
	if hostnameExists {
		result.Status = "skipped"
		result.Message = fmt.Sprintf("hostname %s already exists", candidate.Name)
		return result
	}

	// Generate VNC credentials
	vncPort := 5900 + int(time.Now().UnixNano()%100)
	vncPassword := s.generateRandomPassword(12)

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
		Hostname:     candidate.Name,
		OSTemplateID: req.OSTemplateID,
		Resources: models.Resources{
			CPU:  candidate.CPU,
			RAM:  candidate.Memory,
			Disk: totalDiskGB,
		},
		Status:          models.VMStatusStopped,
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

	diskMappings, updatedXMLPath, err := s.remapDiskArtifacts(discovered.XMLPath, candidate, req)
	if err != nil {
		_ = s.vmRepo.Delete(ctx, vm.ID)
		result.Status = "error"
		result.Message = fmt.Sprintf("failed remapping disk artifacts: %v", err)
		return result
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

	// Enqueue import job
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
		// Job enqueue failed, but VM record was created
		// Update VM status to error
		_ = s.vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusError)

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
	Status      string     `json:"status"`       // pending, processing, completed, failed
	State       string     `json:"state"`        // Raw River state
	Source      string     `json:"source"`       // e.g., "virtualizor"
	SourceID    string     `json:"source_id"`    // Original VM UUID
	NodeID      string     `json:"node_id"`
	Attempt     int        `json:"attempt"`
	MaxAttempts int        `json:"max_attempts"`
	Error       string     `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	AttemptedAt *time.Time `json:"attempted_at,omitempty"`
	FinalizedAt *time.Time `json:"finalized_at,omitempty"`
}

// ListImportableVMs scans a node and returns VMs that can be imported
// without actually importing them (dry-run / preview)
func (s *ImportService) ListImportableVMs(ctx context.Context, nodeID string, customPath string) ([]DiscoveredVM, error) {
	if _, err := s.nodeRepo.GetByID(ctx, nodeID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNodeNotFound
		}
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	req := &ImportVirtualizorRequest{
		NodeID:     nodeID,
		CustomPath: customPath,
	}

	return s.scanForVirtualizorVMs(ctx, req)
}

// GetVMByID retrieves a VM by ID for conflict checking
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
