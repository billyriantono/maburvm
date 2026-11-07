package handler

import (
	"os"
	"strings"
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
	if NewOIDCHandler(config.OIDCConfig{Issuer: "https://idp.example", ClientID: "id"}, nil) != nil {
		t.Fatal("SSO must stay off without a client secret")
	}
	h := NewOIDCHandler(config.OIDCConfig{Issuer: "https://idp.example", ClientID: "id", ClientSecret: "s"}, nil)
	if h == nil || !strings.HasSuffix(h.redirectURL, "/api/v1/auth/oidc/callback") {
		t.Fatalf("redirect url: %+v", h)
	}
}
