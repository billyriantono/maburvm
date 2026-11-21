package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// SharedPoolHandler serves shared storage pools. Staff only: the storage
// permissions are held by no customer role.
type SharedPoolHandler struct {
	svc *service.SharedPoolService
}

// RegisterSharedPoolRoutes registers the shared storage pool routes.
func RegisterSharedPoolRoutes(e *echo.Echo, svc *service.SharedPoolService, db *gorm.DB) {
	h := &SharedPoolHandler{svc: svc}
	g := e.Group("/api/v1/storage/shared-pools", middleware.RequireAuth(db))
	g.GET("", h.List, middleware.RequirePermission("node:read"))
	g.POST("", h.Create, middleware.RequirePermission("storage:create"))
	g.DELETE("/:id", h.Delete, middleware.RequirePermission("storage:delete"))
	g.POST("/:id/members", h.AddMember, middleware.RequirePermission("storage:update"))
	g.DELETE("/:id/members/:nodeId", h.RemoveMember, middleware.RequirePermission("storage:update"))
	g.POST("/:id/check", h.Check, middleware.RequirePermission("storage:update"))
}

func sharedPoolError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrSharedPoolNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "Shared storage pool or member not found"})
	case errors.Is(err, service.ErrNodeNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "Node not found"})
	case errors.Is(err, service.ErrSharedPool):
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
	return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
}

// List handles GET /storage/shared-pools: each pool once, with its members.
func (h *SharedPoolHandler) List(c echo.Context) error {
	pools, err := h.svc.List(c.Request().Context())
	if err != nil {
		return sharedPoolError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": pools})
}

// Create handles POST /storage/shared-pools. No node is touched.
func (h *SharedPoolHandler) Create(c echo.Context) error {
	var in models.SharedStoragePool
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "invalid request body"})
	}
	p, err := h.svc.Create(c.Request().Context(), in)
	if err != nil {
		return sharedPoolError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": p})
}

// Delete handles DELETE /storage/shared-pools/:id, for a pool with no members.
func (h *SharedPoolHandler) Delete(c echo.Context) error {
	if err := h.svc.Delete(c.Request().Context(), c.Param("id")); err != nil {
		return sharedPoolError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// AddMember handles POST /storage/shared-pools/:id/members {node_id}: the
// node mounts the pool and proves it sees the same storage as the others.
func (h *SharedPoolHandler) AddMember(c echo.Context) error {
	var req struct {
		NodeID string `json:"node_id"`
	}
	if err := c.Bind(&req); err != nil || req.NodeID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "node_id is required"})
	}
	if err := h.svc.AddMember(c.Request().Context(), c.Param("id"), req.NodeID); err != nil {
		return sharedPoolError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "The node is a member of the pool"})
}

// RemoveMember handles DELETE /storage/shared-pools/:id/members/:nodeId.
func (h *SharedPoolHandler) RemoveMember(c echo.Context) error {
	if err := h.svc.RemoveMember(c.Request().Context(), c.Param("id"), c.Param("nodeId")); err != nil {
		return sharedPoolError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Check handles POST /storage/shared-pools/:id/check: every member is proved
// against the pool now. data maps node ids to what is wrong ("" passes).
func (h *SharedPoolHandler) Check(c echo.Context) error {
	res, err := h.svc.Check(c.Request().Context(), c.Param("id"))
	if err != nil {
		return sharedPoolError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": res})
}
