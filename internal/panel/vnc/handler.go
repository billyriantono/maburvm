// Package vnc provides HTTP handlers for VNC WebSocket proxy endpoints.
package vnc

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// Handler provides HTTP handlers for VNC operations
type Handler struct {
	proxyServer *ProxyServer
}

// NewHandler creates a new VNC handler
func NewHandler(proxyServer *ProxyServer) *Handler {
	return &Handler{
		proxyServer: proxyServer,
	}
}

// HandleWebSocket handles WebSocket connections for VNC proxy
// Route: /ws/vnc?token=<short-lived-token>
func (h *Handler) HandleWebSocket(c echo.Context) error {
	return h.proxyServer.HandleWebSocket(c)
}

// TokenRequest represents a request to generate a VNC token
type TokenRequest struct {
	VMID   string `json:"vm_id" validate:"required,uuid"`
	NodeID string `json:"node_id" validate:"required,uuid"`
}

// TokenResponse represents the response with a VNC token
type TokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt string `json:"expires_at"`
}

// GenerateToken handles token generation requests
// Route: POST /api/vnc/token
func (h *Handler) GenerateToken(c echo.Context) error {
	// Get user from context (requires authentication middleware)
	userID, ok := c.Get("user_id").(string)
	if !ok || userID == "" {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "User authentication required",
		})
	}

	var req TokenRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	if err := c.Validate(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": err.Error(),
		})
	}

	// Generate token
	token, expiresAt, err := h.proxyServer.GenerateVNCToken(req.VMID, userID, req.NodeID, TokenExpiry)
	if err != nil {
		return c.JSON(http.StatusTooManyRequests, map[string]interface{}{
			"error":   "Rate Limited",
			"message": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "VNC token generated",
		"data": TokenResponse{
			Token:     token,
			ExpiresAt: expiresAt.Format(http.TimeFormat),
		},
	})
}

// RevokeTokenRequest represents a request to revoke a VNC token
type RevokeTokenRequest struct {
	Token string `json:"token" validate:"required"`
}

// RevokeToken handles token revocation requests
// Route: POST /api/vnc/revoke
func (h *Handler) RevokeToken(c echo.Context) error {
	var req RevokeTokenRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	if err := c.Validate(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": err.Error(),
		})
	}

	if err := h.proxyServer.RevokeToken(req.Token); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Token revoked successfully",
	})
}

// ConnectionInfo represents information about an active connection
type ConnectionInfo struct {
	ID        string `json:"id"`
	VMID      string `json:"vm_id"`
	UserID    string `json:"user_id"`
	NodeID    string `json:"node_id"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
	Duration  string `json:"duration"`
}

// ListConnections handles listing active VNC connections
// Route: GET /api/vnc/connections
func (h *Handler) ListConnections(c echo.Context) error {
	connIDs := h.proxyServer.GetActiveConnections()

	var connections []ConnectionInfo
	for _, connID := range connIDs {
		info, err := h.proxyServer.GetConnectionInfo(connID)
		if err != nil {
			continue
		}

		connections = append(connections, ConnectionInfo{
			ID:        info["id"].(string),
			VMID:      info["vm_id"].(string),
			UserID:    info["user_id"].(string),
			NodeID:    info["node_id"].(string),
			CreatedAt: info["created_at"].(string),
			ExpiresAt: info["expires_at"].(string),
			Duration:  info["duration"].(string),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Active connections retrieved",
		"data": map[string]interface{}{
			"connections": connections,
			"count":       len(connections),
		},
	})
}

// CloseConnection handles force-closing a VNC connection
// Route: POST /api/vnc/connections/:id/close
func (h *Handler) CloseConnection(c echo.Context) error {
	connID := c.Param("id")
	if connID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Connection ID required",
		})
	}

	if err := h.proxyServer.CloseConnection(connID); err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{
			"error":   "Not Found",
			"message": err.Error(),
		})
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Connection closed successfully",
	})
}

// RegisterRoutes registers VNC routes with the Echo router
func RegisterRoutes(e *echo.Echo, handler *Handler, requireAuth echo.MiddlewareFunc) {
	// WebSocket endpoint (no auth middleware, uses token in query param)
	e.GET("/ws/vnc", handler.HandleWebSocket)

	// API endpoints (require authentication)
	vnc := e.Group("/api/v1/vnc")
	vnc.Use(requireAuth)

	// Token management
	vnc.POST("/token", handler.GenerateToken)
	vnc.POST("/revoke", handler.RevokeToken)

	// Connection management (admin only)
	vnc.GET("/connections", handler.ListConnections)
	vnc.POST("/connections/:id/close", handler.CloseConnection)
}
