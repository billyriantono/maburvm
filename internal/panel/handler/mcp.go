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
	{Name: "list_vms", Description: "List VMs visible to the caller, with status, node and resources.", Method: "GET", Path: "/api/v1/vms", ReadOnly: true,
		Props: map[string]any{"status": str("Filter by status (running, stopped, suspended, error...)"), "node_id": str("Filter by node id"),
			"search": str("Match hostname or IP"), "page": num("Page number (1-based)"), "page_size": num("Page size")}},
	{Name: "get_vm", Description: "Get one VM's details. include_status=true also asks the node for live state.", Method: "GET", Path: "/api/v1/vms/{id}", ReadOnly: true,
		Props: withVM(map[string]any{"include_status": flag("Query the node agent for live status")}), Required: []string{"id"}},
	{Name: "get_vm_metrics", Description: "Current CPU, memory, disk and network usage of a VM.", Method: "GET", Path: "/api/v1/vms/{id}/metrics", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
	{Name: "get_vm_operation", Description: "Progress of the VM's in-flight operation (create, rebuild, migrate...).", Method: "GET", Path: "/api/v1/vms/{id}/operation", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
	{Name: "get_vm_activity", Description: "Recent audit log entries for a VM.", Method: "GET", Path: "/api/v1/vms/{id}/activity", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
	{Name: "list_snapshots", Description: "List a VM's snapshots.", Method: "GET", Path: "/api/v1/vms/{id}/snapshots", ReadOnly: true,
		Props: vmID, Required: []string{"id"}},
	{Name: "list_nodes", Description: "List hypervisor nodes with status and capacity (admin).", Method: "GET", Path: "/api/v1/nodes", ReadOnly: true,
		Props: map[string]any{"status": str("Filter by node status")}},
	{Name: "get_node", Description: "Get one node's details (admin).", Method: "GET", Path: "/api/v1/nodes/{id}", ReadOnly: true,
		Props: map[string]any{"id": str("Node id")}, Required: []string{"id"}},
	{Name: "get_node_metrics", Description: "Current host CPU, memory, disk and network usage of a node (admin).", Method: "GET", Path: "/api/v1/nodes/{id}/metrics", ReadOnly: true,
		Props: map[string]any{"id": str("Node id")}, Required: []string{"id"}},
	{Name: "list_templates", Description: "List OS templates usable for create_vm / rebuild_vm.", Method: "GET", Path: "/api/v1/templates", ReadOnly: true,
		Props: map[string]any{}},
	{Name: "list_plans", Description: "List VPS plans (resource presets) usable for create_vm.", Method: "GET", Path: "/api/v1/plans", ReadOnly: true,
		Props: map[string]any{"active": flag("Only active plans")}},

	// --- management ---
	{Name: "vm_power", Description: "Change a VM's power state.", Method: "POST", Path: "/api/v1/vms/{id}/{action}",
		Props: withVM(map[string]any{"action": map[string]any{"type": "string",
			"enum": []string{"start", "stop", "force-stop", "restart", "suspend", "unsuspend"}}}), Required: []string{"id", "action"}},
	{Name: "create_vm", Description: "Create a VM. Returns the new VM id and a job id; poll get_vm_operation for progress.", Method: "POST", Path: "/api/v1/vms",
		Props: map[string]any{"hostname": str("Hostname"), "os_template_id": str("Template id from list_templates"), "resources": resourcesSchema,
			"plan_id": str("Plan id from list_plans"), "node_id": str("Pin to a node (admin)"), "ip_pool_id": str("IPv4 pool id"),
			"password": str("Root password (min 8)"), "regenerate_password": flag("Generate a root password and return it once"),
			"ssh_key_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Saved SSH key ids"},
			"user_data":   str("cloud-init user-data")},
		Required: []string{"hostname", "resources"}},
	{Name: "update_vm", Description: "Rename a VM or resize its resources.", Method: "PUT", Path: "/api/v1/vms/{id}",
		Props: withVM(map[string]any{"hostname": str("New hostname"), "resources": resourcesSchema}), Required: []string{"id"}},
	{Name: "rebuild_vm", Description: "Reinstall a VM's OS. ALL DATA ON THE DISK IS LOST.", Method: "POST", Path: "/api/v1/vms/{id}/rebuild", Destructive: true,
		Props: withVM(map[string]any{"template_id": str("Template id (default: current)"), "preserve_ip": flag("Keep current IPs"),
			"password": str("New root password"), "regenerate_password": flag("Generate a root password and return it once")}), Required: []string{"id"}},
	{Name: "delete_vm", Description: "Delete a VM.", Method: "DELETE", Path: "/api/v1/vms/{id}", Destructive: true,
		Props: withVM(map[string]any{"delete_volume": flag("Also delete the disk volume")}), Required: []string{"id"}},
	{Name: "reset_password", Description: "Set a VM's root password.", Method: "POST", Path: "/api/v1/vms/{id}/reset-password", Destructive: true,
		Props: withVM(map[string]any{"password": str("New root password")}), Required: []string{"id", "password"}},
	{Name: "create_snapshot", Description: "Snapshot a VM.", Method: "POST", Path: "/api/v1/vms/{id}/snapshots",
		Props: withVM(map[string]any{"name": str("Snapshot name")}), Required: []string{"id", "name"}},
	{Name: "restore_snapshot", Description: "Roll a VM back to a snapshot. Changes since the snapshot are lost.", Method: "POST", Path: "/api/v1/vms/{id}/snapshots/{snapshot_id}/restore", Destructive: true,
		Props: withVM(map[string]any{"snapshot_id": str("Snapshot id")}), Required: []string{"id", "snapshot_id"}},
	{Name: "migrate_vm", Description: "Move a VM to another node (admin).", Method: "POST", Path: "/api/v1/vms/{id}/migrate",
		Props: withVM(map[string]any{"dest_node_id": str("Destination node id"), "live": flag("Live migrate (default true)"),
			"copy_storage": flag("Copy the disk to the destination")}), Required: []string{"id", "dest_node_id"}},
}

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
			"instructions":    "Monitor and manage MaburVM virtual machines. Destructive tools (rebuild, delete, restore, reset_password) act immediately; confirm with the user first.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := make([]map[string]any, 0, len(mcpTools))
		u, _ := middleware.GetUserContext(c)
		for _, t := range mcpTools {
			if u != nil && u.APIKeyReadOnly && !t.ReadOnly {
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
		status, body, err := callMCPTool(c, e, t, p.Arguments)
		if err != nil {
			return mcpText(err.Error(), true), nil
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
