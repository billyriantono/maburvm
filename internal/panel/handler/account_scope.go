package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// actingAccount is the account a request about an account's own resources
// (images, floating IPs, VPCs, firewall groups, SSH keys, recipes) acts in:
// ?account_id= for a team account the caller works in, else their own. A read
// needs a role there that views the account, anything else one that changes
// its resources; both need a membership over every VM. Staff act as
// themselves and staff is true. When ok is false the answer is written.
func actingAccount(c echo.Context, db *gorm.DB) (u *middleware.UserContext, accountID string, staff, ok bool) {
	return actingAccountFor(c, db, "")
}

// actingAccountFor is actingAccount with the AccountCan* the request needs
// in a team account; "" decides by the method.
func actingAccountFor(c echo.Context, db *gorm.DB, what string) (*middleware.UserContext, string, bool, bool) {
	u, has := middleware.GetUserContext(c)
	if !has {
		_ = c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized", "message": "authentication required"})
		return nil, "", false, false
	}
	own := u.ID.String()
	if u.Role == models.RoleAdmin {
		return u, own, true, true
	}
	accountID := c.QueryParam("account_id")
	if accountID == "" || accountID == own {
		return u, own, false, true
	}
	if what == "" {
		what = service.AccountCanResources
		if isReadRequest(c) {
			what = service.AccountCanView
		}
	}
	if _, err := service.RequireAccountRole(c.Request().Context(), db, own, accountID, what); err != nil {
		_ = teamError(c, err)
		return nil, "", false, false
	}
	return u, accountID, false, true
}
