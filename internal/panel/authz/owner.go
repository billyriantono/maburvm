// Package authz provides server-side, domain-level tenant authorization for
// VM-scoped resources. It is intentionally implemented as explicit helpers that
// handlers call per route — NOT as a broad HTTP bypass/middleware. This keeps
// containment close to the resource it protects and preserves the existing
// per-resource ownership model used by the VM handler.
//
// Semantics (Phase 1 accepted policy):
//   - Admins may act on any VM (the current admin role semantics are reused; no
//     new RBAC roles are invented here).
//   - Every non-admin user may act only on VMs they own.
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

// vmOwner returns the owning user ID for a VM. It returns gorm.ErrRecordNotFound
// (mapped to 404 by callers) when the VM does not exist.
func (a *Authorizer) vmOwner(ctx context.Context, vmID string) (string, error) {
	if a.db == nil {
		return "", errors.New("authorizer has no database")
	}
	var vm models.VM
	if err := a.db.WithContext(ctx).
		Model(&models.VM{}).
		Select("user_id").
		Where("id = ?", vmID).
		First(&vm).Error; err != nil {
		return "", err
	}
	return vm.UserID, nil
}

// AuthorizeVM enforces per-resource tenant isolation for a VM-scoped request.
// It returns true when the caller is authorized. When not, it writes the
// appropriate response (401 if unauthenticated, 404 otherwise) and returns
// false. The caller should then `return nil`, as the response is committed.
func (a *Authorizer) AuthorizeVM(c echo.Context, vmID string) bool {
	userCtx, ok := middleware.GetUserContext(c)
	if !ok {
		Unauthorized(c)
		return false
	}
	// Admins bypass ownership checks (reused current admin semantics).
	if userCtx.Role == models.RoleAdmin {
		return true
	}
	ownerID, err := a.vmOwner(c.Request().Context(), vmID)
	if err != nil || ownerID != userCtx.ID.String() {
		NotFound(c)
		return false
	}
	return true
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
