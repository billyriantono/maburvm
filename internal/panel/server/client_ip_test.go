package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maburvm/panel/internal/shared/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	ip := func(trusted, remote, xff string) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = remote + ":5555"
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		return clientIPExtractor(config.ServerConfig{TrustedProxies: trusted})(req)
	}

	// Default: Caddy on the host, the web container, then the panel.
	assert.Equal(t, "198.51.100.7", ip("", "172.18.0.5", "198.51.100.7"))
	assert.Equal(t, "198.51.100.7", ip("", "172.18.0.5", "6.6.6.6, 198.51.100.7, 127.0.0.1"), "a forged leftmost entry is never used")
	assert.Equal(t, "198.51.100.7", ip("", "198.51.100.7", "6.6.6.6"), "a direct caller cannot claim another address")

	// Configured: only those hops are believed.
	assert.Equal(t, "198.51.100.7", ip("10.9.0.0/16, 192.0.2.10", "10.9.1.2", "198.51.100.7"))
	assert.Equal(t, "172.18.0.5", ip("10.9.0.0/16", "172.18.0.5", "198.51.100.7"), "private ranges are no longer trusted by default")
}

func TestTrustedProxiesMustParse(t *testing.T) {
	nets, err := config.ServerConfig{TrustedProxies: "127.0.0.1, 10.0.0.0/8 ::1"}.TrustedProxyNets()
	require.NoError(t, err)
	require.Len(t, nets, 3)
	_, err = config.ServerConfig{TrustedProxies: "10.0.0.0/8,caddy"}.TrustedProxyNets()
	require.Error(t, err)
}
