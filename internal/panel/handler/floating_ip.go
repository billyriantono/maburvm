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

// FloatingIPHandler exposes floating (elastic) IPs: addresses that live on the
// host and are 1:1-NATed to a VM, so they can be moved between VMs on a node and
// survive the deletion of the VM they were attached to.
type FloatingIPHandler struct {
	service *service.VMService
}

func NewFloatingIPHandler(s *service.VMService) *FloatingIPHandler {
	return &FloatingIPHandler{service: s}
}

func RegisterFloatingIPRoutes(e *echo.Echo, h *FloatingIPHandler, db *gorm.DB) {
	g := e.Group("/api/v1/floating-ips")
	g.Use(middleware.RequireAuth(db))
	g.GET("", h.List, middleware.RequirePermission("network:read"))
	// Allocating and releasing consume a node's public address space, so they
	// stay admin-only; attach/detach is a tenant action on an address they own.
	g.POST("", h.Allocate, middleware.RequirePermission("admin:access"))
	g.DELETE("/:id", h.Release, middleware.RequirePermission("admin:access"))
	g.POST("/:id/attach", h.Attach, middleware.RequirePermission("network:update"))
	g.POST("/:id/detach", h.Detach, middleware.RequirePermission("network:update"))
}

// authorizeFloatingIP enforces tenant isolation on a floating IP. Admins may act
// on any address; everyone else only on floating IPs they own. Like the VM
// guard, an unowned address answers 404 rather than 403 so ownership can't be
// probed.
func (h *FloatingIPHandler) authorizeFloatingIP(c echo.Context, addressID string) bool {
	userCtx, ok := middleware.GetUserContext(c)
	if !ok {
		_ = c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized", "message": "authentication required"})
		return false
	}
	if userCtx.Role == models.RoleAdmin {
		return true
	}
	addr, err := h.service.GetFloatingIP(c.Request().Context(), addressID)
	if err != nil || addr.UserID == nil || *addr.UserID != userCtx.ID.String() {
		_ = c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "floating IP not found"})
		return false
	}
	return true
}

func (h *FloatingIPHandler) List(c echo.Context) error {
	userCtx, ok := middleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	owner := ""
	if userCtx.Role != models.RoleAdmin {
		owner = userCtx.ID.String()
	}
	addrs, err := h.service.ListFloatingIPs(c.Request().Context(), owner)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": addrs})
}

func (h *FloatingIPHandler) Allocate(c echo.Context) error {
	var req struct {
		PoolID      string `json:"pool_id"`
		NodeID      string `json:"node_id,omitempty"`
		UserID      string `json:"user_id,omitempty"`
		RequestedIP string `json:"requested_ip,omitempty"`
	}
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "Invalid request body")
	}
	if req.PoolID == "" {
		return badRequest(c, "pool_id is required")
	}
	// Default the owner to the caller so an admin allocating for themselves
	// doesn't create an ownerless address.
	if req.UserID == "" {
		if userCtx, ok := middleware.GetUserContext(c); ok {
			req.UserID = userCtx.ID.String()
		}
	}
	addr, err := h.service.AllocateFloatingIP(c.Request().Context(), req.PoolID, req.NodeID, req.UserID, req.RequestedIP)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrIPPoolNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "IP pool not found"})
		case errors.Is(err, service.ErrNoAvailableIPAddress), errors.Is(err, service.ErrIPAddressNotFound):
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": err.Error()})
		default:
			return badRequest(c, err.Error())
		}
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": addr})
}

func (h *FloatingIPHandler) Attach(c echo.Context) error {
	if !h.authorizeFloatingIP(c, c.Param("id")) {
		return nil
	}
	var req struct {
		VMID    string `json:"vm_id"`
		NATMode string `json:"nat_mode,omitempty"`
	}
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "Invalid request body")
	}
	if req.VMID == "" {
		return badRequest(c, "vm_id is required")
	}
	// A tenant may only point their floating IP at their own VM.
	if userCtx, ok := middleware.GetUserContext(c); ok && userCtx.Role != models.RoleAdmin {
		owner, err := h.service.GetVMOwner(c.Request().Context(), req.VMID)
		if err != nil || owner != userCtx.ID.String() {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		}
	}
	addr, err := h.service.AttachFloatingIP(c.Request().Context(), c.Param("id"), req.VMID, req.NATMode)
	if err != nil {
		return floatingIPError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": addr})
}

func (h *FloatingIPHandler) Detach(c echo.Context) error {
	if !h.authorizeFloatingIP(c, c.Param("id")) {
		return nil
	}
	addr, err := h.service.DetachFloatingIP(c.Request().Context(), c.Param("id"))
	if err != nil {
		return floatingIPError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": addr})
}

func (h *FloatingIPHandler) Release(c echo.Context) error {
	if err := h.service.ReleaseFloatingIP(c.Request().Context(), c.Param("id")); err != nil {
		return floatingIPError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func floatingIPError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrIPAddressNotFound), errors.Is(err, service.ErrNotAFloatingIP), errors.Is(err, service.ErrVMNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": err.Error()})
	case errors.Is(err, service.ErrFloatingIPInUse), errors.Is(err, service.ErrFloatingIPWrongNode):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": err.Error()})
	case errors.Is(err, service.ErrFloatingIPNoPoolBridge), errors.Is(err, service.ErrVMHasNoAddress),
		errors.Is(err, service.ErrFullNATNeedsPrivateVM):
		return badRequest(c, err.Error())
	default:
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
}
