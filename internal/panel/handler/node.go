package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// NodeHandler handles HTTP requests for node management
type NodeHandler struct {
	service *service.NodeService
}

// NewNodeHandler creates a new NodeHandler instance
func NewNodeHandler(service *service.NodeService) *NodeHandler {
	return &NodeHandler{
		service: service,
	}
}

// RegisterRequest represents a request to register a new node
type RegisterRequest struct {
	Name      string `json:"name" validate:"required,max=100"`
	IPAddress string `json:"ip_address" validate:"required,ip"`
}

// RegisterResponse represents the response after registering a node
type RegisterResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IPAddress string `json:"ip_address"`
	Status    string `json:"status"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

// RegisterNode handles POST /api/nodes - Register a new node
func (h *NodeHandler) RegisterNode(c echo.Context) error {
	var req RegisterRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	// Validate request
	if req.Name == "" || req.IPAddress == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Name and IP address are required",
		})
	}

	// Create node
	createReq := &service.CreateNodeRequest{
		Name:      req.Name,
		IPAddress: req.IPAddress,
	}

	resp, err := h.service.CreateNode(c.Request().Context(), createReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNodeAlreadyExists):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": err.Error(),
			})
		case errors.Is(err, service.ErrInvalidNodeIP):
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

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"message": "Node registered successfully",
		"data": RegisterResponse{
			ID:        resp.Node.ID,
			Name:      resp.Node.Name,
			IPAddress: resp.Node.IPAddress,
			Status:    string(resp.Node.Status),
			Token:     resp.Token,
			CreatedAt: resp.Node.CreatedAt.Format("2006-01-02T15:04:05Z"),
		},
	})
}

// NodeListItem represents a node in the list response
type NodeListItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IPAddress string `json:"ip_address"`
	Status    string `json:"status"`
	Online    bool   `json:"online"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ListNodes handles GET /api/nodes - List all nodes with health status
func (h *NodeHandler) ListNodes(c echo.Context) error {
	// Parse optional query parameters
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

	// Get nodes with health status
	nodes, err := h.service.GetNodesWithHealth(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	// Filter by status if provided
	if status != "" {
		filtered := make([]service.NodeWithHealth, 0)
		for _, node := range nodes {
			if string(node.Node.Status) == status {
				filtered = append(filtered, node)
			}
		}
		nodes = filtered
	}

	// Apply pagination
	if offset > 0 && offset < len(nodes) {
		nodes = nodes[offset:]
	}
	if limit > 0 && limit < len(nodes) {
		nodes = nodes[:limit]
	}

	// Map to response format
	items := make([]NodeListItem, len(nodes))
	for i, node := range nodes {
		items[i] = NodeListItem{
			ID:        node.Node.ID,
			Name:      node.Node.Name,
			IPAddress: node.Node.IPAddress,
			Status:    string(node.Node.Status),
			Online:    node.Online,
			CreatedAt: node.Node.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt: node.Node.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Nodes retrieved successfully",
		"data":    items,
	})
}

// NodeDetailResponse represents detailed node information
type NodeDetailResponse struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	IPAddress string       `json:"ip_address"`
	Status    string       `json:"status"`
	Online    bool         `json:"online"`
	Metrics   *NodeMetrics `json:"metrics,omitempty"`
	CreatedAt string       `json:"created_at"`
	UpdatedAt string       `json:"updated_at"`
}

// NodeMetrics represents node performance metrics in the response
type NodeMetrics struct {
	CPUUsage    float64 `json:"cpu_usage"`
	MemoryUsage float64 `json:"memory_usage"`
	DiskUsage   float64 `json:"disk_usage"`
	VMCount     int     `json:"vm_count"`
}

// GetNode handles GET /api/nodes/:id - Get node details and metrics
func (h *NodeHandler) GetNode(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Node ID is required",
		})
	}

	// Get node
	node, err := h.service.GetNode(c.Request().Context(), id)
	if err != nil {
		if errors.Is(err, service.ErrNodeNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Node not found",
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	// Check health
	online, _ := h.service.CheckNodeHealthByID(c.Request().Context(), id)

	// Get metrics
	metrics, _ := h.service.GetNodeMetrics(c.Request().Context(), id)

	resp := NodeDetailResponse{
		ID:        node.ID,
		Name:      node.Name,
		IPAddress: node.IPAddress,
		Status:    string(node.Status),
		Online:    online,
		CreatedAt: node.CreatedAt.Format("2006-01-02T15:04:05Z"),
		UpdatedAt: node.UpdatedAt.Format("2006-01-02T15:04:05Z"),
	}

	if metrics != nil {
		resp.Metrics = &NodeMetrics{
			CPUUsage:    metrics.CPUUsage,
			MemoryUsage: metrics.MemoryUsage,
			DiskUsage:   metrics.DiskUsage,
			VMCount:     metrics.VMCount,
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Node retrieved successfully",
		"data":    resp,
	})
}

// UpdateNodeRequest represents a request to update a node
type UpdateNodeRequest struct {
	Name      string `json:"name,omitempty"`
	IPAddress string `json:"ip_address,omitempty"`
	Status    string `json:"status,omitempty"`
}

// UpdateNode handles PUT /api/nodes/:id - Update node information
func (h *NodeHandler) UpdateNode(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Node ID is required",
		})
	}

	var req UpdateNodeRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	// Build update request
	updateReq := &service.UpdateNodeRequest{}
	if req.Name != "" {
		updateReq.Name = req.Name
	}
	if req.IPAddress != "" {
		updateReq.IPAddress = req.IPAddress
	}
	if req.Status != "" {
		status := models.NodeStatus(req.Status)
		updateReq.Status = &status
	}

	// Update node
	node, err := h.service.UpdateNode(c.Request().Context(), id, updateReq)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNodeNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Node not found",
			})
		case errors.Is(err, service.ErrNodeAlreadyExists):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": err.Error(),
			})
		case errors.Is(err, service.ErrInvalidNodeIP):
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
		"message": "Node updated successfully",
		"data": map[string]interface{}{
			"id":         node.ID,
			"name":       node.Name,
			"ip_address": node.IPAddress,
			"status":     string(node.Status),
			"updated_at": node.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		},
	})
}

// DeleteNodeRequest represents a request to delete a node with cascade options
type DeleteNodeRequest struct {
	DeleteVMs      bool `json:"delete_vms"`
	DeleteBackups  bool `json:"delete_backups"`
	DeleteNetworks bool `json:"delete_networks"`
	Force          bool `json:"force"`
}

// DeleteNode handles DELETE /api/nodes/:id - Remove a node
func (h *NodeHandler) DeleteNode(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Node ID is required",
		})
	}

	// Parse optional cascade options from body
	var opts DeleteNodeRequest
	if err := c.Bind(&opts); err == nil {
		// Body is optional, ignore errors
	}

	cascadeOpts := &service.DeleteNodeCascadeOptions{
		DeleteVMs:      opts.DeleteVMs,
		DeleteBackups:  opts.DeleteBackups,
		DeleteNetworks: opts.DeleteNetworks,
		Force:          opts.Force,
	}

	// Delete node
	if err := h.service.DeleteNode(c.Request().Context(), id, cascadeOpts); err != nil {
		switch {
		case errors.Is(err, service.ErrNodeNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Node not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Node deleted successfully",
	})
}

// RegenerateTokenResponse represents the response after token regeneration
type RegenerateTokenResponse struct {
	Token string `json:"token"`
}

// RegenerateToken handles POST /api/nodes/:id/regenerate-token - Rotate auth token
func (h *NodeHandler) RegenerateToken(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Node ID is required",
		})
	}

	// Regenerate token
	newToken, err := h.service.RegenerateToken(c.Request().Context(), id)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrNodeNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Node not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Token regenerated successfully",
		"data": RegenerateTokenResponse{
			Token: newToken,
		},
	})
}

// RegisterNodeRoutes registers all node routes with the Echo router
func RegisterNodeRoutes(e *echo.Echo, handler *NodeHandler, db interface{}) {
	// Create node routes group
	nodes := e.Group("/api/v1/nodes")

	// Apply authentication middleware
	nodes.Use(middleware.RequireAuth(nil))

	// Apply permission middleware for node management
	nodes.Use(middleware.RequirePermission("node:read"))

	// List nodes - requires node:read
	nodes.GET("", handler.ListNodes)

	// Get node details - requires node:read
	nodes.GET("/:id", handler.GetNode)

	// Create node - requires node:create
	nodes.POST("", handler.RegisterNode, middleware.RequirePermission("node:create"))

	// Update node - requires node:update
	nodes.PUT("/:id", handler.UpdateNode, middleware.RequirePermission("node:update"))

	// Delete node - requires node:delete
	nodes.DELETE("/:id", handler.DeleteNode, middleware.RequirePermission("node:delete"))

	// Regenerate token - requires node:update
	nodes.POST("/:id/regenerate-token", handler.RegenerateToken, middleware.RequirePermission("node:update"))
}
