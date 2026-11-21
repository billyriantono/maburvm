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
	OSType      string `json:"os_type"`
	// Kind is disk or iso; empty is told from the address.
	Kind string `json:"kind"`
	// ChecksumSHA256, or ChecksumURL (a SHA256SUMS-style list), is what the
	// nodes check the download against; one is required for an http(s) image
	// unless SkipChecksum confirms the publisher gives none.
	ChecksumSHA256 string `json:"checksum_sha256"`
	ChecksumURL    string `json:"checksum_url"`
	SkipChecksum   bool   `json:"skip_checksum"`
	AllowHTTP      bool   `json:"allow_http"`
	AutoRefresh    bool   `json:"auto_refresh"`
	Firmware       string `json:"firmware"`
	SecureBoot     bool   `json:"secure_boot"`
	TPM            bool   `json:"tpm"`
}

// UpdateTemplateRequest represents the request body for updating a template
type UpdateTemplateRequest struct {
	Name        string `json:"name,omitempty" validate:"omitempty,max=100"`
	Version     string `json:"version,omitempty" validate:"omitempty,max=50"`
	Description string `json:"description,omitempty" validate:"omitempty,max=500"`
	IsActive    *bool  `json:"is_active,omitempty"`
	OSType      string `json:"os_type,omitempty"`
	// Availability rules: a licensed system, and the plans it is offered on.
	Licensed *bool     `json:"licensed,omitempty"`
	PlanIDs  *[]string `json:"plan_ids,omitempty"`
	// Image integrity and boot; omitted leaves each as it is.
	ChecksumSHA256 *string `json:"checksum_sha256,omitempty"`
	ChecksumURL    *string `json:"checksum_url,omitempty"`
	AllowHTTP      *bool   `json:"allow_http,omitempty"`
	AutoRefresh    *bool   `json:"auto_refresh,omitempty"`
	Firmware       *string `json:"firmware,omitempty"`
	SecureBoot     *bool   `json:"secure_boot,omitempty"`
	TPM            *bool   `json:"tpm,omitempty"`
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

	// Reads are available to any authenticated user (clients need the catalog to
	// order a VM). Mutations — creating, editing, deleting, triggering multi-GB
	// image downloads onto nodes, or node syncs — are admin-only.
	admin := middleware.RequirePermission("template:update")

	api.GET("", h.ListTemplates)
	api.GET("/:id", h.GetTemplate)
	api.POST("", h.CreateTemplate, admin)
	api.POST("/download-url", h.DownloadFromURL, admin)
	api.PUT("/:id", h.UpdateTemplate, admin)
	api.DELETE("/:id", h.DeleteTemplate, admin)
	api.POST("/:id/sync", h.SyncTemplate, admin)
	api.POST("/:id/refresh", h.RefreshTemplate, admin)

	// ISOs uploaded to the panel, in chunks, and the drivers disc choice.
	isos := e.Group("/api/v1/isos", authMiddleware, admin)
	isos.POST("/uploads", h.StartUpload)
	isos.PUT("/uploads/:upload", h.UploadChunk)
	isos.POST("/uploads/:upload/finish", h.FinishUpload)
	isos.DELETE("/uploads/:upload", h.CancelUpload)
	isos.GET("/drivers", h.GetDriversISO)
	isos.PUT("/drivers", h.SetDriversISO)
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
		Name:           req.Name,
		Version:        req.Version,
		FileURL:        req.FileURL,
		Description:    req.Description,
		OSType:         req.OSType,
		Kind:           req.Kind,
		ChecksumSHA256: req.ChecksumSHA256,
		ChecksumURL:    req.ChecksumURL,
		SkipChecksum:   req.SkipChecksum,
		AllowHTTP:      req.AllowHTTP,
		AutoRefresh:    req.AutoRefresh,
		Firmware:       req.Firmware,
		SecureBoot:     req.SecureBoot,
		TPM:            req.TPM,
	}

	resp, err := h.templateService.CreateTemplate(c.Request().Context(), &svcReq)
	if errors.Is(err, service.ErrInvalidTemplate) {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "validation_error", Message: err.Error()})
	}
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
	// Which nodes hold a template is staff information.
	includeNodeStatus := c.QueryParam("include_nodes") == "true" && (middleware.HasPermission(c, "node:read") || middleware.HasPermission(c, "template:update"))

	// type=iso lists the ISOs, type=disk the disk images; none lists both.
	kind := c.QueryParam("type")
	if kind != "" && kind != models.TemplateKindISO && kind != models.TemplateKindDisk {
		kind = ""
	}
	templates, err := h.templateService.ListTemplatesOfKind(c.Request().Context(), limit, offset, includeNodeStatus, kind)
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

	// Which nodes hold a template is staff information.
	includeNodeStatus := c.QueryParam("include_nodes") == "true" && (middleware.HasPermission(c, "node:read") || middleware.HasPermission(c, "template:update"))

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
		Name:           req.Name,
		Version:        req.Version,
		Description:    req.Description,
		IsActive:       req.IsActive,
		OSType:         req.OSType,
		Licensed:       req.Licensed,
		PlanIDs:        req.PlanIDs,
		ChecksumSHA256: req.ChecksumSHA256,
		ChecksumURL:    req.ChecksumURL,
		AllowHTTP:      req.AllowHTTP,
		AutoRefresh:    req.AutoRefresh,
		Firmware:       req.Firmware,
		SecureBoot:     req.SecureBoot,
		TPM:            req.TPM,
	}

	template, err := h.templateService.UpdateTemplate(c.Request().Context(), id, &svcReq)
	if errors.Is(err, service.ErrInvalidTemplate) {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "validation_error", Message: err.Error()})
	}
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
		// The ISO's sha256, or a checksum list to read it from; or
		// skip_checksum when the publisher gives none.
		ChecksumSHA256 string `json:"checksum_sha256"`
		ChecksumURL    string `json:"checksum_url"`
		SkipChecksum   bool   `json:"skip_checksum"`
		OSType         string `json:"os_type"`
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
		Name:           req.Filename,
		Version:        "1.0",
		FileURL:        req.URL,
		OSType:         req.OSType,
		Kind:           models.TemplateKindISO,
		ChecksumSHA256: req.ChecksumSHA256,
		ChecksumURL:    req.ChecksumURL,
		SkipChecksum:   req.SkipChecksum,
	}

	resp, err := h.templateService.CreateTemplate(c.Request().Context(), &svcReq)
	if err != nil {
		// If already exists, that's fine — just report success
		if err == service.ErrTemplateAlreadyExists {
			return c.JSON(http.StatusOK, SuccessResponse{
				Message: "Template already exists, download skipped",
			})
		}
		if errors.Is(err, service.ErrInvalidTemplate) {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "validation_error", Message: err.Error()})
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

// RefreshTemplate handles POST /api/v1/templates/:id/refresh: read the
// checksum list again and, when the image changed, have every node fetch it.
// {"force": true} fetches onto the nodes even when nothing changed.
func (h *TemplateHandler) RefreshTemplate(c echo.Context) error {
	var req struct {
		Force bool `json:"force"`
	}
	_ = c.Bind(&req)
	res, err := h.templateService.RefreshTemplate(c.Request().Context(), c.Param("id"), req.Force)
	switch {
	case errors.Is(err, service.ErrTemplateNotFound):
		return c.JSON(http.StatusNotFound, ErrorResponse{Error: "not_found", Message: "Template not found"})
	case errors.Is(err, service.ErrInvalidTemplate):
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "validation_error", Message: err.Error()})
	case err != nil:
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "internal_error", Message: err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": res})
}

// StartUpload handles POST /api/v1/isos/uploads: {name, size, sha256?, os_type?}.
func (h *TemplateHandler) StartUpload(c echo.Context) error {
	var req struct {
		Name   string `json:"name"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
		OSType string `json:"os_type"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "invalid_request", Message: "Failed to parse request body"})
	}
	userID := ""
	if u, ok := middleware.GetUserContext(c); ok {
		userID = u.ID.String()
	}
	id, err := h.templateService.StartImageUpload(req.Name, req.Size, req.SHA256, req.OSType, userID)
	if err != nil {
		return uploadError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": map[string]interface{}{"upload_id": id, "chunk_bytes": isoUploadChunk}})
}

// isoUploadChunk is the chunk size the web page sends, under the body limit.
const isoUploadChunk = 8 << 20

// UploadChunk handles PUT /api/v1/isos/uploads/:upload?offset=N with the
// chunk's bytes as the body.
func (h *TemplateHandler) UploadChunk(c echo.Context) error {
	offset, err := strconv.ParseInt(c.QueryParam("offset"), 10, 64)
	if err != nil || offset < 0 {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "validation_error", Message: "offset is required"})
	}
	next, err := h.templateService.WriteImageChunk(c.Param("upload"), offset, c.Request().Body)
	if err != nil {
		if errors.Is(err, service.ErrInvalidTemplate) {
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": "conflict", "message": err.Error(), "data": map[string]int64{"offset": next}})
		}
		return uploadError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]int64{"offset": next}})
}

// FinishUpload handles POST /api/v1/isos/uploads/:upload/finish.
func (h *TemplateHandler) FinishUpload(c echo.Context) error {
	t, err := h.templateService.FinishImageUpload(c.Request().Context(), c.Param("upload"))
	if err != nil {
		if errors.Is(err, service.ErrTemplateAlreadyExists) {
			return c.JSON(http.StatusConflict, ErrorResponse{Error: "already_exists", Message: "This ISO is uploaded already"})
		}
		return uploadError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": t})
}

// CancelUpload handles DELETE /api/v1/isos/uploads/:upload.
func (h *TemplateHandler) CancelUpload(c echo.Context) error {
	if err := h.templateService.CancelImageUpload(c.Param("upload")); err != nil {
		return uploadError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true})
}

func uploadError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrUploadNotFound):
		return c.JSON(http.StatusNotFound, ErrorResponse{Error: "not_found", Message: err.Error()})
	case errors.Is(err, service.ErrInvalidTemplate):
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "validation_error", Message: err.Error()})
	}
	return c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "internal_error", Message: err.Error()})
}

// GetDriversISO handles GET /api/v1/isos/drivers.
func (h *TemplateHandler) GetDriversISO(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": service.LoadImageSettings(c.Request().Context(), h.templateService.DB())})
}

// SetDriversISO handles PUT /api/v1/isos/drivers: {drivers_iso_template_id}.
func (h *TemplateHandler) SetDriversISO(c echo.Context) error {
	var req service.ImageSettings
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "invalid_request", Message: "Failed to parse request body"})
	}
	if err := service.SetDriversISO(c.Request().Context(), h.templateService.DB(), req.DriversISOTemplateID); err != nil {
		if errors.Is(err, service.ErrTemplateNotFound) || errors.Is(err, service.ErrInvalidTemplate) {
			return c.JSON(http.StatusBadRequest, ErrorResponse{Error: "validation_error", Message: err.Error()})
		}
		return c.JSON(http.StatusInternalServerError, ErrorResponse{Error: "internal_error", Message: err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": req})
}
