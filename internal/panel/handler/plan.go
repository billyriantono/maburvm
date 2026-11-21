package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"gorm.io/gorm"
)

// PlanHandler handles VPS plan endpoints.
type PlanHandler struct {
	service *service.PlanService
	// vms and net apply a plan's changes to the VMs on it.
	vms *service.VMService
	net *service.NetworkService
}

// SetRollout wires what applying a plan to its existing VMs needs.
func (h *PlanHandler) SetRollout(vms *service.VMService, net *service.NetworkService) {
	h.vms, h.net = vms, net
}

// Rollout handles GET (preview) and POST (apply) /api/v1/plans/:id/apply:
// the VMs on the plan and what bringing each to the plan's current settings
// changes. POST applies it; a VM whose size changes and is not stopped keeps
// its size and says so.
func (h *PlanHandler) Rollout(c echo.Context) error {
	if h.vms == nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{"error": "Applying a plan is not available"})
	}
	out, err := h.vms.RolloutPlan(c.Request().Context(), c.Param("id"), h.net, c.Request().Method == http.MethodPost)
	if err != nil {
		if errors.Is(err, service.ErrPlanNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Plan not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

// NewPlanHandler creates a new PlanHandler.
func NewPlanHandler(s *service.PlanService) *PlanHandler {
	return &PlanHandler{service: s}
}

// ListPlans handles GET /api/v1/plans (optionally ?active=true).
func (h *PlanHandler) ListPlans(c echo.Context) error {
	activeOnly := c.QueryParam("active") == "true"
	plans, err := h.service.ListPlans(c.Request().Context(), activeOnly)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to list plans"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": plans})
}

// GetPlan handles GET /api/v1/plans/:id.
func (h *PlanHandler) GetPlan(c echo.Context) error {
	plan, err := h.service.GetPlan(c.Request().Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, service.ErrPlanNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Plan not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": plan})
}

// CreatePlan handles POST /api/v1/plans (admin).
func (h *PlanHandler) CreatePlan(c echo.Context) error {
	var req service.PlanRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	plan, err := h.service.CreatePlan(c.Request().Context(), &req)
	if err != nil {
		if errors.Is(err, service.ErrPlanNameExists) {
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Plan name already exists"})
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": plan})
}

// UpdatePlan handles PUT /api/v1/plans/:id (admin).
func (h *PlanHandler) UpdatePlan(c echo.Context) error {
	var req service.PlanRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	plan, err := h.service.UpdatePlan(c.Request().Context(), c.Param("id"), &req)
	if err != nil {
		if errors.Is(err, service.ErrPlanNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Plan not found"})
		}
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": plan})
}

// DeletePlan handles DELETE /api/v1/plans/:id (admin).
func (h *PlanHandler) DeletePlan(c echo.Context) error {
	if err := h.service.DeletePlan(c.Request().Context(), c.Param("id")); err != nil {
		if errors.Is(err, service.ErrPlanNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Plan not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Plan deleted"})
}

// RegisterPlanRoutes registers VPS plan routes. List/get require auth; mutations require admin.
func RegisterPlanRoutes(e *echo.Echo, h *PlanHandler, db *gorm.DB) {
	g := e.Group("/api/v1/plans")
	g.Use(panelMiddleware.RequireAuth(db))
	adminOnly := panelMiddleware.RequirePermission("plan:update")
	g.GET("", h.ListPlans)
	g.GET("/:id", h.GetPlan)
	g.POST("", h.CreatePlan, adminOnly)
	g.PUT("/:id", h.UpdatePlan, adminOnly)
	g.DELETE("/:id", h.DeletePlan, adminOnly)
	// Changes customers' VMs, so it also takes the right to change VMs.
	g.GET("/:id/apply", h.Rollout, adminOnly, panelMiddleware.RequirePermission("vm:read"))
	g.POST("/:id/apply", h.Rollout, adminOnly, panelMiddleware.RequirePermission("vm:update"))
}
