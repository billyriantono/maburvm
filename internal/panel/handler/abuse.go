package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// AbuseHandler exposes which guests are opening new outbound connections fast
// enough to be worth an operator's attention, and lets one be cut off.
type AbuseHandler struct {
	service *service.AbuseService
	audit   *repository.AuditRepository
}

func NewAbuseHandler(s *service.AbuseService, audit *repository.AuditRepository) *AbuseHandler {
	return &AbuseHandler{service: s, audit: audit}
}

// RegisterAbuseRoutes mounts the routes. Admin-only throughout: this exposes
// every guest on every node, including other tenants' machines, and quarantining
// takes a customer offline.
func RegisterAbuseRoutes(e *echo.Echo, h *AbuseHandler, db *gorm.DB) {
	g := e.Group("/api/v1/abuse")
	g.Use(middleware.RequireAuth(db))
	g.Use(middleware.RequirePermission("admin:access"))

	g.GET("/guests", h.ListGuests)
	g.POST("/guests/quarantine", h.SetQuarantine)
}

// ListGuests returns guests worst-first. It defaults to flagged guests only,
// because on a healthy fleet every guest appears with a rate near zero and that
// noise buries the row that matters; ?all=true shows everything.
func (h *AbuseHandler) ListGuests(c echo.Context) error {
	flaggedOnly := c.QueryParam("all") != "true"

	guests, err := h.service.List(c.Request().Context(), flaggedOnly)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}
	if guests == nil {
		guests = []models.GuestConnection{}
	}
	return c.JSON(http.StatusOK, map[string]any{
		"data":      guests,
		"threshold": models.AbuseSYNRateThreshold,
		"success":   true,
	})
}

type setQuarantineRequest struct {
	NodeID      string `json:"node_id"`
	MAC         string `json:"mac"`
	Quarantined bool   `json:"quarantined"`
	Reason      string `json:"reason"`
}

// SetQuarantine cuts a guest off the network or puts it back. The guest keeps
// running either way — this is deliberately not a power action, so a mistaken
// call costs connectivity rather than the customer's data.
func (h *AbuseHandler) SetQuarantine(c echo.Context) error {
	var req setQuarantineRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"error":   "Bad Request",
			"message": "invalid request body",
		})
	}
	if req.NodeID == "" || req.MAC == "" {
		return c.JSON(http.StatusBadRequest, map[string]any{
			"error":   "Bad Request",
			"message": "node_id and mac are required",
		})
	}

	if err := h.service.SetQuarantine(c.Request().Context(), req.NodeID, req.MAC, req.Reason, req.Quarantined); err != nil {
		return c.JSON(http.StatusBadGateway, map[string]any{
			"error":   "Bad Gateway",
			"message": err.Error(),
		})
	}

	h.logQuarantine(c, req)
	return c.JSON(http.StatusOK, map[string]any{"success": true})
}

// logQuarantine records who cut a guest off and why. Best-effort, like the rest
// of the audit trail — but it matters more here than most: this action takes a
// paying customer offline, and "who did this and when" is the first question
// asked afterwards.
func (h *AbuseHandler) logQuarantine(c echo.Context, req setQuarantineRequest) {
	if h.audit == nil {
		return
	}
	action := "guest.quarantine"
	if !req.Quarantined {
		action = "guest.quarantine.release"
	}
	mac := req.MAC
	entry := &models.AuditLog{
		Action:       action,
		ResourceType: "guest_connection",
		ResourceID:   &mac,
		IPAddress:    c.RealIP(),
		UserAgent:    c.Request().UserAgent(),
		Details: map[string]any{
			"node_id": req.NodeID,
			"mac":     req.MAC,
			"reason":  req.Reason,
		},
	}
	if uc, ok := middleware.GetUserContext(c); ok {
		uid := uc.ID.String()
		entry.UserID = &uid
	}
	_ = h.audit.Create(c.Request().Context(), entry)
}
