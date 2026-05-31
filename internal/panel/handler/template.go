package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/service"
)

// TemplateHandler handles HTTP requests for OS template management
type TemplateHandler struct {
	templateService *service.TemplateService
}

// NewTemplateHandler creates a new TemplateHandler
func NewTemplateHandler(templateService *service.TemplateService) *TemplateHandler {
	return &TemplateHandler{
		templateService: templateService,
	}
}

// CreateTemplateRequest represents the request body for creating a template
type CreateTemplateRequest struct {
	Name        string `json:"name" validate:"required,max=100"`
	Version     string `json:"version" validate:"required,max=50"`
	FileURL     string `json:"file_url" validate:"required,url"`
	Description string `json:"description" validate:"max=500"`
}

// UpdateTemplateRequest represents the request body for updating a template
type UpdateTemplateRequest struct {
	Name        string `json:"name,omitempty" validate:"omitempty,max=100"`
	Version     string `json:"version,omitempty" validate:"omitempty,max=50"`
	Description string `json:"description,omitempty" validate:"omitempty,max=500"`
	IsActive    *bool  `json:"is_active,omitempty"`
}

// SyncTemplateRequest represents the request body for syncing a template
type SyncTemplateRequest struct {
	NodeIDs []string `json:"node_ids,omitempty"` // Empty means all nodes
	Force   bool     `json:"force,omitempty"`    // Force re-sync even if exists
}

// ErrorResponse represents an error response
type ErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// SuccessResponse represents a generic success response
type SuccessResponse struct {
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// RegisterRoutes registers all template routes
func (h *TemplateHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc) {
	api := e.Group("/api/v1/templates", authMiddleware)

	api.POST("", h.CreateTemplate)
	api.GET("", h.ListTemplates)
	api.POST("/download-url", h.DownloadFromURL)
	api.GET("/:id", h.GetTemplate)
	api.PUT("/:id", h.UpdateTemplate)
	api.DELETE("/:id", h.DeleteTemplate)
	api.POST("/:id/sync", h.SyncTemplate)
}

// CreateTemplate handles POST /api/templates
// Uploads a new template with metadata and file URL
func (h *TemplateHandler) CreateTemplate(c echo.Context) error {
	var req CreateTemplateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_request",
			Message: "Failed to parse request body",
		})
	}

	// Validate request
	if req.Name == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "validation_error",
			Message: "Name is required",
		})
	}
	if req.Version == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "validation_error",
			Message: "Version is required",
		})
	}
	if req.FileURL == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "validation_error",
			Message: "File URL is required",
		})
	}

	svcReq := service.CreateTemplateRequest{
		Name:        req.Name,
		Version:     req.Version,
		FileURL:     req.FileURL,
		Description: req.Description,
	}

	resp, err := h.templateService.CreateTemplate(c.Request().Context(), &svcReq)
	if err != nil {
		switch err {
		case service.ErrTemplateAlreadyExists:
			return c.JSON(http.StatusConflict, ErrorResponse{
				Error:   "already_exists",
				Message: "Template with this name and version already exists",
			})
		case service.ErrInvalidFileURL:
			return c.JSON(http.StatusBadRequest, ErrorResponse{
				Error:   "invalid_file_url",
				Message: err.Error(),
			})
		default:
			return c.JSON(http.StatusInternalServerError, ErrorResponse{
				Error:   "internal_error",
				Message: err.Error(),
			})
		}
	}

	return c.JSON(http.StatusCreated, SuccessResponse{
		Message: "Template created successfully",
		Data: map[string]interface{}{
			"template": resp.Template,
			"checksum": resp.Checksum,
		},
	})
}

// ListTemplates handles GET /api/templates
// Lists all templates with optional node status
func (h *TemplateHandler) ListTemplates(c echo.Context) error {
	// Parse query parameters
	limit := 0
	offset := 0
	includeNodeStatus := c.QueryParam("include_nodes") == "true"

	templates, err := h.templateService.ListTemplates(c.Request().Context(), limit, offset, includeNodeStatus)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, SuccessResponse{
		Message: "Templates retrieved successfully",
		Data:    templates,
	})
}

// GetTemplate handles GET /api/templates/:id
// Gets a specific template by ID with optional node status
func (h *TemplateHandler) GetTemplate(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "validation_error",
			Message: "Template ID is required",
		})
	}

	includeNodeStatus := c.QueryParam("include_nodes") == "true"

	template, err := h.templateService.GetTemplate(c.Request().Context(), id, includeNodeStatus)
	if err != nil {
		switch err {
		case service.ErrTemplateNotFound:
			return c.JSON(http.StatusNotFound, ErrorResponse{
				Error:   "not_found",
				Message: "Template not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, ErrorResponse{
				Error:   "internal_error",
				Message: err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, SuccessResponse{
		Message: "Template retrieved successfully",
		Data:    template,
	})
}

// UpdateTemplate handles PUT /api/templates/:id
// Updates template metadata
func (h *TemplateHandler) UpdateTemplate(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "validation_error",
			Message: "Template ID is required",
		})
	}

	var req UpdateTemplateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_request",
			Message: "Failed to parse request body",
		})
	}

	svcReq := service.UpdateTemplateRequest{
		Name:        req.Name,
		Version:     req.Version,
		Description: req.Description,
		IsActive:    req.IsActive,
	}

	template, err := h.templateService.UpdateTemplate(c.Request().Context(), id, &svcReq)
	if err != nil {
		switch err {
		case service.ErrTemplateNotFound:
			return c.JSON(http.StatusNotFound, ErrorResponse{
				Error:   "not_found",
				Message: "Template not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, ErrorResponse{
				Error:   "internal_error",
				Message: err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, SuccessResponse{
		Message: "Template updated successfully",
		Data:    template,
	})
}

// DeleteTemplate handles DELETE /api/templates/:id
// Removes a template
func (h *TemplateHandler) DeleteTemplate(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "validation_error",
			Message: "Template ID is required",
		})
	}

	if err := h.templateService.DeleteTemplate(c.Request().Context(), id); err != nil {
		switch err {
		case service.ErrTemplateNotFound:
			return c.JSON(http.StatusNotFound, ErrorResponse{
				Error:   "not_found",
				Message: "Template not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, ErrorResponse{
				Error:   "internal_error",
				Message: err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, SuccessResponse{
		Message: "Template deleted successfully",
	})
}

// DownloadFromURL handles POST /api/v1/templates/download-url
// Initiates download of an ISO/template from a remote URL
func (h *TemplateHandler) DownloadFromURL(c echo.Context) error {
	var req struct {
		URL      string `json:"url"`
		Filename string `json:"filename"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_request",
			Message: "Failed to parse request body",
		})
	}
	if req.URL == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "validation_error",
			Message: "URL is required",
		})
	}
	if req.Filename == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "validation_error",
			Message: "Filename is required",
		})
	}

	// Create template record with the download URL
	svcReq := service.CreateTemplateRequest{
		Name:    req.Filename,
		Version: "1.0",
		FileURL: req.URL,
	}

	resp, err := h.templateService.CreateTemplate(c.Request().Context(), &svcReq)
	if err != nil {
		// If already exists, that's fine — just report success
		if err == service.ErrTemplateAlreadyExists {
			return c.JSON(http.StatusOK, SuccessResponse{
				Message: "Template already exists, download skipped",
			})
		}
		return c.JSON(http.StatusInternalServerError, ErrorResponse{
			Error:   "internal_error",
			Message: err.Error(),
		})
	}

	return c.JSON(http.StatusOK, SuccessResponse{
		Message: "Download initiated successfully",
		Data: map[string]interface{}{
			"template_id": resp.Template.ID,
			"filename":    req.Filename,
			"url":         req.URL,
			"status":      "downloading",
		},
	})
}

// SyncTemplate handles POST /api/templates/:id/sync
// Syncs template to specific nodes or all nodes
func (h *TemplateHandler) SyncTemplate(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "validation_error",
			Message: "Template ID is required",
		})
	}

	var req SyncTemplateRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{
			Error:   "invalid_request",
			Message: "Failed to parse request body",
		})
	}

	svcReq := service.SyncTemplateRequest{
		NodeIDs: req.NodeIDs,
		Force:   req.Force,
	}

	resp, err := h.templateService.SyncTemplate(c.Request().Context(), id, &svcReq)
	if err != nil {
		switch err {
		case service.ErrTemplateNotFound:
			return c.JSON(http.StatusNotFound, ErrorResponse{
				Error:   "not_found",
				Message: "Template not found",
			})
		case service.ErrNodeNotFound:
			return c.JSON(http.StatusBadRequest, ErrorResponse{
				Error:   "node_not_found",
				Message: err.Error(),
			})
		default:
			return c.JSON(http.StatusInternalServerError, ErrorResponse{
				Error:   "internal_error",
				Message: err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, SuccessResponse{
		Message: "Template sync initiated successfully",
		Data: map[string]interface{}{
			"template_id": resp.TemplateID,
			"node_ids":    resp.NodeIDs,
		},
	})
}
