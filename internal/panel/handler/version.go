package handler

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/agentsign"
	"github.com/maburvm/panel/internal/shared/buildinfo"
	"github.com/maburvm/panel/internal/shared/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	// Alerts hears of an update the node accepted but did not keep. Optional.
	Alerts *service.AlertService
	// confirmSettle, confirmWait and confirmPoll pace confirmUpdate; tests
	// shorten them.
	confirmSettle, confirmWait, confirmPoll time.Duration
}

func NewVersionHandler(nodeService *service.NodeService) *VersionHandler {
	dir := os.Getenv("AGENT_BINARY_DIR")
	if dir == "" {
		dir = "bin/linux"
	}
	return &VersionHandler{nodeService: nodeService, agentDir: dir, confirmSettle: agentUpdateSettle, confirmWait: agentUpdateWait, confirmPoll: 5 * time.Second}
}

// agentUpdateWait is how long a node has to report the new build after it
// accepted it: the swap, the restart and the node's own 30 second check.
const agentUpdateWait = 3 * time.Minute

// agentUpdateSettle is when the node has decided: the swap script waits 2s,
// restarts the agent and rolls it back unless it is still up 30s later. A new
// build seen before then may yet be rolled back.
const agentUpdateSettle = 45 * time.Second

// RegisterVersionRoutes mounts the endpoint. Authenticated but not admin-only: a
// build identifier is not a secret, and support conversations go faster when the
// person reporting a problem can read it.
func RegisterVersionRoutes(e *echo.Echo, h *VersionHandler, db *gorm.DB) {
	g := e.Group("/api/v1/system")
	g.Use(middleware.RequireAuth(db))
	g.GET("/version", h.Version)
	// Pushes code that runs as root on the node: admins only.
	g.POST("/nodes/:id/update-agent", h.UpdateAgent, middleware.RequirePermission("node:update"))
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
	// Without a commit the node cannot check that the binary it test-runs is the one sent, nor can the panel confirm it took.
	if buildinfo.Get().Commit == "" {
		return fail(http.StatusConflict, "this panel image is not stamped with a revision, so it cannot update agents. Rebuild it with GIT_COMMIT set (scripts/deploy-panel.sh does this)")
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
		hint := ""
		if arch == "arm64" {
			hint = ". Build the panel image with --build-arg AGENT_ARM64=1 to bundle one"
		}
		return fail(http.StatusServiceUnavailable, "this panel image has no "+arch+" agent bundled ("+path+")"+hint)
	}

	// An offline signature, when the operator made one, sits beside the binary.
	var signature []byte
	if raw, err := os.ReadFile(path + ".sig"); err == nil {
		if signature, err = agentsign.DecodeSignature(string(raw)); err != nil {
			return fail(http.StatusServiceUnavailable, "the signature beside the bundled agent ("+path+".sig) is not valid: "+err.Error())
		}
	}

	panelCommit := buildinfo.Get().Commit
	msg, err := h.nodeService.AgentClient().UpdateAgent(ctx, nodeID, arch, panelCommit, binary, signature)
	if err != nil {
		if status.Code(err) == codes.Unimplemented {
			return fail(http.StatusBadRequest, "this node's agent is too old to be updated from the panel. Update it once on the node with scripts/deploy-agent.sh; later updates can be made from here")
		}
		return fail(http.StatusBadGateway, err.Error())
	}
	name := nodeID
	if n, nerr := h.nodeService.GetNode(ctx, nodeID); nerr == nil {
		name = n.Name
	}
	slog.Default().Info("agent update accepted by node", "node", name, "node_id", nodeID, "commit", panelCommit, "arch", arch)
	go h.confirmUpdate(nodeID, name, panelCommit, info.AgentCommit, func(ctx context.Context) (string, error) {
		i, err := h.nodeService.AgentClient().GetNodeInfo(ctx, nodeID)
		if err != nil {
			return "", err
		}
		return i.AgentCommit, nil
	})
	return c.JSON(http.StatusOK, map[string]any{"success": true, "message": msg})
}

// confirmUpdate waits for a node that accepted an update to report the new
// build. The node rolls back an agent that does not stay up, and the page that
// asked may be closed by then, so a node that is not on the new build when the
// wait is over raises an alert. read asks the node for its agent's commit.
func (h *VersionHandler) confirmUpdate(nodeID, name, want, from string, read func(context.Context) (string, error)) bool {
	deadline := time.Now().Add(h.confirmWait)
	time.Sleep(h.confirmSettle)
	seen := "did not answer"
	for time.Now().Before(deadline) {
		time.Sleep(h.confirmPoll)
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		commit, err := read(ctx)
		cancel()
		if err != nil {
			seen = "did not answer (" + err.Error() + ")"
			continue
		}
		if want != "" && commit == want {
			slog.Default().Info("agent update confirmed: node runs the new build", "node", name, "node_id", nodeID, "commit", want)
			return true
		}
		seen = "still runs " + shortOr(commit, "an unstamped agent")
	}
	detail := "The node accepted " + shortOr(want, "the bundled agent") + " but " + seen +
		" a few minutes later. A node that is back on " + shortOr(from, "its previous agent") + " rolled the update back; on the node, journalctl -u 'maburvm-agent-update-*' says why."
	slog.Default().Warn("agent update not confirmed", "node", name, "node_id", nodeID, "commit", want, "seen", seen)
	h.Alerts.Event(context.Background(), service.AlertInput{
		Key: "agent_update:" + nodeID, Kind: models.AlertKindJobFailed, Severity: models.AlertSeverityWarning,
		Title: "Agent update on " + name + " did not take", Detail: detail,
		ResourceType: "node", ResourceID: nodeID, ResourceName: name,
	})
	return false
}

func shortOr(commit, missing string) string {
	if commit == "" {
		return missing
	}
	return buildinfo.ShortSHA(commit)
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

	// Node names, states and agent errors are infrastructure; customers get the
	// panel build only (and do not get to fan a request out to every node).
	if isClientRole(c) {
		return c.JSON(http.StatusOK, map[string]any{
			"data":    map[string]any{"panel": panelBuild, "nodes": []nodeBuild{}},
			"success": true,
		})
	}

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
