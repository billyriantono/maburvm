// Package secret resolves the panel's internal cryptographic secrets (JWT
// signing key, AES-256 data key, …) without forcing the operator to set them.
//
// Resolution order for each secret:
//  1. Environment variable, if set — advanced / multi-instance / external secret
//     manager override.
//  2. A value persisted in the data directory's secrets file.
//  3. Otherwise a cryptographically random value is generated, persisted (0600),
//     and reused on every subsequent boot.
//
// This means a basic single-node install needs ZERO secret env vars and is still
// secure by default (each install gets unique random secrets), while advanced
// deployments can still pin secrets via env. Secrets are NEVER a hardcoded
// public constant.
//
// The data directory defaults to ./data and can be overridden with
// MABURVM_DATA_DIR. The secrets file (secrets.json) is created 0600. Keep this
// file (or the env overrides) stable across restarts: rotating the JWT key
// invalidates active sessions, and rotating the AES key makes previously
// encrypted data (2FA secrets, stored credentials) unreadable — which is why the
// AES key lives in a file next to the panel, not in the database it protects.
package secret

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	dataDirEnv  = "MABURVM_DATA_DIR"
	defaultDir  = "./data"
	secretsFile = "secrets.json"
)

type store struct {
	path string
	mu   sync.Mutex
	data map[string]string
}

var (
	once   sync.Once
	global *store
)

func instance() *store {
	once.Do(func() {
		dir := os.Getenv(dataDirEnv)
		if dir == "" {
			dir = defaultDir
		}
		global = newStore(filepath.Join(dir, secretsFile))
	})
	return global
}

func newStore(path string) *store {
	s := &store{path: path, data: map[string]string{}}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &s.data) // a corrupt file just means we regenerate
	}
	return s
}

// resolve returns the secret for fileKey using env override → persisted →
// generate+persist. gen must return a non-empty value.
func (s *store) resolve(envKey, fileKey string, gen func() string) string {
	if v := os.Getenv(envKey); v != "" {
		return v
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.data[fileKey]; ok && v != "" {
		return v
	}
	v := gen()
	s.data[fileKey] = v
	s.persist()
	return v
}

// persist writes the secrets file atomically with 0600 permissions. Best effort:
// a write failure is non-fatal (the in-memory value is still used this run), but
// it is surfaced on stderr so the operator can fix the data dir — otherwise
// secrets would silently regenerate on the next boot and invalidate sessions.
func (s *store) persist() {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "secret: cannot create data dir %q: %v\n", filepath.Dir(s.path), err)
		return
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "secret: cannot write %q: %v\n", s.path, err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		fmt.Fprintf(os.Stderr, "secret: cannot finalize %q: %v\n", s.path, err)
	}
}

func mustRandom(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("secret: crypto/rand failed: " + err.Error())
	}
	return b
}

// JWTSecret returns the HMAC key used to sign session and console tokens.
// Env override: JWT_SECRET_KEY.
func JWTSecret() string {
	return instance().resolve("JWT_SECRET_KEY", "jwt_secret", func() string {
		return hex.EncodeToString(mustRandom(48)) // 96 hex chars
	})
}

// AESKey returns the exactly-32-byte key for AES-256 encryption at rest.
// Env override: AES_KEY (must be exactly 32 bytes). A generated key is 24 random
// bytes base64-encoded, which is exactly 32 characters (= 32 bytes).
func AESKey() string {
	return instance().resolve("AES_KEY", "aes_key", func() string {
		return base64.StdEncoding.EncodeToString(mustRandom(24)) // 32 chars
	})
}
