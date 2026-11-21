package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

func jsonError(c echo.Context, status int, code, message string) error {
	return c.JSON(status, map[string]interface{}{"error": code, "message": message})
}

func ok(c echo.Context, data interface{}) error {
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": data})
}

// auditStaffEvent records a staff-management action in the caller's words.
func (s *Server) auditStaffEvent(c echo.Context, action, resourceType, resourceID string, details map[string]any) {
	actor, _ := panelMiddleware.GetUserContext(c)
	entry := &models.AuditLog{Action: action, ResourceType: resourceType, IPAddress: c.RealIP(),
		UserAgent: panelMiddleware.Clip(c.Request().UserAgent(), 500), Details: details}
	if actor != nil {
		id := actor.ID.String()
		entry.UserID = &id
	}
	if _, err := uuid.Parse(resourceID); err == nil {
		entry.ResourceID = &resourceID
	}
	_ = s.db.WithContext(c.Request().Context()).Create(entry).Error
}

// fillStaffRoles sets the effective role of each staff account for display:
// the Owner, Administrator when none was set, or the role it holds.
func (s *Server) fillStaffRoles(ctx context.Context, users []models.User) {
	founding := panelMiddleware.FoundingAdminID(ctx, s.db)
	names := map[string]string{}
	for i := range users {
		if users[i].Role != models.RoleAdmin {
			continue
		}
		key := panelMiddleware.StaffRoleKey(&users[i], founding)
		name, seen := names[key]
		if !seen {
			r, _ := panelMiddleware.LookupStaffRole(ctx, s.db, key)
			name, names[key] = r.Name, r.Name
		}
		users[i].StaffRole, users[i].StaffRoleName = &key, name
	}
}

// --- Staff roles ---

func (s *Server) setupStaffRoleRoutes(g *echo.Group) {
	roles := g.Group("/staff-roles", panelMiddleware.RequireAuth(s.db), panelMiddleware.RequirePermission(panelMiddleware.PermStaff))
	manage := panelMiddleware.RequirePermission(panelMiddleware.PermStaffManage)
	roles.GET("", s.listStaffRoles)
	roles.POST("", s.saveStaffRole, manage)
	roles.PUT("/:id", s.saveStaffRole, manage)
	roles.DELETE("/:id", s.deleteStaffRole, manage)
}

type staffRoleView struct {
	panelMiddleware.StaffRole
	Members int `json:"members"`
}

func (s *Server) listStaffRoles(c echo.Context) error {
	ctx := c.Request().Context()
	var custom []models.CustomStaffRole
	if err := s.db.WithContext(ctx).Order("name").Find(&custom).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "list_failed", "The roles could not be read")
	}
	var staff []models.User
	if err := s.db.WithContext(ctx).Where("role = ?", models.RoleAdmin).Find(&staff).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "list_failed", "The staff accounts could not be read")
	}
	founding := panelMiddleware.FoundingAdminID(ctx, s.db)
	members := map[string]int{}
	for i := range staff {
		if !staff[i].Expired() {
			members[panelMiddleware.StaffRoleKey(&staff[i], founding)]++
		}
	}
	roles := []staffRoleView{}
	for _, r := range panelMiddleware.BuiltInStaffRoles {
		roles = append(roles, staffRoleView{r, members[r.ID]})
	}
	for _, row := range custom {
		r, _ := panelMiddleware.LookupStaffRole(ctx, s.db, row.ID)
		roles = append(roles, staffRoleView{r, members[r.ID]})
	}
	caller, _ := panelMiddleware.GetUserContext(c)
	return ok(c, map[string]interface{}{
		"areas":      panelMiddleware.PermissionCatalog,
		"roles":      roles,
		"can_manage": caller.Can(panelMiddleware.PermStaffManage) && !caller.APIKeyRestricted,
	})
}

func (s *Server) saveStaffRole(c echo.Context) error {
	ctx := c.Request().Context()
	caller, _ := panelMiddleware.GetUserContext(c)
	if caller.APIKeyRestricted {
		return jsonError(c, http.StatusForbidden, "restricted_key", "This API key may not manage staff roles")
	}
	var req struct {
		Name        string   `json:"name"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if err := c.Bind(&req); err != nil {
		return jsonError(c, http.StatusBadRequest, "invalid_body", "Invalid request body")
	}
	req.Name, req.Description = strings.TrimSpace(req.Name), strings.TrimSpace(req.Description)
	if req.Name == "" || len([]rune(req.Name)) > 100 {
		return jsonError(c, http.StatusBadRequest, "invalid_name", "Give the role a name of up to 100 characters")
	}
	if len([]rune(req.Description)) > 500 {
		return jsonError(c, http.StatusBadRequest, "invalid_description", "The description is too long (500 characters at most)")
	}
	perms := []string{}
	for _, p := range req.Permissions {
		if !panelMiddleware.InCatalog(p) {
			return jsonError(c, http.StatusBadRequest, "unknown_permission", "Unknown permission: "+p)
		}
		if !slices.Contains(perms, p) {
			perms = append(perms, p)
		}
	}
	if !panelMiddleware.CanGrant(caller.Permissions, perms) {
		return jsonError(c, http.StatusForbidden, "cannot_grant", "A role can only hold permissions you hold yourself")
	}

	id := c.Param("id")
	var existing models.CustomStaffRole
	if id != "" {
		if r, builtIn := panelMiddleware.LookupStaffRole(ctx, nil, id); builtIn {
			return jsonError(c, http.StatusBadRequest, "built_in_role", r.Name+" is built in and cannot be changed")
		}
		if err := s.db.WithContext(ctx).Where("id = ?", id).First(&existing).Error; err != nil {
			return jsonError(c, http.StatusNotFound, "role_not_found", "No such role")
		}
		if !panelMiddleware.CanGrant(caller.Permissions, existing.Permissions) {
			return jsonError(c, http.StatusForbidden, "cannot_grant", "This role holds permissions you do not, so you cannot change it")
		}
	}
	for _, r := range panelMiddleware.BuiltInStaffRoles {
		if strings.EqualFold(r.Name, req.Name) {
			return jsonError(c, http.StatusConflict, "name_taken", "A built-in role already has that name")
		}
	}
	var clash int64
	s.db.WithContext(ctx).Model(&models.CustomStaffRole{}).Where("LOWER(name) = ? AND id <> ?", strings.ToLower(req.Name), id).Count(&clash)
	if clash > 0 {
		return jsonError(c, http.StatusConflict, "name_taken", "Another role already has that name")
	}

	action := "staff_role.update"
	if id == "" {
		action = "staff_role.create"
		existing = models.CustomStaffRole{ID: uuid.NewString()}
	}
	existing.Name, existing.Description, existing.Permissions = req.Name, req.Description, perms
	write := s.db.WithContext(ctx).Save
	if id == "" {
		write = s.db.WithContext(ctx).Create
	}
	if err := write(&existing).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "save_failed", "The role could not be saved")
	}
	s.auditStaffEvent(c, action, "STAFF_ROLE", existing.ID, map[string]any{"name": existing.Name, "permissions": perms})
	role, _ := panelMiddleware.LookupStaffRole(ctx, s.db, existing.ID)
	return ok(c, role)
}

func (s *Server) deleteStaffRole(c echo.Context) error {
	ctx := c.Request().Context()
	caller, _ := panelMiddleware.GetUserContext(c)
	if caller.APIKeyRestricted {
		return jsonError(c, http.StatusForbidden, "restricted_key", "This API key may not manage staff roles")
	}
	id := c.Param("id")
	if r, builtIn := panelMiddleware.LookupStaffRole(ctx, nil, id); builtIn {
		return jsonError(c, http.StatusBadRequest, "built_in_role", r.Name+" is built in and cannot be deleted")
	}
	var role models.CustomStaffRole
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&role).Error; err != nil {
		return jsonError(c, http.StatusNotFound, "role_not_found", "No such role")
	}
	if !panelMiddleware.CanGrant(caller.Permissions, role.Permissions) {
		return jsonError(c, http.StatusForbidden, "cannot_grant", "This role holds permissions you do not, so you cannot delete it")
	}
	var holders int64
	s.db.WithContext(ctx).Model(&models.User{}).Where("role = ? AND staff_role = ?", models.RoleAdmin, id).Count(&holders)
	if holders > 0 {
		return jsonError(c, http.StatusConflict, "role_in_use",
			"Give the "+strconv.FormatInt(holders, 10)+" staff account(s) with this role another role first")
	}
	if err := s.db.WithContext(ctx).Delete(&role).Error; err != nil {
		return jsonError(c, http.StatusInternalServerError, "delete_failed", "The role could not be deleted")
	}
	s.auditStaffEvent(c, "staff_role.delete", "STAFF_ROLE", role.ID, map[string]any{"name": role.Name})
	return ok(c, nil)
}

// --- Per-account: staff role, view as customer, sessions ---

func (s *Server) setupStaffAccountRoutes(users *echo.Group, userRepo *repository.UserRepository) {
	recoverPerm := panelMiddleware.RequirePermission("user:recover")

	// The role of another staff account. Only someone holding every permission
	// of both the old and the new role may change it, and never the Owner's.
	users.PUT("/:id/staff-role", func(c echo.Context) error {
		ctx := c.Request().Context()
		caller, _ := panelMiddleware.GetUserContext(c)
		if caller.APIKeyRestricted {
			return jsonError(c, http.StatusForbidden, "restricted_key", "This API key may not change staff roles")
		}
		var req struct {
			Role string `json:"role"`
		}
		if err := c.Bind(&req); err != nil {
			return jsonError(c, http.StatusBadRequest, "invalid_body", "Invalid request body")
		}
		target := s.targetUser(c, userRepo)
		if target == nil {
			return nil
		}
		if target.Role != models.RoleAdmin {
			return jsonError(c, http.StatusBadRequest, "not_staff", "Only staff accounts have a staff role")
		}
		if target.ID == caller.ID {
			return jsonError(c, http.StatusForbidden, "own_role", "You cannot change your own role")
		}
		founding := panelMiddleware.FoundingAdminID(ctx, s.db)
		if target.ID == founding {
			return jsonError(c, http.StatusForbidden, "owner_role", "The founding administrator is always the Owner")
		}
		newRole, found := panelMiddleware.LookupStaffRole(ctx, s.db, req.Role)
		if !found || req.Role == panelMiddleware.StaffRoleOwner {
			return jsonError(c, http.StatusBadRequest, "unknown_role", "Choose one of the roles on the Staff roles page")
		}
		current, _ := panelMiddleware.LookupStaffRole(ctx, s.db, panelMiddleware.StaffRoleKey(target, founding))
		if !panelMiddleware.CanGrant(caller.Permissions, newRole.Permissions) || !panelMiddleware.CanGrant(caller.Permissions, current.Permissions) {
			return jsonError(c, http.StatusForbidden, "cannot_grant", "You can only move someone between roles whose permissions you hold yourself")
		}
		if err := s.db.WithContext(ctx).Table("users").Where("id = ? AND role = ?", target.ID, models.RoleAdmin).
			Update("staff_role", newRole.ID).Error; err != nil {
			return jsonError(c, http.StatusInternalServerError, "save_failed", "The role could not be changed")
		}
		service.AuditUserEvent(ctx, s.db, "user.staff_role", caller.ID.String(), target.ID.String(), c.RealIP(), c.Request().UserAgent(),
			map[string]any{"email": target.Email, "role_from": current.ID, "role_to": newRole.ID})
		return ok(c, map[string]string{"staff_role": newRole.ID, "staff_role_name": newRole.Name})
	}, panelMiddleware.RequirePermission(panelMiddleware.PermStaffManage))

	// Open the panel as a customer sees it, for a limited time. The staff
	// session is kept aside and comes back when the view ends.
	users.POST("/:id/impersonate", func(c echo.Context) error {
		ctx := c.Request().Context()
		caller, _ := panelMiddleware.GetUserContext(c)
		staffCookie, cerr := c.Cookie(panelMiddleware.AccessTokenCookieName)
		var staffClaims *panelMiddleware.JWTClaims
		if cerr == nil {
			staffClaims, cerr = panelMiddleware.ParseAndValidateToken(staffCookie.Value)
		}
		if caller.APIKeyID != "" || caller.ImpersonatorID != "" || cerr != nil ||
			staffClaims.UserID != caller.ID.String() || staffClaims.ExpiresAt == nil {
			return jsonError(c, http.StatusForbidden, "browser_session_required", "Open a customer's view from your own signed-in browser session")
		}
		target := s.targetUser(c, userRepo)
		if target == nil {
			return nil
		}
		if target.Role != models.RoleClient {
			return jsonError(c, http.StatusBadRequest, "not_a_customer", "Only a customer account can be viewed this way")
		}
		if target.Locked() {
			return jsonError(c, http.StatusBadRequest, "account_suspended", "This account is suspended. Restore it first.")
		}
		token, err := panelMiddleware.IssueSession(s.db.WithContext(ctx), target, panelMiddleware.SessionMeta{
			Method: panelMiddleware.SignInImpersonation, IP: c.RealIP(), UserAgent: c.Request().UserAgent(), ImpersonatorID: &caller.ID,
		}, panelMiddleware.ImpersonationLifetime)
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, "session_failed", "The customer's view could not be opened")
		}
		panelMiddleware.SetStaffCookie(c, staffCookie.Value, time.Until(staffClaims.ExpiresAt.Time))
		panelMiddleware.SetSessionCookie(c, token, panelMiddleware.ImpersonationLifetime)
		service.AuditUserEvent(ctx, s.db, "user.impersonate.start", caller.ID.String(), target.ID.String(), c.RealIP(), c.Request().UserAgent(),
			map[string]any{"email": target.Email, "minutes": int(panelMiddleware.ImpersonationLifetime.Minutes())})
		service.RecordSignIn(ctx, s.db, models.SignInEvent{UserID: &target.ID, Email: target.Email, Method: panelMiddleware.SignInImpersonation,
			Success: true, Note: caller.Email, IPAddress: c.RealIP(), UserAgent: c.Request().UserAgent()})
		return ok(c, map[string]interface{}{"redirect": "/client/dashboard", "expires_at": time.Now().Add(panelMiddleware.ImpersonationLifetime)})
	}, panelMiddleware.RequirePermission(panelMiddleware.PermImpersonate))

	// Where another staff account signs in from is the Owner's to see.
	users.GET("/:id/sessions", func(c echo.Context) error {
		target := s.manageableUser(c, userRepo)
		if target == nil {
			return nil
		}
		caller, _ := panelMiddleware.GetUserContext(c)
		list, lerr := service.ActiveSessions(c.Request().Context(), s.db, target.ID, caller.SessionID)
		if lerr != nil {
			return jsonError(c, http.StatusInternalServerError, "list_failed", "The sessions could not be read")
		}
		return ok(c, list)
	})
	users.GET("/:id/sign-ins", func(c echo.Context) error {
		target := s.manageableUser(c, userRepo)
		if target == nil {
			return nil
		}
		list, lerr := service.SignInHistory(c.Request().Context(), s.db, target.ID, 100)
		if lerr != nil {
			return jsonError(c, http.StatusInternalServerError, "list_failed", "The sign-in history could not be read")
		}
		return ok(c, list)
	})
	users.DELETE("/:id/sessions/:sid", func(c echo.Context) error {
		target := s.manageableUser(c, userRepo)
		if target == nil {
			return nil
		}
		found, rerr := service.RevokeSession(c.Request().Context(), s.db, target.ID, c.Param("sid"))
		switch {
		case rerr != nil:
			return jsonError(c, http.StatusInternalServerError, "revoke_failed", "The session could not be signed out")
		case !found:
			return jsonError(c, http.StatusNotFound, "session_not_found", "That session has already ended")
		}
		s.auditSessionsRevoked(c, target, c.Param("sid"))
		return ok(c, nil)
	}, recoverPerm)
	users.POST("/:id/sessions/revoke-all", func(c echo.Context) error {
		target := s.manageableUser(c, userRepo)
		if target == nil {
			return nil
		}
		if err := service.RevokeAllSessions(c.Request().Context(), s.db, target.ID); err != nil {
			return jsonError(c, http.StatusInternalServerError, "revoke_failed", "The sessions could not be signed out")
		}
		s.auditSessionsRevoked(c, target, "all")
		return ok(c, nil)
	}, recoverPerm)
}

func (s *Server) auditSessionsRevoked(c echo.Context, target *models.User, which string) {
	caller, _ := panelMiddleware.GetUserContext(c)
	service.AuditUserEvent(c.Request().Context(), s.db, "user.sessions_revoked", caller.ID.String(), target.ID.String(), c.RealIP(),
		c.Request().UserAgent(), map[string]any{"email": target.Email, "session": which})
}

// targetUser reads the account named in the path. nil when it answered the
// request instead.
func (s *Server) targetUser(c echo.Context, userRepo *repository.UserRepository) *models.User {
	id, err := parseUUID(c.Param("id"))
	if err != nil {
		_ = jsonError(c, http.StatusBadRequest, "invalid_user_id", "Invalid user ID")
		return nil
	}
	user, err := userRepo.GetByID(c.Request().Context(), id)
	if err != nil {
		_ = jsonError(c, http.StatusNotFound, "user_not_found", "User not found")
		return nil
	}
	return user
}

// manageableUser is targetUser for a change: another staff account is the
// founding administrator's to manage.
func (s *Server) manageableUser(c echo.Context, userRepo *repository.UserRepository) *models.User {
	target := s.targetUser(c, userRepo)
	if target == nil {
		return nil
	}
	caller, _ := panelMiddleware.GetUserContext(c)
	if rbErr := s.mayManageAccount(c.Request().Context(), caller, target); rbErr != nil {
		_ = writeRoleBoundaryErr(c, rbErr)
		return nil
	}
	return target
}

// --- One's own sessions and sign-in history ---

func (s *Server) setupOwnSessionRoutes(settings *echo.Group) {
	settings.GET("/sessions", func(c echo.Context) error {
		u, _ := panelMiddleware.GetUserContext(c)
		list, err := service.ActiveSessions(c.Request().Context(), s.db, u.ID, u.SessionID)
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, "list_failed", "Your sessions could not be read")
		}
		return ok(c, list)
	})
	settings.GET("/sign-ins", func(c echo.Context) error {
		u, _ := panelMiddleware.GetUserContext(c)
		list, err := service.SignInHistory(c.Request().Context(), s.db, u.ID, 50)
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, "list_failed", "Your sign-in history could not be read")
		}
		return ok(c, list)
	})
	settings.DELETE("/sessions/:id", func(c echo.Context) error {
		u, _ := panelMiddleware.GetUserContext(c)
		if c.Param("id") == u.SessionID {
			return jsonError(c, http.StatusBadRequest, "current_session", "This is the session you are using. Sign out instead.")
		}
		found, err := service.RevokeSession(c.Request().Context(), s.db, u.ID, c.Param("id"))
		switch {
		case err != nil:
			return jsonError(c, http.StatusInternalServerError, "revoke_failed", "The session could not be signed out")
		case !found:
			return jsonError(c, http.StatusNotFound, "session_not_found", "That session has already ended")
		}
		return ok(c, nil)
	})
	// Signs out everywhere but here. The session in use gets a fresh token,
	// as the account's token cutoff moves to now.
	settings.POST("/sessions/revoke-others", func(c echo.Context) error {
		ctx := c.Request().Context()
		u, _ := panelMiddleware.GetUserContext(c)
		// Otherwise an API key could mint itself a browser session that outlives the key.
		if u.APIKeyID != "" {
			return jsonError(c, http.StatusForbidden, "browser_session_required", "Sign out other sessions from a signed-in browser session")
		}
		user, err := repository.NewUserRepository(s.db).GetByID(ctx, u.ID)
		if err != nil {
			return jsonError(c, http.StatusNotFound, "user_not_found", "User not found")
		}
		sid := u.SessionID
		if sid == "" {
			// A token from before sessions were recorded: record this one, to keep it.
			token, err := panelMiddleware.IssueSession(s.db.WithContext(ctx), user, panelMiddleware.SessionMeta{
				Method: panelMiddleware.SignInPassword, IP: c.RealIP(), UserAgent: c.Request().UserAgent(),
			}, panelMiddleware.LoadSignInPolicy(s.db).SessionLifetime())
			var claims *panelMiddleware.JWTClaims
			if err == nil {
				claims, err = panelMiddleware.ParseAndValidateToken(token)
			}
			if err != nil || claims.SessionID == "" {
				return jsonError(c, http.StatusInternalServerError, "revoke_failed", "The sessions could not be signed out")
			}
			sid = claims.SessionID
		}
		n, err := service.RevokeOtherSessions(ctx, s.db, user.ID, sid)
		if err != nil {
			return jsonError(c, http.StatusInternalServerError, "revoke_failed", "The sessions could not be signed out")
		}
		token, ttl, err := panelMiddleware.ReissueSession(s.db.WithContext(ctx), user, sid)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return jsonError(c, http.StatusUnauthorized, "session_ended", "Your session has ended. Sign in again.")
			}
			return jsonError(c, http.StatusInternalServerError, "reissue_failed", "Signed out elsewhere, but this session could not be renewed. Sign in again.")
		}
		panelMiddleware.SetSessionCookie(c, token, ttl)
		return ok(c, map[string]interface{}{"revoked": n, "token": token})
	})
}
