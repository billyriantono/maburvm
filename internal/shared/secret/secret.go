// Package secret resolves the panel's internal cryptographic secrets (JWT
// signing key, AES-256 data key, …) without forcing the operator to set them.
//
// Resolution order for the COMPLETE key pair (both keys together):
//  1. Environment override — only when BOTH JWT_SECRET_KEY and AES_KEY are set
//     and valid. A partial environment (exactly one of the two) is a
//     misconfiguration and is rejected fail-closed, because it cannot yield a
//     deterministic complete pair and would risk silent key rotation.
//  2. A persisted, COMPLETE, valid pair in the data directory's secrets.json.
//  3. Otherwise (no env, and no usable persisted file) a cryptographically
//     random complete pair is generated, persisted (0600, fsynced) atomically,
//     and reused on every subsequent boot.
//
// Fail-closed semantics (Oracle remediation):
//   - A MISSING secrets.json is valid first boot: generate + persist a complete
//     pair together.
//   - A file that exists but is zero-byte / whitespace-only / `{}` / malformed
//     JSON / an INCOMPLETE pair / or holds an invalid persisted value MUST
//     surface an error. We never silently regenerate in memory, and we never
//     log secret contents.
//   - The resolved pair is cached immutably the first time it is needed. Later
//     deletion or a transient read failure of the persisted file NEVER causes
//     the in-memory keys to rotate: request/auth and VNC paths use the cached
//     values and never re-open or regenerate the persisted file per request.
//
// The data directory defaults to ./data and can be overridden with
// MABURVM_DATA_DIR. The secrets file (secrets.json) is created 0600. Keep this
// file (or the env overrides) stable across restarts: rotating the JWT key
// invalidates active sessions, and rotating the AES key makes previously
// encrypted data (2FA secrets, stored credentials) unreadable — which is why the
// AES key lives in a file next to the panel, not in the database it protects.
package secret

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	dataDirEnv  = "MABURVM_DATA_DIR"
	defaultDir  = "./data"
	secretsFile = "secrets.json"

	// jwtKeyBytes is the cryptographic size of a generated JWT key (96 hex chars).
	jwtKeyBytes = 48
	// aesKeyBytes is the exact AES-256 key size (32 bytes). This is also the
	// canonical string length of the normalized (raw) AES key handed to callers:
	// both a raw 32-byte value and valid base64-of-32-bytes normalize to this.
	aesKeyBytes = 32
	// minJWTKeyBytes is the minimum operator-supplied JWT key length in bytes.
	minJWTKeyBytes = 32
)

// Common error sentinels so callers can wrap with context. None of these
// messages contain secret material.
var (
	ErrFileUnreadable   = errors.New("secret file exists but cannot be read")
	ErrFileMalformed    = errors.New("secret file contains malformed JSON")
	ErrFileInvalid      = errors.New("secret file is present but empty or incomplete")
	ErrInvalidValue     = errors.New("invalid persisted secret value")
	ErrPersistFailed    = errors.New("failed to durably persist secrets")
	ErrDirUnavailable   = errors.New("secrets data directory unavailable")
	ErrGenerationFailed = errors.New("cryptographic generator failed")
	ErrPartialEnv       = errors.New("only one of JWT_SECRET_KEY/AES_KEY set; both or neither required")
)

// Pair is the complete, immutable set of cryptographic secrets.
type Pair struct {
	JWT string
	AES string
}

type resolver struct {
	once sync.Once
	pair *Pair
	err  error
}

// singleton caches the resolved pair process-wide. It is immutable once set.
var singleton = &resolver{}

// reset clears the cached pair. Intended ONLY for tests; never call in
// production code (it would defeat the once-only guarantee).
func reset() {
	singleton = &resolver{}
}

// ResetForTest clears the cached pair. Intended ONLY for tests in OTHER packages
// (e.g. config) that share this process-wide singleton without access to the
// unexported reset; never call in production code.
func ResetForTest() {
	reset()
}

// Resolve returns the cached secret pair, resolving it exactly once. Resolution
// is fail-closed and never rotates keys after the first successful resolution.
func Resolve() (*Pair, error) {
	singleton.once.Do(func() {
		singleton.pair, singleton.err = resolveOnce()
	})
	if singleton.err != nil {
		return nil, singleton.err
	}
	return singleton.pair, nil
}

// resolveOnce performs the single bootstrap resolution (see package doc).
func resolveOnce() (*Pair, error) {
	jwtEnv := os.Getenv("JWT_SECRET_KEY")
	aesEnv := os.Getenv("AES_KEY")

	// (1) Complete environment override.
	if jwtEnv != "" && aesEnv != "" {
		if err := validateJWTSecret(jwtEnv); err != nil {
			return nil, fmt.Errorf("JWT_SECRET_KEY: %w: %v", ErrInvalidValue, err)
		}
		aes, err := normalizeAESKey(aesEnv)
		if err != nil {
			return nil, fmt.Errorf("AES_KEY: %w: %v", ErrInvalidValue, err)
		}
		// Env is the source of truth; do not touch the persisted file.
		return &Pair{JWT: jwtEnv, AES: aes}, nil
	}

	// Partial environment (exactly one set) is rejected: it cannot produce a
	// deterministic complete pair and risks silent rotation.
	if jwtEnv != "" || aesEnv != "" {
		return nil, ErrPartialEnv
	}

	// (2)/(3) No env: consult the persisted file.
	path := secretPath()
	exists, data, err := loadFile(path)
	if err != nil {
		return nil, err
	}

	if !exists {
		// First boot: generate a complete pair and persist it together.
		pair, err := generatePair()
		if err != nil {
			return nil, err
		}
		if err := persistPair(path, pair); err != nil {
			return nil, err
		}
		return pair, nil
	}

	// File present: it must contain a COMPLETE, valid pair or we fail closed.
	jwt, ok1 := data["jwt_secret"]
	aes, ok2 := data["aes_key"]
	if !ok1 || !ok2 {
		return nil, fmt.Errorf("%s (%s): %w", secretsFile, path, ErrFileInvalid)
	}
	if err := validateJWTSecret(jwt); err != nil {
		return nil, fmt.Errorf("jwt_secret (%s): %w: %v", path, ErrInvalidValue, err)
	}
	aesKey, err := normalizeAESKey(aes)
	if err != nil {
		return nil, fmt.Errorf("aes_key (%s): %w: %v", path, ErrInvalidValue, err)
	}
	return &Pair{JWT: jwt, AES: aesKey}, nil
}

// loadFile reads the persisted secrets file. A missing file is reported as
// exists=false with no error (first-boot candidate). Any other problem
// (unreadable / empty / malformed / incomplete) is returned as an error so the
// caller fails closed.
func loadFile(path string) (exists bool, data map[string]string, err error) {
	b, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return false, nil, nil
		}
		return true, nil, fmt.Errorf("%w: %v", ErrFileUnreadable, rerr)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		// Zero-byte / whitespace-only file: present but invalid (incomplete).
		return true, nil, fmt.Errorf("%w: empty file %s", ErrFileInvalid, path)
	}
	data = map[string]string{}
	if err := json.Unmarshal(b, &data); err != nil {
		return true, nil, fmt.Errorf("%w: %v", ErrFileMalformed, err)
	}
	return true, data, nil
}

// generatePair creates a fresh, valid complete pair. The AES key is generated
// from a full 32 random bytes (256-bit AES-256 entropy). The in-memory Pair.AES
// is the canonical RAW 32-byte form handed to callers; persistPair encodes it as
// standard base64 for durable, valid-UTF-8 JSON storage (see persistPair).
func generatePair() (*Pair, error) {
	jb, err := mustRandom(jwtKeyBytes)
	if err != nil {
		return nil, err
	}
	ab, err := mustRandom(aesKeyBytes) // 32 random bytes → 256-bit AES-256 key
	if err != nil {
		return nil, err
	}
	return &Pair{
		JWT: hex.EncodeToString(jb),
		AES: string(ab), // canonical raw 32-byte key (may be non-UTF8 in memory only)
	}, nil
}

// persistPair writes the complete pair to the secrets file atomically and
// durably: 0600 temp → write → fsync temp → close → rename → fsync parent dir.
// Failure at ANY step — including opening, syncing, or closing the parent
// directory — is surfaced as ErrPersistFailed (fail-closed). We never silently
// claim durable persistence when the directory fsync cannot be confirmed. A
// half-written temp file is cleaned up.
//
// The raw AES key (canonical in-memory form) is persisted as standard base64 so
// the JSON stays valid UTF-8 (no invalid-UTF8 binary in the file). On restart the
// persisted base64 is decoded back to the identical raw 32 bytes by normalizeAESKey,
// so the cached key is stable across boots.
func persistPair(path string, pair *Pair) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: cannot create %q: %v", ErrDirUnavailable, dir, err)
	}
	b, err := json.MarshalIndent(map[string]string{
		"jwt_secret": pair.JWT,
		"aes_key":    base64.StdEncoding.EncodeToString([]byte(pair.AES)),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: cannot marshal: %v", ErrPersistFailed, err)
	}

	tmp, err := os.CreateTemp(dir, ".secrets-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: cannot create temp file: %v", ErrPersistFailed, err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	defer cleanup()

	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: cannot chmod temp file: %v", ErrPersistFailed, err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: cannot write temp file: %v", ErrPersistFailed, err)
	}
	// fsync the temp file so the bytes are on disk before the rename.
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: cannot fsync temp file: %v", ErrPersistFailed, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: cannot close temp file: %v", ErrPersistFailed, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("%w: cannot finalize %q: %v", ErrPersistFailed, path, err)
	}
	// fsync the containing directory so the rename is durable where supported.
	// This is fail-closed: if the parent directory cannot be opened, synced, or
	// closed, we MUST surface ErrPersistFailed rather than claim durable
	// persistence. Note the rename may have already succeeded on disk; we prefer
	// to report the durability failure (honestly) over silently declaring
	// success. The work is delegated to dirFsync so tests can inject
	// open/sync failures without relying on an unmountable filesystem.
	if err := dirFsync(dir); err != nil {
		return err
	}
	return nil
}

// dirFsync durably flushes the parent directory so the just-completed rename is
// persisted where the platform supports directory fsync. It is a package-level
// variable (an injectable seam) so tests can simulate directory-open / directory-
// sync failures deterministically without an unmountable filesystem. The default
// implementation never silently swallows an open failure: any open/sync/close
// error is surfaced wrapped in ErrPersistFailed.
var dirFsync = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("%w: cannot open directory %q: %v", ErrPersistFailed, dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("%w: cannot fsync directory %q: %v", ErrPersistFailed, dir, err)
	}
	return nil
}

func mustRandom(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrGenerationFailed, err)
	}
	return b, nil
}

// validateJWTSecret requires an operator-supplied JWT key of at least
// minJWTKeyBytes (32 bytes); generated keys are 48 bytes. Empty values are
// rejected.
func validateJWTSecret(v string) error {
	if len(v) < minJWTKeyBytes {
		return fmt.Errorf("must be at least %d bytes, got %d", minJWTKeyBytes, len(v))
	}
	return nil
}

// validateAESKey accepts exactly 32 bytes (raw) OR a base64 string that decodes
// to exactly 32 bytes. It does NOT mutate the value; use normalizeAESKey to get
// the canonical raw form.
func validateAESKey(v string) error {
	_, err := normalizeAESKey(v)
	return err
}

// normalizeAESKey returns the canonical, exactly-32-byte AES key for any accepted
// input:
//   - a raw value of exactly 32 bytes is returned as-is (raw takes precedence,
//     even if those 32 bytes happen to parse as valid base64);
//   - otherwise the value must be standard base64 that decodes to exactly 32
//     bytes, in which case the decoded bytes are returned.
//
// Every other input is rejected fail-closed. The returned value is the single
// contract handed to Config/callers (e.g. aes.NewCipher), so consumers never
// need to know whether the operator supplied raw or base64.
func normalizeAESKey(v string) (string, error) {
	// Raw exactly-32-byte input takes precedence (even if it parses as base64).
	if len(v) == aesKeyBytes {
		return v, nil
	}
	b, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", fmt.Errorf("not 32 bytes and not valid base64: %w", err)
	}
	if len(b) != aesKeyBytes {
		return "", fmt.Errorf("must decode to %d bytes, got %d", aesKeyBytes, len(b))
	}
	return string(b), nil
}

// JWTSecret returns the cached JWT signing key. It resolves the complete pair
// exactly once and never re-reads or regenerates per call.
func JWTSecret() (string, error) {
	pair, err := Resolve()
	if err != nil {
		return "", err
	}
	return pair.JWT, nil
}

// AESKey returns the cached AES-256 key. See JWTSecret for caching semantics.
func AESKey() (string, error) {
	pair, err := Resolve()
	if err != nil {
		return "", err
	}
	return pair.AES, nil
}

func secretPath() string {
	dir := os.Getenv(dataDirEnv)
	if dir == "" {
		dir = defaultDir
	}
	return filepath.Join(dir, secretsFile)
}
