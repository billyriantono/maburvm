package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// refusedByAccount answers what an account, its plans or its team role do not
// allow. It returns false for any other error, which the caller then maps.
func refusedByAccount(c echo.Context, err error) bool {
	var allowance *service.AllowanceError
	switch {
	case errors.As(err, &allowance):
		_ = c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "code": "plan_allowance", "message": allowance.Message})
	case errors.Is(err, service.ErrMemberReadOnly):
		_ = c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "code": "member_read_only", "message": err.Error()})
	case errors.Is(err, service.ErrNotAccountManager):
		_ = c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "code": "not_account_manager", "message": err.Error()})
	case errors.Is(err, service.ErrMemberOnlyAccount):
		_ = c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "code": "member_only_account", "message": err.Error()})
	default:
		return false
	}
	return true
}

// teamError maps the team and project errors.
func teamError(c echo.Context, err error) error {
	if refusedByAccount(c, err) {
		return nil
	}
	switch {
	case errors.Is(err, service.ErrMemberNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": err.Error()})
	case errors.Is(err, service.ErrProjectNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": err.Error()})
	case errors.Is(err, service.ErrMemberExists), errors.Is(err, service.ErrProjectExists):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
	case errors.Is(err, service.ErrInvitationInvalid):
		return c.JSON(http.StatusGone, map[string]interface{}{"error": "Gone", "code": "invitation_invalid", "message": err.Error()})
	default:
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
}

type accountTeamHandler struct {
	db *gorm.DB
}

// customer is the signed-in customer; staff accounts have no team.
func (h *accountTeamHandler) customer(c echo.Context) (*middleware.UserContext, bool) {
	u, ok := middleware.GetUserContext(c)
	if !ok {
		_ = c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
		return nil, false
	}
	if u.Role == models.RoleAdmin {
		// Staff are signed in, just not in a team: reads find nothing, and a
		// change is a request that does not apply to them.
		if isReadRequest(c) {
			_ = c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": []interface{}{}})
		} else {
			_ = c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "code": "staff_account",
				"message": "Teams and projects belong to customer accounts; staff work on them from the customer's page or by viewing as the customer."})
		}
		return nil, false
	}
	return u, true
}

// account is the account a request is about: account_id from the query or
// body, else the caller's own. It checks the caller may do `what` there.
func (h *accountTeamHandler) account(c echo.Context, what, fromBody string) (*middleware.UserContext, string, bool) {
	u, ok := h.customer(c)
	if !ok {
		return nil, "", false
	}
	accountID := c.QueryParam("account_id")
	if accountID == "" {
		accountID = fromBody
	}
	if accountID == "" {
		accountID = u.ID.String()
	}
	if _, err := service.RequireAccountRole(c.Request().Context(), h.db, u.ID.String(), accountID, what); err != nil {
		_ = teamError(c, err)
		return nil, "", false
	}
	return u, accountID, true
}

// audit records a team change in its own words, with the account it was made
// in, so the owner's and staff's audit pages say what happened.
func (h *accountTeamHandler) audit(c echo.Context, actorID, action, resourceType, resourceID string, details map[string]any) {
	recordAudit(c, h.db, actorID, action, resourceType, resourceID, details)
}

type inviteRequest struct {
	AccountID string `json:"account_id"`
	service.MemberInput
}

func (h *accountTeamHandler) listMembers(c echo.Context) error {
	_, accountID, ok := h.account(c, service.AccountCanManage, "")
	if !ok {
		return nil
	}
	rows, err := service.ListMembers(c.Request().Context(), h.db, accountID)
	if err != nil {
		return teamError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": rows})
}

func (h *accountTeamHandler) invite(c echo.Context) error {
	var req inviteRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	u, accountID, ok := h.account(c, service.AccountCanManage, req.AccountID)
	if !ok {
		return nil
	}
	m, err := service.InviteMember(c.Request().Context(), h.db, accountID, u.ID.String(), req.MemberInput)
	if m == nil {
		return teamError(c, err)
	}
	h.audit(c, u.ID.String(), "account.member.invite", "ACCOUNT_MEMBER", m.ID,
		map[string]any{"account_id": accountID, "email": m.Email, "role": m.Role, "vm_ids": m.VMIDs, "project_ids": m.ProjectIDs})
	out := map[string]interface{}{"success": true, "data": m}
	if err != nil { // saved, not emailed
		out["warning"] = err.Error()
	}
	return c.JSON(http.StatusCreated, out)
}

func (h *accountTeamHandler) updateMember(c echo.Context) error {
	var req inviteRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	u, accountID, ok := h.account(c, service.AccountCanManage, req.AccountID)
	if !ok {
		return nil
	}
	m, err := service.UpdateMember(c.Request().Context(), h.db, accountID, u.ID.String(), c.Param("id"), req.MemberInput)
	if err != nil {
		return teamError(c, err)
	}
	h.audit(c, u.ID.String(), "account.member.update", "ACCOUNT_MEMBER", m.ID,
		map[string]any{"account_id": accountID, "email": m.Email, "role": m.Role, "vm_ids": m.VMIDs, "project_ids": m.ProjectIDs})
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": m})
}

func (h *accountTeamHandler) removeMember(c echo.Context) error {
	u, accountID, ok := h.account(c, service.AccountCanManage, "")
	if !ok {
		return nil
	}
	m, err := service.RemoveMember(c.Request().Context(), h.db, accountID, c.Param("id"))
	if err != nil {
		return teamError(c, err)
	}
	h.audit(c, u.ID.String(), "account.member.revoke", "ACCOUNT_MEMBER", m.ID,
		map[string]any{"account_id": accountID, "email": m.Email, "role": m.Role, "accepted": m.AcceptedAt != nil})
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true})
}

func (h *accountTeamHandler) memberships(c echo.Context) error {
	u, ok := h.customer(c)
	if !ok {
		return nil
	}
	ctx := c.Request().Context()
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{
		"memberships": service.ListMemberships(ctx, h.db, u.ID.String()),
		"member_only": service.IsMemberOnlyAccount(ctx, h.db, u.ID.String()),
	}})
}

func (h *accountTeamHandler) leave(c echo.Context) error {
	u, ok := h.customer(c)
	if !ok {
		return nil
	}
	m, err := service.LeaveAccount(c.Request().Context(), h.db, u.ID.String(), c.Param("id"))
	if err != nil {
		return teamError(c, err)
	}
	h.audit(c, u.ID.String(), "account.member.leave", "ACCOUNT_MEMBER", m.ID, map[string]any{"account_id": m.AccountID})
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true})
}

// lookupInvitation takes the token in the body, not the path, so it never
// lands in the request log.
func (h *accountTeamHandler) lookupInvitation(c echo.Context) error {
	middleware.SkipAudit(c)
	var req struct {
		Token string `json:"token"`
	}
	_ = c.Bind(&req)
	v, err := service.LookupInvitation(c.Request().Context(), h.db, req.Token)
	if err != nil {
		return teamError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": v})
}

func (h *accountTeamHandler) acceptInvitation(c echo.Context) error {
	var req service.AcceptInput
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	m, userID, err := service.AcceptInvitation(c.Request().Context(), h.db, req)
	if err != nil {
		return teamError(c, err)
	}
	h.audit(c, userID, "account.member.accept", "ACCOUNT_MEMBER", m.ID, map[string]any{"account_id": m.AccountID, "email": m.Email, "role": m.Role})
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"email": m.Email}})
}

type projectRequest struct {
	AccountID string `json:"account_id"`
	service.ProjectInput
}

func (h *accountTeamHandler) listProjects(c echo.Context) error {
	u, ok := h.customer(c)
	if !ok {
		return nil
	}
	accountID := c.QueryParam("account_id")
	if accountID == "" {
		accountID = u.ID.String()
	}
	// Anyone in the account sees its projects: they name the VMs they see.
	if role, _ := service.AccountRole(c.Request().Context(), h.db, u.ID.String(), accountID); role == "" {
		return teamError(c, service.ErrMemberNotFound)
	}
	rows, err := service.ListProjects(c.Request().Context(), h.db, accountID)
	if err != nil {
		return teamError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": rows})
}

func (h *accountTeamHandler) createProject(c echo.Context) error {
	var req projectRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	u, accountID, ok := h.account(c, service.AccountCanManage, req.AccountID)
	if !ok {
		return nil
	}
	p, err := service.CreateProject(c.Request().Context(), h.db, accountID, req.ProjectInput)
	if err != nil {
		return teamError(c, err)
	}
	h.audit(c, u.ID.String(), "account.project.create", "PROJECT", p.ID, map[string]any{"account_id": accountID, "name": p.Name})
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": p})
}

func (h *accountTeamHandler) updateProject(c echo.Context) error {
	var req projectRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	u, accountID, ok := h.account(c, service.AccountCanManage, req.AccountID)
	if !ok {
		return nil
	}
	p, err := service.UpdateProject(c.Request().Context(), h.db, accountID, c.Param("id"), req.ProjectInput)
	if err != nil {
		return teamError(c, err)
	}
	h.audit(c, u.ID.String(), "account.project.update", "PROJECT", p.ID, map[string]any{"account_id": accountID, "name": p.Name})
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": p})
}

func (h *accountTeamHandler) deleteProject(c echo.Context) error {
	u, accountID, ok := h.account(c, service.AccountCanManage, "")
	if !ok {
		return nil
	}
	p, err := service.DeleteProject(c.Request().Context(), h.db, accountID, c.Param("id"))
	if err != nil {
		return teamError(c, err)
	}
	h.audit(c, u.ID.String(), "account.project.delete", "PROJECT", p.ID, map[string]any{"account_id": accountID, "name": p.Name})
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true})
}

// setVMProject handles PUT /api/v1/vms/:id/project {project_id}: "" takes the
// VM out of its project. Needs a role that operates the VM.
func (h *accountTeamHandler) setVMProject(c echo.Context) error {
	id := c.Param("id")
	if !authz.NewAuthorizer(h.db).AuthorizeVM(c, id) {
		return nil
	}
	var req struct {
		ProjectID string `json:"project_id"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	var vm models.VM
	if err := h.db.WithContext(c.Request().Context()).First(&vm, "id = ?", id).Error; err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
	}
	if err := service.SetVMProject(c.Request().Context(), h.db, &vm, req.ProjectID); err != nil {
		return teamError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"project_id": req.ProjectID}})
}

// staffTeam handles GET /api/v1/users/:id/team: a customer's members and the
// teams they belong to, for the admin user page.
func (h *accountTeamHandler) staffTeam(c echo.Context) error {
	ctx := c.Request().Context()
	id := c.Param("id")
	members, err := service.ListMembers(ctx, h.db, id)
	if err != nil {
		return teamError(c, err)
	}
	projects, err := service.ListProjects(ctx, h.db, id)
	if err != nil {
		return teamError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{
		"members": members, "projects": projects,
		"memberships": service.ListMemberships(ctx, h.db, id),
		"member_only": service.IsMemberOnlyAccount(ctx, h.db, id),
	}})
}

// staffMemberOnly handles PUT /api/v1/users/:id/member-only {member_only}:
// staff letting an account made by an invitation order on its own.
func (h *accountTeamHandler) staffMemberOnly(c echo.Context) error {
	var req struct {
		MemberOnly *bool `json:"member_only"`
	}
	if err := c.Bind(&req); err != nil || req.MemberOnly == nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "member_only is required"})
	}
	if err := service.SetMemberOnly(c.Request().Context(), h.db, c.Param("id"), *req.MemberOnly); err != nil {
		if errors.Is(err, service.ErrUserNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "User not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"member_only": *req.MemberOnly}})
}

// RegisterAccountTeamRoutes wires customer teams and projects.
func RegisterAccountTeamRoutes(e *echo.Echo, db *gorm.DB) {
	h := &accountTeamHandler{db: db}
	auth := middleware.RequireAuth(db)

	// Public: the accept page is opened from the invitation email, often by
	// someone with no account yet. The token is the credential.
	inv := e.Group("/api/v1/account/invitations", middleware.LoginRateLimiter())
	inv.POST("/lookup", h.lookupInvitation)
	inv.POST("/accept", h.acceptInvitation)

	g := e.Group("/api/v1/account", auth)
	g.GET("/members", h.listMembers)
	g.POST("/members", h.invite)
	g.PUT("/members/:id", h.updateMember)
	g.DELETE("/members/:id", h.removeMember)
	g.GET("/memberships", h.memberships)
	g.DELETE("/memberships/:id", h.leave)
	g.GET("/projects", h.listProjects)
	g.POST("/projects", h.createProject)
	g.PUT("/projects/:id", h.updateProject)
	g.DELETE("/projects/:id", h.deleteProject)

	e.PUT("/api/v1/vms/:id/project", h.setVMProject, auth, middleware.RequirePermission("vm:update"))

	e.GET("/api/v1/users/:id/team", h.staffTeam, auth, middleware.RequirePermission("user:read"))
	e.PUT("/api/v1/users/:id/member-only", h.staffMemberOnly, auth, middleware.RequirePermission("user:update"))
}

// recordAudit writes an audit row in the handler's own words; the request
// middleware then skips its generic one. Best-effort: auditing never breaks
// the operation.
func recordAudit(c echo.Context, db *gorm.DB, actorID, action, resourceType, resourceID string, details map[string]any) {
	entry := &models.AuditLog{Action: action, ResourceType: resourceType, IPAddress: c.RealIP(),
		UserAgent: c.Request().UserAgent(), Details: details}
	if actorID != "" {
		entry.UserID = &actorID
	}
	if resourceID != "" {
		entry.ResourceID = &resourceID
	}
	_ = repository.NewAuditRepository(db).Create(c.Request().Context(), entry)
}
