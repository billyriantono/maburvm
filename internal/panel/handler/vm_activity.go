package handler

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

// logVMActivity records a VM lifecycle action to audit_logs. It is best-effort:
// a nil audit repo or a write error is swallowed so it never fails the
// underlying operation. resource_type is fixed to "vm"; details is optional.
// vmHostname returns a VM's hostname best-effort (no agent round-trip), or "" if
// it can't be resolved. Used to enrich audit details with a human-readable name,
// especially for delete where the VM row is gone afterward.
func (h *VMHandler) vmHostname(c echo.Context, vmID string) string {
	if h.service == nil || vmID == "" {
		return ""
	}
	resp, err := h.service.GetVM(c.Request().Context(), vmID, false)
	if err != nil || resp == nil || resp.VM == nil {
		return ""
	}
	return resp.VM.Hostname
}

func (h *VMHandler) logVMActivity(c echo.Context, vmID, action string, details map[string]any) {
	if h.audit == nil || vmID == "" {
		return
	}
	entry := &models.AuditLog{
		Action:       action,
		ResourceType: "vm",
		ResourceID:   &vmID,
		IPAddress:    c.RealIP(),
		UserAgent:    c.Request().UserAgent(),
		Details:      details,
	}
	if uc, ok := middleware.GetUserContext(c); ok {
		uid := uc.ID.String()
		entry.UserID = &uid
	}
	// Best-effort: ignore errors so auditing never breaks the operation.
	_ = h.audit.Create(c.Request().Context(), entry)
}

// GetVMActivity handles GET /api/v1/vms/:id/activity - returns this VM's audit
// log entries (resource_type='vm' AND resource_id=:id), newest first.
func (h *VMHandler) GetVMActivity(c echo.Context) error {
	id := c.Param("id")
	if id == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	// Same ownership guard as other /vms/:id routes.
	if !h.authorizeVM(c, id) {
		return nil
	}

	limit := 50
	if v := c.QueryParam("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	logs, err := h.audit.ListByResource(c.Request().Context(), "vm", id, limit, 0)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": "Failed to load VM activity",
		})
	}
	if logs == nil {
		logs = []models.AuditLog{}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"data": logs,
	})
}
