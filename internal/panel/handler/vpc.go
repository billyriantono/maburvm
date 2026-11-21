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

// VPCHandler exposes tenant-owned private networks. Customers pick their own
// subnet; two customers may pick the SAME one, because each VPC's gateway lives
// in its own router namespace on the node. Only a customer's own VPCs are
// checked against each other for overlap.
type VPCHandler struct {
	service *service.VPCService
	db      *gorm.DB
}

func NewVPCHandler(s *service.VPCService) *VPCHandler { return &VPCHandler{service: s} }

func RegisterVPCRoutes(e *echo.Echo, h *VPCHandler, db *gorm.DB) {
	h.db = db
	g := e.Group("/api/v1/vpcs")
	g.Use(middleware.RequireAuth(db))
	g.GET("", h.List, middleware.RequirePermission("vpc:read"))
	g.GET("/:id", h.Get, middleware.RequirePermission("vpc:read"))
	g.POST("", h.Create, middleware.RequirePermission("vpc:create"))
	g.PUT("/:id", h.Update, middleware.RequirePermission("vpc:update"))
	g.DELETE("/:id", h.Delete, middleware.RequirePermission("vpc:delete"))
}

// UpdateVPCRequest changes a VPC's resolvers.
type UpdateVPCRequest struct {
	Nameservers *string `json:"nameservers"`
}

// Update handles PUT /api/v1/vpcs/:id {nameservers}.
func (h *VPCHandler) Update(c echo.Context) error {
	owner, ok := h.scope(c)
	if !ok {
		return nil
	}
	var req UpdateVPCRequest
	if err := c.Bind(&req); err != nil || req.Nameservers == nil {
		return badRequest(c, "nameservers is required")
	}
	vpc, err := h.service.SetNameservers(c.Request().Context(), owner, c.Param("id"), *req.Nameservers)
	if err != nil {
		return vpcError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": vpc})
}

// scope returns the owner id to filter by: empty for admins (all VPCs), the
// account the caller acts in otherwise. ok false: the answer is written.
func (h *VPCHandler) scope(c echo.Context) (string, bool) {
	_, account, staff, ok := actingAccount(c, h.db)
	if staff {
		return "", ok
	}
	return account, ok
}

func (h *VPCHandler) List(c echo.Context) error {
	owner, ok := h.scope(c)
	if !ok {
		return nil
	}
	vpcs, err := h.service.List(c.Request().Context(), owner)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": vpcs})
}

func (h *VPCHandler) Get(c echo.Context) error {
	owner, ok := h.scope(c)
	if !ok {
		return nil
	}
	vpc, err := h.service.Get(c.Request().Context(), owner, c.Param("id"))
	if err != nil {
		return vpcError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": vpc})
}

func (h *VPCHandler) Create(c echo.Context) error {
	userCtx, account, _, ok := actingAccount(c, h.db)
	if !ok {
		return nil
	}
	var req service.CreateVPCRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "Invalid request body")
	}
	// Only an administrator may pin a VPC to a specific node; for a customer the
	// placement is ours to choose and revealing node ids would leak topology.
	if userCtx.Role != models.RoleAdmin {
		req.NodeID = ""
	}
	vpc, err := h.service.Create(c.Request().Context(), account, &req)
	if err != nil {
		return vpcError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": vpc})
}

func (h *VPCHandler) Delete(c echo.Context) error {
	owner, ok := h.scope(c)
	if !ok {
		return nil
	}
	if err := h.service.Delete(c.Request().Context(), owner, c.Param("id")); err != nil {
		return vpcError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func vpcError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrVPCNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VPC not found"})
	case errors.Is(err, service.ErrVPCSubnetOverlap), errors.Is(err, service.ErrVPCInUse):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": err.Error()})
	case errors.Is(err, service.ErrVPCQuotaExceeded):
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": err.Error()})
	default:
		return badRequest(c, err.Error())
	}
}
