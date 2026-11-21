package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// MCP (Model Context Protocol) endpoint so AI agents can monitor and manage VMs.
//
// Every tool is a thin alias for an existing REST route: the call is replayed
// through the panel's own router with the caller's API key, so RBAC, tenant
// isolation, quotas and the audit log apply exactly as they do for the API.
// Transport is MCP Streamable HTTP in its stateless JSON form (no SSE stream).

const mcpDefaultProtocol = "2025-06-18"

var mcpProtocols = map[string]bool{"2024-11-05": true, "2025-03-26": true, "2025-06-18": true}

type mcpTool struct {
	Name        string
	Description string
	Method      string
	// Path holds {arg} placeholders filled from the call arguments. Remaining
	// arguments become the query string (GET/DELETE) or the JSON body.
	Path        string
	Props       map[string]any
	Required    []string
	ReadOnly    bool
	Destructive bool
	// Perms are the permissions the REST route checks; a caller without one
	// is not shown the tool. The route still checks them on every call.
	Perms []string
	// Route is the Echo pattern an API key's scope is matched against, when
	// Path has a placeholder that is not a path parameter ({action}).
	Route string
}

func str(desc string) map[string]any  { return map[string]any{"type": "string", "description": desc} }
func num(desc string) map[string]any  { return map[string]any{"type": "integer", "description": desc} }
func flag(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }

var vmID = map[string]any{"id": str("VM id (UUID)")}

func withVM(extra map[string]any) map[string]any {
	m := map[string]any{"id": vmID["id"]}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

var resourcesSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"cpu":  num("vCPUs"),
		"ram":  num("RAM in MB"),
		"disk": num("Disk in GB"),
	},
}

var mcpTools = []mcpTool{
	// --- monitoring ---
	{Name: "list_vms", Perms: []string{"vm:read"}, Description: "List VMs visible to the caller, with status, node and resources.", Method: "GET", Path: "/api/v1/vms", ReadOnly: true,
		Props: map[string]any{"status": str("Filter by status (running, stopped, suspended, error...)"), "node_id": str("Filter by node id"),
			"search": str("Match hostname or IP"), "page": num("Page number (1-based)"), "page_size": num("Page size")}},
	{Name: "get_vm", Perms: []string{"vm:read"}, Description: "Get one VM's details. include_status=true also asks the node for live state.", Method: "GET", Path: "/api/v1/vms/{id}", ReadOnly: true,
		Props: withVM(map[string]any{"include_status": flag("Query the node agent for live status")}), Required: []string{"id"}},
	{Name: "get_vm_metrics", Perms: []string{"vm:read"}, Description: "Current CPU, memory, disk and network usage of a VM.", Method: "GET", Path: "/api/v1/vms/{id}/metrics", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
	{Name: "get_vm_operation", Perms: []string{"vm:read"}, Description: "Progress of the VM's in-flight operation (create, rebuild, migrate...).", Method: "GET", Path: "/api/v1/vms/{id}/operation", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
	{Name: "get_vm_activity", Perms: []string{"vm:read"}, Description: "Recent audit log entries for a VM.", Method: "GET", Path: "/api/v1/vms/{id}/activity", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
	{Name: "list_snapshots", Perms: []string{"vm:read"}, Description: "List a VM's snapshots.", Method: "GET", Path: "/api/v1/vms/{id}/snapshots", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
	{Name: "list_nodes", Perms: []string{"node:read"}, Description: "List hypervisor nodes with status and capacity (admin).", Method: "GET", Path: "/api/v1/nodes", ReadOnly: true,
		Props: map[string]any{"status": str("Filter by node status")}},
	{Name: "get_node", Perms: []string{"node:read"}, Description: "Get one node's details (admin).", Method: "GET", Path: "/api/v1/nodes/{id}", ReadOnly: true,
		Props: map[string]any{"id": str("Node id")}, Required: []string{"id"}},
	{Name: "get_node_metrics", Perms: []string{"node:read"}, Description: "Current host CPU, memory, disk and network usage of a node (admin).", Method: "GET", Path: "/api/v1/nodes/{id}/metrics", ReadOnly: true,
		Props: map[string]any{"id": str("Node id")}, Required: []string{"id"}},
	{Name: "list_templates", Description: "List OS templates usable for create_vm / rebuild_vm.", Method: "GET", Path: "/api/v1/templates", ReadOnly: true,
		Props: map[string]any{}},
	{Name: "list_plans", Description: "List VPS plans (resource presets) usable for create_vm.", Method: "GET", Path: "/api/v1/plans", ReadOnly: true,
		Props: map[string]any{"active": flag("Only active plans")}},

	// --- management ---
	{Name: "vm_power", Perms: []string{"vm:read", "vm:power"}, Route: "/api/v1/vms/:id/start", Description: "Change a VM's power state.", Method: "POST", Path: "/api/v1/vms/{id}/{action}",
		Props: withVM(map[string]any{"action": map[string]any{"type": "string",
			"enum": []string{"start", "stop", "force-stop", "restart", "suspend", "unsuspend"}}}), Required: []string{"id", "action"}},
	{Name: "create_vm", Perms: []string{"vm:read", "vm:create"}, Description: "Create a VM. Returns the new VM id and a job id; poll get_vm_operation for progress.", Method: "POST", Path: "/api/v1/vms",
		Props: map[string]any{"hostname": str("Hostname"), "os_template_id": str("Template id from list_templates"), "resources": resourcesSchema,
			"plan_id": str("Plan id from list_plans. Customer keys must pass it: the plan sets the size and resources is ignored"), "node_id": str("Pin to a node (admin)"), "ip_pool_id": str("IPv4 pool id"),
			"password": str("Root password (min 8)"), "regenerate_password": flag("Generate a root password and return it once"),
			"ssh_key_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Saved SSH key ids"},
			"user_data":   str("cloud-init user-data")},
		Required: []string{"hostname"}},
	{Name: "update_vm", Perms: []string{"vm:read", "vm:update"}, Description: "Rename a VM or resize its resources.", Method: "PUT", Path: "/api/v1/vms/{id}",
		Props: withVM(map[string]any{"hostname": str("New hostname"), "resources": resourcesSchema}), Required: []string{"id"}},
	{Name: "rebuild_vm", Perms: []string{"vm:read", "vm:lifecycle"}, Description: "Reinstall a VM's OS. ALL DATA ON THE DISK IS LOST.", Method: "POST", Path: "/api/v1/vms/{id}/rebuild", Destructive: true,
		Props: withVM(map[string]any{"template_id": str("Template id (default: current)"), "preserve_ip": flag("Keep current IPs"),
			"password": str("New root password"), "regenerate_password": flag("Generate a root password and return it once")}), Required: []string{"id"}},
	{Name: "delete_vm", Perms: []string{"vm:read", "vm:delete"}, Description: "Delete a VM.", Method: "DELETE", Path: "/api/v1/vms/{id}", Destructive: true,
		Props: withVM(map[string]any{"delete_volume": flag("Also delete the disk volume")}), Required: []string{"id"}},
	{Name: "reset_password", Perms: []string{"vm:read", "vm:lifecycle"}, Description: "Set a VM's root password.", Method: "POST", Path: "/api/v1/vms/{id}/reset-password", Destructive: true,
		Props: withVM(map[string]any{"password": str("New root password")}), Required: []string{"id", "password"}},
	{Name: "create_snapshot", Perms: []string{"vm:read", "snapshot:create"}, Description: "Snapshot a VM.", Method: "POST", Path: "/api/v1/vms/{id}/snapshots",
		Props: withVM(map[string]any{"name": str("Snapshot name")}), Required: []string{"id", "name"}},
	{Name: "restore_snapshot", Perms: []string{"vm:read", "snapshot:update"}, Description: "Roll a VM back to a snapshot. Changes since the snapshot are lost.", Method: "POST", Path: "/api/v1/vms/{id}/snapshots/{snapshot_id}/restore", Destructive: true,
		Props: withVM(map[string]any{"snapshot_id": str("Snapshot id")}), Required: []string{"id", "snapshot_id"}},

	// --- backups ---
	{Name: "list_backups", Perms: []string{"backup:read"}, Description: "List a VM's backups, newest first.", Method: "GET", Path: "/api/v1/vms/{id}/backups", ReadOnly: true,
		Props: withVM(map[string]any{"status": str("Filter by status (pending, running, completed, failed)"), "limit": num("Items to return"), "offset": num("Items to skip")}), Required: []string{"id"}},
	{Name: "create_backup", Perms: []string{"backup:create"}, Description: "Take a backup of a VM now. Returns the backup id; poll list_backups for its status.", Method: "POST", Path: "/api/v1/vms/{id}/backups",
		Props: withVM(map[string]any{"compression": str("Compression (default: the panel's)")}), Required: []string{"id"}},
	{Name: "restore_backup", Perms: []string{"backup:update"}, Description: "Restore a backup over the VM. EVERYTHING WRITTEN SINCE THE BACKUP IS LOST.", Method: "POST", Path: "/api/v1/vms/{id}/backups/{backup_id}/restore", Destructive: true,
		Props: withVM(map[string]any{"backup_id": str("Backup id from list_backups")}), Required: []string{"id", "backup_id"}},

	// --- firewall ---
	{Name: "list_firewall_rules", Perms: []string{"vm:read"}, Description: "List a VM's firewall rules.", Method: "GET", Path: "/api/v1/vms/{id}/firewall-rules", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
	{Name: "add_firewall_rule", Perms: []string{"vm:update"}, Description: "Add a firewall rule to a VM.", Method: "POST", Path: "/api/v1/vms/{id}/firewall-rules",
		Props: withVM(map[string]any{
			"protocol":   map[string]any{"type": "string", "enum": []string{"tcp", "udp", "icmp", "all"}},
			"action":     map[string]any{"type": "string", "enum": []string{"allow", "deny", "reject"}},
			"direction":  map[string]any{"type": "string", "enum": []string{"inbound", "outbound"}},
			"port_range": str("Port or range, e.g. 22 or 8000-8100 (tcp and udp)"),
			"source_ip":  str("Source address or CIDR (inbound)"),
			"priority":   num("1 to 1000; lower runs first"),
		}), Required: []string{"id", "protocol", "action", "direction", "priority"}},
	{Name: "delete_firewall_rule", Perms: []string{"vm:update"}, Description: "Remove a firewall rule from a VM.", Method: "DELETE", Path: "/api/v1/vms/{id}/firewall-rules/{rule_id}", Destructive: true,
		Props: withVM(map[string]any{"rule_id": str("Rule id from list_firewall_rules")}), Required: []string{"id", "rule_id"}},

	// --- addresses ---
	{Name: "list_vm_networks", Perms: []string{"vm:read"}, Description: "A VM's network interfaces with their IP addresses.", Method: "GET", Path: "/api/v1/vms/{id}/networks", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
	{Name: "list_floating_ips", Perms: []string{"floating_ip:read"}, Description: "Floating IPs visible to the caller and the VM each points at.", Method: "GET", Path: "/api/v1/floating-ips", ReadOnly: true,
		Props: map[string]any{}},
	{Name: "list_ip_pools", Perms: []string{"network:read"}, Description: "IP pools with their usage (staff).", Method: "GET", Path: "/api/v1/ip-pools", ReadOnly: true,
		Props: map[string]any{}},

	// --- staff ---
	{Name: "list_users", Perms: []string{"user:read"}, Description: "List accounts (staff).", Method: "GET", Path: "/api/v1/users", ReadOnly: true,
		Props: map[string]any{"search": str("Match email or name"), "role": str("admin or client"), "page": num("Page number (1-based)"), "page_size": num("Page size")}},
	{Name: "get_user", Perms: []string{"user:read"}, Description: "Get one account (staff).", Method: "GET", Path: "/api/v1/users/{id}", ReadOnly: true,
		Props: map[string]any{"id": str("Account id")}, Required: []string{"id"}},
	{Name: "list_alerts", Perms: []string{"ops:read"}, Description: "Alerts: node and VM problems, failed backups and jobs (staff).", Method: "GET", Path: "/api/v1/alerts", ReadOnly: true,
		Props: map[string]any{"state": str("open for firing alerts only"), "page": num("Page number (1-based)"), "page_size": num("Page size")}},
	{Name: "get_vm_migration", Perms: []string{"vm:migrate"}, Description: "The VM's latest move to another node and its progress (staff).", Method: "GET", Path: "/api/v1/vms/{id}/migration", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
}

// MCPToolRoute is one tool's REST route, for the test that holds each tool
// to the permissions its route really checks.
type MCPToolRoute struct {
	Name, Method, Path string
	Perms              []string
}

// MCPToolRoutes lists every tool's route as an Echo pattern.
func MCPToolRoutes() []MCPToolRoute {
	out := make([]MCPToolRoute, 0, len(mcpTools))
	for _, t := range mcpTools {
		out = append(out, MCPToolRoute{Name: t.Name, Method: t.Method, Path: t.route(), Perms: t.Perms})
	}
	return out
}

// route is the Echo pattern of the tool's REST route.
func (t mcpTool) route() string {
	if t.Route != "" {
		return t.Route
	}
	return mcpPlaceholder.ReplaceAllString(t.Path, ":$1")
}

// refusal says why the caller may not use the tool, or "" when it may: its
// key's scope must reach the route and it must hold every permission the
// route checks.
func (t mcpTool) refusal(u *middleware.UserContext) string {
	switch {
	case u == nil:
		return "Not signed in"
	case u.APIKeyReadOnly && !t.ReadOnly:
		return "This API key is read-only"
	case !middleware.ScopeAllowsRoute(u.APIKeyScope, t.Method, t.route()):
		return "This API key's scope does not include " + t.Name
	}
	for _, p := range t.Perms {
		if !u.Can(p) {
			return "This account may not use " + t.Name + " (it needs " + p + ")"
		}
	}
	return ""
}

func (t mcpTool) usable(u *middleware.UserContext) bool { return t.refusal(u) == "" }

var (
	mcpToolIndex = func() map[string]mcpTool {
		m := map[string]mcpTool{}
		for _, t := range mcpTools {
			m[t.Name] = t
		}
		return m
	}()
	mcpPlaceholder = regexp.MustCompile(`\{(\w+)\}`)
	mcpSafeSegment = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func mcpError(id json.RawMessage, code int, msg string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": msg}}
}

// RegisterMCPRoutes mounts POST /api/v1/mcp (under /api so it rides the web proxy). Only API keys are accepted (never the
// browser session cookie) so a web page can't drive the endpoint cross-site.
func RegisterMCPRoutes(e *echo.Echo, db *gorm.DB) {
	requireKey := func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			h := c.Request().Header
			if h.Get("X-API-Key") == "" && !strings.HasPrefix(h.Get("Authorization"), "Bearer "+models.APITokenPrefix) {
				return c.JSON(http.StatusUnauthorized, map[string]any{"error": "Unauthorized", "message": "MCP requires an API key (Authorization: Bearer mvk_...)"})
			}
			return next(c)
		}
	}
	// The POST is only transport, so read-only keys may use it; every tool call
	// re-enters RequireAuth and is refused there if it would write.
	g := e.Group("/api/v1/mcp", requireKey, middleware.AllowReadOnlyKey, middleware.RequireAuth(db))
	g.POST("", func(c echo.Context) error { return serveMCP(c, e) })
	// No server-initiated stream: the spec says answer GET with 405.
	g.GET("", func(c echo.Context) error { return c.NoContent(http.StatusMethodNotAllowed) })
}

func serveMCP(c echo.Context, e *echo.Echo) error {
	var req mcpRequest
	if err := json.NewDecoder(io.LimitReader(c.Request().Body, 1<<20)).Decode(&req); err != nil {
		return c.JSON(http.StatusBadRequest, mcpError(nil, -32700, "parse error"))
	}
	if len(req.ID) == 0 { // notification (e.g. notifications/initialized)
		return c.NoContent(http.StatusAccepted)
	}
	result, rpcErr := handleMCP(c, e, req)
	if rpcErr != nil {
		return c.JSON(http.StatusOK, rpcErr)
	}
	return c.JSON(http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
}

func handleMCP(c echo.Context, e *echo.Echo, req mcpRequest) (any, map[string]any) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		version := mcpDefaultProtocol
		if mcpProtocols[p.ProtocolVersion] {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "maburvm", "version": "1"},
			"instructions":    "Monitor and manage MaburVM virtual machines, backups, firewalls and addresses; staff keys also see nodes, accounts, alerts and migrations. Only the tools this key may use are listed. Destructive tools (rebuild, delete, restore, reset_password, delete_firewall_rule) act immediately; confirm with the user first.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := make([]map[string]any, 0, len(mcpTools))
		u, _ := middleware.GetUserContext(c)
		for _, t := range mcpTools {
			if !t.usable(u) {
				continue // would be refused anyway; don't advertise it
			}
			schema := map[string]any{"type": "object", "properties": t.Props}
			if len(t.Required) > 0 {
				schema["required"] = t.Required
			}
			tools = append(tools, map[string]any{
				"name": t.Name, "description": t.Description, "inputSchema": schema,
				"annotations": map[string]any{"readOnlyHint": t.ReadOnly, "destructiveHint": t.Destructive},
			})
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, mcpError(req.ID, -32602, "invalid params")
		}
		t, ok := mcpToolIndex[p.Name]
		if !ok {
			return nil, mcpError(req.ID, -32602, "unknown tool: "+p.Name)
		}
		if u, _ := middleware.GetUserContext(c); t.refusal(u) != "" {
			return mcpText(t.refusal(u), true), nil
		}
		status, body, err := callMCPTool(c, e, t, p.Arguments)
		if err != nil {
			return mcpText(err.Error(), true), nil
		}
		if status >= 400 {
			// The error envelope: say what went wrong first, then the whole answer.
			var env struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(body), &env) == nil && env.Error.Message != "" {
				return mcpText(fmt.Sprintf("HTTP %d %s: %s\n%s", status, env.Error.Code, env.Error.Message, body), true), nil
			}
		}
		return mcpText(fmt.Sprintf("HTTP %d\n%s", status, body), status >= 400), nil
	}
	return nil, mcpError(req.ID, -32601, "method not found: "+req.Method)
}

func mcpText(text string, isError bool) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}

// callMCPTool replays the tool as a REST request through the panel's router,
// carrying the caller's credentials and client IP headers.
func callMCPTool(c echo.Context, e *echo.Echo, t mcpTool, args map[string]any) (int, string, error) {
	rest := map[string]any{}
	for k, v := range args {
		rest[k] = v
	}
	var missing error
	path := mcpPlaceholder.ReplaceAllStringFunc(t.Path, func(m string) string {
		name := m[1 : len(m)-1]
		v, _ := rest[name].(string)
		delete(rest, name)
		if !mcpSafeSegment.MatchString(v) {
			missing = fmt.Errorf("argument %q is required and must be an id", name)
		}
		return v
	})
	if missing != nil {
		return 0, "", missing
	}
	for _, r := range t.Required {
		if _, ok := args[r]; !ok {
			return 0, "", fmt.Errorf("missing required argument %q", r)
		}
	}

	var body io.Reader
	if t.Method == http.MethodGet || t.Method == http.MethodDelete {
		q := url.Values{}
		for k, v := range rest {
			q.Set(k, fmt.Sprint(v))
		}
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
	} else {
		b, _ := json.Marshal(rest)
		body = bytes.NewReader(b)
	}

	in := c.Request()
	out, err := http.NewRequestWithContext(in.Context(), t.Method, path, body)
	if err != nil {
		return 0, "", err
	}
	out.RemoteAddr = in.RemoteAddr
	out.Header.Set("Content-Type", "application/json")
	for _, h := range []string{"Authorization", "X-API-Key", "X-Forwarded-For", "X-Real-IP", "User-Agent"} {
		if v := in.Header.Get(h); v != "" {
			out.Header.Set(h, v)
		}
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, out)
	return rec.Code, rec.Body.String(), nil
}
