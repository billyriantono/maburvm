package vncproxy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Session represents an active VNC proxy session
type Session struct {
	Token     string
	VMID      string
	VNCPort   int
	WSPort    int
	ExpiresAt time.Time
	cancel    context.CancelFunc
}

// Proxy manages WebSocket-to-TCP VNC proxy sessions
type Proxy struct {
	sessions map[string]*Session
	servers  map[int]*http.Server
	mu       sync.RWMutex
}

// NewProxy creates a new VNC proxy manager
func NewProxy() *Proxy {
	return &Proxy{
		sessions: make(map[string]*Session),
		servers:  make(map[int]*http.Server),
	}
}

// StartSession starts a websockify proxy for a VM's VNC port
// Returns: websocket URL, token, expiry time, error
func (p *Proxy) StartSession(vmID string, vncPort, wsPort int, expireSeconds int32) (wsURL, token string, expiresAt time.Time, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Generate a random token
	token = generateToken()

	// Set expiry time
	expiresAt = time.Now().Add(time.Duration(expireSeconds) * time.Second)

	// Create session context
	ctx, cancel := context.WithDeadline(context.Background(), expiresAt)

	// Store session
	session := &Session{
		Token:     token,
		VMID:      vmID,
		VNCPort:   vncPort,
		WSPort:    wsPort,
		ExpiresAt: expiresAt,
		cancel:    cancel,
	}
	p.sessions[token] = session

	// Start HTTP server if not already running
	if _, exists := p.servers[wsPort]; !exists {
		if err := p.startServer(ctx, wsPort, token); err != nil {
			delete(p.sessions, token)
			cancel()
			return "", "", time.Time{}, fmt.Errorf("failed to start WebSocket server: %w", err)
		}
	}

	// Start cleanup goroutine
	go p.cleanupSession(ctx, token)

	wsURL = fmt.Sprintf("ws://0.0.0.0:%d/websockify?token=%s", wsPort, token)
	log.Printf("[VNCProxy] Started session for VM %s on port %d -> VNC port %d (token: %s)", vmID, wsPort, vncPort, token)

	return wsURL, token, expiresAt, nil
}

// startServer starts the HTTP/WebSocket server on the specified port
func (p *Proxy) startServer(ctx context.Context, port int, expectedToken string) error {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/websockify", func(w http.ResponseWriter, r *http.Request) {
		// Validate token if provided
		token := r.URL.Query().Get("token")
		if token == "" {
			// Try to get from header
			token = r.Header.Get("X-VNC-Token")
		}

		p.mu.RLock()
		session, exists := p.sessions[token]
		p.mu.RUnlock()

		if !exists || session.ExpiresAt.Before(time.Now()) {
			http.Error(w, "Invalid or expired token", http.StatusUnauthorized)
			return
		}

		// Upgrade to WebSocket
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[VNCProxy] WebSocket upgrade failed: %v", err)
			return
		}
		defer conn.Close()

		// Proxy the connection
		p.proxyWebSocket(conn, session.VNCPort)
	})

	server := &http.Server{
		Addr:    fmt.Sprintf("0.0.0.0:%d", port),
		Handler: mux,
	}

	p.servers[port] = server

	// Start server in goroutine
	go func() {
		log.Printf("[VNCProxy] Starting WebSocket server on port %d", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[VNCProxy] Server error on port %d: %v", port, err)
		}
	}()

	// Handle context cancellation
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(shutdownCtx)
		p.mu.Lock()
		delete(p.servers, port)
		p.mu.Unlock()
	}()

	return nil
}

// proxyWebSocket handles bidirectional proxying between WebSocket and TCP
func (p *Proxy) proxyWebSocket(wsConn *websocket.Conn, vncPort int) {
	// Connect to VNC server
	tcpConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", vncPort))
	if err != nil {
		log.Printf("[VNCProxy] Failed to connect to VNC port %d: %v", vncPort, err)
		return
	}
	defer tcpConn.Close()

	log.Printf("[VNCProxy] Proxying WebSocket <-> TCP port %d", vncPort)

	// Create error channel
	errCh := make(chan error, 2)

	// WebSocket -> TCP
	go func() {
		for {
			messageType, data, err := wsConn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("[VNCProxy] WebSocket read error: %v", err)
				}
				errCh <- err
				return
			}

			if messageType == websocket.BinaryMessage {
				if _, err := tcpConn.Write(data); err != nil {
					log.Printf("[VNCProxy] TCP write error: %v", err)
					errCh <- err
					return
				}
			}
		}
	}()

	// TCP -> WebSocket
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := tcpConn.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("[VNCProxy] TCP read error: %v", err)
				}
				errCh <- err
				return
			}

			if err := wsConn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				log.Printf("[VNCProxy] WebSocket write error: %v", err)
				errCh <- err
				return
			}
		}
	}()

	// Wait for either direction to error
	<-errCh
	log.Printf("[VNCProxy] Proxy connection closed for port %d", vncPort)
}

// cleanupSession removes the session when it expires
func (p *Proxy) cleanupSession(ctx context.Context, token string) {
	<-ctx.Done()

	p.mu.Lock()
	if session, exists := p.sessions[token]; exists {
		delete(p.sessions, token)
		log.Printf("[VNCProxy] Session expired for VM %s (token: %s)", session.VMID, token)
	}
	p.mu.Unlock()
}

// StopSession stops a specific VNC proxy session
func (p *Proxy) StopSession(token string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	session, exists := p.sessions[token]
	if !exists {
		return fmt.Errorf("session not found")
	}

	if session.cancel != nil {
		session.cancel()
	}
	delete(p.sessions, token)

	log.Printf("[VNCProxy] Stopped session for VM %s (token: %s)", session.VMID, token)
	return nil
}

// GetSession returns session info if valid
func (p *Proxy) GetSession(token string) (*Session, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	session, exists := p.sessions[token]
	if !exists || session.ExpiresAt.Before(time.Now()) {
		return nil, false
	}

	return session, true
}

// generateToken generates a cryptographically random base64 token
func generateToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based token
		return fmt.Sprintf("vnc-%d", time.Now().UnixNano())
	}
	return base64.URLEncoding.EncodeToString(b)
}
