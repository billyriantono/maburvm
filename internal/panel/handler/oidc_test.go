package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/maburvm/panel/internal/shared/config"
)

func TestOIDCSignedValues(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-oidc-tests-0123456789")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	in := oidcState{State: "s1", Verifier: "v1", Exp: 42}
	v, err := signJSON("oidc-state", in)
	if err != nil {
		t.Fatal(err)
	}
	var out oidcState
	if err := verifyJSON("oidc-state", v, &out); err != nil || out != in {
		t.Fatalf("round trip: %+v %v", out, err)
	}
	payload, mac, _ := strings.Cut(v, ".")
	if verifyJSON("oidc-state", "e30."+mac, &out) == nil || verifyJSON("oidc-state", payload+".AAAA", &out) == nil {
		t.Fatal("tampered value accepted")
	}
	// A login state must not pass as a two-factor ticket, or the reverse.
	var ticket sso2FATicket
	if verifyJSON("sso-2fa", v, &ticket) == nil {
		t.Fatal("value signed for one purpose accepted for another")
	}
}

func TestOIDCHandlerOffWithoutConfig(t *testing.T) {
	t.Setenv("OIDC_ISSUER", "https://idp.example")
	t.Setenv("OIDC_CLIENT_ID", "id")
	t.Setenv("OIDC_CLIENT_SECRET", "")
	t.Setenv("PANEL_PUBLIC_URL", "https://panel.example.com/")
	h := NewOIDCHandler(config.OIDCConfig{Scopes: "openid email"}, nil)
	if h.current().Enabled() {
		t.Fatal("SSO must stay off without a client secret")
	}
	// Turned on without a restart: read again on the next request.
	t.Setenv("OIDC_CLIENT_SECRET", "s")
	cfg := h.current()
	if !cfg.Enabled() || cfg.ButtonLabel != "Sign in with SSO" || cfg.Scopes != "openid email" {
		t.Fatalf("config: %+v", cfg)
	}
	if got := h.redirectURL(cfg); got != "https://panel.example.com/api/v1/auth/oidc/callback" {
		t.Fatalf("redirect url: %q", got)
	}
}

// Discovery is per issuer, a failure is not remembered, and a request gets the
// endpoints of the issuer it asked about.
func TestOIDCDiscoverPerIssuer(t *testing.T) {
	var down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		p := strings.TrimSuffix(r.URL.Path, "/.well-known/openid-configuration")
		fmt.Fprintf(w, `{"authorization_endpoint":%q,"token_endpoint":%q,"userinfo_endpoint":%q}`, p+"/auth", p+"/token", p+"/user")
	}))
	defer srv.Close()
	h := NewOIDCHandler(config.OIDCConfig{}, nil)
	ctx := context.Background()

	down.Store(true)
	if _, err := h.discover(ctx, srv.URL+"/a"); err == nil {
		t.Fatal("a provider that is down must fail discovery")
	}
	down.Store(false)
	ep, err := h.discover(ctx, srv.URL+"/a")
	if err != nil || ep.token != "/a/token" {
		t.Fatalf("after the provider came back: %+v %v", ep, err)
	}
	if ep, err = h.discover(ctx, srv.URL+"/b/"); err != nil || ep.auth != "/b/auth" || ep.user != "/b/user" {
		t.Fatalf("another issuer: %+v %v", ep, err)
	}
}
