package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"github.com/riverqueue/river"
	"gorm.io/gorm"
)

var (
	// ErrTemplateNotFound is returned when a template is not found
	ErrTemplateNotFound = errors.New("template not found")
	// ErrTemplateAlreadyExists is returned when a template with the same name/version exists
	ErrTemplateAlreadyExists = errors.New("template with this name and version already exists")
	// ErrInvalidChecksum is returned when checksum verification fails
	ErrInvalidChecksum = errors.New("checksum verification failed")
	// ErrInvalidFileURL is returned when the file URL is invalid or inaccessible
	ErrInvalidFileURL = errors.New("invalid or inaccessible file URL")
)

// TemplateSyncStatus represents the sync status of a template on a node
type TemplateSyncStatus string

const (
	// SyncStatusPending indicates the template is queued for sync
	SyncStatusPending TemplateSyncStatus = "pending"
	// SyncStatusSyncing indicates the template is currently being downloaded
	SyncStatusSyncing TemplateSyncStatus = "syncing"
	// SyncStatusAvailable indicates the template is available on the node
	SyncStatusAvailable TemplateSyncStatus = "available"
	// SyncStatusError indicates an error occurred during sync
	SyncStatusError TemplateSyncStatus = "error"
)

// TemplateNodeStatus tracks template sync status per node
type TemplateNodeStatus struct {
	TemplateID string             `json:"template_id" gorm:"type:uuid;not null;primaryKey"`
	NodeID     string             `json:"node_id" gorm:"type:uuid;not null;primaryKey"`
	Status     TemplateSyncStatus `json:"status" gorm:"type:varchar(20);not null;default:'pending'"`
	ErrorMsg   string             `json:"error_msg,omitempty" gorm:"type:text"`
	SyncedAt   *time.Time         `json:"synced_at,omitempty"`
	CreatedAt  time.Time          `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt  time.Time          `json:"updated_at" gorm:"not null;default:NOW()"`
}

// TableName specifies the table name for TemplateNodeStatus
func (TemplateNodeStatus) TableName() string {
	return "template_node_status"
}

// TemplateService handles template-related operations
type TemplateService struct {
	db           *gorm.DB
	templateRepo *repository.TemplateRepository
	nodeRepo     *repository.NodeRepository
	riverClient  *river.Client[pgx.Tx]
	httpClient   *http.Client
}

// TemplateMetadata represents template metadata from upload request
type TemplateMetadata struct {
	Name        string `json:"name" validate:"required,max=100"`
	Version     string `json:"version" validate:"required,max=50"`
	FileURL     string `json:"file_url" validate:"required,url"`
	Description string `json:"description" validate:"max=500"`
}

// CreateTemplateRequest contains data for creating a new template
type CreateTemplateRequest struct {
	Name        string `json:"name" validate:"required,max=100"`
	Version     string `json:"version" validate:"required,max=50"`
	FileURL     string `json:"file_url" validate:"required,url"`
	Description string `json:"description" validate:"max=500"`
}

// CreateTemplateResponse contains the created template data
type CreateTemplateResponse struct {
	Template *models.OSTemplate `json:"template"`
	Checksum string             `json:"checksum"`
}

// UpdateTemplateRequest contains data for updating a template
type UpdateTemplateRequest struct {
	Name        string `json:"name,omitempty" validate:"omitempty,max=100"`
	Version     string `json:"version,omitempty" validate:"omitempty,max=50"`
	Description string `json:"description,omitempty" validate:"omitempty,max=500"`
	IsActive    *bool  `json:"is_active,omitempty"`
}

// SyncTemplateRequest contains data for syncing a template to nodes
type SyncTemplateRequest struct {
	NodeIDs []string `json:"node_ids,omitempty"` // Empty means all nodes
	Force   bool     `json:"force,omitempty"`    // Force re-sync even if exists
}

// SyncTemplateResponse contains the sync operation result
type SyncTemplateResponse struct {
	TemplateID string   `json:"template_id"`
	NodeIDs    []string `json:"node_ids"`
}

// TemplateWithStatus includes template and its sync status across nodes
type TemplateWithStatus struct {
	*models.OSTemplate
	Checksum string               `json:"checksum"`
	Nodes    []NodeTemplateStatus `json:"nodes,omitempty"`
}

// NodeTemplateStatus represents a node's template sync status
type NodeTemplateStatus struct {
	NodeID   string             `json:"node_id"`
	NodeName string             `json:"node_name"`
	Status   TemplateSyncStatus `json:"status"`
	ErrorMsg string             `json:"error_msg,omitempty"`
	SyncedAt *time.Time         `json:"synced_at,omitempty"`
}

// NewTemplateService creates a new TemplateService
func NewTemplateService(
	db *gorm.DB,
	templateRepo *repository.TemplateRepository,
	nodeRepo *repository.NodeRepository,
	riverClient *river.Client[pgx.Tx],
) *TemplateService {
	return &TemplateService{
		db:           db,
		templateRepo: templateRepo,
		nodeRepo:     nodeRepo,
		riverClient:  riverClient,
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// calculateChecksum downloads the file and calculates SHA256 checksum
func (s *TemplateService) calculateChecksum(fileURL string) (string, error) {
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidFileURL, err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidFileURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%w: HTTP %d", ErrInvalidFileURL, resp.StatusCode)
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, resp.Body); err != nil {
		return "", fmt.Errorf("failed to compute checksum: %w", err)
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// CreateTemplate creates a new template with checksum verification
func (s *TemplateService) CreateTemplate(ctx context.Context, req *CreateTemplateRequest) (*CreateTemplateResponse, error) {
	// Check if template with same name/version already exists
	exists, err := s.templateRepo.NameAndVersionExists(ctx, req.Name, req.Version)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing template: %w", err)
	}
	if exists {
		return nil, ErrTemplateAlreadyExists
	}

	// Store the source URL (or local path) as the image reference. The node
	// agent downloads and caches the image on first use, so we intentionally do
	// NOT pull the (potentially multi-GB) image here just to hash it — that made
	// template creation hang and duplicated bytes onto the panel host.
	template := &models.OSTemplate{
		Name:        req.Name,
		Version:     req.Version,
		ImagePath:   req.FileURL,
		Description: req.Description,
		IsActive:    true,
	}

	if err := s.templateRepo.Create(ctx, template); err != nil {
		return nil, fmt.Errorf("failed to create template: %w", err)
	}

	return &CreateTemplateResponse{
		Template: template,
	}, nil
}

// GetTemplate retrieves a template by ID with optional node status
func (s *TemplateService) GetTemplate(ctx context.Context, id string, includeNodeStatus bool) (*TemplateWithStatus, error) {
	template, err := s.templateRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTemplateNotFound
		}
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	result := &TemplateWithStatus{
		OSTemplate: template,
	}

	// Get checksum from sync status records or compute on demand
	// For simplicity, we'll store checksum in the database

	if includeNodeStatus {
		nodes, err := s.getTemplateNodeStatus(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("failed to get node status: %w", err)
		}
		result.Nodes = nodes
	}

	return result, nil
}

// getTemplateNodeStatus retrieves sync status for all nodes
func (s *TemplateService) getTemplateNodeStatus(ctx context.Context, templateID string) ([]NodeTemplateStatus, error) {
	var statuses []TemplateNodeStatus
	if err := s.db.WithContext(ctx).Where("template_id = ?", templateID).Find(&statuses).Error; err != nil {
		return nil, err
	}

	// Get all nodes
	nodes, err := s.nodeRepo.List(ctx, 0, 0)
	if err != nil {
		return nil, err
	}

	// Create map of existing statuses
	statusMap := make(map[string]TemplateNodeStatus)
	for _, s := range statuses {
		statusMap[s.NodeID] = s
	}

	// Build result with all nodes
	var result []NodeTemplateStatus
	for _, node := range nodes {
		ns := NodeTemplateStatus{
			NodeID:   node.ID,
			NodeName: node.Name,
		}

		if status, exists := statusMap[node.ID]; exists {
			ns.Status = status.Status
			ns.ErrorMsg = status.ErrorMsg
			ns.SyncedAt = status.SyncedAt
		} else {
			ns.Status = SyncStatusPending
		}

		result = append(result, ns)
	}

	return result, nil
}

// ListTemplates retrieves all templates with optional pagination
func (s *TemplateService) ListTemplates(ctx context.Context, limit, offset int, includeNodeStatus bool) ([]*TemplateWithStatus, error) {
	templates, err := s.templateRepo.List(ctx, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to list templates: %w", err)
	}

	var result []*TemplateWithStatus
	for _, template := range templates {
		tws := &TemplateWithStatus{
			OSTemplate: &template,
		}

		if includeNodeStatus {
			nodes, err := s.getTemplateNodeStatus(ctx, template.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to get node status for template %s: %w", template.ID, err)
			}
			tws.Nodes = nodes
		}

		result = append(result, tws)
	}

	return result, nil
}

// UpdateTemplate updates template metadata
func (s *TemplateService) UpdateTemplate(ctx context.Context, id string, req *UpdateTemplateRequest) (*models.OSTemplate, error) {
	template, err := s.templateRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTemplateNotFound
		}
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	// Update fields if provided
	if req.Name != "" {
		template.Name = req.Name
	}
	if req.Version != "" {
		template.Version = req.Version
	}
	if req.Description != "" {
		template.Description = req.Description
	}
	if req.IsActive != nil {
		template.IsActive = *req.IsActive
	}

	if err := s.templateRepo.Update(ctx, template); err != nil {
		return nil, fmt.Errorf("failed to update template: %w", err)
	}

	return template, nil
}

// DeleteTemplate removes a template and its sync records
func (s *TemplateService) DeleteTemplate(ctx context.Context, id string) error {
	// Check if template exists
	_, err := s.templateRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrTemplateNotFound
		}
		return fmt.Errorf("failed to get template: %w", err)
	}

	// Delete sync status records first
	if err := s.db.WithContext(ctx).Where("template_id = ?", id).Delete(&TemplateNodeStatus{}).Error; err != nil {
		return fmt.Errorf("failed to delete sync status records: %w", err)
	}

	// Delete template
	if err := s.templateRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("failed to delete template: %w", err)
	}

	return nil
}

// SyncTemplate syncs a template to specified nodes or all nodes
func (s *TemplateService) SyncTemplate(ctx context.Context, templateID string, req *SyncTemplateRequest) (*SyncTemplateResponse, error) {
	// Verify template exists
	_, err := s.templateRepo.GetByID(ctx, templateID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTemplateNotFound
		}
		return nil, fmt.Errorf("failed to get template: %w", err)
	}

	// Determine target nodes
	var nodeIDs []string
	if len(req.NodeIDs) > 0 {
		// Validate specified nodes exist
		for _, nodeID := range req.NodeIDs {
			_, err := s.nodeRepo.GetByID(ctx, nodeID)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil, fmt.Errorf("%w: %s", ErrNodeNotFound, nodeID)
				}
				return nil, fmt.Errorf("failed to get node %s: %w", nodeID, err)
			}
		}
		nodeIDs = req.NodeIDs
	} else {
		// Get all active nodes
		nodes, err := s.nodeRepo.ListActive(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list nodes: %w", err)
		}
		for _, node := range nodes {
			nodeIDs = append(nodeIDs, node.ID)
		}
	}

	if len(nodeIDs) == 0 {
		return nil, errors.New("no target nodes available")
	}

	// Create or update sync status records
	for _, nodeID := range nodeIDs {
		status := TemplateNodeStatus{
			TemplateID: templateID,
			NodeID:     nodeID,
			Status:     SyncStatusPending,
		}

		// Upsert the status record
		err := s.db.WithContext(ctx).Save(&status).Error
		if err != nil {
			return nil, fmt.Errorf("failed to create sync status for node %s: %w", nodeID, err)
		}
	}

	// Create River job for template sync
	jobArgs := queue.TemplateSyncJob{
		TemplateID: templateID,
		NodeIDs:    nodeIDs,
		Force:      req.Force,
	}

	_, err = s.riverClient.Insert(ctx, jobArgs, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create sync job: %w", err)
	}

	return &SyncTemplateResponse{
		TemplateID: templateID,
		NodeIDs:    nodeIDs,
	}, nil
}

// UpdateNodeSyncStatus updates the sync status for a template on a specific node
func (s *TemplateService) UpdateNodeSyncStatus(ctx context.Context, templateID, nodeID string, status TemplateSyncStatus, errorMsg string) error {
	now := time.Now()
	record := TemplateNodeStatus{
		TemplateID: templateID,
		NodeID:     nodeID,
		Status:     status,
		ErrorMsg:   errorMsg,
	}

	if status == SyncStatusAvailable {
		record.SyncedAt = &now
	}

	return s.db.WithContext(ctx).Save(&record).Error
}

// GetNodeSyncStatus retrieves the sync status for a template on a specific node
func (s *TemplateService) GetNodeSyncStatus(ctx context.Context, templateID, nodeID string) (*TemplateNodeStatus, error) {
	var status TemplateNodeStatus
	if err := s.db.WithContext(ctx).Where("template_id = ? AND node_id = ?", templateID, nodeID).First(&status).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil // No status record yet
		}
		return nil, err
	}
	return &status, nil
}
