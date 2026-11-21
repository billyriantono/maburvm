package handler

import (
	"errors"
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
	db      *gorm.DB
}

func NewAbuseHandler(s *service.AbuseService, audit *repository.AuditRepository, db *gorm.DB) *AbuseHandler {
	return &AbuseHandler{service: s, audit: audit, db: db}
}

// RegisterAbuseRoutes mounts the routes. Admin-only throughout: this exposes
// every guest on every node, including other tenants' machines, and quarantining
// takes a customer offline.
func RegisterAbuseRoutes(e *echo.Echo, h *AbuseHandler, db *gorm.DB) {
	g := e.Group("/api/v1/abuse")
	g.Use(middleware.RequireAuth(db))
	g.Use(middleware.RequireByMethod("network:read", "network:update"))

	g.GET("/guests", h.ListGuests)
	g.POST("/guests/quarantine", h.SetQuarantine)
	g.GET("/events", h.ListEvents)
	g.POST("/events/:id/release", h.ReleaseEvent)
	g.POST("/null-routes", h.NullRoute)
	g.GET("/policy", h.GetPolicy)
	g.PUT("/policy", h.SavePolicy)
}

// staffName is who to record as having acted.
func staffName(c echo.Context) string {
	if uc, ok := middleware.GetUserContext(c); ok {
		return uc.Email
	}
	return ""
}

// ListEvents returns the abuse timeline, newest first. ?open=true keeps the
// episodes still going on or with an action in force.
func (h *AbuseHandler) ListEvents(c echo.Context) error {
	limit, offset := listWindow(c)
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	events, total, err := h.service.ListEvents(c.Request().Context(), c.QueryParam("open") == "true", limit, offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]any{"error": "Internal Server Error", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"success": true, "data": events, "meta": OffsetPageMeta(limit, offset, total)})
}

// ReleaseEvent lifts the quarantine, rate limit or null-route of an episode.
func (h *AbuseHandler) ReleaseEvent(c echo.Context) error {
	ev, err := h.service.Release(c.Request().Context(), c.Param("id"), staffName(c))
	if err != nil {
		if errors.Is(err, service.ErrAbuseEventNotFound) {
			return c.JSON(http.StatusNotFound, map[string]any{"error": "Not Found", "message": err.Error()})
		}
		return c.JSON(http.StatusBadGateway, map[string]any{"error": "Bad Gateway", "message": err.Error()})
	}
	h.logAction(c, "abuse.release", ev.NodeID, map[string]any{"event_id": ev.ID, "action": ev.Action, "mac": ev.MAC, "targets": ev.Targets})
	return c.JSON(http.StatusOK, map[string]any{"success": true, "data": ev})
}

// NullRoute drops all traffic to one address on a node for a while.
func (h *AbuseHandler) NullRoute(c echo.Context) error {
	var in service.NullRouteInput
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "Bad Request", "message": "invalid request body"})
	}
	ev, err := h.service.NullRoute(c.Request().Context(), in, staffName(c))
	if err != nil {
		if ev == nil {
			return c.JSON(http.StatusBadRequest, map[string]any{"error": "Bad Request", "message": err.Error()})
		}
		return c.JSON(http.StatusBadGateway, map[string]any{"error": "Bad Gateway", "message": err.Error(), "data": ev})
	}
	h.logAction(c, "abuse.null_route", in.NodeID, map[string]any{"event_id": ev.ID, "ip": ev.Targets, "minutes": in.Minutes, "reason": in.Reason})
	return c.JSON(http.StatusCreated, map[string]any{"success": true, "data": ev})
}

// GetPolicy returns what guests are watched for and what is done on its own.
func (h *AbuseHandler) GetPolicy(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]any{"success": true, "data": service.LoadAbusePolicy(c.Request().Context(), h.db)})
}

// SavePolicy stores the abuse policy.
func (h *AbuseHandler) SavePolicy(c echo.Context) error {
	p := service.DefaultAbusePolicy()
	if err := c.Bind(&p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "Bad Request", "message": "invalid request body"})
	}
	if err := service.SaveAbusePolicy(c.Request().Context(), h.db, p); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "Bad Request", "message": err.Error()})
	}
	h.logAction(c, "abuse.policy", "", map[string]any{"policy": p})
	return c.JSON(http.StatusOK, map[string]any{"success": true, "data": p})
}

// logAction records an abuse action in the audit trail. resource_id is a uuid
// column, so it holds the node when there is one.
func (h *AbuseHandler) logAction(c echo.Context, action, nodeID string, details map[string]any) {
	if h.audit == nil {
		return
	}
	entry := &models.AuditLog{
		Action: action, ResourceType: "abuse", IPAddress: c.RealIP(), UserAgent: c.Request().UserAgent(), Details: details,
	}
	if nodeID != "" {
		entry.ResourceID = &nodeID
	}
	if uc, ok := middleware.GetUserContext(c); ok {
		uid := uc.ID.String()
		entry.UserID = &uid
	}
	if err := h.audit.Create(c.Request().Context(), entry); err != nil {
		c.Logger().Errorf("audit: could not record %s: %v", action, err)
	}
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
	policy := service.LoadAbusePolicy(c.Request().Context(), h.db)
	return c.JSON(http.StatusOK, map[string]any{
		"data":      guests,
		"threshold": policy.OutboundSYN.Threshold,
		"policy":    policy,
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

	if err := h.service.SetQuarantineBy(c.Request().Context(), req.NodeID, req.MAC, req.Reason, req.Quarantined, staffName(c)); err != nil {
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
	// resource_id is a uuid column, so it holds the node — the only UUID this
	// action has. The guest is identified by MAC, which lives in details.
	// Putting the MAC in resource_id looks natural and fails the insert
	// silently, which is how this trail came back empty the first time.
	nodeID := req.NodeID
	entry := &models.AuditLog{
		Action:       action,
		ResourceType: "guest_connection",
		ResourceID:   &nodeID,
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
	// Best-effort, but never silent: an audit write that fails without a trace
	// is indistinguishable from an action nobody took.
	if err := h.audit.Create(c.Request().Context(), entry); err != nil {
		c.Logger().Errorf("audit: could not record %s for %s on node %s: %v", action, req.MAC, req.NodeID, err)
	}
}
