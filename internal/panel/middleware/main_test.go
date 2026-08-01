package middleware

import (
	"os"
	"testing"
)

// TestMain sets a complete, valid secret pair for the whole package's tests.
// GetJWTSecret/AESKey resolve once via the cached secret resolver, which now
// requires EITHER a complete env pair (both keys) OR no env at all (file/gen).
// Because GenerateTokenPair/ParseAndValidateToken mint and verify real tokens,
// the test must provide BOTH a JWT secret (>= 32 bytes) and a 32-byte AES key.
func TestMain(m *testing.M) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-middleware-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	os.Exit(m.Run())
}
