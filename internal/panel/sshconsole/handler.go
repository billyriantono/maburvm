package sshconsole

import (
	"context"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

// HostResolver resolves a VM's reachable SSH host server-side. Resolving the
// host here (rather than trusting a client-supplied address) prevents the proxy
// from being used to reach arbitrary hosts.
type HostResolver interface {
	ResolveVMHost(ctx context.Context, vmID string) (host string, err error)
}

// OwnerResolver returns the user ID that owns a VM, for enforcing that a caller
// can only open an SSH console to their own VMs.
type OwnerResolver interface {
	VMOwner(ctx context.Context, vmID string) (userID string, err error)
}

// Handler exposes the SSH console token endpoint.
type Handler struct {
	proxy    *ProxyServer
	resolver HostResolver
	owners   OwnerResolver
}

// NewHandler creates an SSH console HTTP handler.
func NewHandler(proxy *ProxyServer, resolver HostResolver, owners OwnerResolver) *Handler {
	return &Handler{proxy: proxy, resolver: resolver, owners: owners}
}

// TokenRequest is the body for minting an SSH console token.
type TokenRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// GenerateToken handles POST /api/v1/vms/:id/ssh/token.
func (h *Handler) GenerateToken(c echo.Context) error {
	// RequireAuth stores the user under the "user" context key (UserContextKey),
	// not a plain "user_id" — read it via GetUserContext.
	user, ok := panelMiddleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	userID := user.ID.String()
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "VM ID required"})
	}

	// Tenant isolation: a non-admin may only open an SSH console to a VM they own.
	// Without this any authenticated user could mint a bridge token for any VM ID
	// and brute-force SSH against another tenant's guest. 404 (not 403) avoids
	// leaking which VM IDs exist.
	if h.owners != nil && user.Role != models.RoleAdmin {
		owner, err := h.owners.VMOwner(c.Request().Context(), vmID)
		if err != nil || owner != userID {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "VM not found"})
		}
	}

	var req TokenRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		username = "root"
	}
	if req.Password == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Password is required for the SSH console"})
	}

	host, err := h.resolver.ResolveVMHost(c.Request().Context(), vmID)
	if err != nil || host == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "VM has no reachable IP",
			"message": "The console needs the VM to be running with a known IP address.",
		})
	}

	token, expiresAt, err := h.proxy.GenerateToken(vmID, userID, host, DefaultSSHPort, username, req.Password, TokenExpiry)
	if err != nil {
		return c.JSON(http.StatusTooManyRequests, map[string]interface{}{"error": err.Error()})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"token":      token,
			"expires_at": expiresAt.Format("2006-01-02T15:04:05Z07:00"),
			"ws_path":    "/ws/ssh",
		},
	})
}

// HandleWebSocket adapts the proxy's net/http WebSocket handler to Echo.
// Route: GET /ws/ssh?token=<token>
func (h *Handler) HandleWebSocket(c echo.Context) error {
	h.proxy.HandleWebSocket(c.Response(), c.Request())
	return nil
}
