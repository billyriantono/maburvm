package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// CephHandler serves the Ceph clusters staff register. Staff only: the
// storage permissions are held by no customer role. No answer carries a
// cluster's key.
type CephHandler struct {
	svc *service.CephService
}

// RegisterCephRoutes registers the Ceph cluster routes.
func RegisterCephRoutes(e *echo.Echo, svc *service.CephService, db *gorm.DB) {
	h := &CephHandler{svc: svc}
	g := e.Group("/api/v1/storage/ceph-clusters", middleware.RequireAuth(db))
	g.GET("", h.List, middleware.RequirePermission("node:read"))
	g.POST("", h.Create, middleware.RequirePermission("storage:create"))
	g.PUT("/:id", h.Update, middleware.RequirePermission("storage:update"))
	g.DELETE("/:id", h.Delete, middleware.RequirePermission("storage:delete"))
	g.POST("/:id/nodes", h.EnableNode, middleware.RequirePermission("storage:update"))
	g.DELETE("/:id/nodes/:nodeId", h.DisableNode, middleware.RequirePermission("storage:update"))
	g.POST("/:id/check", h.Check, middleware.RequirePermission("storage:update"))
}

func cephError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrCephNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "Ceph cluster not found, or not enabled on that node"})
	case errors.Is(err, service.ErrNodeNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "Node not found"})
	case errors.Is(err, service.ErrCeph):
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
	return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
}

// List handles GET /storage/ceph-clusters: each cluster with its health,
// capacity, nodes and RBD pools.
func (h *CephHandler) List(c echo.Context) error {
	clusters, err := h.svc.List(c.Request().Context())
	if err != nil {
		return cephError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": clusters})
}

// Create handles POST /storage/ceph-clusters. No node is touched.
func (h *CephHandler) Create(c echo.Context) error {
	var in service.CephClusterInput
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "invalid request body"})
	}
	cluster, err := h.svc.Create(c.Request().Context(), in)
	if err != nil {
		return cephError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": cluster})
}

// Update handles PUT /storage/ceph-clusters/:id. An empty client_key keeps
// the stored key.
func (h *CephHandler) Update(c echo.Context) error {
	var in service.CephClusterInput
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "invalid request body"})
	}
	cluster, err := h.svc.Update(c.Request().Context(), c.Param("id"), in)
	if err != nil {
		return cephError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": cluster})
}

// Delete handles DELETE /storage/ceph-clusters/:id, for a cluster with no
// RBD pools and no nodes.
func (h *CephHandler) Delete(c echo.Context) error {
	if err := h.svc.Delete(c.Request().Context(), c.Param("id")); err != nil {
		return cephError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// EnableNode handles POST /storage/ceph-clusters/:id/nodes {node_id}: the
// node gets the cluster's conf, keyring and libvirt secret, and proves it
// reaches the same cluster as the others.
func (h *CephHandler) EnableNode(c echo.Context) error {
	var req struct {
		NodeID string `json:"node_id"`
	}
	if err := c.Bind(&req); err != nil || req.NodeID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "node_id is required"})
	}
	if err := h.svc.EnableNode(c.Request().Context(), c.Param("id"), req.NodeID); err != nil {
		return cephError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "The cluster is enabled on the node"})
}

// DisableNode handles DELETE /storage/ceph-clusters/:id/nodes/:nodeId.
func (h *CephHandler) DisableNode(c echo.Context) error {
	if err := h.svc.DisableNode(c.Request().Context(), c.Param("id"), c.Param("nodeId")); err != nil {
		return cephError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

// Check handles POST /storage/ceph-clusters/:id/check: every node is asked
// now. data maps node ids to what is wrong ("" passes).
func (h *CephHandler) Check(c echo.Context) error {
	res, err := h.svc.Check(c.Request().Context(), c.Param("id"))
	if err != nil {
		return cephError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": res})
}
