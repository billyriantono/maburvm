package middleware

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// Context keys for user ID
type contextKey string

const (
	UserIDKey contextKey = "user_id"
)

// WhitelistedIPsGetter interface for getting user IP whitelist from database
type WhitelistedIPsGetter interface {
	GetUserIPWhitelist(userID uuid.UUID) ([]string, error)
}

// DBWhitelistedIPsGetter implements WhitelistedIPsGetter using GORM
type DBWhitelistedIPsGetter struct {
	DB *gorm.DB
}

// GetUserIPWhitelist retrieves user's IP whitelist from database
// Note: This fetches fresh data each time to avoid caching issues
func (g *DBWhitelistedIPsGetter) GetUserIPWhitelist(userID uuid.UUID) ([]string, error) {
	var user models.User
	err := g.DB.First(&user, "id = ?", userID).Error
	if err != nil {
		return nil, err
	}
	return user.IPWhitelist, nil
}

// SkipEndpointFunc determines if an endpoint should skip IP whitelist check
type SkipEndpointFunc func(r *http.Request) bool

// DefaultSkipEndpoints returns true for endpoints that should skip IP whitelist
func DefaultSkipEndpoints(r *http.Request) bool {
	path := r.URL.Path

	// Skip auth endpoints
	if strings.HasPrefix(path, "/auth/") {
		return true
	}

	// Skip health check endpoints (exact paths only — no broad prefix bypass)
	if path == "/health" || path == "/healthz" || path == "/livez" || path == "/readyz" {
		return true
	}

	// Skip metrics endpoints
	if strings.HasPrefix(path, "/metrics") {
		return true
	}

	return false
}

// IPWhitelistConfig holds configuration for IP whitelist middleware
type IPWhitelistConfig struct {
	// WhitelistedIPsGetter retrieves user's IP whitelist from database
	Getter WhitelistedIPsGetter
	// SkipEndpointFunc determines if an endpoint should skip whitelist check
	// If nil, DefaultSkipEndpoints is used
	SkipEndpointFunc SkipEndpointFunc
	// TrustProxyHeaders enables reading from X-Forwarded-For header
	// Only enable this if this server is behind a trusted reverse proxy
	TrustProxyHeaders bool
}

// IPWhitelist returns a middleware that enforces IP whitelist validation
func IPWhitelist(config IPWhitelistConfig) func(http.Handler) http.Handler {
	// Use default skip function if not provided
	if config.SkipEndpointFunc == nil {
		config.SkipEndpointFunc = DefaultSkipEndpoints
	}

	// Use default getter if not provided (for backwards compatibility)
	if config.Getter == nil {
		panic("IPWhitelist middleware requires a WhitelistedIPsGetter")
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Check if endpoint should be skipped
			if config.SkipEndpointFunc(r) {
				next.ServeHTTP(w, r)
				return
			}

			// Extract client IP
			clientIP := extractClientIP(r, config.TrustProxyHeaders)
			if clientIP == "" {
				http.Error(w, "Unable to determine client IP", http.StatusInternalServerError)
				return
			}

			// Get user ID from context (set by auth middleware)
			userIDStr := r.Context().Value(string(UserIDKey))
			if userIDStr == nil {
				// No user in context, continue without IP check
				// This handles unauthenticated requests that may be handled elsewhere
				next.ServeHTTP(w, r)
				return
			}

			userID, ok := userIDStr.(uuid.UUID)
			if !ok {
				// Invalid user ID type in context, allow request
				next.ServeHTTP(w, r)
				return
			}

			// Get fresh IP whitelist from database (not cached)
			whitelist, err := config.Getter.GetUserIPWhitelist(userID)
			if err != nil {
				// Database error - fail open to avoid blocking legitimate users
				// Log this in production
				next.ServeHTTP(w, r)
				return
			}

			// Empty whitelist means opt-in feature not enabled - allow all
			if len(whitelist) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			// Check if client IP matches any whitelist entry
			if !isIPWhitelisted(clientIP, whitelist) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)

				response := map[string]string{
					"error":           "access denied",
					"message":         "Your IP address is not whitelisted for this user account",
					"client_ip":       clientIP,
					"whitelist_count": fmt.Sprintf("%d", len(whitelist)),
				}
				json.NewEncoder(w).Encode(response)
				return
			}

			// IP is whitelisted, proceed
			next.ServeHTTP(w, r)
		})
	}
}

// extractClientIP extracts the client IP from the request
// It handles X-Forwarded-For header when TrustProxyHeaders is true
// IMPORTANT: Does NOT use X-Forwarded-From (easily spoofable)
func extractClientIP(r *http.Request, trustProxy bool) string {
	// First, try to get direct remote address
	remoteAddr := r.RemoteAddr
	if remoteAddr != "" {
		// Remove port if present
		ip, _, err := net.SplitHostPort(remoteAddr)
		if err == nil {
			// Direct IP without port
			if !trustProxy {
				return ip
			}
			// If trusting proxy, still check X-Forwarded-For
		}
	}

	// If trustProxy is false, return the direct IP
	if !trustProxy {
		if remoteAddr != "" {
			ip, _, err := net.SplitHostPort(remoteAddr)
			if err == nil {
				return ip
			}
			return remoteAddr
		}
		return ""
	}

	// Handle X-Forwarded-For header
	// X-Forwarded-For can contain multiple IPs: client, proxy1, proxy2
	// The leftmost is the original client IP
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		// Take the first IP (original client)
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			clientIP := strings.TrimSpace(ips[0])
			if isValidIP(clientIP) {
				return clientIP
			}
		}
	}

	// Fall back to remote addr
	if remoteAddr != "" {
		ip, _, err := net.SplitHostPort(remoteAddr)
		if err == nil {
			return ip
		}
		return remoteAddr
	}

	return ""
}

// isValidIP checks if a string is a valid IP address
func isValidIP(ipStr string) bool {
	return net.ParseIP(ipStr) != nil
}

// isIPWhitelisted checks if the client IP matches any entry in the whitelist
// Supports exact IPs (e.g., "192.168.1.100") and CIDR ranges (e.g., "10.0.0.0/8")
func isIPWhitelisted(clientIPStr string, whitelist []string) bool {
	clientIP := net.ParseIP(clientIPStr)
	if clientIP == nil {
		return false
	}

	for _, entry := range whitelist {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// Try to parse as CIDR first
		_, network, err := net.ParseCIDR(entry)
		if err == nil {
			// CIDR range - check if client IP is in range
			if network.Contains(clientIP) {
				return true
			}
			continue
		}

		// Try to parse as exact IP
		whitelistedIP := net.ParseIP(entry)
		if whitelistedIP != nil {
			// Exact IP match
			if whitelistedIP.Equal(clientIP) {
				return true
			}
		}
	}

	return false
}
