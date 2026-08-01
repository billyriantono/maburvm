package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestExtractClientIP(t *testing.T) {
	tests := []struct {
		name          string
		remoteAddr    string
		xForwardedFor string
		trustProxy    bool
		expectedIP    string
	}{
		{
			name:       "no proxy, direct connection",
			remoteAddr: "192.168.1.100:12345",
			trustProxy: false,
			expectedIP: "192.168.1.100",
		},
		{
			name:          "with X-Forwarded-For, trust proxy",
			remoteAddr:    "10.0.0.1:8080",
			xForwardedFor: "192.168.1.100, 10.0.0.1",
			trustProxy:    true,
			expectedIP:    "192.168.1.100",
		},
		{
			name:          "with X-Forwarded-For, no trust proxy",
			remoteAddr:    "10.0.0.1:8080",
			xForwardedFor: "192.168.1.100",
			trustProxy:    false,
			expectedIP:    "10.0.0.1",
		},
		{
			name:          "X-Forwarded-For with spaces",
			remoteAddr:    "10.0.0.1:8080",
			xForwardedFor: " 192.168.1.100 , 10.0.0.1 ",
			trustProxy:    true,
			expectedIP:    "192.168.1.100",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remoteAddr
			if tt.xForwardedFor != "" {
				r.Header.Set("X-Forwarded-For", tt.xForwardedFor)
			}

			ip := extractClientIP(r, tt.trustProxy)
			if ip != tt.expectedIP {
				t.Errorf("expected %s, got %s", tt.expectedIP, ip)
			}
		})
	}
}

func TestIsIPWhitelisted(t *testing.T) {
	tests := []struct {
		name      string
		clientIP  string
		whitelist []string
		expected  bool
	}{
		{
			name:      "empty whitelist - handled by middleware",
			clientIP:  "192.168.1.100",
			whitelist: []string{},
			expected:  false, // isIPWhitelisted returns false, middleware handles empty whitelist
		},
		{
			name:      "exact IP match",
			clientIP:  "192.168.1.100",
			whitelist: []string{"192.168.1.100"},
			expected:  true,
		},
		{
			name:      "exact IP no match",
			clientIP:  "192.168.1.100",
			whitelist: []string{"192.168.1.200"},
			expected:  false,
		},
		{
			name:      "CIDR /24 match",
			clientIP:  "192.168.1.100",
			whitelist: []string{"192.168.1.0/24"},
			expected:  true,
		},
		{
			name:      "CIDR /24 no match",
			clientIP:  "192.168.2.100",
			whitelist: []string{"192.168.1.0/24"},
			expected:  false,
		},
		{
			name:      "CIDR /8 match",
			clientIP:  "10.1.2.3",
			whitelist: []string{"10.0.0.0/8"},
			expected:  true,
		},
		{
			name:      "CIDR /8 no match",
			clientIP:  "172.16.1.1",
			whitelist: []string{"10.0.0.0/8"},
			expected:  false,
		},
		{
			name:      "multiple whitelist entries, one matches",
			clientIP:  "192.168.1.50",
			whitelist: []string{"10.0.0.0/8", "192.168.1.0/24", "172.16.0.0/12"},
			expected:  true,
		},
		{
			name:      "whitespace handling",
			clientIP:  "192.168.1.100",
			whitelist: []string{" 192.168.1.100 "},
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isIPWhitelisted(tt.clientIP, tt.whitelist)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestDefaultSkipEndpoints(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected bool
	}{
		{
			name:     "auth login",
			path:     "/auth/login",
			expected: true,
		},
		{
			name:     "auth register",
			path:     "/auth/register",
			expected: true,
		},
		{
			name:     "health check",
			path:     "/health",
			expected: true,
		},
		{
			name:     "healthz",
			path:     "/healthz",
			expected: true,
		},
		{
			name:     "livez",
			path:     "/livez",
			expected: true,
		},
		{
			name:     "readyz",
			path:     "/readyz",
			expected: true,
		},
		{
			// Regression guard: only exact health paths bypass — no broad prefix.
			name:     "healthz-subpath not skipped",
			path:     "/livez/extra",
			expected: false,
		},
		{
			name:     "readyz-subpath not skipped",
			path:     "/readyz/extra",
			expected: false,
		},
		{
			// /healthz is a prefix of /healthzapi-style — ensure no wide bypass.
			name:     "api-prefixed health not skipped",
			path:     "/healthzfoobar",
			expected: false,
		},
		{
			name:     "metrics",
			path:     "/metrics",
			expected: true,
		},
		{
			name:     "api endpoint",
			path:     "/api/vms",
			expected: false,
		},
		{
			name:     "user profile",
			path:     "/users/me",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", tt.path, nil)
			result := DefaultSkipEndpoints(r)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

type mockGetter struct {
	whitelist []string
	err       error
}

func (m *mockGetter) GetUserIPWhitelist(userID uuid.UUID) ([]string, error) {
	return m.whitelist, m.err
}

func TestIPWhitelistMiddleware(t *testing.T) {
	userID := uuid.New()

	tests := []struct {
		name          string
		whitelist     []string
		clientIP      string
		skipEndpoint  bool
		expectAllowed bool
	}{
		{
			name:          "empty whitelist allows all",
			whitelist:     []string{},
			clientIP:      "192.168.1.100",
			expectAllowed: true,
		},
		{
			name:          "IP in whitelist",
			whitelist:     []string{"192.168.1.100"},
			clientIP:      "192.168.1.100",
			expectAllowed: true,
		},
		{
			name:          "IP not in whitelist",
			whitelist:     []string{"192.168.1.100"},
			clientIP:      "192.168.1.200",
			expectAllowed: false,
		},
		{
			name:          "CIDR range match",
			whitelist:     []string{"192.168.1.0/24"},
			clientIP:      "192.168.1.50",
			expectAllowed: true,
		},
		{
			name:          "CIDR range no match",
			whitelist:     []string{"192.168.1.0/24"},
			clientIP:      "192.168.2.50",
			expectAllowed: false,
		},
		{
			name:          "skipped endpoint",
			whitelist:     []string{"192.168.1.100"},
			clientIP:      "192.168.1.200",
			skipEndpoint:  true,
			expectAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := IPWhitelistConfig{
				Getter:            &mockGetter{whitelist: tt.whitelist},
				SkipEndpointFunc:  func(r *http.Request) bool { return tt.skipEndpoint },
				TrustProxyHeaders: false,
			}

			handler := IPWhitelist(config)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			r := httptest.NewRequest("GET", "/api/test", nil)
			r.RemoteAddr = tt.clientIP + ":12345"
			r = r.WithContext(withUserID(r.Context(), userID))

			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)

			if tt.expectAllowed && w.Code != http.StatusOK {
				t.Errorf("expected status 200, got %d", w.Code)
			}
			if !tt.expectAllowed && w.Code != http.StatusForbidden {
				t.Errorf("expected status 403, got %d", w.Code)
			}
		})
	}
}

func withUserID(ctx context.Context, userID uuid.UUID) context.Context {
	return context.WithValue(ctx, string(UserIDKey), userID)
}
