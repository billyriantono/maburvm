package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// quotaService is the narrow surface the QuotaHandler depends on. *service.QuotaService
// already satisfies it, so production wiring (server.go) is unchanged, while tests can
// supply a stub without a live DB.
type quotaService interface {
	GetStatus(ctx context.Context, userID string) (*service.QuotaStatus, error)
	SetQuota(ctx context.Context, userID string, req *service.SetQuotaRequest) (*models.UserQuota, error)
}

// QuotaHandler handles per-user resource quota endpoints.
type QuotaHandler struct {
	service quotaService
}

// NewQuotaHandler creates a new QuotaHandler.
func NewQuotaHandler(s quotaService) *QuotaHandler {
	return &QuotaHandler{service: s}
}

// mapQuotaServiceError translates a service-layer error into a stable HTTP
// contract. Expected managed-account states map to 409 Conflict with a generic,
// non-leaking code/message. An absent target user maps to a generic 404 so the
// caller learns the user does not exist without leaking policy/cap/DB detail.
// Unexpected errors remain 500.
func mapQuotaServiceError(err error) (int, map[string]interface{}) {
	switch {
	case errors.Is(err, repository.ErrUserNotFound):
		// The target user does not exist (e.g. a direct legacy Upsert whose
		// authoritative users row is missing). Generic 404; no account/state
		// details leaked.
		return http.StatusNotFound, map[string]interface{}{
			"error": "User not found",
			"code":  "user_not_found",
		}
	case errors.Is(err, gorm.ErrRecordNotFound):
		// An admin quota read for a non-existent user surfaces as a bare
		// gorm.ErrRecordNotFound from the user lookup. Map it to the same
		// generic 404 as ErrUserNotFound (matched by identity, never by
		// string). This does NOT catch managed missing-row states, which the
		// service already converts to ErrQuotaNotAvailable (409).
		return http.StatusNotFound, map[string]interface{}{
			"error": "User not found",
			"code":  "user_not_found",
		}
	case errors.Is(err, service.ErrQuotaNotAvailable):
		// Managed account is pending/unprovisioned: not a server failure. No
		// policy/cap details are leaked.
		return http.StatusConflict, map[string]interface{}{
			"error": "Quota is not available for this account",
			"code":  "quota_not_available",
		}
	case errors.Is(err, repository.ErrManagedQuotaDirectMutation):
		// The direct legacy endpoint cannot alter managed accounts; the future
		// policy-assignment flow owns that. No account/state details leaked.
		return http.StatusConflict, map[string]interface{}{
			"error": "Managed quota cannot be changed through this endpoint",
			"code":  "managed_quota_direct_mutation",
		}
	case errors.Is(err, service.ErrQuotaNegative):
		// A negative quota limit is invalid input, not a server fault. Generic
		// 400; we never surface the negative value or policy/cap detail.
		return http.StatusBadRequest, map[string]interface{}{
			"error": "Quota limits must be non-negative",
			"code":  "quota_negative",
		}
	case errors.Is(err, service.ErrDiskQuotaExceeded):
		// Disk admission was rejected because the extra disk would exceed the
		// user's disk limit. This is a quota unavailability/exceeded condition,
		// NOT a generic 500, and must not leak policy/cap detail.
		return http.StatusBadRequest, map[string]interface{}{
			"error": "Disk quota exceeded",
			"code":  "disk_quota_exceeded",
		}
	default:
		return http.StatusInternalServerError, map[string]interface{}{"error": err.Error()}
	}
}

// GetMyQuota handles GET /api/v1/quota (current user's limits + usage).
func (h *QuotaHandler) GetMyQuota(c echo.Context) error {
	user, ok := panelMiddleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	status, err := h.service.GetStatus(c.Request().Context(), user.ID.String())
	if err != nil {
		code, body := mapQuotaServiceError(err)
		return c.JSON(code, body)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": status})
}

// GetUserQuota handles GET /api/v1/users/:id/quota (admin).
func (h *QuotaHandler) GetUserQuota(c echo.Context) error {
	status, err := h.service.GetStatus(c.Request().Context(), c.Param("id"))
	if err != nil {
		code, body := mapQuotaServiceError(err)
		return c.JSON(code, body)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": status})
}

// SetUserQuota handles PUT /api/v1/users/:id/quota (admin).
func (h *QuotaHandler) SetUserQuota(c echo.Context) error {
	var req service.SetQuotaRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	quota, err := h.service.SetQuota(c.Request().Context(), c.Param("id"), &req)
	if err != nil {
		code, body := mapQuotaServiceError(err)
		return c.JSON(code, body)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": quota})
}

// RegisterQuotaRoutes registers quota routes. Self-quota is any authenticated
// user; per-user quota management requires admin.
func RegisterQuotaRoutes(e *echo.Echo, h *QuotaHandler, db *gorm.DB) {
	auth := panelMiddleware.RequireAuth(db)
	adminOnly := panelMiddleware.RequirePermission("admin:access")

	e.GET("/api/v1/quota", h.GetMyQuota, auth)

	g := e.Group("/api/v1/users/:id/quota")
	g.Use(auth)
	g.GET("", h.GetUserQuota, adminOnly)
	g.PUT("", h.SetUserQuota, adminOnly)
}
