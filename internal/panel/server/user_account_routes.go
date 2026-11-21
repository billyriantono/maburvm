package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// setupUserAccountRoutes adds what an administrator does for someone who
// cannot get into their account: a new password, two-factor taken off, a
// sign-in lock lifted. Each leaves an audit row. An administrator's own
// account is not handled here: Profile asks for the password or a code first.
func (s *Server) setupUserAccountRoutes(users *echo.Group, userRepo *repository.UserRepository) {
	userService, err := service.NewUserService(s.db, s.cfg.JWT.AESKey, s.cfg.JWT.SecretKey, "maburvm-panel", s.cfg.JWT.AESPreviousKeys...)
	if err != nil {
		slog.Default().Error("users: failed to init user service for account recovery", "error", err)
	}
	fail := func(c echo.Context, status int, code, message string) error {
		return c.JSON(status, map[string]interface{}{"error": code, "message": message})
	}
	// target resolves the account and checks the caller may manage it.
	target := func(c echo.Context) (*panelMiddleware.UserContext, *models.User, error) {
		id, err := parseUUID(c.Param("id"))
		if err != nil {
			return nil, nil, fail(c, http.StatusBadRequest, "invalid_user_id", "Invalid user ID")
		}
		caller, ok := panelMiddleware.GetUserContext(c)
		if !ok || caller.Role != models.RoleAdmin {
			return nil, nil, fail(c, http.StatusForbidden, "insufficient_permissions", "Insufficient permissions")
		}
		user, err := userRepo.GetByID(c.Request().Context(), id)
		if err != nil {
			return nil, nil, fail(c, http.StatusNotFound, "user_not_found", "User not found")
		}
		if caller.ID == user.ID {
			return nil, nil, fail(c, http.StatusForbidden, "own_account", "Manage your own password and two-factor under Profile")
		}
		if rbErr := s.mayManageAccount(c.Request().Context(), caller, user); rbErr != nil {
			return nil, nil, fail(c, rbErr.status, rbErr.code, "Only the founding administrator can change another administrator's account")
		}
		if userService == nil {
			return nil, nil, fail(c, http.StatusInternalServerError, "unavailable", "Account recovery is unavailable")
		}
		return caller, user, nil
	}
	audit := func(c echo.Context, action string, caller *panelMiddleware.UserContext, user *models.User, details map[string]any) {
		details["email"] = user.Email
		service.AuditUserEvent(c.Request().Context(), s.db, action, caller.ID.String(), user.ID.String(), c.RealIP(), c.Request().UserAgent(), details)
	}

	// method "email" sends the user a link to choose a password; "temporary"
	// sets a random one and returns it once, for when mail cannot reach them.
	users.POST("/:id/reset-password", func(c echo.Context) error {
		var req struct {
			Method string `json:"method"`
		}
		if err := c.Bind(&req); err != nil || (req.Method != "email" && req.Method != "temporary") {
			return fail(c, http.StatusBadRequest, "invalid_method", `method must be "email" or "temporary"`)
		}
		caller, user, failed := target(c)
		if user == nil {
			return failed
		}
		ctx := c.Request().Context()
		if req.Method == "email" {
			if err := userService.SendPasswordResetLink(ctx, user); err != nil {
				if errors.Is(err, service.ErrEmailNotConfigured) || errors.Is(err, service.ErrPublicURLNotSet) {
					return fail(c, http.StatusBadRequest, "email_unavailable", "Not sent: "+err.Error()+". Set a temporary password instead.")
				}
				slog.Default().Warn("password reset link not sent", "user_id", user.ID, "error", err)
				return fail(c, http.StatusBadGateway, "email_failed", "The reset link could not be sent: "+err.Error())
			}
			audit(c, "user.password_reset", caller, user, map[string]any{"method": "email"})
			return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"sent_to": user.Email}})
		}
		password, err := userService.SetTemporaryPassword(ctx, user.ID)
		if err != nil {
			slog.Default().Error("temporary password not set", "user_id", user.ID, "error", err)
			return fail(c, http.StatusInternalServerError, "reset_failed", "The password could not be changed")
		}
		audit(c, "user.password_reset", caller, user, map[string]any{"method": "temporary"})
		notifyAdminChange(c, s.db, user.ID.String(), service.EmailPasswordChanged)
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"temporary_password": password}})
	}, panelMiddleware.RequirePermission("user:recover"))

	// For someone who lost their authenticator and their backup codes.
	users.POST("/:id/reset-2fa", func(c echo.Context) error {
		caller, user, failed := target(c)
		if user == nil {
			return failed
		}
		if err := userService.Disable2FA(user.ID); err != nil {
			if errors.Is(err, service.Err2FANotEnabled) {
				return fail(c, http.StatusBadRequest, "two_factor_not_set", "This account has no two-factor authentication to reset")
			}
			slog.Default().Error("two-factor not reset", "user_id", user.ID, "error", err)
			return fail(c, http.StatusInternalServerError, "reset_failed", "Two-factor authentication could not be reset")
		}
		audit(c, "user.two_factor_reset", caller, user, map[string]any{})
		notifyAdminChange(c, s.db, user.ID.String(), service.EmailTwoFactorDisabled)
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"two_factor_enabled": false}})
	}, panelMiddleware.RequirePermission("user:recover"))

	// Lifts a sign-in lock before it runs out by itself. Never one's own: a
	// held session could then guess at two-factor codes without limit.
	users.POST("/:id/unlock", func(c echo.Context) error {
		caller, user, failed := target(c)
		if user == nil {
			return failed
		}
		if err := service.ClearSignInLock(c.Request().Context(), s.db, user.ID); err != nil {
			return fail(c, http.StatusInternalServerError, "unlock_failed", "The account could not be unlocked")
		}
		audit(c, "user.unlock", caller, user, map[string]any{})
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"locked": false}})
	}, panelMiddleware.RequirePermission("user:recover"))
}

// notifyAdminChange tells an account that an administrator changed how it is
// signed in to. Best-effort: the change is made.
func notifyAdminChange(c echo.Context, db *gorm.DB, userID, key string) {
	if err := service.NotifyAccount(context.WithoutCancel(c.Request().Context()), db, userID, key,
		map[string]string{"who": service.ChangeSource(true, "")}); err != nil {
		slog.Default().Warn("account changed by an administrator, but its owner could not be told", "user_id", userID, "email", key, "error", err)
	}
}
