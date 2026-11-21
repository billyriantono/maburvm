package middleware

import (
	"context"
	"net/http"
	"slices"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// A staff account is a user with role "admin". Its staff role says what it may
// do. The founding administrator is always the Owner.
const (
	StaffRoleOwner         = "owner"
	StaffRoleAdministrator = "administrator"
	StaffRoleSupport       = "support"
	StaffRoleBilling       = "billing"
	StaffRoleReadOnly      = "readonly"
)

const (
	// PermAll is the Administrator's wildcard: every permission except the
	// Owner's own. It keeps the name routes have always checked.
	PermAll = "admin:access"
	// PermStaff comes with every staff role: the admin area itself.
	PermStaff = "staff"
	// PermStaffManage assigns staff roles and edits custom ones.
	PermStaffManage = "staff:manage"
	// PermImpersonate opens a customer's view of the panel.
	PermImpersonate = "user:impersonate"
)

// ownerOnly permissions are not covered by the Administrator's wildcard.
var ownerOnly = map[string]bool{PermStaffManage: true}

// Permission is one checkbox on the staff roles page.
type Permission struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// PermissionArea groups permissions the way the staff roles page shows them.
type PermissionArea struct {
	Area        string       `json:"area"`
	Permissions []Permission `json:"permissions"`
}

// PermissionCatalog is every permission a custom role may hold. Each key is
// one a route checks; anything a route checks that is not here (restore to a
// new VM, data export, support access) needs the Administrator role.
var PermissionCatalog = []PermissionArea{
	{"Virtual machines", []Permission{
		{"vm:read", "See VMs"},
		{"vm:power", "Start, stop and restart VMs"},
		{"vm:console", "Open VM consoles"},
		{"vm:rescue", "Boot VMs into rescue mode"},
		{"vm:lifecycle", "Reinstall, suspend, attach ISOs and disks, clone"},
		{"vm:migrate", "Move VMs to another node"},
		{"vm:update", "Change VM settings, addresses, firewall and limits"},
		{"vm:create", "Create VMs"},
		{"vm:delete", "Delete VMs"},
		{"vm:credentials", "Reveal initial passwords"},
		{"vm:import", "Import VMs from a node"},
	}},
	{"Backups and snapshots", []Permission{
		{"backup:read", "See backups"},
		{"backup:create", "Take backups"},
		{"backup:update", "Restore backups and change schedules"},
		{"backup:delete", "Delete backups"},
		{"backup:download", "Download backup disk images"},
		{"snapshot:read", "See snapshots"},
		{"snapshot:create", "Take snapshots and images"},
		{"snapshot:update", "Restore snapshots"},
		{"snapshot:delete", "Delete snapshots and images"},
	}},
	{"Networking", []Permission{
		{"network:read", "See IP pools, DNS, abuse and reputation"},
		{"network:create", "Create networks"},
		{"network:update", "Change IP pools, DNS, reverse DNS, quarantine and null-routes, the abuse policy"},
		{"network:delete", "Delete networks"},
		{"floating_ip:read", "See floating IPs"},
		{"floating_ip:update", "Attach and detach floating IPs"},
		{"floating_ip:delete", "Release floating IPs"},
		{"vpc:read", "See VPCs"},
		{"vpc:create", "Create VPCs"},
		{"vpc:update", "Change VPCs' nameservers"},
		{"vpc:delete", "Delete VPCs"},
		{"firewall_group:read", "See firewall groups"},
		{"firewall_group:update", "Create, change and delete firewall groups for everyone"},
	}},
	{"Infrastructure", []Permission{
		{"node:read", "See nodes and storage"},
		{"node:create", "Add nodes"},
		{"node:update", "Change nodes and regions, update agents, see node tokens"},
		{"node:delete", "Remove nodes"},
		{"node:drain", "Drain nodes: move every VM off a node"},
		{"node:recover", "Recover a failed node's VMs on other nodes, and remove what is left of them on it"},
		{"storage:create", "Add storage pools and volumes"},
		{"storage:update", "Change storage pools"},
		{"storage:delete", "Remove storage pools and volumes"},
		{"template:update", "Add and change templates"},
	}},
	{"Customers", []Permission{
		{"user:read", "See accounts"},
		{"user:create", "Create customer accounts"},
		{"user:update", "Change and suspend accounts, set quotas"},
		{"user:delete", "Delete accounts"},
		{"user:recover", "Reset passwords and two-factor, unlock, sign out sessions"},
		{PermImpersonate, "View and act as a customer, with everything their account can do"},
	}},
	{"Plans and billing", []Permission{
		{"plan:update", "Create and change plans"},
		{"billing:read", "See billing deliveries"},
		{"billing:update", "Retry billing deliveries"},
	}},
	{"Operations", []Permission{
		{"audit:read", "Read the audit log"},
		{"ops:read", "See alerts and the job queue"},
		{"ops:update", "Close alerts, retry and cancel jobs"},
		{"report:read", "See capacity and customer usage reports"},
		{"announcement:read", "See announcements and who they are for"},
		{"announcement:update", "Write announcements and email them to customers"},
		{"import:manage", "Import customers, plans, IP pools and VMs from another panel, and invite the imported customers"},
	}},
	{"Settings", []Permission{
		{"settings:read", "See system settings"},
		{"settings:update", "Change system settings"},
		{"webhook:read", "See outbound webhooks and what was sent to them"},
		{"webhook:update", "Add, change, test and remove outbound webhooks, and send events again"},
	}},
	{"Staff", []Permission{
		{PermStaffManage, "Assign staff roles and edit custom roles"},
	}},
}

// StaffRole is a built-in or custom role.
type StaffRole struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions"`
	BuiltIn     bool     `json:"built_in"`
}

// readEverything is every read permission in the catalog.
func readEverything(except ...string) []string {
	var out []string
	for _, a := range PermissionCatalog {
		for _, p := range a.Permissions {
			if len(p.Key) > 5 && p.Key[len(p.Key)-5:] == ":read" && !slices.Contains(except, p.Key) {
				out = append(out, p.Key)
			}
		}
	}
	return out
}

// BuiltInStaffRoles are the roles every panel has.
var BuiltInStaffRoles = []StaffRole{
	{ID: StaffRoleOwner, Name: "Owner", BuiltIn: true, Permissions: []string{PermAll, PermStaffManage},
		Description: "The founding administrator: everything, and the only one who creates or deletes staff accounts."},
	{ID: StaffRoleAdministrator, Name: "Administrator", BuiltIn: true, Permissions: []string{PermAll},
		Description: "Everything except managing staff."},
	{ID: StaffRoleSupport, Name: "Support", BuiltIn: true,
		Permissions: append(readEverything("settings:read", "billing:read", "webhook:read"), "vm:power", "vm:console", "vm:rescue", "user:recover"),
		Description: "Sees everything except settings and billing; powers VMs, opens consoles and rescue mode; recovers customer accounts."},
	{ID: StaffRoleBilling, Name: "Billing", BuiltIn: true,
		Permissions: []string{"user:read", "user:create", "user:update", "vm:read", "plan:update", "billing:read", "billing:update"},
		Description: "Customers, plans and billing."},
	{ID: StaffRoleReadOnly, Name: "Read-only", BuiltIn: true, Permissions: readEverything(),
		Description: "Sees everything, changes nothing. Stored credentials stay hidden."},
}

func builtInRole(id string) (StaffRole, bool) {
	for _, r := range BuiltInStaffRoles {
		if r.ID == id {
			return r, true
		}
	}
	return StaffRole{}, false
}

// catalogKeys is the set of permissions a custom role may hold.
var catalogKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, a := range PermissionCatalog {
		for _, p := range a.Permissions {
			m[p.Key] = true
		}
	}
	return m
}()

// InCatalog reports whether a custom role may hold p.
func InCatalog(p string) bool { return catalogKeys[p] }

// Can reports whether these permissions allow p.
func Can(perms []string, p string) bool {
	for _, x := range perms {
		if x == p || (x == PermAll && !ownerOnly[p]) {
			return true
		}
	}
	return false
}

// Can reports whether the request's user may do p.
func (u *UserContext) Can(p string) bool { return u != nil && Can(u.Permissions, p) }

// CanGrant reports whether the holder has every one of perms: nobody hands out
// a permission they do not hold.
func CanGrant(holder []string, perms []string) bool {
	for _, p := range perms {
		if p != PermStaff && !Can(holder, p) {
			return false
		}
	}
	return true
}

// FoundingAdminID is the earliest-created permanent administrator, the Owner.
// A temporary (support) account is never it. uuid.Nil when there is none.
func FoundingAdminID(ctx context.Context, db *gorm.DB) uuid.UUID {
	var row struct{ ID uuid.UUID }
	if db == nil || db.WithContext(ctx).Model(&models.User{}).Select("id").
		Where("role = ? AND expires_at IS NULL", models.RoleAdmin).
		Order("created_at ASC, id ASC").Take(&row).Error != nil {
		return uuid.Nil
	}
	return row.ID
}

// StaffRoleKey is the role a staff account has: the Owner for the founding
// administrator, Administrator when none was set.
func StaffRoleKey(u *models.User, founding uuid.UUID) string {
	if u.ID == founding {
		return StaffRoleOwner
	}
	if u.StaffRole == nil || *u.StaffRole == "" || *u.StaffRole == StaffRoleOwner {
		return StaffRoleAdministrator
	}
	return *u.StaffRole
}

// LookupStaffRole finds a built-in or custom role by key.
func LookupStaffRole(ctx context.Context, db *gorm.DB, key string) (StaffRole, bool) {
	if r, ok := builtInRole(key); ok {
		return r, true
	}
	var row models.CustomStaffRole
	if db == nil || db.WithContext(ctx).Where("id = ?", key).First(&row).Error != nil {
		return StaffRole{ID: key, Name: "Removed role"}, false
	}
	perms := []string{}
	for _, p := range row.Permissions {
		if catalogKeys[p] {
			perms = append(perms, p)
		}
	}
	return StaffRole{ID: row.ID, Name: row.Name, Description: row.Description, Permissions: perms}, true
}

// ResolveStaffRole is the role of a staff account and what it grants (the
// role's permissions plus the admin area itself).
func ResolveStaffRole(ctx context.Context, db *gorm.DB, u *models.User) (StaffRole, []string) {
	role, _ := LookupStaffRole(ctx, db, StaffRoleKey(u, FoundingAdminID(ctx, db)))
	return role, append(slices.Clone(role.Permissions), PermStaff)
}

// RequireAnyPermission lets the request through with any one of perms.
func RequireAnyPermission(perms ...string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			for _, p := range perms {
				if HasPermission(c, p) {
					return next(c)
				}
			}
			return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "Insufficient permissions"})
		}
	}
}

// RequireByMethod checks read on safe methods and write on the rest.
func RequireByMethod(read, write string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			p := write
			switch c.Request().Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				p = read
			}
			if !HasPermission(c, p) {
				return c.JSON(http.StatusForbidden, map[string]interface{}{
					"error":   "Forbidden",
					"message": "Insufficient permissions",
				})
			}
			return next(c)
		}
	}
}
