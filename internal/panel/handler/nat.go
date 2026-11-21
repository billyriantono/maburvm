package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// NATHandler serves NAT VMs' public ports and hostnames, and nodes' NAT port
// ranges.
type NATHandler struct {
	nat   *service.NATService
	authz *authz.Authorizer
}

func NewNATHandler(nat *service.NATService, a *authz.Authorizer) *NATHandler {
	return &NATHandler{nat: nat, authz: a}
}

func natError(c echo.Context, err error) error {
	status := http.StatusInternalServerError
	var allowance *service.AllowanceError
	switch {
	case errors.Is(err, service.ErrHostnameNotFound), errors.Is(err, service.ErrNodeNotFound), err.Error() == "VM not found":
		status = http.StatusNotFound
	case errors.Is(err, service.ErrInvalidHostname), errors.Is(err, service.ErrInvalidPortRange), errors.Is(err, service.ErrNotNATVM):
		status = http.StatusBadRequest
	case errors.Is(err, service.ErrHostnameTaken), errors.Is(err, service.ErrWebPortsForwarded), errors.Is(err, service.ErrNeedsAgentUpdate),
		errors.Is(err, service.ErrHostnameUnproven), errors.As(err, &allowance):
		status = http.StatusConflict
	}
	return c.JSON(status, map[string]interface{}{"error": http.StatusText(status), "message": err.Error()})
}

// Info handles GET /api/v1/vms/:id/nat: the VM's public address, ports and
// hostnames; nat is false for a VM with an address of its own.
func (h *NATHandler) Info(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	info, err := h.nat.Info(c.Request().Context(), vmID)
	if err != nil {
		return natError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": info})
}

// AddHostname handles POST /api/v1/vms/:id/hostnames.
func (h *NATHandler) AddHostname(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	var req service.AddHostnameRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	out, err := h.nat.AddHostname(c.Request().Context(), vmID, &req)
	if err != nil {
		return natError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": out})
}

// VerifyHostname handles POST /api/v1/vms/:id/hostnames/:hostname_id/verify.
func (h *NATHandler) VerifyHostname(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	out, err := h.nat.VerifyHostname(c.Request().Context(), vmID, c.Param("hostname_id"))
	if err != nil {
		return natError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

// RemoveHostname handles DELETE /api/v1/vms/:id/hostnames/:hostname_id.
func (h *NATHandler) RemoveHostname(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	if err := h.nat.RemoveHostname(c.Request().Context(), vmID, c.Param("hostname_id")); err != nil {
		return natError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Hostname removed"})
}

// NodeNAT handles GET /api/v1/nodes/:id/nat.
func (h *NATHandler) NodeNAT(c echo.Context) error {
	out, err := h.nat.NodeNAT(c.Request().Context(), c.Param("id"))
	if err != nil {
		return natError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

// SetNodeNAT handles PUT /api/v1/nodes/:id/nat {"port_start", "port_end"}.
func (h *NATHandler) SetNodeNAT(c echo.Context) error {
	var req struct {
		PortStart int `json:"port_start"`
		PortEnd   int `json:"port_end"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	out, err := h.nat.SetNodeNAT(c.Request().Context(), c.Param("id"), req.PortStart, req.PortEnd)
	if err != nil {
		return natError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

// RegisterNATRoutes registers the NAT routes.
func RegisterNATRoutes(e *echo.Echo, h *NATHandler, db *gorm.DB) {
	vms := e.Group("/api/v1/vms")
	vms.Use(middleware.RequireAuth(db))
	vms.GET("/:id/nat", h.Info, middleware.RequirePermission("vm:read"))
	vms.POST("/:id/hostnames", h.AddHostname, middleware.RequirePermission("vm:update"))
	vms.POST("/:id/hostnames/:hostname_id/verify", h.VerifyHostname, middleware.RequirePermission("vm:update"))
	vms.DELETE("/:id/hostnames/:hostname_id", h.RemoveHostname, middleware.RequirePermission("vm:update"))

	nodes := e.Group("/api/v1/nodes")
	nodes.Use(middleware.RequireAuth(db))
	nodes.GET("/:id/nat", h.NodeNAT, middleware.RequirePermission("node:read"))
	nodes.PUT("/:id/nat", h.SetNodeNAT, middleware.RequirePermission("node:update"))
}
