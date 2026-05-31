package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"time"

	"github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// Common errors for node operations
var (
	ErrNodeNotFound          = fmt.Errorf("node not found")
	ErrNodeAlreadyExists     = fmt.Errorf("node with this name or IP already exists")
	ErrInvalidNodeIP         = fmt.Errorf("invalid node IP address")
	ErrNodeUnreachable       = fmt.Errorf("node is unreachable")
	ErrTokenGenerationFailed = fmt.Errorf("failed to generate authentication token")
)

// Default agent port for connectivity checks
const DefaultAgentPort = 50051

// TokenLength is the length of the generated auth token in bytes
const TokenLength = 32

// NodeService handles node-related operations
type NodeService struct {
	repo        *repository.NodeRepository
	db          *gorm.DB
	agentPort   int
	timeout     time.Duration
	agentClient *client.AgentClient
}

// NewNodeService creates a new NodeService instance
func NewNodeService(repo *repository.NodeRepository, db ...*gorm.DB) *NodeService {
	var database *gorm.DB
	if len(db) > 0 {
		database = db[0]
	}
	agentCfg := &client.ClientConfig{
		MaxRetries:         2,
		InitialBackoff:     100 * time.Millisecond,
		MaxBackoff:         5 * time.Second,
		BackoffMultiplier:  2.0,
		RequestTimeout:     10 * time.Second,
		ConnectionTimeout:  5 * time.Second,
		InsecureSkipVerify: true, // Agent uses self-signed TLS certs
	}
	agentClient, _ := client.NewAgentClient(agentCfg)
	return &NodeService{
		repo:        repo,
		db:          database,
		agentPort:   DefaultAgentPort,
		timeout:     5 * time.Second,
		agentClient: agentClient,
	}
}

// SetAgentPort sets the agent port for connectivity checks (useful for testing)
func (s *NodeService) SetAgentPort(port int) {
	s.agentPort = port
}

// SetTimeout sets the timeout for connectivity checks
func (s *NodeService) SetTimeout(timeout time.Duration) {
	s.timeout = timeout
}

// CreateNodeRequest contains data for creating a new node
type CreateNodeRequest struct {
	Name      string `json:"name" validate:"required,max=100"`
	IPAddress string `json:"ip_address" validate:"required,ip"`
}

// CreateNodeResponse contains the created node and auth token
type CreateNodeResponse struct {
	Node  *models.Node `json:"node"`
	Token string       `json:"token"`
}

// CreateNode registers a new node and generates an auth token
func (s *NodeService) CreateNode(ctx context.Context, req *CreateNodeRequest) (*CreateNodeResponse, error) {
	// Validate IP address format
	if net.ParseIP(req.IPAddress) == nil {
		return nil, ErrInvalidNodeIP
	}

	// Check if node with same name exists
	exists, err := s.repo.NameExists(ctx, req.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to check name existence: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("%w: name already exists", ErrNodeAlreadyExists)
	}

	// Check if node with same IP exists
	exists, err = s.repo.IPAddressExists(ctx, req.IPAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to check IP existence: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("%w: IP address already registered", ErrNodeAlreadyExists)
	}

	// Generate secure auth token
	token, err := s.generateToken()
	if err != nil {
		return nil, ErrTokenGenerationFailed
	}

	// Create node
	node := &models.Node{
		Name:      req.Name,
		IPAddress: req.IPAddress,
		Status:    models.NodeStatusOffline,
		Token:     token,
	}

	// Validate node struct
	if errs := node.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("validation failed: %v", errs)
	}

	// Save to database
	if err := s.repo.Create(ctx, node); err != nil {
		return nil, fmt.Errorf("failed to create node: %w", err)
	}

	return &CreateNodeResponse{
		Node:  node,
		Token: token,
	}, nil
}

// GetNode retrieves a node by ID with health status
func (s *NodeService) GetNode(ctx context.Context, id string) (*models.Node, error) {
	node, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrNodeNotFound
	}
	return node, nil
}

// ListNodes retrieves all nodes with optional filtering
func (s *NodeService) ListNodes(ctx context.Context, status *models.NodeStatus, limit, offset int) ([]models.Node, error) {
	if status != nil {
		return s.repo.ListByStatus(ctx, *status, limit, offset)
	}
	return s.repo.List(ctx, limit, offset)
}

// UpdateNodeRequest contains data for updating a node
type UpdateNodeRequest struct {
	Name      string             `json:"name,omitempty" validate:"omitempty,max=100"`
	IPAddress string             `json:"ip_address,omitempty" validate:"omitempty,ip"`
	Status    *models.NodeStatus `json:"status,omitempty" validate:"omitempty,oneof=active maintenance offline"`
}

// UpdateNode updates an existing node's information
func (s *NodeService) UpdateNode(ctx context.Context, id string, req *UpdateNodeRequest) (*models.Node, error) {
	// Get existing node
	node, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrNodeNotFound
	}

	// Update fields if provided
	if req.Name != "" {
		// Check if new name already exists (excluding current node)
		exists, err := s.repo.NameExists(ctx, req.Name)
		if err != nil {
			return nil, fmt.Errorf("failed to check name existence: %w", err)
		}
		if exists && node.Name != req.Name {
			return nil, fmt.Errorf("%w: name already exists", ErrNodeAlreadyExists)
		}
		node.Name = req.Name
	}

	if req.IPAddress != "" {
		// Validate IP
		if net.ParseIP(req.IPAddress) == nil {
			return nil, ErrInvalidNodeIP
		}
		// Check if new IP already exists (excluding current node)
		exists, err := s.repo.IPAddressExists(ctx, req.IPAddress)
		if err != nil {
			return nil, fmt.Errorf("failed to check IP existence: %w", err)
		}
		if exists && node.IPAddress != req.IPAddress {
			return nil, fmt.Errorf("%w: IP address already registered", ErrNodeAlreadyExists)
		}
		node.IPAddress = req.IPAddress
	}

	if req.Status != nil {
		node.Status = *req.Status
	}

	// Validate updated node
	if errs := node.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("validation failed: %v", errs)
	}

	// Save changes
	if err := s.repo.Update(ctx, node); err != nil {
		return nil, fmt.Errorf("failed to update node: %w", err)
	}

	return node, nil
}

// DeleteNodeCascadeOptions contains options for cascading delete
type DeleteNodeCascadeOptions struct {
	DeleteVMs      bool `json:"delete_vms"`      // Delete all VMs on this node
	DeleteBackups  bool `json:"delete_backups"`  // Delete all backups
	DeleteNetworks bool `json:"delete_networks"` // Delete all networks
	Force          bool `json:"force"`           // Force delete even if resources exist
}

// DeleteNode removes a node from the database
func (s *NodeService) DeleteNode(ctx context.Context, id string, opts *DeleteNodeCascadeOptions) error {
	// Check if node exists
	_, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return ErrNodeNotFound
	}

	// Database foreign key constraints will prevent deletion if resources exist

	if err := s.repo.Delete(ctx, id); err != nil {
		return fmt.Errorf("failed to delete node: %w", err)
	}

	return nil
}

// RegenerateToken generates a new auth token for a node
func (s *NodeService) RegenerateToken(ctx context.Context, id string) (string, error) {
	// Check if node exists
	node, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return "", ErrNodeNotFound
	}

	// Generate new token
	newToken, err := s.generateToken()
	if err != nil {
		return "", ErrTokenGenerationFailed
	}

	// Update node with new token
	if err := s.repo.UpdateToken(ctx, node.ID, newToken); err != nil {
		return "", fmt.Errorf("failed to update token: %w", err)
	}

	return newToken, nil
}

// CheckNodeHealth checks if a node is reachable via TCP connection
func (s *NodeService) CheckNodeHealth(ctx context.Context, node *models.Node) (bool, error) {
	address := fmt.Sprintf("%s:%d", node.IPAddress, s.agentPort)

	// Create a dialer with timeout
	dialer := net.Dialer{
		Timeout: s.timeout,
	}

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return false, nil // Node is offline, not an error
	}
	defer conn.Close()

	return true, nil
}

// CheckNodeHealthByID checks health of a node by its ID
func (s *NodeService) CheckNodeHealthByID(ctx context.Context, id string) (bool, error) {
	node, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return false, ErrNodeNotFound
	}

	return s.CheckNodeHealth(ctx, node)
}

// GetNodesWithHealth retrieves all nodes with their health status
func (s *NodeService) GetNodesWithHealth(ctx context.Context) ([]NodeWithHealth, error) {
	nodes, err := s.repo.List(ctx, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	result := make([]NodeWithHealth, len(nodes))
	for i, node := range nodes {
		online, _ := s.CheckNodeHealth(ctx, &node)
		result[i] = NodeWithHealth{
			Node:   node,
			Online: online,
		}

		// Auto-sync status based on health check
		if online && node.Status == models.NodeStatusOffline {
			_ = s.repo.UpdateStatus(ctx, node.ID, models.NodeStatusActive)
			result[i].Node.Status = models.NodeStatusActive
		} else if !online && node.Status == models.NodeStatusActive {
			_ = s.repo.UpdateStatus(ctx, node.ID, models.NodeStatusOffline)
			result[i].Node.Status = models.NodeStatusOffline
		}
	}

	return result, nil
}

// NodeWithHealth represents a node with its health status
type NodeWithHealth struct {
	models.Node
	Online bool `json:"online"`
}

// ValidateNodeIP checks if the node IP is reachable
func (s *NodeService) ValidateNodeIP(ctx context.Context, ipAddress string) error {
	if net.ParseIP(ipAddress) == nil {
		return ErrInvalidNodeIP
	}

	address := fmt.Sprintf("%s:%d", ipAddress, s.agentPort)

	dialer := net.Dialer{
		Timeout: s.timeout,
	}

	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return ErrNodeUnreachable
	}
	defer conn.Close()

	return nil
}

// generateToken generates a cryptographically secure random token
func (s *NodeService) generateToken() (string, error) {
	bytes := make([]byte, TokenLength)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// GetNodeMetrics retrieves metrics for a node from the agent
func (s *NodeService) GetNodeMetrics(ctx context.Context, id string) (*NodeMetrics, error) {
	node, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrNodeNotFound
	}

	// Check if node is online first
	online, err := s.CheckNodeHealth(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("failed to check node health: %w", err)
	}

	// Get VM count from database
	var vmCount int64
	s.db.WithContext(ctx).Model(&models.VM{}).Where("node_id = ?", id).Count(&vmCount)

	if !online {
		return &NodeMetrics{
			NodeID:      id,
			Timestamp:   time.Now(),
			CPUUsage:    0,
			MemoryUsage: 0,
			DiskUsage:   0,
			VMCount:     int(vmCount),
			Status:      "offline",
		}, nil
	}

	// Register node with agent client and try to get metrics
	if s.agentClient != nil {
		nodeInfo := client.NodeInfo{
			ID:         node.ID,
			Address:    fmt.Sprintf("%s:%d", node.IPAddress, s.agentPort),
			Token:      node.Token,
			TLSEnabled: true,
		}
		s.agentClient.RegisterNode(nodeInfo)

		// Get system info from agent
		sysInfo, err := s.agentClient.GetNodeInfo(ctx, id)
		if err == nil {
			memTotalMB := sysInfo.MemoryTotal / (1024 * 1024)
			diskTotalGB := sysInfo.DiskTotal / (1024 * 1024 * 1024)

			return &NodeMetrics{
				NodeID:               id,
				Timestamp:            time.Now(),
				CPUUsage:             sysInfo.CpuPercent,
				MemoryUsage:          sysInfo.MemoryUsedPercent,
				MemoryUsed:           sysInfo.MemoryUsedBytes,
				MemoryTotal:          sysInfo.MemoryTotal,
				DiskUsage:            sysInfo.DiskUsedPercent,
				DiskUsed:             sysInfo.DiskUsedBytes,
				DiskTotal:            sysInfo.DiskTotal,
				NetworkRxBytesPerSec: float64(sysInfo.NetworkRxBytesPerSec),
				NetworkTxBytesPerSec: float64(sysInfo.NetworkTxBytesPerSec),
				DiskReadBytesPerSec:  float64(sysInfo.DiskReadBytesPerSec),
				DiskWriteBytesPerSec: float64(sysInfo.DiskWriteBytesPerSec),
				VMCount:              int(vmCount),
				AvailableCPUs:        int(sysInfo.AvailableCpus),
				AvailableMemoryMB:    memTotalMB,
				AvailableDiskGB:      diskTotalGB,
				LoadAvg:              []float64{sysInfo.LoadAvg1, sysInfo.LoadAvg5, sysInfo.LoadAvg15},
				Status:               "online",
			}, nil
		}
		// If gRPC call fails, fall through to return online with zero metrics
	}

	return &NodeMetrics{
		NodeID:               id,
		Timestamp:            time.Now(),
		CPUUsage:             0,
		MemoryUsage:          0,
		MemoryUsed:           0,
		MemoryTotal:          0,
		DiskUsage:            0,
		DiskUsed:             0,
		DiskTotal:            0,
		NetworkRxBytesPerSec: 0,
		NetworkTxBytesPerSec: 0,
		DiskReadBytesPerSec:  0,
		DiskWriteBytesPerSec: 0,
		VMCount:              int(vmCount),
		AvailableCPUs:        0,
		AvailableMemoryMB:    0,
		AvailableDiskGB:      0,
		LoadAvg:              []float64{0, 0, 0},
		Status:               "online",
	}, nil
}

// NodeMetrics represents node performance metrics
type NodeMetrics struct {
	NodeID               string    `json:"node_id"`
	Timestamp            time.Time `json:"timestamp"`
	CPUUsage             float64   `json:"cpu_usage"`    // Percentage
	MemoryUsage          float64   `json:"memory_usage"` // Percentage
	MemoryUsed           int64     `json:"memory_used"`  // Bytes
	MemoryTotal          int64     `json:"memory_total"` // Bytes
	DiskUsage            float64   `json:"disk_usage"`   // Percentage
	DiskUsed             int64     `json:"disk_used"`    // Bytes
	DiskTotal            int64     `json:"disk_total"`   // Bytes
	NetworkRxBytesPerSec float64   `json:"network_rx_bytes_per_sec"`
	NetworkTxBytesPerSec float64   `json:"network_tx_bytes_per_sec"`
	DiskReadBytesPerSec  float64   `json:"disk_read_bytes_per_sec"`
	DiskWriteBytesPerSec float64   `json:"disk_write_bytes_per_sec"`
	VMCount              int       `json:"vm_count"`
	AvailableCPUs        int       `json:"available_cpus"`
	AvailableMemoryMB    int64     `json:"available_memory_mb"`
	AvailableDiskGB      int64     `json:"available_disk_gb"`
	LoadAvg              []float64 `json:"load_avg"`
	Status               string    `json:"status"` // online, offline
}
