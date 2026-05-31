package sshconsole

import (
	"testing"
	"time"
)

func newTestProxy() *ProxyServer {
	return NewProxyServer(nil, "test-secret-key-for-ssh-console-unit")
}

func TestGenerateAndValidateToken(t *testing.T) {
	s := newTestProxy()
	token, expiresAt, err := s.GenerateToken("vm-1", "user-1", "10.0.0.5", 22, "root", "hunter2", TokenExpiry)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expiresAt should be in the future, got %v", expiresAt)
	}

	claims, err := s.validateToken(token)
	if err != nil {
		t.Fatalf("validateToken: %v", err)
	}
	if claims.VMID != "vm-1" || claims.UserID != "user-1" || claims.Host != "10.0.0.5" || claims.SSHUser != "root" || claims.Port != 22 {
		t.Fatalf("claims mismatch: %+v", claims)
	}
	if claims.Type != "ssh_access" {
		t.Fatalf("expected type ssh_access, got %q", claims.Type)
	}
}

func TestValidateTokenRejectsGarbage(t *testing.T) {
	s := newTestProxy()
	if _, err := s.validateToken("not-a-jwt"); err == nil {
		t.Fatal("expected error for garbage token")
	}
	// A token signed with a different secret must be rejected.
	other := NewProxyServer(nil, "a-totally-different-secret-value!!")
	token, _, _ := other.GenerateToken("vm", "u", "h", 22, "root", "pw", TokenExpiry)
	if _, err := s.validateToken(token); err == nil {
		t.Fatal("expected error for token signed with another secret")
	}
}

func TestConsumeCredentialIsOneTime(t *testing.T) {
	s := newTestProxy()
	token, _, err := s.GenerateToken("vm-1", "user-1", "10.0.0.5", 22, "root", "s3cret", TokenExpiry)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	claims, err := s.validateToken(token)
	if err != nil {
		t.Fatalf("validateToken: %v", err)
	}

	pw, ok := s.consumeCredential(claims.ID)
	if !ok || pw != "s3cret" {
		t.Fatalf("first consume should return the password, got (%q, %v)", pw, ok)
	}
	// Second consume must fail — credential is one-time.
	if _, ok := s.consumeCredential(claims.ID); ok {
		t.Fatal("second consume should fail (one-time credential)")
	}
}

func TestConsumeRejectsExpiredCredential(t *testing.T) {
	s := newTestProxy()
	token, _, _ := s.GenerateToken("vm-1", "user-1", "10.0.0.5", 22, "root", "pw", TokenExpiry)
	claims, _ := s.validateToken(token)
	// Force the stored credential to be already expired.
	s.creds.Store(claims.ID, credEntry{password: "pw", expiresAt: time.Now().Add(-time.Minute)})
	if _, ok := s.consumeCredential(claims.ID); ok {
		t.Fatal("expired credential should not be consumable")
	}
}

func TestTokenRateLimit(t *testing.T) {
	s := newTestProxy()
	const user = "rate-user"
	for i := 0; i < MaxTokensPerUser; i++ {
		if _, _, err := s.GenerateToken("vm", user, "h", 22, "root", "pw", TokenExpiry); err != nil {
			t.Fatalf("token %d should succeed: %v", i, err)
		}
	}
	if _, _, err := s.GenerateToken("vm", user, "h", 22, "root", "pw", TokenExpiry); err == nil {
		t.Fatal("expected rate-limit error after exceeding MaxTokensPerUser")
	}
	// A different user is unaffected.
	if _, _, err := s.GenerateToken("vm", "other-user", "h", 22, "root", "pw", TokenExpiry); err != nil {
		t.Fatalf("a different user should not be rate-limited: %v", err)
	}
}

func TestConnectionLimit(t *testing.T) {
	s := newTestProxy()
	const user = "conn-user"
	for i := 0; i < MaxConnectionsPerUser; i++ {
		if !s.addConnection(user) {
			t.Fatalf("connection %d should be allowed", i)
		}
	}
	if s.addConnection(user) {
		t.Fatal("connection beyond MaxConnectionsPerUser should be rejected")
	}
	s.removeConnection(user)
	if !s.addConnection(user) {
		t.Fatal("after removing one, a new connection should be allowed")
	}
}
