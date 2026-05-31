package handler

import (
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

//go:embed install_agent.sh
var installAgentScript string

// ProvisionHandler serves the node bootstrap installer script and the prebuilt
// agent binary so a new node can be enrolled with a single copy-paste command:
//
//	curl -fsSL https://<panel>/install-agent.sh | sudo TOKEN=<node-token> bash
//
// Both endpoints are intentionally unauthenticated: a brand-new node has no
// panel credentials yet, and neither the script nor the binary is secret (the
// node token supplied by the operator is what authorizes the agent).
type ProvisionHandler struct {
	binaryDir string // where prebuilt agent-<arch> binaries live (AGENT_BINARY_DIR)
	publicURL string // panel base URL baked into the script (PANEL_PUBLIC_URL)
}

// NewProvisionHandler builds the handler. binaryDir defaults to ./bin/linux.
func NewProvisionHandler(binaryDir, publicURL string) *ProvisionHandler {
	if binaryDir == "" {
		binaryDir = "bin/linux"
	}
	return &ProvisionHandler{binaryDir: binaryDir, publicURL: strings.TrimRight(publicURL, "/")}
}

// baseURL resolves the panel URL to bake into the script: the configured
// PANEL_PUBLIC_URL when set, otherwise derived from the incoming request.
func (h *ProvisionHandler) baseURL(c echo.Context) string {
	if h.publicURL != "" {
		return h.publicURL
	}
	scheme := "https"
	if fp := c.Request().Header.Get("X-Forwarded-Proto"); fp != "" {
		scheme = fp
	} else if c.Request().TLS == nil {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s", scheme, c.Request().Host)
}

// InstallScript serves the bootstrap installer with the panel URL templated in.
func (h *ProvisionHandler) InstallScript(c echo.Context) error {
	script := strings.ReplaceAll(installAgentScript, "__PANEL_URL__", h.baseURL(c))
	return c.Blob(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(script))
}

// AgentBinary serves the prebuilt agent for the requested arch (amd64|arm64).
func (h *ProvisionHandler) AgentBinary(c echo.Context) error {
	arch := c.QueryParam("arch")
	if arch == "" {
		arch = "amd64"
	}
	if arch != "amd64" && arch != "arm64" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "unsupported arch", "message": "arch must be amd64 or arm64"})
	}

	path := filepath.Join(h.binaryDir, "agent-"+arch)
	if _, err := os.Stat(path); err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
			"error": "agent binary not available",
			"message": fmt.Sprintf("expected %q. Build it with `make build-agent-linux` (or set AGENT_BINARY_DIR) "+
				"and place agent-%s there.", path, arch),
		})
	}
	c.Response().Header().Set("Content-Disposition", "attachment; filename=maburvm-agent")
	return c.Attachment(path, "maburvm-agent")
}

// RegisterProvisionRoutes wires the public bootstrap endpoints.
func RegisterProvisionRoutes(e *echo.Echo, h *ProvisionHandler) {
	e.GET("/install-agent.sh", h.InstallScript)
	e.GET("/api/v1/nodes/agent-binary", h.AgentBinary)
}
