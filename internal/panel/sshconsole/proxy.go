// Package sshconsole provides an in-browser SSH console: a WebSocket endpoint
// that bridges an xterm.js terminal to an SSH shell on a VM. It mirrors the VNC
// proxy's short-lived-token design — an authenticated HTTP call mints a JWT that
// encodes the (server-resolved) target, and the WebSocket presents that token.
//
// The SSH password is never placed in the token (which may appear in URLs/logs);
// it is held server-side, one-time, keyed by the token's unique ID and consumed
// when the WebSocket connects.
package sshconsole

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/ssh"
)

const (
	// TokenExpiry is the default lifetime of an SSH console token.
	TokenExpiry = 5 * time.Minute
	// MaxTokenExpiry caps the token lifetime.
	MaxTokenExpiry = 10 * time.Minute
	// WriteWait is the time allowed to write a message to the WebSocket peer.
	WriteWait = 10 * time.Second
	// PongWait is the time allowed to read the next pong from the peer.
	PongWait = 60 * time.Second
	// PingPeriod is the interval between keepalive pings.
	PingPeriod = (PongWait * 9) / 10
	// MaxConnectionsPerUser bounds concurrent SSH consoles per user.
	MaxConnectionsPerUser = 3
	// MaxTokensPerUser bounds token requests per user per minute.
	MaxTokensPerUser = 10
	// DefaultSSHPort is the SSH port used when none is specified.
	DefaultSSHPort = 22
	// dialTimeout bounds the SSH TCP+handshake.
	dialTimeout = 15 * time.Second
	// maxStdinMessage bounds a single inbound terminal message.
	maxStdinMessage = 64 * 1024
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// TokenClaims are the JWT claims for an SSH console token.
type TokenClaims struct {
	VMID    string `json:"vm_id"`
	UserID  string `json:"user_id"`
	Host    string `json:"host"`
	Port    int    `json:"port"`
	SSHUser string `json:"ssh_user"`
	Type    string `json:"type"`
	jwt.RegisteredClaims
}

// credEntry is a one-time, server-held SSH password awaiting its WebSocket.
type credEntry struct {
	password  string
	expiresAt time.Time
}

// ProxyServer mints SSH console tokens and bridges WebSocket ↔ SSH shells.
type ProxyServer struct {
	logger      *slog.Logger
	jwtSecret   []byte
	creds       sync.Map // jti -> credEntry
	connCounter atomic.Int64

	mu          sync.Mutex
	connections map[string]int         // userID -> active connections
	tokenTimes  map[string][]time.Time // userID -> recent token request times
}

// NewProxyServer creates an SSH console proxy. jwtSecret should be the panel's
// JWT secret so tokens are unforgeable.
func NewProxyServer(logger *slog.Logger, jwtSecret string) *ProxyServer {
	if logger == nil {
		logger = slog.Default()
	}
	secret := []byte(jwtSecret)
	if len(secret) == 0 {
		secret = make([]byte, 32)
		_, _ = rand.Read(secret)
	}
	return &ProxyServer{
		logger:      logger,
		jwtSecret:   secret,
		connections: make(map[string]int),
		tokenTimes:  make(map[string][]time.Time),
	}
}

// canMintToken enforces the per-user token request rate limit.
func (s *ProxyServer) canMintToken(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-time.Minute)
	kept := s.tokenTimes[userID][:0]
	for _, t := range s.tokenTimes[userID] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.tokenTimes[userID] = kept
	if len(kept) >= MaxTokensPerUser {
		return false
	}
	s.tokenTimes[userID] = append(kept, time.Now())
	return true
}

func (s *ProxyServer) addConnection(userID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connections[userID] >= MaxConnectionsPerUser {
		return false
	}
	s.connections[userID]++
	return true
}

func (s *ProxyServer) removeConnection(userID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connections[userID] > 0 {
		s.connections[userID]--
	}
	if s.connections[userID] == 0 {
		delete(s.connections, userID)
	}
}

// GenerateToken mints a short-lived SSH console token for a server-resolved
// target and stashes the password one-time under the token's ID.
func (s *ProxyServer) GenerateToken(vmID, userID, host string, port int, sshUser, password string, expiry time.Duration) (string, time.Time, error) {
	if !s.canMintToken(userID) {
		return "", time.Time{}, fmt.Errorf("rate limit exceeded: too many token requests")
	}
	if expiry <= 0 || expiry > MaxTokenExpiry {
		expiry = TokenExpiry
	}
	if port <= 0 {
		port = DefaultSSHPort
	}

	now := time.Now()
	expiresAt := now.Add(expiry)
	jti := newTokenID()

	claims := TokenClaims{
		VMID:    vmID,
		UserID:  userID,
		Host:    host,
		Port:    port,
		SSHUser: sshUser,
		Type:    "ssh_access",
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Subject:   userID,
			ID:        jti,
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("failed to sign token: %w", err)
	}

	s.sweepCreds()
	s.creds.Store(jti, credEntry{password: password, expiresAt: expiresAt})
	s.logger.Info("Generated SSH console token", "vm_id", vmID, "user_id", userID, "host", host, "ssh_user", sshUser)
	return signed, expiresAt, nil
}

// sweepCreds drops expired one-time passwords (lazy GC on token mint).
func (s *ProxyServer) sweepCreds() {
	now := time.Now()
	s.creds.Range(func(k, v interface{}) bool {
		if entry, ok := v.(credEntry); ok && now.After(entry.expiresAt) {
			s.creds.Delete(k)
		}
		return true
	})
}

func (s *ProxyServer) validateToken(tokenString string) (*TokenClaims, error) {
	token, err := jwt.ParseWithClaims(tokenString, &TokenClaims{}, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return s.jwtSecret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}
	claims, ok := token.Claims.(*TokenClaims)
	if !ok || !token.Valid || claims.Type != "ssh_access" {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}

// consumeCredential returns and removes the one-time password for a token.
func (s *ProxyServer) consumeCredential(jti string) (string, bool) {
	v, ok := s.creds.LoadAndDelete(jti)
	if !ok {
		return "", false
	}
	entry, ok := v.(credEntry)
	if !ok || time.Now().After(entry.expiresAt) {
		return "", false
	}
	return entry.password, true
}

// resizeMessage is the control frame an xterm.js client sends on terminal resize.
type resizeMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// HandleWebSocket upgrades the request and bridges it to an SSH shell.
// Route: GET /ws/ssh?token=<token>
func (s *ProxyServer) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	tokenString := r.URL.Query().Get("token")
	if tokenString == "" {
		http.Error(w, "token required", http.StatusUnauthorized)
		return
	}
	claims, err := s.validateToken(tokenString)
	if err != nil {
		http.Error(w, "invalid or expired token", http.StatusUnauthorized)
		return
	}

	password, ok := s.consumeCredential(claims.ID)
	if !ok {
		http.Error(w, "credential expired or already used", http.StatusUnauthorized)
		return
	}

	if !s.addConnection(claims.UserID) {
		http.Error(w, "maximum concurrent SSH consoles reached", http.StatusTooManyRequests)
		return
	}
	defer s.removeConnection(claims.UserID)

	wsConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		s.logger.Error("ssh console: websocket upgrade failed", "error", err)
		return
	}
	defer wsConn.Close()

	connID := fmt.Sprintf("%s-%d", claims.VMID, s.connCounter.Add(1))
	s.logger.Info("ssh console: connection established", "conn_id", connID, "vm_id", claims.VMID, "user_id", claims.UserID)

	deadline := claims.ExpiresAt.Time
	s.bridge(wsConn, claims, password, deadline)
	s.logger.Info("ssh console: connection closed", "conn_id", connID, "vm_id", claims.VMID)
}

// bridge dials SSH, opens a PTY shell, and copies bytes both ways until either
// side closes or the token deadline passes.
func (s *ProxyServer) bridge(wsConn *websocket.Conn, claims *TokenClaims, password string, deadline time.Time) {
	cfg := &ssh.ClientConfig{
		User:            claims.SSHUser,
		Auth:            []ssh.AuthMethod{ssh.Password(password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // console to the user's own VM; key may change on rebuild
		Timeout:         dialTimeout,
	}
	addr := net.JoinHostPort(claims.Host, strconv.Itoa(claims.Port))
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		s.sendError(wsConn, "SSH connection failed: "+err.Error())
		return
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		s.sendError(wsConn, "failed to open SSH session: "+err.Error())
		return
	}
	defer session.Close()

	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := session.RequestPty("xterm-256color", 24, 80, modes); err != nil {
		s.sendError(wsConn, "failed to request PTY: "+err.Error())
		return
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		s.sendError(wsConn, "failed to attach stdin: "+err.Error())
		return
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		s.sendError(wsConn, "failed to attach stdout: "+err.Error())
		return
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		s.sendError(wsConn, "failed to attach stderr: "+err.Error())
		return
	}
	if err := session.Shell(); err != nil {
		s.sendError(wsConn, "failed to start shell: "+err.Error())
		return
	}

	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	var writeMu sync.Mutex
	writeBinary := func(b []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		wsConn.SetWriteDeadline(time.Now().Add(WriteWait))
		return wsConn.WriteMessage(websocket.BinaryMessage, b)
	}

	// SSH stdout/stderr → WebSocket.
	pump := func(reader interface{ Read([]byte) (int, error) }) {
		buf := make([]byte, 4096)
		for {
			n, rerr := reader.Read(buf)
			if n > 0 {
				if werr := writeBinary(buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if rerr != nil {
				cancel()
				return
			}
		}
	}
	go pump(stdout)
	go pump(stderr)

	// Keepalive pings.
	go func() {
		ticker := time.NewTicker(PingPeriod)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				writeMu.Lock()
				wsConn.SetWriteDeadline(time.Now().Add(WriteWait))
				err := wsConn.WriteMessage(websocket.PingMessage, nil)
				writeMu.Unlock()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// Close everything when the deadline/context fires.
	go func() {
		<-ctx.Done()
		session.Close()
		wsConn.Close()
	}()

	// WebSocket → SSH stdin (binary) + resize control frames (text JSON).
	wsConn.SetReadLimit(maxStdinMessage)
	for {
		msgType, data, rerr := wsConn.ReadMessage()
		if rerr != nil {
			cancel()
			return
		}
		switch msgType {
		case websocket.BinaryMessage:
			if _, werr := stdin.Write(data); werr != nil {
				cancel()
				return
			}
		case websocket.TextMessage:
			var msg resizeMessage
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" && msg.Cols > 0 && msg.Rows > 0 {
				_ = session.WindowChange(msg.Rows, msg.Cols)
			}
		}
	}
}

func (s *ProxyServer) sendError(wsConn *websocket.Conn, message string) {
	payload, _ := json.Marshal(map[string]string{"type": "error", "message": message})
	wsConn.SetWriteDeadline(time.Now().Add(WriteWait))
	_ = wsConn.WriteMessage(websocket.TextMessage, payload)
}

func newTokenID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return uuid.New().String()
	}
	return base64.URLEncoding.EncodeToString(b)
}
