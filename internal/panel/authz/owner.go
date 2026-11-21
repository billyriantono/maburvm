// Package authz provides server-side, domain-level tenant authorization for
// VM-scoped resources. It is intentionally implemented as explicit helpers that
// handlers call per route — NOT as a broad HTTP bypass/middleware. This keeps
// containment close to the resource it protects and preserves the existing
// per-resource ownership model used by the VM handler.
//
// Semantics:
//   - Admins may act on any VM (the current admin role semantics are reused; no
//     new RBAC roles are invented here).
//   - Every non-admin user may act on VMs they own, and on the VMs their team
//     memberships cover as their member role allows (service.CheckVMAccess).
//   - Anti-enumeration: a non-owner and a nonexistent VM-scoped resource map
//     IDENTICALLY to 404 ("Not Found"). Missing authentication maps to 401.
//   - Sub-resource operations (network/port-forward/firewall) are additionally
//     guarded against VM/resource mismatch (confused-deputy) by verifying the
//     referenced resource actually belongs to the route's VM ID.
package authz

import (
	"context"
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// Authorizer enforces owner-or-admin authorization against the panel database.
type Authorizer struct {
	db *gorm.DB
}

// NewAuthorizer builds an Authorizer bound to the supplied database handle.
func NewAuthorizer(db *gorm.DB) *Authorizer {
	return &Authorizer{db: db}
}

// NotFound writes the anti-enumeration 404. A non-owner, a nonexistent VM, and
// a VM/resource mismatch all map identically to this response.
func NotFound(c echo.Context) {
	_ = c.JSON(http.StatusNotFound, map[string]interface{}{
		"error":   "Not Found",
		"message": "VM not found",
	})
}

// Unauthorized writes 401 when the authenticated identity is missing.
func Unauthorized(c echo.Context) {
	_ = c.JSON(http.StatusUnauthorized, map[string]interface{}{
		"error":   "Unauthorized",
		"message": "authentication required",
	})
}

// Forbidden writes 403 when an authenticated, non-admin user attempts an
// admin-only operation (e.g. host storage pool/volume management).
func Forbidden(c echo.Context) {
	_ = c.JSON(http.StatusForbidden, map[string]interface{}{
		"error":   "Forbidden",
		"message": "administrator access required",
	})
}

// AuthorizeVM enforces per-resource tenant isolation for a VM-scoped request,
// as service.CheckVMAccess decides: a read needs the VM to be the caller's or
// covered by one of their team memberships; anything else also needs a role
// that operates and a VM that is not suspended. It returns true when the
// caller is authorized. When not, it writes the response (401 if
// unauthenticated, 403 for a suspended VM or a view-only member, 404
// otherwise) and returns false. The caller should then `return nil`, as the
// response is committed.
func (a *Authorizer) AuthorizeVM(c echo.Context, vmID string) bool {
	need := service.AccessChange
	if m := c.Request().Method; m == http.MethodGet || m == http.MethodHead {
		need = service.AccessRead
	}
	return a.authorize(c, vmID, need)
}

// AuthorizeVMOperate is AuthorizeVM for a read that hands over control or a
// secret: it needs a role that operates, but not a VM that is running.
func (a *Authorizer) AuthorizeVMOperate(c echo.Context, vmID string) bool {
	return a.authorize(c, vmID, service.AccessOperate)
}

func (a *Authorizer) authorize(c echo.Context, vmID string, need service.AccessNeed) bool {
	userCtx, ok := middleware.GetUserContext(c)
	if !ok {
		Unauthorized(c)
		return false
	}
	// Admins bypass ownership checks (reused current admin semantics).
	if userCtx.Role == models.RoleAdmin {
		return true
	}
	var vm models.VM
	err := errors.New("authorizer has no database")
	if a.db != nil {
		err = a.db.WithContext(c.Request().Context()).Model(&models.VM{}).
			Select("id", "user_id", "status", "suspended").Where("id = ?", vmID).First(&vm).Error
	}
	if err == nil {
		err = service.CheckVMAccess(c.Request().Context(), a.db, &vm, userCtx.ID.String(), false, need)
	}
	switch {
	case err == nil:
		return true
	case errors.Is(err, service.ErrVMSuspended):
		_ = c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":   "Forbidden",
			"message": "This VM is suspended. Contact support to restore service.",
		})
	case errors.Is(err, service.ErrMemberReadOnly):
		_ = c.JSON(http.StatusForbidden, map[string]interface{}{
			"error":   "Forbidden",
			"code":    "member_read_only",
			"message": "Your role in this account lets you view this VM, not change it.",
		})
	default:
		NotFound(c)
	}
	return false
}

// AuthorizeAdmin restricts an action to administrators. It returns true when the
// caller is an authenticated admin. Otherwise it writes 401 (missing auth) or
// 403 (authenticated non-admin) and returns false.
func (a *Authorizer) AuthorizeAdmin(c echo.Context) bool {
	userCtx, ok := middleware.GetUserContext(c)
	if !ok {
		Unauthorized(c)
		return false
	}
	if userCtx.Role != models.RoleAdmin {
		Forbidden(c)
		return false
	}
	return true
}

// AuthorizeVMResource guards a sub-resource (network/port-forward/firewall)
// against VM/resource mismatch. The caller MUST have already authorized the
// route VM via AuthorizeVM. resourceVMID is the VM the sub-resource is attached
// to; an empty value (unresolvable resource) is treated as not found. A mismatch
// maps to 404 to preserve anti-enumeration.
func (a *Authorizer) AuthorizeVMResource(c echo.Context, routeVMID, resourceVMID string) bool {
	if resourceVMID != routeVMID {
		NotFound(c)
		return false
	}
	return true
}

// NetworkVMID resolves the VM ID a network interface belongs to, or
// gorm.ErrRecordNotFound when it does not exist.
func (a *Authorizer) NetworkVMID(ctx context.Context, networkID string) (string, error) {
	if a.db == nil {
		return "", errors.New("authorizer has no database")
	}
	var n struct {
		VMID string
	}
	if err := a.db.WithContext(ctx).
		Model(&models.Network{}).
		Select("vm_id").
		Where("id = ?", networkID).
		First(&n).Error; err != nil {
		return "", err
	}
	return n.VMID, nil
}

// PortForwardVMID resolves the VM ID a port forward belongs to, or
// gorm.ErrRecordNotFound when it does not exist.
func (a *Authorizer) PortForwardVMID(ctx context.Context, forwardID string) (string, error) {
	if a.db == nil {
		return "", errors.New("authorizer has no database")
	}
	var pf struct {
		VMID string
	}
	if err := a.db.WithContext(ctx).
		Model(&models.PortForward{}).
		Select("vm_id").
		Where("id = ?", forwardID).
		First(&pf).Error; err != nil {
		return "", err
	}
	return pf.VMID, nil
}

// FirewallRuleVMID resolves the VM ID a firewall rule belongs to, or
// gorm.ErrRecordNotFound when it does not exist.
func (a *Authorizer) FirewallRuleVMID(ctx context.Context, ruleID string) (string, error) {
	if a.db == nil {
		return "", errors.New("authorizer has no database")
	}
	var r struct {
		VMID string
	}
	if err := a.db.WithContext(ctx).
		Model(&models.FirewallRule{}).
		Select("vm_id").
		Where("id = ?", ruleID).
		First(&r).Error; err != nil {
		return "", err
	}
	return r.VMID, nil
}
