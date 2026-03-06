// Package vnc provides WebSocket proxy functionality for VNC console access.
package vnc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/shared/config"
)

const (
	// TokenExpiry is the default expiry for VNC tokens (5 minutes)
	TokenExpiry = 5 * time.Minute

	// MaxTokenExpiry is the maximum allowed token expiry (10 minutes)
	MaxTokenExpiry = 10 * time.Minute

	// WriteWait is the time allowed to write a message to the peer
	WriteWait = 10 * time.Second

	// PongWait is the time allowed to read the next pong message from the peer
	PongWait = 60 * time.Second

	// PingPeriod is the period between ping messages
	PingPeriod = (PongWait * 9) / 10

	// MaxMessageSize is the maximum message size allowed from peer
	MaxMessageSize = 1024 * 1024 // 1MB

	// RateLimitWindow is the time window for rate limiting
	RateLimitWindow = time.Minute

	// MaxConnectionsPerUser is the maximum concurrent VNC connections per user
	MaxConnectionsPerUser = 3

	// MaxTokensPerUser is the maximum token requests per user per window
	MaxTokensPerUser = 10
)

// Upgrader configuration for WebSocket connections
var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		// In production, this should validate the origin against allowed domains
		return true
	},
}

// VNCTokenClaims represents the JWT claims for VNC tokens
type VNCTokenClaims struct {
	VMID   string `json:"vm_id"`
	UserID string `json:"user_id"`
	NodeID string `json:"node_id"`
	Type   string `json:"type"`
	jwt.RegisteredClaims
}

// ConnectionRateLimiter tracks rate limiting for users
type ConnectionRateLimiter struct {
	mu            sync.RWMutex
	connections   map[string]int         // userID -> active connection count
	tokenRequests map[string][]time.Time // userID -> list of token request timestamps
	window        time.Duration
}

// NewConnectionRateLimiter creates a new rate limiter
func NewConnectionRateLimiter() *ConnectionRateLimiter {
	return &ConnectionRateLimiter{
		connections:   make(map[string]int),
		tokenRequests: make(map[string][]time.Time),
		window:        RateLimitWindow,
	}
}

// CanCreateToken checks if a user can request a new token
func (rl *ConnectionRateLimiter) CanCreateToken(userID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Clean old entries and count recent requests
	requests := rl.tokenRequests[userID]
	var validRequests []time.Time
	for _, t := range requests {
		if t.After(cutoff) {
			validRequests = append(validRequests, t)
		}
	}
	rl.tokenRequests[userID] = validRequests

	return len(validRequests) < MaxTokensPerUser
}

// RecordTokenRequest records a token request for a user
func (rl *ConnectionRateLimiter) RecordTokenRequest(userID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.tokenRequests[userID] = append(rl.tokenRequests[userID], time.Now())
}

// CanConnect checks if a user can open a new connection
func (rl *ConnectionRateLimiter) CanConnect(userID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	return rl.connections[userID] < MaxConnectionsPerUser
}

// AddConnection increments the connection count for a user
func (rl *ConnectionRateLimiter) AddConnection(userID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.connections[userID] >= MaxConnectionsPerUser {
		return false
	}
	rl.connections[userID]++
	return true
}

// RemoveConnection decrements the connection count for a user
func (rl *ConnectionRateLimiter) RemoveConnection(userID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.connections[userID] > 0 {
		rl.connections[userID]--
	}
	if rl.connections[userID] == 0 {
		delete(rl.connections, userID)
	}
}

// ProxyServer handles WebSocket to VNC proxy connections
type ProxyServer struct {
	logger      *slog.Logger
	cfg         *config.Config
	agentClient *client.AgentClient
	jwtSecret   []byte
	rateLimiter *ConnectionRateLimiter
	activeConns sync.Map // map[string]*ProxyConnection
	connCounter atomic.Int64
}

// ProxyConnection represents an active WebSocket ↔ VNC bridge
type ProxyConnection struct {
	ID        string
	VMID      string
	UserID    string
	NodeID    string
	Token     string
	CreatedAt time.Time
	ExpiresAt time.Time
	wsConn    *websocket.Conn
	tcpConn   net.Conn
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    atomic.Bool
}

// NewProxyServer creates a new VNC proxy server
func NewProxyServer(cfg *config.Config, agentClient *client.AgentClient, logger *slog.Logger, jwtSecret string) *ProxyServer {
	if logger == nil {
		logger = slog.Default()
	}

	secret := []byte(jwtSecret)
	if len(secret) == 0 {
		// Fallback to a generated secret (in production, this should be configured)
		secret = generateFallbackSecret()
	}

	return &ProxyServer{
		logger:      logger,
		cfg:         cfg,
		agentClient: agentClient,
		jwtSecret:   secret,
		rateLimiter: NewConnectionRateLimiter(),
	}
}

// generateFallbackSecret creates a fallback JWT secret
func generateFallbackSecret() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback to static secret if crypto/rand fails
		return []byte("maburvm-vnc-proxy-fallback-secret")
	}
	return b
}

// GenerateVNCToken creates a new short-lived VNC access token
func (s *ProxyServer) GenerateVNCToken(vmID, userID, nodeID string, expiry time.Duration) (string, time.Time, error) {
	// Check rate limiting
	if !s.rateLimiter.CanCreateToken(userID) {
		return "", time.Time{}, fmt.Errorf("rate limit exceeded: too many token requests")
	}

	// Validate expiry
	if expiry <= 0 || expiry > MaxTokenExpiry {
		expiry = TokenExpiry
	}

	now := time.Now()
	expiresAt := now.Add(expiry)

	claims := VNCTokenClaims{
		VMID:   vmID,
		UserID: userID,
		NodeID: nodeID,
		Type:   "vnc_access",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Subject:   userID,
			ID:        generateTokenID(),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	tokenString, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to sign token: %w", err)
	}

	s.rateLimiter.RecordTokenRequest(userID)
	s.logger.Info("Generated VNC token",
		"vm_id", vmID,
		"user_id", userID,
		"expires_at", expiresAt,
	)

	return tokenString, expiresAt, nil
}

// generateTokenID creates a unique token ID
func generateTokenID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return uuid.New().String()
	}
	return base64.URLEncoding.EncodeToString(b)
}

// ValidateToken validates and parses a VNC token
func (s *ProxyServer) ValidateToken(tokenString string) (*VNCTokenClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &VNCTokenClaims{}, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return s.jwtSecret, nil
	})

	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*VNCTokenClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	// Validate token type
	if claims.Type != "vnc_access" {
		return nil, fmt.Errorf("invalid token type")
	}

	return claims, nil
}

// HandleWebSocket handles WebSocket upgrade and proxy connections
func (s *ProxyServer) HandleWebSocket(c echo.Context) error {
	// Extract token from query parameter
	tokenString := c.QueryParam("token")
	if tokenString == "" {
		s.logger.Warn("WebSocket connection attempt without token")
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "Token required",
		})
	}

	// Validate token
	claims, err := s.ValidateToken(tokenString)
	if err != nil {
		s.logger.Warn("Invalid token attempt",
			"error", err,
			"remote_addr", c.Request().RemoteAddr,
		)
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"error":   "Unauthorized",
			"message": "Invalid or expired token",
		})
	}

	// Check rate limiting for connections
	if !s.rateLimiter.CanConnect(claims.UserID) {
		s.logger.Warn("Connection rate limit exceeded",
			"user_id", claims.UserID,
		)
		return c.JSON(http.StatusTooManyRequests, map[string]interface{}{
			"error":   "Too Many Requests",
			"message": "Maximum concurrent VNC connections reached",
		})
	}

	// Upgrade to WebSocket
	wsConn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		s.logger.Error("Failed to upgrade WebSocket", "error", err)
		return err
	}

	// Add connection to rate limiter
	if !s.rateLimiter.AddConnection(claims.UserID) {
		wsConn.Close()
		return c.JSON(http.StatusTooManyRequests, map[string]interface{}{
			"error":   "Too Many Requests",
			"message": "Maximum concurrent VNC connections reached",
		})
	}

	// Create proxy connection
	connID := fmt.Sprintf("%s-%d", claims.VMID, s.connCounter.Add(1))
	proxyConn := &ProxyConnection{
		ID:        connID,
		VMID:      claims.VMID,
		UserID:    claims.UserID,
		NodeID:    claims.NodeID,
		Token:     tokenString,
		CreatedAt: time.Now(),
		ExpiresAt: claims.ExpiresAt.Time,
		wsConn:    wsConn,
	}

	s.activeConns.Store(connID, proxyConn)

	s.logger.Info("WebSocket connection established",
		"conn_id", connID,
		"vm_id", claims.VMID,
		"user_id", claims.UserID,
		"remote_addr", c.Request().RemoteAddr,
	)

	// Start proxying
	go s.proxyConnection(proxyConn)

	return nil
}

// proxyConnection handles the bidirectional proxy between WebSocket and TCP
func (s *ProxyServer) proxyConnection(pc *ProxyConnection) {
	defer s.cleanupConnection(pc)

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	pc.cancel = cancel

	// Call agent to get VNC connection details
	tcpAddr, err := s.getVNCAddress(ctx, pc)
	if err != nil {
		s.logger.Error("Failed to get VNC address",
			"conn_id", pc.ID,
			"error", err,
		)
		s.sendErrorMessage(pc, "Failed to connect to VNC server")
		return
	}

	// Connect to VNC server via TCP
	tcpConn, err := net.DialTimeout("tcp", tcpAddr, 10*time.Second)
	if err != nil {
		s.logger.Error("Failed to connect to VNC server",
			"conn_id", pc.ID,
			"address", tcpAddr,
			"error", err,
		)
		s.sendErrorMessage(pc, "Failed to connect to VNC server")
		return
	}
	pc.tcpConn = tcpConn

	s.logger.Info("Connected to VNC server",
		"conn_id", pc.ID,
		"address", tcpAddr,
	)

	// Start goroutines for bidirectional copying
	var wg sync.WaitGroup
	wg.Add(2)

	// WebSocket → TCP
	go func() {
		defer wg.Done()
		s.wsToTCP(pc)
	}()

	// TCP → WebSocket
	go func() {
		defer wg.Done()
		s.tcpToWS(pc)
	}()

	// Wait for either direction to finish or token expiry
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		s.logger.Info("Proxy connection closed",
			"conn_id", pc.ID,
			"vm_id", pc.VMID,
		)
	case <-ctx.Done():
		s.logger.Info("Proxy connection cancelled",
			"conn_id", pc.ID,
		)
	case <-time.After(time.Until(pc.ExpiresAt)):
		s.logger.Info("Token expired, closing connection",
			"conn_id", pc.ID,
		)
		s.sendErrorMessage(pc, "Session expired")
	}
}

// getVNCAddress retrieves the VNC server address from the agent
func (s *ProxyServer) getVNCAddress(ctx context.Context, pc *ProxyConnection) (string, error) {
	if s.agentClient == nil {
		// Fallback for testing: use local VNC server
		return fmt.Sprintf("localhost:%d", 5900), nil
	}

	// Request VNC proxy from agent with extended expiry
	result, err := s.agentClient.StartVNCProxy(ctx, pc.NodeID, pc.VMID, int32(TokenExpiry.Seconds()))
	if err != nil {
		return "", fmt.Errorf("failed to start VNC proxy: %w", err)
	}

	// Build TCP address from agent response
	// The agent should return a local TCP port for VNC connection
	return fmt.Sprintf("localhost:%d", result.WebSocketPort), nil
}

// wsToTCP copies data from WebSocket to TCP
func (s *ProxyServer) wsToTCP(pc *ProxyConnection) {
	defer pc.cancel()

	pc.wsConn.SetReadLimit(MaxMessageSize)
	pc.wsConn.SetReadDeadline(time.Now().Add(PongWait))
	pc.wsConn.SetPongHandler(func(string) error {
		pc.wsConn.SetReadDeadline(time.Now().Add(PongWait))
		return nil
	})

	for {
		messageType, data, err := pc.wsConn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				s.logger.Warn("WebSocket read error",
					"conn_id", pc.ID,
					"error", err,
				)
			}
			return
		}

		// Only handle binary messages (VNC protocol data)
		if messageType != websocket.BinaryMessage {
			s.logger.Debug("Ignoring non-binary message",
				"conn_id", pc.ID,
				"type", messageType,
			)
			continue
		}

		// Write to TCP connection
		if _, err := pc.tcpConn.Write(data); err != nil {
			s.logger.Error("Failed to write to TCP",
				"conn_id", pc.ID,
				"error", err,
			)
			return
		}

		// Update read deadline
		pc.wsConn.SetReadDeadline(time.Now().Add(PongWait))
	}
}

// tcpToWS copies data from TCP to WebSocket
func (s *ProxyServer) tcpToWS(pc *ProxyConnection) {
	defer pc.cancel()

	ticker := time.NewTicker(PingPeriod)
	defer ticker.Stop()

	buffer := make([]byte, 4096)

	for {
		select {
		case <-ticker.C:
			// Send ping to keep connection alive
			pc.mu.Lock()
			pc.wsConn.SetWriteDeadline(time.Now().Add(WriteWait))
			err := pc.wsConn.WriteMessage(websocket.PingMessage, nil)
			pc.mu.Unlock()
			if err != nil {
				return
			}

		default:
			// Set read deadline on TCP connection
			pc.tcpConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))

			n, err := pc.tcpConn.Read(buffer)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Timeout, continue to check for ping
					continue
				}
				if err != io.EOF {
					s.logger.Error("TCP read error",
						"conn_id", pc.ID,
						"error", err,
					)
				}
				return
			}

			// Write to WebSocket as binary message
			pc.mu.Lock()
			pc.wsConn.SetWriteDeadline(time.Now().Add(WriteWait))
			err = pc.wsConn.WriteMessage(websocket.BinaryMessage, buffer[:n])
			pc.mu.Unlock()
			if err != nil {
				s.logger.Error("WebSocket write error",
					"conn_id", pc.ID,
					"error", err,
				)
				return
			}
		}
	}
}

// sendErrorMessage sends an error message to the WebSocket client
func (s *ProxyServer) sendErrorMessage(pc *ProxyConnection, message string) {
	errorData := map[string]interface{}{
		"type":    "error",
		"message": message,
	}

	data, _ := json.Marshal(errorData)

	pc.mu.Lock()
	defer pc.mu.Unlock()

	pc.wsConn.SetWriteDeadline(time.Now().Add(WriteWait))
	pc.wsConn.WriteMessage(websocket.TextMessage, data)
}

// cleanupConnection cleans up a proxy connection
func (s *ProxyServer) cleanupConnection(pc *ProxyConnection) {
	if pc.closed.CompareAndSwap(false, true) {
		// Cancel context
		if pc.cancel != nil {
			pc.cancel()
		}

		// Close WebSocket connection
		if pc.wsConn != nil {
			pc.wsConn.Close()
		}

		// Close TCP connection
		if pc.tcpConn != nil {
			pc.tcpConn.Close()
		}

		// Remove from active connections
		s.activeConns.Delete(pc.ID)

		// Remove from rate limiter
		s.rateLimiter.RemoveConnection(pc.UserID)

		s.logger.Info("Connection cleaned up",
			"conn_id", pc.ID,
			"vm_id", pc.VMID,
			"duration", time.Since(pc.CreatedAt),
		)
	}
}

// CloseConnection forcibly closes a specific connection
func (s *ProxyServer) CloseConnection(connID string) error {
	if conn, ok := s.activeConns.Load(connID); ok {
		pc := conn.(*ProxyConnection)
		s.cleanupConnection(pc)
		return nil
	}
	return fmt.Errorf("connection not found: %s", connID)
}

// CloseAllConnections closes all active connections
func (s *ProxyServer) CloseAllConnections() {
	s.activeConns.Range(func(key, value interface{}) bool {
		pc := value.(*ProxyConnection)
		s.cleanupConnection(pc)
		return true
	})
}

// GetActiveConnections returns a list of active connection IDs
func (s *ProxyServer) GetActiveConnections() []string {
	var connections []string
	s.activeConns.Range(func(key, value interface{}) bool {
		connections = append(connections, key.(string))
		return true
	})
	return connections
}

// GetConnectionInfo returns information about a specific connection
func (s *ProxyServer) GetConnectionInfo(connID string) (map[string]interface{}, error) {
	if conn, ok := s.activeConns.Load(connID); ok {
		pc := conn.(*ProxyConnection)
		return map[string]interface{}{
			"id":         pc.ID,
			"vm_id":      pc.VMID,
			"user_id":    pc.UserID,
			"node_id":    pc.NodeID,
			"created_at": pc.CreatedAt,
			"expires_at": pc.ExpiresAt,
			"duration":   time.Since(pc.CreatedAt).String(),
		}, nil
	}
	return nil, fmt.Errorf("connection not found: %s", connID)
}

// RevokeToken revokes a token by forcibly closing its connection
func (s *ProxyServer) RevokeToken(tokenString string) error {
	// Validate token to get claims
	claims, err := s.ValidateToken(tokenString)
	if err != nil {
		return fmt.Errorf("invalid token: %w", err)
	}

	// Find and close connections for this VM and user
	var closed int
	s.activeConns.Range(func(key, value interface{}) bool {
		pc := value.(*ProxyConnection)
		if pc.VMID == claims.VMID && pc.UserID == claims.UserID {
			s.cleanupConnection(pc)
			closed++
		}
		return true
	})

	s.logger.Info("Token revoked",
		"vm_id", claims.VMID,
		"user_id", claims.UserID,
		"connections_closed", closed,
	)

	return nil
}
