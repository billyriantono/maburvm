package middleware

import (
	"os"
	"testing"
)

// TestMain sets a JWT signing secret for the whole package's tests. GetJWTSecret
// now fails closed (panics) when JWT_SECRET_KEY is unset instead of falling back
// to a guessable constant, so tests that mint or verify tokens must provide one.
func TestMain(m *testing.M) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-middleware-tests")
	}
	os.Exit(m.Run())
}
