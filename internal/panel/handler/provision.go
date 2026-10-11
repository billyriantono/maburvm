package handler

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
)

//go:embed install_agent.sh
var installAgentScript string

// agentBootstrapUnavailable is the canonical diagnostic returned by the public
// node-bootstrap endpoints while Phase 0 containment is active. It deliberately
// reveals no paths, tokens, environment, or binaries — only that the verified
// Phase 1 node deployment and trust flow is required before auto-bootstrap works.
var agentBootstrapUnavailable = map[string]string{
	"error":   "agent_bootstrap_unavailable",
	"message": "Agent auto-bootstrap is unavailable pending the verified Phase 1 node deployment and trust flow.",
}

// ProvisionHandler serves the node bootstrap installer script and the prebuilt
// agent binary. All endpoints are Phase 0-contained: they intentionally return
// a JSON 503 instead of serving the installer/binary/checksums, because the
// agent artifact is not present in the panel image and the agent startup
// contract is incomplete.
//
// The installer/binary serving logic is preserved but gated behind contained;
// while containment is active it is never executed (the handler returns before
// reaching it). Phase 1 owns deliberately re-enabling these endpoints.
type ProvisionHandler struct {
	binaryDir string // where prebuilt agent-<arch> binaries live (AGENT_BINARY_DIR)
	publicURL string // panel base URL baked into the script (PANEL_PUBLIC_URL)

	// contained gates the real bootstrap behavior. When true (the Phase 0
	// default) InstallScript and AgentBinary return a JSON 503 and never run the
	// embedded installer/binary-serving code. Phase 1 sets this to false only
	// after the verified node deployment and trust flow exists.
	contained bool
}

// NewProvisionHandler builds the handler in Phase 0 contained mode. binaryDir
// defaults to ./bin/linux. Passing contained=false re-enables the real
// bootstrap behavior (owned by Phase 1).
func NewProvisionHandler(binaryDir, publicURL string) *ProvisionHandler {
	if binaryDir == "" {
		binaryDir = "bin/linux"
	}
	return &ProvisionHandler{binaryDir: binaryDir, publicURL: strings.TrimRight(publicURL, "/"), contained: true}
}

// baseURL resolves the panel URL to bake into the script: the configured
// PANEL_PUBLIC_URL when set, otherwise derived from the incoming request.
//
// NOTE: only reached when containment is lifted (Phase 1).
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

// binaryPath is where the prebuilt agent for arch lives under binaryDir.
func binaryPath(binaryDir, arch string) string {
	return filepath.Join(binaryDir, "agent-"+arch)
}

// binaryDigest returns the hex-encoded SHA-256 of the prebuilt agent binary
// for arch, or "" when the binary is absent/unreadable. InstallScript bakes
// both arch digests into the served installer, so an empty digest makes the
// installer abort with a clear message instead of piping an unverified binary
// into bash.
func (h *ProvisionHandler) binaryDigest(arch string) string {
	data, err := os.ReadFile(binaryPath(h.binaryDir, arch))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// InstallScript serves the bootstrap installer with the panel URL templated in.
//
// The SHA-256 digest of each arch's agent binary is templated in alongside the
// panel URL, so the installer verifies the downloaded binary against digests it
// received before the download — a truncated or tampered binary aborts the
// install instead of being executed as root.
//
// While Phase 0 containment is active it returns a JSON 503 diagnostic instead
// and never reaches the embedded installer. The Next.js rewrite of
// /install-agent.sh transparently relays this (no frontend change needed).
func (h *ProvisionHandler) InstallScript(c echo.Context) error {
	if h.contained {
		return c.JSON(http.StatusServiceUnavailable, agentBootstrapUnavailable)
	}
	script := strings.ReplaceAll(installAgentScript, "__PANEL_URL__", h.baseURL(c))
	script = strings.ReplaceAll(script, "__AGENT_SHA256_AMD64__", h.binaryDigest("amd64"))
	script = strings.ReplaceAll(script, "__AGENT_SHA256_ARM64__", h.binaryDigest("arm64"))
	return c.Blob(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(script))
}

// AgentBinary serves the prebuilt agent for the requested arch (amd64|arm64).
//
// While Phase 0 containment is active it returns a JSON 503 diagnostic instead
// and never reaches the binary attachment code.
func (h *ProvisionHandler) AgentBinary(c echo.Context) error {
	if h.contained {
		return c.JSON(http.StatusServiceUnavailable, agentBootstrapUnavailable)
	}
	arch := c.QueryParam("arch")
	if arch == "" {
		arch = "amd64"
	}
	if arch != "amd64" && arch != "arm64" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "unsupported arch", "message": "arch must be amd64 or arm64"})
	}

	path := binaryPath(h.binaryDir, arch)
	if _, err := os.Stat(path); err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
			"error": "agent binary not available",
			"message": fmt.Sprintf("expected %q. Build it with `make build-agent-linux` (or set AGENT_BINARY_DIR) "+
				"and place agent-%s there.", path, arch),
		})
	}
	// Publish the digest with the binary so scripted consumers (self-update,
	// mirrors) can verify without a second request. The installer additionally
	// embeds both digests at script-serve time; this header covers whoever
	// downloads the binary directly.
	if digest := h.binaryDigest(arch); digest != "" {
		c.Response().Header().Set("X-Checksum-Sha256", digest)
	}
	c.Response().Header().Set("Content-Disposition", "attachment; filename=maburvm-agent")
	return c.Attachment(path, "maburvm-agent")
}

// AgentBinaryChecksum reports the SHA-256 of the prebuilt agent binary for the
// requested arch as JSON ({arch, algorithm, digest}). Lets operators and
// tooling pin a digest out-of-band and verify a downloaded binary before
// running it. Same Phase 0 containment as the other provision endpoints.
func (h *ProvisionHandler) AgentBinaryChecksum(c echo.Context) error {
	if h.contained {
		return c.JSON(http.StatusServiceUnavailable, agentBootstrapUnavailable)
	}
	arch := c.QueryParam("arch")
	if arch == "" {
		arch = "amd64"
	}
	if arch != "amd64" && arch != "arm64" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "unsupported arch", "message": "arch must be amd64 or arm64"})
	}
	digest := h.binaryDigest(arch)
	if digest == "" {
		path := binaryPath(h.binaryDir, arch)
		return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
			"error": "agent binary not available",
			"message": fmt.Sprintf("expected %q. Build it with `make build-agent-linux` (or set AGENT_BINARY_DIR) "+
				"and place agent-%s there.", path, arch),
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success":   true,
		"arch":      arch,
		"algorithm": "sha256",
		"digest":    digest,
	})
}

// RegisterProvisionRoutes wires the public bootstrap endpoints and preserves
// their URL shape (unauthenticated) so Phase 1 can deliberately re-enable the
// underlying behavior later without changing route registration.
func RegisterProvisionRoutes(e *echo.Echo, h *ProvisionHandler) {
	e.GET("/install-agent.sh", h.InstallScript)
	e.GET("/api/v1/nodes/agent-binary", h.AgentBinary)
	e.GET("/api/v1/nodes/agent-binary/checksum", h.AgentBinaryChecksum)
}
