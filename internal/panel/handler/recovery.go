package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// Recovering a failed node's VMs on other nodes. Staff only: no customer role
// holds node:recover.

func recoveryError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrRecoveryRefused):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
	case errors.Is(err, service.ErrNoLeftover):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": err.Error()})
	}
	return migrationError(c, err)
}

type recoveryHandler struct {
	svc *service.MigrationService
}

// plan handles GET /nodes/:id/recovery: the node's VMs, where each could be
// recovered, and the recoveries made from it. It changes nothing.
func (h *recoveryHandler) plan(c echo.Context) error {
	plan, err := h.svc.RecoveryPlanFor(c.Request().Context(), c.Param("id"))
	if err != nil {
		return recoveryError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": plan})
}

// start handles POST /nodes/:id/recovery.
func (h *recoveryHandler) start(c echo.Context) error {
	var req service.RecoveryRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	req.RequestedBy, req.IP, req.UserAgent = requesterID(c), c.RealIP(), c.Request().UserAgent()
	out, err := h.svc.StartRecovery(c.Request().Context(), c.Param("id"), req)
	if err != nil {
		return recoveryError(c, err)
	}
	return c.JSON(http.StatusAccepted, map[string]interface{}{"success": true, "data": out})
}

// leftovers handles GET /nodes/:id/recovery/leftovers: what the node, now
// back, still holds of the VMs recovered from it. Asking tells the node again
// not to run them.
func (h *recoveryHandler) leftovers(c echo.Context) error {
	left, err := h.svc.FenceNode(c.Request().Context(), c.Param("id"))
	if err != nil {
		return recoveryError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": left})
}

// removeLeftover handles POST /nodes/:id/recovery/leftovers/:vm_id/remove.
func (h *recoveryHandler) removeLeftover(c echo.Context) error {
	var req struct {
		ForceStop bool `json:"force_stop"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	out, err := h.svc.RemoveLeftover(c.Request().Context(), c.Param("id"), c.Param("vm_id"), req.ForceStop, requesterID(c), c.RealIP(), c.Request().UserAgent())
	if err != nil {
		return recoveryError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"removed": out.GetRemoved(), "kept": out.GetKept()}})
}

// RegisterRecoveryRoutes registers the routes for recovering a failed node's VMs.
func RegisterRecoveryRoutes(e *echo.Echo, svc *service.MigrationService, db *gorm.DB) {
	h := &recoveryHandler{svc: svc}
	auth := middleware.RequireAuth(db)
	can := middleware.RequirePermission("node:recover")
	e.GET("/api/v1/nodes/:id/recovery", h.plan, auth, middleware.RequirePermission("node:read"))
	e.POST("/api/v1/nodes/:id/recovery", h.start, auth, can)
	e.GET("/api/v1/nodes/:id/recovery/leftovers", h.leftovers, auth, can)
	e.POST("/api/v1/nodes/:id/recovery/leftovers/:vm_id/remove", h.removeLeftover, auth, can)
}
