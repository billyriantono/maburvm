package handler

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/buildinfo"
	"gorm.io/gorm"
)

// VersionHandler answers "which build is actually running?" — for the panel and
// for every node.
//
// It exists because that question had no answer: the settings page showed a
// hardcoded "Version: 1.0.0", so a deploy could not be confirmed from the UI.
// Node agents matter as much as the panel here, since they are deployed
// separately and a node with a long-running export is deliberately left on an
// older build until it finishes.
type VersionHandler struct {
	nodeService *service.NodeService
	// agentDir holds agent-<arch> binaries built into the panel image from the
	// panel's own commit (Dockerfile.panel), shared with the bootstrap installer.
	agentDir string
	// updating stops two clicks from scheduling two swaps on the same node.
	updating sync.Map
}

func NewVersionHandler(nodeService *service.NodeService) *VersionHandler {
	dir := os.Getenv("AGENT_BINARY_DIR")
	if dir == "" {
		dir = "bin/linux"
	}
	return &VersionHandler{nodeService: nodeService, agentDir: dir}
}

// RegisterVersionRoutes mounts the endpoint. Authenticated but not admin-only: a
// build identifier is not a secret, and support conversations go faster when the
// person reporting a problem can read it.
func RegisterVersionRoutes(e *echo.Echo, h *VersionHandler, db *gorm.DB) {
	g := e.Group("/api/v1/system")
	g.Use(middleware.RequireAuth(db))
	g.GET("/version", h.Version)
	// Pushes code that runs as root on the node: admins only.
	g.POST("/nodes/:id/update-agent", h.UpdateAgent, middleware.RequirePermission("admin:access"))
}

// UpdateAgent pushes the agent binary bundled with this panel to one node.
//
// The bundled binary was built from the panel's own commit, so after the node
// restarts it reports "same as panel". The node verifies and test-runs the
// binary before swapping, and rolls itself back if the new agent does not stay
// up — see internal/agent/server/self_update.go.
func (h *VersionHandler) UpdateAgent(c echo.Context) error {
	nodeID := c.Param("id")
	fail := func(code int, msg string) error {
		return c.JSON(code, map[string]any{"success": false, "error": msg, "message": msg})
	}
	if h.nodeService == nil {
		return fail(http.StatusServiceUnavailable, "node service unavailable")
	}
	if _, busy := h.updating.LoadOrStore(nodeID, true); busy {
		return fail(http.StatusConflict, "an update for this node is already in progress")
	}
	defer h.updating.Delete(nodeID)

	ctx, cancel := context.WithTimeout(c.Request().Context(), 3*time.Minute)
	defer cancel()
	if err := h.nodeService.EnsureAgentRegistered(ctx, nodeID); err != nil {
		return fail(http.StatusBadGateway, "node unreachable: "+err.Error())
	}
	info, err := h.nodeService.AgentClient().GetNodeInfo(ctx, nodeID)
	if err != nil {
		return fail(http.StatusBadGateway, "node unreachable: "+err.Error())
	}
	arch := map[string]string{"x86_64": "amd64", "amd64": "amd64", "aarch64": "arm64", "arm64": "arm64"}[info.Architecture]
	if arch == "" {
		return fail(http.StatusBadRequest, "unsupported node architecture: "+info.Architecture)
	}
	path := filepath.Join(h.agentDir, "agent-"+arch)
	binary, err := os.ReadFile(path)
	if err != nil {
		return fail(http.StatusServiceUnavailable, "this panel image has no "+arch+" agent bundled ("+path+")")
	}

	panelCommit := buildinfo.Get().Commit
	msg, err := h.nodeService.AgentClient().UpdateAgent(ctx, nodeID, arch, panelCommit, binary)
	if err != nil {
		return fail(http.StatusBadGateway, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]any{"success": true, "message": msg})
}

// nodeBuild is one node's agent build, or why it could not be read.
type nodeBuild struct {
	NodeID    string `json:"node_id"`
	NodeName  string `json:"node_name"`
	Status    string `json:"status"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	ShortSHA  string `json:"short_sha"`
	BuildTime string `json:"build_time"`
	// Matches is true when this agent was built from the same commit as the
	// panel. That comparison is the point of the whole endpoint.
	Matches bool   `json:"matches_panel"`
	Error   string `json:"error,omitempty"`
}

func (h *VersionHandler) Version(c echo.Context) error {
	panelBuild := buildinfo.Get()

	return c.JSON(http.StatusOK, map[string]any{
		"data": map[string]any{
			"panel": panelBuild,
			"nodes": h.nodeBuilds(c.Request().Context(), panelBuild.Commit),
		},
		"success": true,
	})
}

// nodeBuilds asks every node which agent it is running, in parallel.
//
// One unreachable node must not decide how long this page takes, so each is
// given its own short deadline and a failure is reported per node rather than
// failing the request — "cannot reach this node" is itself the answer someone
// checking a deploy needs.
func (h *VersionHandler) nodeBuilds(ctx context.Context, panelCommit string) []nodeBuild {
	if h.nodeService == nil {
		return []nodeBuild{}
	}
	nodes, err := h.nodeService.ListNodes(ctx, nil, 0, 0)
	if err != nil {
		return []nodeBuild{}
	}

	out := make([]nodeBuild, len(nodes))
	var wg sync.WaitGroup
	for i := range nodes {
		out[i] = nodeBuild{
			NodeID:   nodes[i].ID,
			NodeName: nodes[i].Name,
			Status:   string(nodes[i].Status),
		}
		wg.Add(1)
		go func(idx int, nodeID string) {
			defer wg.Done()
			nctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			defer cancel()

			if err := h.nodeService.EnsureAgentRegistered(nctx, nodeID); err != nil {
				out[idx].Error = err.Error()
				return
			}
			info, err := h.nodeService.AgentClient().GetNodeInfo(nctx, nodeID)
			if err != nil {
				out[idx].Error = err.Error()
				return
			}
			out[idx].Version = info.AgentVersion
			out[idx].Commit = info.AgentCommit
			out[idx].ShortSHA = buildinfo.ShortSHA(info.AgentCommit)
			out[idx].BuildTime = info.AgentBuildTime
			// Both empty would compare equal, which would report an unstamped
			// agent as matching an unstamped panel — the one case where we know
			// nothing at all.
			out[idx].Matches = info.AgentCommit != "" && info.AgentCommit == panelCommit
		}(i, nodes[i].ID)
	}
	wg.Wait()
	return out
}
