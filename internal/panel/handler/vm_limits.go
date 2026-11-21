package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// limitsStaffOnly answers anyone but an administrator. Returns true when it
// has answered.
func (h *VMHandler) limitsStaffOnly(c echo.Context) bool {
	if !h.authorizeVM(c, c.Param("id")) {
		return true
	}
	if u, ok := middleware.GetUserContext(c); !ok || u.Role != models.RoleAdmin {
		_ = c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "Only an administrator can change a VM's CPU and disk limits."})
		return true
	}
	return false
}

func limitsReply(c echo.Context, res *service.LimitsResult, err error) error {
	switch {
	case err == nil:
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": res})
	case errors.Is(err, service.ErrVMNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
	case errors.Is(err, service.ErrInvalidLimits):
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	case errors.Is(err, service.ErrVMLimitsBusy):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
}

// SetVMLimits handles PUT /api/v1/vms/:id/limits {"limits": {...}}: the VM's
// own CPU and disk caps, applied by its node at once.
func (h *VMHandler) SetVMLimits(c echo.Context) error {
	if h.limitsStaffOnly(c) {
		return nil
	}
	var req struct {
		Limits *models.VMLimits `json:"limits"`
	}
	if err := c.Bind(&req); err != nil || req.Limits == nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "limits is required"})
	}
	res, err := h.service.SetVMLimits(c.Request().Context(), c.Param("id"), *req.Limits)
	return limitsReply(c, res, err)
}

// ApplyVMLimits handles POST /api/v1/vms/:id/limits/apply: send the VM's
// limits to its node again.
func (h *VMHandler) ApplyVMLimits(c echo.Context) error {
	if h.limitsStaffOnly(c) {
		return nil
	}
	res, err := h.service.ApplyVMLimits(c.Request().Context(), c.Param("id"))
	return limitsReply(c, res, err)
}

// ThrottleVM handles POST /api/v1/vms/:id/throttle {"cpu_cap_percent", "iops",
// "mbps", "cpu_weight"}: an operator's caps on top of the VM's own, until
// lifted.
func (h *VMHandler) ThrottleVM(c echo.Context) error {
	if h.limitsStaffOnly(c) {
		return nil
	}
	var t models.VMLimits
	if err := c.Bind(&t); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	res, err := h.service.ThrottleVM(c.Request().Context(), c.Param("id"), t)
	return limitsReply(c, res, err)
}

// UnthrottleVM handles DELETE /api/v1/vms/:id/throttle.
func (h *VMHandler) UnthrottleVM(c echo.Context) error {
	if h.limitsStaffOnly(c) {
		return nil
	}
	res, err := h.service.UnthrottleVM(c.Request().Context(), c.Param("id"))
	return limitsReply(c, res, err)
}
