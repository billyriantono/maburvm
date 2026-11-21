package handler

import (
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/agentsign"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

//go:embed install_agent.sh
var installAgentScript string

// agentGRPCPort is the port the panel dials every agent on (NodeService).
const agentGRPCPort = 50051

// ProvisionHandler serves the installer for new nodes, the agent binary it
// installs, and the enrollment that gives a new node its agent credential.
//
// The script and the checksum hold nothing secret and are public. The binary
// and the enrollment take the node's install token as a bearer token: issued
// in the Add node flow, single-use, an hour long, and never part of a URL.
type ProvisionHandler struct {
	db        *gorm.DB
	binaryDir string // where agent-<arch> binaries live (AGENT_BINARY_DIR)
	publicURL string // fixed base URL (tests); empty reads the panel address per request
}

// NewProvisionHandler builds the handler. binaryDir defaults to ./bin/linux.
func NewProvisionHandler(db *gorm.DB, binaryDir, publicURL string) *ProvisionHandler {
	if binaryDir == "" {
		binaryDir = "bin/linux"
	}
	return &ProvisionHandler{db: db, binaryDir: binaryDir, publicURL: strings.TrimRight(publicURL, "/")}
}

// baseURL is the panel URL the installer talks to: the panel address (System
// settings, Panel, or PANEL_PUBLIC_URL) when set, otherwise derived from the request.
func (h *ProvisionHandler) baseURL(c echo.Context) string {
	if h.publicURL != "" {
		return h.publicURL
	}
	if u := service.PublicURL(); u != "" {
		return u
	}
	scheme := "https"
	if fp := c.Request().Header.Get("X-Forwarded-Proto"); fp != "" {
		scheme = fp
	} else if c.Request().TLS == nil {
		scheme = "http"
	}
	return fmt.Sprintf("%s://%s", scheme, c.Request().Host)
}

func binaryPath(binaryDir, arch string) string {
	return filepath.Join(binaryDir, "agent-"+arch)
}

// binaryDigest is the hex SHA-256 of the agent for arch, "" when it is absent.
// The served script carries both digests, so an empty one makes the installer
// stop instead of running an unverified binary as root.
func (h *ProvisionHandler) binaryDigest(arch string) string {
	data, err := os.ReadFile(binaryPath(h.binaryDir, arch))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func provisionErr(c echo.Context, code int, msg string) error {
	return c.JSON(code, map[string]any{"success": false, "error": msg, "message": msg})
}

// archParam reads ?arch=, amd64 by default.
func archParam(c echo.Context) (string, bool) {
	arch := c.QueryParam("arch")
	if arch == "" {
		arch = "amd64"
	}
	return arch, arch == "amd64" || arch == "arm64"
}

// enrollmentToken is the bearer token, "" when the request carries none.
func enrollmentToken(c echo.Context) string {
	auth := c.Request().Header.Get(echo.HeaderAuthorization)
	if t, ok := strings.CutPrefix(auth, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// checkToken returns the enrollment a request's install token names, or nil
// after answering a request whose token is missing or bad.
func (h *ProvisionHandler) checkToken(c echo.Context) *service.PendingEnrollment {
	if h.db == nil {
		_ = provisionErr(c, http.StatusServiceUnavailable, "node enrollment is not available on this panel")
		return nil
	}
	token := enrollmentToken(c)
	if token == "" {
		_ = provisionErr(c, http.StatusUnauthorized, errNoInstallToken)
		return nil
	}
	pending, err := service.CheckNodeEnrollment(c.Request().Context(), h.db, token)
	switch {
	case errors.Is(err, service.ErrEnrollmentInvalid):
		_ = provisionErr(c, http.StatusForbidden, err.Error())
		return nil
	case errors.Is(err, service.ErrNodeHasAgent):
		_ = provisionErr(c, http.StatusConflict, err.Error())
		return nil
	case err != nil:
		_ = provisionErr(c, http.StatusInternalServerError, "could not check the install token")
		return nil
	}
	return pending
}

const errNoInstallToken = "an install token is required: copy the install command from the node's page in the panel"

// InstallScript serves the installer with the panel URL and each arch's agent
// digest templated in; the installer verifies the binary against them.
func (h *ProvisionHandler) InstallScript(c echo.Context) error {
	script := strings.ReplaceAll(installAgentScript, "__PANEL_URL__", h.baseURL(c))
	script = strings.ReplaceAll(script, "__AGENT_SHA256_AMD64__", h.binaryDigest("amd64"))
	script = strings.ReplaceAll(script, "__AGENT_SHA256_ARM64__", h.binaryDigest("arm64"))
	c.Response().Header().Set("Cache-Control", "no-store")
	return c.Blob(http.StatusOK, "text/x-shellscript; charset=utf-8", []byte(script))
}

// AgentBinary serves the bundled agent to an installer holding a good token.
// The digest and, when the operator signed the build, the signature ride in
// headers.
func (h *ProvisionHandler) AgentBinary(c echo.Context) error {
	arch, ok := archParam(c)
	if !ok {
		return provisionErr(c, http.StatusBadRequest, "arch must be amd64 or arm64")
	}
	if h.checkToken(c) == nil {
		return nil
	}
	path := binaryPath(h.binaryDir, arch)
	digest := h.binaryDigest(arch)
	if digest == "" {
		return provisionErr(c, http.StatusServiceUnavailable, "this panel image has no "+arch+" agent bundled")
	}
	if raw, err := os.ReadFile(path + ".sig"); err == nil {
		sig, err := agentsign.DecodeSignature(string(raw))
		if err != nil {
			return provisionErr(c, http.StatusServiceUnavailable, "the signature beside the bundled agent is not valid: "+err.Error())
		}
		c.Response().Header().Set("X-Agent-Signature", base64.StdEncoding.EncodeToString(sig))
	}
	c.Response().Header().Set("X-Checksum-Sha256", digest)
	return c.Attachment(path, "maburvm-agent")
}

// AgentBinaryChecksum reports the SHA-256 of the bundled agent, so operators
// can pin a digest they checked out of band.
func (h *ProvisionHandler) AgentBinaryChecksum(c echo.Context) error {
	arch, ok := archParam(c)
	if !ok {
		return provisionErr(c, http.StatusBadRequest, "arch must be amd64 or arm64")
	}
	digest := h.binaryDigest(arch)
	if digest == "" {
		return provisionErr(c, http.StatusServiceUnavailable, "this panel image has no "+arch+" agent bundled")
	}
	return c.JSON(http.StatusOK, map[string]any{"success": true, "arch": arch, "algorithm": "sha256", "digest": digest})
}

// CheckEnrollment tells the installer which node a token is for, without
// using it up, so a bad token stops it before anything is installed.
func (h *ProvisionHandler) CheckEnrollment(c echo.Context) error {
	pending := h.checkToken(c)
	if pending == nil {
		return nil
	}
	return c.JSON(http.StatusOK, map[string]any{"success": true, "data": pending})
}

type enrollRequest struct {
	Hostname string `json:"hostname"`
	Arch     string `json:"arch"`
}

// Enroll uses the token up and returns the node's agent credential.
func (h *ProvisionHandler) Enroll(c echo.Context) error {
	if h.db == nil {
		return provisionErr(c, http.StatusServiceUnavailable, "node enrollment is not available on this panel")
	}
	token := enrollmentToken(c)
	if token == "" {
		return provisionErr(c, http.StatusUnauthorized, errNoInstallToken)
	}
	var req enrollRequest
	_ = c.Bind(&req)
	ctx := c.Request().Context()
	node, err := service.RedeemNodeEnrollment(ctx, h.db, token, c.RealIP())
	switch {
	case errors.Is(err, service.ErrEnrollmentInvalid):
		return provisionErr(c, http.StatusForbidden, err.Error())
	case errors.Is(err, service.ErrNodeHasAgent):
		return provisionErr(c, http.StatusConflict, err.Error())
	case err != nil:
		return provisionErr(c, http.StatusInternalServerError, "could not enroll the node")
	}
	_ = h.db.WithContext(ctx).Create(&models.AuditLog{
		Action: "ENROLL", ResourceType: "NODE", ResourceID: &node.NodeID,
		IPAddress: c.RealIP(), UserAgent: clip(c.Request().UserAgent(), 500),
		Details: map[string]any{"event": "node_enrolled", "node_name": node.NodeName,
			"hostname": clip(req.Hostname, 255), "arch": clip(req.Arch, 16)},
	}).Error
	return c.JSON(http.StatusOK, map[string]any{"success": true, "data": map[string]any{
		"node_id": node.NodeID, "node_name": node.NodeName, "agent_token": node.AgentToken, "grpc_port": agentGRPCPort,
	}})
}

// InstallCommand creates a node's install token and the command that uses it.
// Refused for a node an agent already answers for: the installer is for new
// machines only.
func (h *ProvisionHandler) InstallCommand(c echo.Context) error {
	user, _ := middleware.GetUserContext(c)
	createdBy := ""
	if user != nil {
		createdBy = user.ID.String()
	}
	enr, err := service.IssueNodeEnrollment(c.Request().Context(), h.db, c.Param("id"), createdBy)
	switch {
	case errors.Is(err, service.ErrNodeNotFound):
		return provisionErr(c, http.StatusNotFound, "node not found")
	case errors.Is(err, service.ErrNodeHasAgent):
		return provisionErr(c, http.StatusConflict, err.Error())
	case err != nil:
		return provisionErr(c, http.StatusInternalServerError, err.Error())
	}
	scriptURL := h.baseURL(c) + "/install-agent.sh"
	return c.JSON(http.StatusOK, map[string]any{"success": true, "data": map[string]any{
		"token":      enr.Token,
		"expires_at": enr.ExpiresAt,
		"script_url": scriptURL,
		"command":    fmt.Sprintf("curl -fsSL %s | sudo bash -s -- --token %s", scriptURL, enr.Token),
	}})
}

// RegisterProvisionRoutes wires the installer endpoints. The public ones share
// one per-address rate limit.
func RegisterProvisionRoutes(e *echo.Echo, h *ProvisionHandler) {
	limit := middleware.LoginRateLimiter()
	e.GET("/install-agent.sh", h.InstallScript, limit)
	e.GET("/api/v1/nodes/agent-binary", h.AgentBinary, limit)
	e.GET("/api/v1/nodes/agent-binary/checksum", h.AgentBinaryChecksum, limit)
	e.GET("/api/v1/nodes/enroll", h.CheckEnrollment, limit)
	e.POST("/api/v1/nodes/enroll", h.Enroll, limit)
	if h.db != nil {
		e.POST("/api/v1/nodes/:id/install-command", h.InstallCommand,
			middleware.RequireAuth(h.db), middleware.RequirePermission("node:create"))
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
