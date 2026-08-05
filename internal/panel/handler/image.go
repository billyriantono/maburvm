package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// ImageHandler exposes user-owned image endpoints (capture from a VM, list,
// delete). Ownership is enforced in the service, so the same routes serve both
// admins (all images) and clients (their own).
type ImageHandler struct {
	service *service.ImageService
}

// NewImageHandler creates a new ImageHandler.
func NewImageHandler(s *service.ImageService) *ImageHandler {
	return &ImageHandler{service: s}
}

// CreateImageRequest is the body for capturing an image from a VM.
type CreateImageRequest struct {
	VMID string `json:"vm_id" validate:"required,uuid"`
	Name string `json:"name" validate:"omitempty,max=255"`
}

// CreateImage captures a VM's disk to a standalone image.
func (h *ImageHandler) CreateImage(c echo.Context) error {
	var req CreateImageRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	if req.VMID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "vm_id is required"})
	}
	userCtx, ok := middleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}

	img, err := h.service.CreateImageFromVM(c.Request().Context(), userCtx.ID, userCtx.Role == models.RoleAdmin, req.VMID, req.Name)
	if err != nil {
		if errors.Is(err, service.ErrVMNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
	return c.JSON(http.StatusAccepted, map[string]interface{}{"data": img})
}

// ListImages returns the caller's images (all for admins).
func (h *ImageHandler) ListImages(c echo.Context) error {
	userCtx, ok := middleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	images, err := h.service.ListImages(c.Request().Context(), userCtx.ID, userCtx.Role == models.RoleAdmin)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"data": images})
}

// DeleteImage removes an image and its backing object-storage file.
func (h *ImageHandler) DeleteImage(c echo.Context) error {
	userCtx, ok := middleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	id := c.Param("id")
	if err := h.service.DeleteImage(c.Request().Context(), id, userCtx.ID, userCtx.Role == models.RoleAdmin); err != nil {
		if errors.Is(err, service.ErrImageNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "Image not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"message": "Image deleted"})
}

// RegisterImageRoutes registers image endpoints. Read requires vm:read; capture
// Capturing and deleting an image use snapshot:create / snapshot:delete — the
// permissions the roles are actually granted. They previously required
// "vm:snapshot", a string no role has ever held, so the endpoints were
// unreachable for every non-admin: the client area offered "Save as Image" and
// the request came back 403. Admins were unaffected because admin:access
// bypasses the check, which is why it survived.
func RegisterImageRoutes(e *echo.Echo, handler *ImageHandler, db *gorm.DB) {
	images := e.Group("/api/v1/images")
	images.Use(middleware.RequireAuth(db))
	images.GET("", handler.ListImages, middleware.RequirePermission("vm:read"))
	images.POST("", handler.CreateImage, middleware.RequirePermission("snapshot:create"))
	images.DELETE("/:id", handler.DeleteImage, middleware.RequirePermission("snapshot:delete"))
}
