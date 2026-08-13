package handler

import (
	"context"
	"net/http"
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
}

func NewVersionHandler(nodeService *service.NodeService) *VersionHandler {
	return &VersionHandler{nodeService: nodeService}
}

// RegisterVersionRoutes mounts the endpoint. Authenticated but not admin-only: a
// build identifier is not a secret, and support conversations go faster when the
// person reporting a problem can read it.
func RegisterVersionRoutes(e *echo.Echo, h *VersionHandler, db *gorm.DB) {
	g := e.Group("/api/v1/system")
	g.Use(middleware.RequireAuth(db))
	g.GET("/version", h.Version)
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
