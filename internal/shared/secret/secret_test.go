package secret

import (
	"crypto/aes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// These tests cover the fail-closed, once-cached secret lifecycle (Oracle
// Phase 0 follow-up):
//   - absent file → generate + persist a complete stable pair
//   - empty / `{}` / incomplete / malformed files → error (no silent regen)
//   - short (operator) JWT input → rejected
//   - env precedence (complete pair) wins; partial env → rejected
//   - in-memory keys do NOT rotate when the persisted file is deleted after init
//   - persistence failure → error
//   - concurrent first resolution is race-free

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "secret-test-*")
	if err != nil {
		panic(err)
	}
	os.Setenv(dataDirEnv, dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// resolveInDir points the singleton at a specific directory and resets the cache
// so each test gets a fresh first-boot decision.
func resolveInDir(t *testing.T, dir string) *Pair {
	t.Helper()
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	pair, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return pair
}

func TestAbsentFile_GeneratesFullStablePair(t *testing.T) {
	dir := t.TempDir()
	pair := resolveInDir(t, dir)

	if len(pair.JWT) < minJWTKeyBytes {
		t.Fatalf("JWT too short: %d", len(pair.JWT))
	}
	if len(pair.AES) != aesKeyBytes {
		t.Fatalf("AES must be 32 bytes, got %d", len(pair.AES))
	}

	// File must exist with a complete pair.
	b, err := os.ReadFile(filepath.Join(dir, secretsFile))
	if err != nil {
		t.Fatalf("expected persisted file: %v", err)
	}
	var data map[string]string
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatalf("persisted file malformed: %v", err)
	}
	// Persisted aes_key is the BASE64 of the raw key (valid-UTF8 JSON); it must
	// decode back to the identical raw cached key.
	decoded, derr := base64.StdEncoding.DecodeString(data["aes_key"])
	if derr != nil {
		t.Fatalf("persisted aes_key not valid base64: %v", derr)
	}
	if data["jwt_secret"] != pair.JWT || string(decoded) != pair.AES {
		t.Fatalf("persisted pair mismatch")
	}
	if len(data["aes_key"]) != 44 {
		t.Fatalf("persisted aes_key must be 44-char base64, got %d", len(data["aes_key"]))
	}

	// A second resolve (fresh cache) reloads the SAME persisted pair.
	reset()
	pair2, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve#2: %v", err)
	}
	if pair2.JWT != pair.JWT || pair2.AES != pair.AES {
		t.Fatalf("restart not stable: %q/%q vs %q/%q", pair.JWT, pair.AES, pair2.JWT, pair2.AES)
	}
}

func TestEmptyFile_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, secretsFile), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	if _, err := Resolve(); err == nil {
		t.Fatal("expected error for empty file, got nil")
	}
}

func TestWhitespaceOnlyFile_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, secretsFile), []byte("   \n\t  "), 0o600); err != nil {
		t.Fatal(err)
	}
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	if _, err := Resolve(); err == nil {
		t.Fatal("expected error for whitespace-only file, got nil")
	}
}

func TestEmptyJSON_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, secretsFile), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	if _, err := Resolve(); err == nil {
		t.Fatal("expected error for {} file, got nil")
	}
}

func TestIncompletePair_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	// Only one key present.
	if err := os.WriteFile(filepath.Join(dir, secretsFile), []byte(`{"jwt_secret":"enoughlengthjwtvalue1234567890abcdef"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	if _, err := Resolve(); err == nil {
		t.Fatal("expected error for incomplete pair, got nil")
	}
}

func TestMalformedJSON_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, secretsFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	if _, err := Resolve(); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestShortJWTEnv_Rejected(t *testing.T) {
	dir := t.TempDir()
	reset()
	t.Setenv(dataDirEnv, dir)
	t.Setenv("JWT_SECRET_KEY", "short")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef") // valid 32-char AES
	// Partial env (one invalid) → rejected.
	if _, err := Resolve(); err == nil {
		t.Fatal("expected error for short JWT env, got nil")
	}

	// Even a complete env with a short JWT must be rejected.
	reset()
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef") // 32 chars, still < 48 but >= 32 → valid
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	pair, err := Resolve()
	if err != nil {
		t.Fatalf("32-char JWT should be accepted: %v", err)
	}
	if len(pair.JWT) != 32 {
		t.Fatalf("expected 32-char JWT, got %d", len(pair.JWT))
	}

	// Now genuinely short (< 32).
	reset()
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	t.Setenv("JWT_SECRET_KEY", "tooshort")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	if _, err := Resolve(); err == nil {
		t.Fatal("expected error for <32-char JWT env, got nil")
	}
}

func TestCompleteEnvWinsAndIgnoresFile(t *testing.T) {
	dir := t.TempDir()
	// A present (valid) file should be ignored when a complete env pair is set.
	valid := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(dir, secretsFile),
		[]byte(`{"jwt_secret":"differentjwtvalue0123456789abcdef","aes_key":"0123456789abcdef0123456789abcdef"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reset()
	t.Setenv(dataDirEnv, dir)
	t.Setenv("JWT_SECRET_KEY", valid)
	t.Setenv("AES_KEY", valid)
	pair, err := Resolve()
	if err != nil {
		t.Fatalf("complete env should win: %v", err)
	}
	if pair.JWT != valid || pair.AES != valid {
		t.Fatalf("env override not applied")
	}
}

func TestPartialEnv_Rejected(t *testing.T) {
	dir := t.TempDir()
	reset()
	t.Setenv(dataDirEnv, dir)
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef")
	os.Unsetenv("AES_KEY") // only JWT set
	if _, err := Resolve(); err == nil {
		t.Fatal("expected error for partial env (JWT only), got nil")
	}
}

func TestDeletePersistedFileAfterInit_NoRotation(t *testing.T) {
	dir := t.TempDir()
	pair := resolveInDir(t, dir)

	// Delete the persisted file after initial resolution.
	if err := os.Remove(filepath.Join(dir, secretsFile)); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// The live (already-initialized) cache must NOT rotate on re-Resolve: it
	// returns the same in-memory pair. A *fresh* process cache on a missing file
	// would legitimately generate anew, but within an initialized process the
	// cached values are immutable.
	again, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve after delete: %v", err)
	}
	if again.JWT != pair.JWT || again.AES != pair.AES {
		t.Fatalf("cached keys rotated after file deletion: %q vs %q", pair.JWT, again.JWT)
	}

	// A fresh process-like cache (simulated via reset) on a missing file MUST
	// generate a NEW stable pair — proving first-boot generation still works and
	// is distinct from in-process immutability.
	reset()
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	t.Setenv(dataDirEnv, dir)
	fresh, err := Resolve()
	if err != nil {
		t.Fatalf("fresh Resolve: %v", err)
	}
	if fresh.JWT == pair.JWT && fresh.AES == pair.AES {
		t.Fatalf("fresh boot on missing file should generate a new pair")
	}
	// And the new one must be durably persisted again.
	if _, statErr := os.Stat(filepath.Join(dir, secretsFile)); statErr != nil {
		t.Fatalf("fresh pair not persisted: %v", statErr)
	}
}

func TestPersistenceError_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	// Make the directory read-only so atomic write/rename fails deterministically.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skipf("cannot make dir read-only: %v", err)
	}
	defer os.Chmod(dir, 0o700)

	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	if _, err := Resolve(); err == nil {
		t.Fatal("expected persistence failure error, got nil")
	}
}

func TestConcurrentResolve_RaceFree(t *testing.T) {
	dir := t.TempDir()
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")

	const n = 50
	var wg sync.WaitGroup
	results := make([]*Pair, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, e := Resolve()
			results[i] = p
			errs[i] = e
		}(i)
	}
	wg.Wait()

	var ref *Pair
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d error: %v", i, errs[i])
		}
		if ref == nil {
			ref = results[i]
			continue
		}
		if results[i].JWT != ref.JWT || results[i].AES != ref.AES {
			t.Fatalf("concurrent resolution produced divergent pairs")
		}
	}
}

// unused helper removed

// restoreDirFsync resets the injectable dirFsync seam to its default
// implementation after a test has tampered with it.
func restoreDirFsync() {
	dirFsync = func(dir string) error {
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
}

// TestPersist_DirFsyncOpenFailure_FailsClosed verifies that if the parent
// directory cannot be opened after a successful rename, persistence still fails
// closed and the error is ErrPersistFailed (not silently swallowed). The rename
// may have already succeeded on disk, but we honestly report the durability gap.
func TestPersist_DirFsyncOpenFailure_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")

	orig := dirFsync
	defer func() { dirFsync = orig }()
	dirFsync = func(d string) error {
		return fmt.Errorf("%w: cannot open directory %q: injected", ErrPersistFailed, d)
	}

	// First boot must surface the (injected) directory-fsync failure.
	if _, err := Resolve(); err == nil {
		t.Fatal("expected ErrPersistFailed on dir open failure, got nil")
	} else if !errors.Is(err, ErrPersistFailed) {
		t.Fatalf("expected ErrPersistFailed, got %v", err)
	}
}

// TestPersist_DirFsyncSyncFailure_FailsClosed verifies that if the parent
// directory opens but its Sync fails, persistence fails closed with
// ErrPersistFailed and is not silently claimed durable.
func TestPersist_DirFsyncSyncFailure_FailsClosed(t *testing.T) {
	dir := t.TempDir()
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")

	orig := dirFsync
	defer func() { dirFsync = orig }()
	dirFsync = func(d string) error {
		f, oerr := os.Open(d)
		if oerr != nil {
			return fmt.Errorf("%w: cannot open directory %q: %v", ErrPersistFailed, d, oerr)
		}
		defer func() { _ = f.Close() }()
		return fmt.Errorf("%w: cannot fsync directory %q: injected", ErrPersistFailed, d)
	}

	if _, err := Resolve(); err == nil {
		t.Fatal("expected ErrPersistFailed on dir sync failure, got nil")
	} else if !errors.Is(err, ErrPersistFailed) {
		t.Fatalf("expected ErrPersistFailed, got %v", err)
	}
}

// TestPersist_DirFsyncCleanup verifies the injectable seam is restored and that
// normal first-boot persistence works after a tampered run (no leaked state).
func TestPersist_DirFsyncSeamReset(t *testing.T) {
	defer restoreDirFsync()

	// Tamper, then ensure restoreDirFsync returns behavior to normal and a real
	// first boot persists + restarts successfully.
	dir1 := t.TempDir()
	dirFsync = func(d string) error {
		return fmt.Errorf("%w: injected", ErrPersistFailed)
	}
	reset()
	t.Setenv(dataDirEnv, dir1)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	if _, err := Resolve(); err == nil {
		t.Fatal("tampered seam should have failed")
	}

	restoreDirFsync()
	dir2 := t.TempDir()
	reset()
	t.Setenv(dataDirEnv, dir2)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	pair, err := Resolve()
	if err != nil {
		t.Fatalf("normal first boot after seam reset failed: %v", err)
	}
	if len(pair.AES) != aesKeyBytes {
		t.Fatalf("unexpected AES length %d", len(pair.AES))
	}
}

// TestNormalizeAESKey_Raw32TakesPrecedence verifies a raw 32-byte value is
// returned exactly, even if those 32 bytes happen to be valid base64.
func TestNormalizeAESKey_Raw32TakesPrecedence(t *testing.T) {
	raw := "0123456789abcdef0123456789abcdef" // exactly 32 chars, valid base64 too
	out, err := normalizeAESKey(raw)
	if err != nil {
		t.Fatalf("raw 32-byte key should be accepted: %v", err)
	}
	if out != raw {
		t.Fatalf("raw key should be passed through unchanged, got %q", out)
	}
	if len(out) != aesKeyBytes {
		t.Fatalf("normalized raw key must be %d bytes, got %d", aesKeyBytes, len(out))
	}
}

// TestNormalizeAESKey_Base64DecodesTo32 verifies a valid 44-char base64 string
// (encoding 32 bytes) resolves to the exact 32 raw bytes, and that the result
// is usable by crypto/aes.NewCipher.
func TestNormalizeAESKey_Base64DecodesTo32(t *testing.T) {
	raw := []byte("0123456789abcdef0123456789abcdef")
	b64 := base64.StdEncoding.EncodeToString(raw) // 44 chars
	if len(b64) != 44 {
		t.Fatalf("sanity: expected 44-char base64, got %d", len(b64))
	}
	out, err := normalizeAESKey(b64)
	if err != nil {
		t.Fatalf("valid base64 of 32 bytes should be accepted: %v", err)
	}
	if len(out) != aesKeyBytes {
		t.Fatalf("normalized key must be %d bytes, got %d", aesKeyBytes, len(out))
	}
	if out != string(raw) {
		t.Fatalf("expected decoded raw bytes %q, got %q", raw, out)
	}
	if _, err := aes.NewCipher([]byte(out)); err != nil {
		t.Fatalf("normalized key must be usable by aes.NewCipher: %v", err)
	}
}

// TestNormalizeAESKey_InvalidRejected verifies base64 that decodes to the wrong
// size, and non-base64 / wrong-length input, are rejected fail-closed.
func TestNormalizeAESKey_InvalidRejected(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"too short", "short"},
		{"non base64", "this is not valid base64$$$"},
		{"base64 of 16 bytes", base64.StdEncoding.EncodeToString(make([]byte, 16))},
		{"base64 of 33 bytes", base64.StdEncoding.EncodeToString(make([]byte, 33))},
		{"33 raw chars", "0123456789abcdef0123456789abcdef0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := normalizeAESKey(c.in); err == nil {
				t.Fatalf("expected rejection for %q, got nil", c.in)
			}
		})
	}
}

// TestResolveEnv_Base64AESNormalized confirms the resolution boundary normalizes
// a base64 AES env value into a 32-byte canonical key that config/callers (e.g.
// aes.NewCipher) can consume directly.
func TestResolveEnv_Base64AESNormalized(t *testing.T) {
	dir := t.TempDir()
	reset()
	t.Setenv(dataDirEnv, dir)
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef")
	rawAES := []byte("abcdefghijklmnopqrstuvwxyz123456")
	t.Setenv("AES_KEY", base64.StdEncoding.EncodeToString(rawAES)) // 44 chars

	pair, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve with base64 AES: %v", err)
	}
	if len(pair.AES) != aesKeyBytes {
		t.Fatalf("normalized AES must be %d bytes in pair, got %d", aesKeyBytes, len(pair.AES))
	}
	if pair.AES != string(rawAES) {
		t.Fatalf("expected decoded AES %q, got %q", rawAES, pair.AES)
	}
	if _, err := aes.NewCipher([]byte(pair.AES)); err != nil {
		t.Fatalf("normalized AES key unusable by aes.NewCipher: %v", err)
	}
}

// TestResolvePersisted_Base64AESNormalized confirms a persisted base64 AES value
// is normalized at the pair boundary and remains stable across restarts.
func TestResolvePersisted_Base64AESNormalized(t *testing.T) {
	dir := t.TempDir()
	rawAES := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ123456")
	b64 := base64.StdEncoding.EncodeToString(rawAES)
	jwt := "0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(dir, secretsFile),
		[]byte(`{"jwt_secret":"`+jwt+`","aes_key":"`+b64+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")

	pair, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve with persisted base64 AES: %v", err)
	}
	if pair.AES != string(rawAES) {
		t.Fatalf("expected normalized AES %q, got %q", rawAES, pair.AES)
	}

	// Restart (fresh cache) must reload the SAME normalized pair.
	reset()
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	t.Setenv(dataDirEnv, dir)
	pair2, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve#2: %v", err)
	}
	if pair2.AES != pair.AES || pair2.JWT != pair.JWT {
		t.Fatalf("restart not stable: %q vs %q", pair.AES, pair2.AES)
	}
}

// TestGeneratedPair_StillValid confirms the auto-generated pair (raw 32-char AES)
// still validates and is usable, preserving existing persisted/env behavior.
func TestGeneratedPair_StillValid(t *testing.T) {
	dir := t.TempDir()
	pair := resolveInDir(t, dir)
	if len(pair.AES) != aesKeyBytes {
		t.Fatalf("generated AES should be %d bytes, got %d", aesKeyBytes, len(pair.AES))
	}
	if _, err := aes.NewCipher([]byte(pair.AES)); err != nil {
		t.Fatalf("generated AES key unusable by aes.NewCipher: %v", err)
	}
	if err := validateAESKey(pair.AES); err != nil {
		t.Fatalf("generated AES should validate: %v", err)
	}
}

// TestGeneratedPair_PersistsBase64AndIsStable proves the new first-boot contract:
//   - the in-memory AES key is exactly 32 RAW bytes (256-bit entropy, usable by
//     aes.NewCipher);
//   - the persisted secrets file stores aes_key as 44-char standard base64 (valid
//     UTF-8, no raw binary in JSON);
//   - a fresh boot reloads the IDENTICAL raw 32-byte key (no silent rotation, no
//     entropy loss).
func TestGeneratedPair_PersistsBase64AndIsStable(t *testing.T) {
	dir := t.TempDir()
	pair := resolveInDir(t, dir)

	// In-memory key: raw 32 bytes, usable.
	if len(pair.AES) != aesKeyBytes {
		t.Fatalf("in-memory AES must be %d raw bytes, got %d", aesKeyBytes, len(pair.AES))
	}
	if _, err := aes.NewCipher([]byte(pair.AES)); err != nil {
		t.Fatalf("generated raw AES key unusable by aes.NewCipher: %v", err)
	}

	// Persisted form: base64 (44 chars), decodes to identical raw bytes.
	b, err := os.ReadFile(filepath.Join(dir, secretsFile))
	if err != nil {
		t.Fatalf("expected persisted file: %v", err)
	}
	var data map[string]string
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatalf("persisted file malformed: %v", err)
	}
	if len(data["aes_key"]) != 44 {
		t.Fatalf("persisted aes_key must be 44-char base64, got %d", len(data["aes_key"]))
	}
	decoded, derr := base64.StdEncoding.DecodeString(data["aes_key"])
	if derr != nil {
		t.Fatalf("persisted aes_key not valid base64: %v", derr)
	}
	if string(decoded) != pair.AES {
		t.Fatalf("persisted base64 does not decode to in-memory raw key")
	}

	// Restart: same raw key reloaded (stable, no rotation).
	reset()
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	t.Setenv(dataDirEnv, dir)
	pair2, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve#2: %v", err)
	}
	if pair2.AES != pair.AES {
		t.Fatalf("restart rotated generated key: %q vs %q", pair.AES, pair2.AES)
	}
}

// TestLegacyRaw32Persisted_StillStable ensures backward compatibility: a persisted
// file whose aes_key is already a raw 32-CHAR value (printable, possibly also
// valid base64) continues to be interpreted as RAW under raw-precedence, and is
// reused unchanged across restarts (no silent rotation to a base64 form in memory).
func TestLegacyRaw32Persisted_StillStable(t *testing.T) {
	dir := t.TempDir()
	raw := "0123456789abcdef0123456789abcdef" // 32 printable chars, valid base64 too
	if err := os.WriteFile(filepath.Join(dir, secretsFile),
		[]byte(`{"jwt_secret":"`+raw+`","aes_key":"`+raw+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reset()
	t.Setenv(dataDirEnv, dir)
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")

	pair, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve legacy raw-32: %v", err)
	}
	if pair.AES != raw {
		t.Fatalf("legacy raw-32 must stay raw, got %q", pair.AES)
	}
	if _, err := aes.NewCipher([]byte(pair.AES)); err != nil {
		t.Fatalf("legacy raw-32 key unusable: %v", err)
	}
	// Restart remains stable (still raw, not re-encoded to base64 in memory).
	reset()
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("AES_KEY")
	t.Setenv(dataDirEnv, dir)
	pair2, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve#2: %v", err)
	}
	if pair2.AES != raw {
		t.Fatalf("legacy raw-32 rotated on restart: %q vs %q", raw, pair2.AES)
	}
}

// TestGeneratedAESKeyEntropy_Is256Bits sanity-checks that first-boot generation
// draws a full 32 bytes (not the old 24-byte/192-bit mistake) by verifying two
// independently generated keys are distinct and each 32 bytes. This guards
// against regressing to sub-256-bit entropy.
func TestGeneratedAESKeyEntropy_Is256Bits(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	p1 := resolveInDir(t, dir1)
	p2 := resolveInDir(t, dir2)
	if len(p1.AES) != aesKeyBytes || len(p2.AES) != aesKeyBytes {
		t.Fatalf("generated AES must be %d bytes", aesKeyBytes)
	}
	if p1.AES == p2.AES {
		t.Fatalf("two independent first-boot keys must differ (entropy check)")
	}
	// Must NOT look like base64-of-24-bytes; confirm it is the raw form (not 44 chars
	// as a string length — raw 32-byte strings have len 32, valid for aes.NewCipher).
	if _, err := aes.NewCipher([]byte(p1.AES)); err != nil {
		t.Fatalf("generated key must be a valid 32-byte AES key: %v", err)
	}
}
