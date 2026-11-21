package middleware

import (
	"net/http"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
)

// A narrow API key scope is enforced twice: the key's permissions are cut down
// to the scope's, and each request must match one of the scope's routes, so
// routes that check no permission (account pages, SSH keys, tickets) are out of
// reach too. full and read_only keep every route; read_only refuses writes.

var apiKeyScopePerms = map[string][]string{
	models.APIKeyScopeVMPower: {"vm:read", "vm:power"},
	models.APIKeyScopeBilling: {"vm:read", "billing:read", "billing:update", "plan:update",
		"user:read", "user:create", "user:update", "report:read", "floating_ip:read"},
}

// scopeRoute matches a method ("*" for any) and an Echo route pattern, or
// every pattern under it when prefix is set.
type scopeRoute struct {
	method, path string
	prefix       bool
}

var apiKeyScopeRoutes = map[string][]scopeRoute{
	models.APIKeyScopeVMPower: {
		{method: http.MethodGet, path: "/api/v1/vms"},
		{method: http.MethodGet, path: "/api/v1/vms/:id"},
		{method: http.MethodGet, path: "/api/v1/vms/:id/operation"},
		{method: http.MethodGet, path: "/api/v1/vms/:id/metrics"},
		{method: http.MethodPost, path: "/api/v1/vms/:id/start"},
		{method: http.MethodPost, path: "/api/v1/vms/:id/stop"},
		{method: http.MethodPost, path: "/api/v1/vms/:id/force-stop"},
		{method: http.MethodPost, path: "/api/v1/vms/:id/restart"},
	},
	models.APIKeyScopeBilling: {
		{method: "*", path: "/api/v1/billing", prefix: true},
		{method: "*", path: "/api/v1/plans", prefix: true},
		{method: "*", path: "/api/v1/users", prefix: true},
		{method: http.MethodGet, path: "/api/v1/reports", prefix: true},
		{method: http.MethodGet, path: "/api/v1/vms"},
		{method: http.MethodGet, path: "/api/v1/vms/:id"},
		{method: http.MethodPut, path: "/api/v1/vms/:id"},
		{method: http.MethodGet, path: "/api/v1/vms/:id/bandwidth", prefix: true},
		{method: http.MethodGet, path: "/api/v1/floating-ips/billing"},
		{method: http.MethodGet, path: "/api/v1/quota"},
	},
}

// ScopePermissions narrows an owner's permissions to what a key scope allows.
// The staff area itself is kept, so a narrowed staff key is still staff.
func ScopePermissions(scope string, perms []string) []string {
	allowed, narrowed := apiKeyScopePerms[scope]
	if !narrowed {
		return perms
	}
	out := []string{}
	for _, p := range allowed {
		if Can(perms, p) {
			out = append(out, p)
		}
	}
	for _, p := range perms {
		if p == PermStaff {
			out = append(out, PermStaff)
		}
	}
	return out
}

// ScopeAllowsRoute reports whether a key of this scope may call method on an
// Echo route pattern. full and read_only allow every route.
func ScopeAllowsRoute(scope, method, path string) bool {
	routes, narrowed := apiKeyScopeRoutes[scope]
	if !narrowed {
		return true
	}
	for _, r := range routes {
		if r.method != "*" && r.method != method && !(method == http.MethodHead && r.method == http.MethodGet) {
			continue
		}
		if path == r.path || (r.prefix && strings.HasPrefix(path, r.path+"/")) {
			return true
		}
	}
	return false
}

// ValidAPIKeyScope reports a scope a key may be given ("" means full).
func ValidAPIKeyScope(scope string) bool {
	if scope == "" {
		return true
	}
	for _, s := range models.APIKeyScopes {
		if s == scope {
			return true
		}
	}
	return false
}

// apiKeyScopeWords says what a narrowed key may do, for its refusals.
var apiKeyScopeWords = map[string]string{
	models.APIKeyScopeVMPower: "This API key may only list VMs and start, stop or restart them",
	models.APIKeyScopeBilling: "This API key may only reach billing, plans, accounts and usage",
}
