package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// FirewallGroupHandler serves firewall groups and their attachment to VMs.
type FirewallGroupHandler struct {
	groups *service.FirewallGroupService
	authz  *authz.Authorizer
	db     *gorm.DB
}

func NewFirewallGroupHandler(groups *service.FirewallGroupService, a *authz.Authorizer) *FirewallGroupHandler {
	return &FirewallGroupHandler{groups: groups, authz: a}
}

// groupViewer is who is asking, as the service needs it: a customer as the
// account they act in (their own, or a team account with ?account_id=).
func (h *FirewallGroupHandler) groupViewer(c echo.Context) (service.GroupViewer, bool) {
	u, account, staff, ok := actingAccount(c, h.db)
	if !ok {
		return service.GroupViewer{}, false
	}
	return service.GroupViewer{UserID: account, Staff: staff, Manage: staff && u.Can("firewall_group:update")}, true
}

// vmGroupViewer is the viewer for a change to a VM's groups, made once the VM
// is authorized: a customer acts as the VM's account, whose groups go on it.
func (h *FirewallGroupHandler) vmGroupViewer(c echo.Context, vmID string) (service.GroupViewer, bool) {
	u, ok := middleware.GetUserContext(c)
	if !ok {
		_ = c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
		return service.GroupViewer{}, false
	}
	if u.Role == models.RoleAdmin {
		return service.GroupViewer{UserID: u.ID.String(), Staff: true, Manage: u.Can("firewall_group:update")}, true
	}
	var owner struct{ UserID string }
	if err := h.db.WithContext(c.Request().Context()).Table("vms").Select("user_id").Where("id = ?", vmID).Scan(&owner).Error; err != nil || owner.UserID == "" {
		_ = c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
		return service.GroupViewer{}, false
	}
	return service.GroupViewer{UserID: owner.UserID}, true
}

// groupError answers a service error in words.
func groupError(c echo.Context, err error) error {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, service.ErrFirewallGroupNotFound), errors.Is(err, service.ErrFirewallRuleNotFound):
		status = http.StatusNotFound
	case errors.Is(err, service.ErrFirewallGroupForbidden):
		status = http.StatusForbidden
	case errors.Is(err, service.ErrFirewallGroupInUse), errors.Is(err, service.ErrFirewallGroupAttached), errors.Is(err, service.ErrFirewallRuleLimit):
		status = http.StatusConflict
	case errors.Is(err, service.ErrInvalidFirewallGroup), errors.Is(err, service.ErrInvalidFirewallRule):
		status = http.StatusBadRequest
	case err.Error() == "VM not found":
		status = http.StatusNotFound
	}
	return c.JSON(status, map[string]interface{}{"error": http.StatusText(status), "message": err.Error()})
}

// List handles GET /api/v1/firewall-groups (?scope=global: only the groups
// staff manage for everyone).
func (h *FirewallGroupHandler) List(c echo.Context) error {
	v, ok := h.groupViewer(c)
	if !ok {
		return nil
	}
	out, err := h.groups.List(c.Request().Context(), v, c.QueryParam("scope") == "global")
	if err != nil {
		return groupError(c, err)
	}
	if out == nil {
		out = []service.FirewallGroupSummary{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

// Get handles GET /api/v1/firewall-groups/:id.
func (h *FirewallGroupHandler) Get(c echo.Context) error {
	v, ok := h.groupViewer(c)
	if !ok {
		return nil
	}
	out, err := h.groups.Get(c.Request().Context(), v, c.Param("id"))
	if err != nil {
		return groupError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

type groupRequest struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Create handles POST /api/v1/firewall-groups.
func (h *FirewallGroupHandler) Create(c echo.Context) error {
	v, ok := h.groupViewer(c)
	if !ok {
		return nil
	}
	var req groupRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	g, err := h.groups.Create(c.Request().Context(), v, req.Name, req.Description)
	if err != nil {
		return groupError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": g})
}

// Update handles PUT /api/v1/firewall-groups/:id.
func (h *FirewallGroupHandler) Update(c echo.Context) error {
	v, ok := h.groupViewer(c)
	if !ok {
		return nil
	}
	var req groupRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	g, err := h.groups.Update(c.Request().Context(), v, c.Param("id"), req.Name, req.Description)
	if err != nil {
		return groupError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": g})
}

// Delete handles DELETE /api/v1/firewall-groups/:id.
func (h *FirewallGroupHandler) Delete(c echo.Context) error {
	v, ok := h.groupViewer(c)
	if !ok {
		return nil
	}
	if err := h.groups.Delete(c.Request().Context(), v, c.Param("id")); err != nil {
		return groupError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Firewall group deleted"})
}

// AddRule handles POST /api/v1/firewall-groups/:id/rules.
func (h *FirewallGroupHandler) AddRule(c echo.Context) error {
	v, ok := h.groupViewer(c)
	if !ok {
		return nil
	}
	var req AddFirewallRuleRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	rule, err := h.groups.AddRule(c.Request().Context(), v, c.Param("id"), &service.AddFirewallRuleRequest{
		Protocol: req.Protocol, PortRange: req.PortRange, Action: req.Action, Direction: req.Direction,
		SourceIP: req.SourceIP, Priority: req.Priority, ICMPType: req.ICMPType, Family: req.Family,
		// The node's kernel log is the operator's.
		Log: req.Log && v.Staff,
	})
	if err != nil {
		return groupError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": rule})
}

// RemoveRule handles DELETE /api/v1/firewall-groups/:id/rules/:rule_id.
func (h *FirewallGroupHandler) RemoveRule(c echo.Context) error {
	v, ok := h.groupViewer(c)
	if !ok {
		return nil
	}
	if err := h.groups.RemoveRule(c.Request().Context(), v, c.Param("id"), c.Param("rule_id")); err != nil {
		return groupError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Rule removed"})
}

// ListForVM handles GET /api/v1/vms/:id/firewall-groups.
func (h *FirewallGroupHandler) ListForVM(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	out, err := h.groups.ListForVM(c.Request().Context(), vmID)
	if err != nil {
		return groupError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

// Attach handles POST /api/v1/vms/:id/firewall-groups
// {"group_id": "...", "position": "before"|"after"}.
func (h *FirewallGroupHandler) Attach(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	v, ok := h.vmGroupViewer(c, vmID)
	if !ok {
		return nil
	}
	var req struct {
		GroupID  string `json:"group_id"`
		Position string `json:"position"`
	}
	if err := c.Bind(&req); err != nil || strings.TrimSpace(req.GroupID) == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "group_id is required"})
	}
	if err := h.groups.Attach(c.Request().Context(), v, vmID, req.GroupID, req.Position); err != nil {
		return groupError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "message": "Firewall group attached"})
}

// Detach handles DELETE /api/v1/vms/:id/firewall-groups/:group_id.
func (h *FirewallGroupHandler) Detach(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	v, ok := h.vmGroupViewer(c, vmID)
	if !ok {
		return nil
	}
	if err := h.groups.Detach(c.Request().Context(), v, vmID, c.Param("group_id")); err != nil {
		return groupError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Firewall group detached"})
}

// RegisterFirewallGroupRoutes registers the group routes. Customers hold both
// group permissions and see only their own groups (the service filters).
func RegisterFirewallGroupRoutes(e *echo.Echo, h *FirewallGroupHandler, db *gorm.DB) {
	h.db = db
	g := e.Group("/api/v1/firewall-groups")
	g.Use(middleware.RequireAuth(db))
	g.GET("", h.List, middleware.RequirePermission("firewall_group:read"))
	g.GET("/:id", h.Get, middleware.RequirePermission("firewall_group:read"))
	g.POST("", h.Create, middleware.RequirePermission("firewall_group:update"))
	g.PUT("/:id", h.Update, middleware.RequirePermission("firewall_group:update"))
	g.DELETE("/:id", h.Delete, middleware.RequirePermission("firewall_group:update"))
	g.POST("/:id/rules", h.AddRule, middleware.RequirePermission("firewall_group:update"))
	g.DELETE("/:id/rules/:rule_id", h.RemoveRule, middleware.RequirePermission("firewall_group:update"))

	vms := e.Group("/api/v1/vms")
	vms.Use(middleware.RequireAuth(db))
	vms.GET("/:id/firewall-groups", h.ListForVM, middleware.RequirePermission("vm:read"))
	vms.POST("/:id/firewall-groups", h.Attach, middleware.RequirePermission("vm:update"))
	vms.DELETE("/:id/firewall-groups/:group_id", h.Detach, middleware.RequirePermission("vm:update"))
}
