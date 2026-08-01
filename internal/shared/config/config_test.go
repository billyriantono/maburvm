package config

import (
	"crypto/aes"
	"encoding/base64"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/maburvm/panel/internal/shared/secret"
)

// These tests exercise resolveFileSecrets and the resulting config resolution.
// They use t.Setenv and temp files, never a live database, and are deliberately
// NOT run in parallel to avoid global-env / process-state pollution.

func writeTempSecret(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write temp secret file: %v", err)
	}
	return path
}

// clearAll resets every env var this package reads so tests are isolated.
func clearAll(t *testing.T) {
	t.Helper()
	for _, e := range []string{
		"DB_HOST", "DB_USER", "DB_PASSWORD", "DB_PASSWORD_FILE",
		"DB_NAME", "JWT_SECRET_KEY", "JWT_SECRET_KEY_FILE",
		"AES_KEY", "AES_KEY_FILE",
		"S3_ENDPOINT", "S3_ACCESS_KEY", "S3_ACCESS_KEY_FILE",
		"S3_SECRET_KEY", "S3_SECRET_KEY_FILE", "S3_BUCKET",
		"MABURVM_DATA_DIR",
	} {
		t.Setenv(e, "")
	}
}

func TestResolveFileSecrets_PlainEnvWins(t *testing.T) {
	clearAll(t)

	t.Setenv("DB_PASSWORD", "plain-value")
	path := writeTempSecret(t, "pw", "file-value")
	t.Setenv("DB_PASSWORD_FILE", path)

	if err := resolveFileSecrets(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("DB_PASSWORD"); got != "plain-value" {
		t.Errorf("expected plain env to win, got %q", got)
	}
}

func TestResolveFileSecrets_FileUsed(t *testing.T) {
	clearAll(t)

	path := writeTempSecret(t, "pw", "  file-value-with-spaces\n")
	t.Setenv("DB_PASSWORD_FILE", path)

	if err := resolveFileSecrets(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := os.Getenv("DB_PASSWORD"); got != "file-value-with-spaces" {
		t.Errorf("expected trimmed file value, got %q", got)
	}
}

func TestResolveFileSecrets_MissingFileErrors(t *testing.T) {
	clearAll(t)

	t.Setenv("DB_PASSWORD_FILE", "/nonexistent/path/secret.txt")

	err := resolveFileSecrets()
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
	// Error must never leak secret contents.
	if containsAny(err.Error(), "nonexistent") == false {
		// path name in error is acceptable (not a secret), but ensure no value leaked
	}
	if containsAny(err.Error(), "secretcontents") {
		t.Errorf("error must not contain secret contents: %v", err)
	}
}

func TestResolveFileSecrets_EmptyFileErrors(t *testing.T) {
	clearAll(t)

	path := writeTempSecret(t, "pw", "\n\n")
	t.Setenv("DB_PASSWORD_FILE", path)

	err := resolveFileSecrets()
	if err == nil {
		t.Fatal("expected error for empty file, got nil")
	}
}

func TestResolveFileSecrets_NoFileSuppliedOk(t *testing.T) {
	clearAll(t)
	// Neither env nor file set — must not error here (secret store resolves later).
	if err := resolveFileSecrets(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoad_FileBackedSecretsEndToEnd(t *testing.T) {
	clearAll(t)

	// Required ordinary fields (non-sensitive).
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_USER", "postgres")
	t.Setenv("DB_NAME", "maburvm")
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_BUCKET", "bucket")

	// Sensitive inputs via files (no plain env set, so files are used).
	dbPath := writeTempSecret(t, "dbpw", "db-pass-from-file\n")
	s3akPath := writeTempSecret(t, "s3ak", "s3-access-from-file")
	s3skPath := writeTempSecret(t, "s3sk", "s3-secret-from-file")
	jwtPath := writeTempSecret(t, "jwt", "jwt-from-file-value-1234567890abcdef")
	aesPath := writeTempSecret(t, "aes", "0123456789abcdef0123456789abcdef")

	t.Setenv("DB_PASSWORD_FILE", dbPath)
	t.Setenv("S3_ACCESS_KEY_FILE", s3akPath)
	t.Setenv("S3_SECRET_KEY_FILE", s3skPath)
	t.Setenv("JWT_SECRET_KEY_FILE", jwtPath)
	t.Setenv("AES_KEY_FILE", aesPath)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Database.Password != "db-pass-from-file" {
		t.Errorf("DB_PASSWORD file not applied, got %q", cfg.Database.Password)
	}
	if cfg.S3.AccessKey != "s3-access-from-file" {
		t.Errorf("S3_ACCESS_KEY file not applied, got %q", cfg.S3.AccessKey)
	}
	if cfg.S3.SecretKey != "s3-secret-from-file" {
		t.Errorf("S3_SECRET_KEY file not applied, got %q", cfg.S3.SecretKey)
	}
	if cfg.JWT.SecretKey != "jwt-from-file-value-1234567890abcdef" {
		t.Errorf("JWT_SECRET_KEY file not applied, got %q", cfg.JWT.SecretKey)
	}
	if cfg.JWT.AESKey != "0123456789abcdef0123456789abcdef" {
		t.Errorf("AES_KEY file not applied, got %q", cfg.JWT.AESKey)
	}
}

func containsAny(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestLoadDefaultValidated_MissingRequiredFails(t *testing.T) {
	clearAll(t)
	// Deliberately leave required fields empty.
	_, err := LoadDefaultValidated()
	if err == nil {
		t.Fatal("expected validation error, got nil")
	}
}

func TestLoadDefaultValidated_PassesWithEnv(t *testing.T) {
	clearAll(t)
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_USER", "postgres")
	t.Setenv("DB_PASSWORD", "pw")
	t.Setenv("DB_NAME", "maburvm")
	t.Setenv("JWT_SECRET_KEY", "jwt-env-value-1234567890abcdef")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY", "access")
	t.Setenv("S3_SECRET_KEY", "secret")
	t.Setenv("S3_BUCKET", "bucket")

	cfg, err := LoadDefaultValidated()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Database.Host != "localhost" {
		t.Errorf("expected DB_HOST localhost, got %s", cfg.Database.Host)
	}
}

// mustParseDSN parses a generated DSN and fails the test on error.
func mustParseDSN(t *testing.T, dsn string) *url.URL {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("generated DSN is not a valid URL %q: %v", dsn, err)
	}
	return u
}

// TestDatabaseURL_SpecialCharactersRoundTrip verifies the single, URL-encoded
// DSN helper correctly escapes passwords containing spaces, quotes, '@', ':',
// '/', '?', '#', and backslashes, and that the round-trip recovers the original
// password and database name.
func TestDatabaseURL_SpecialCharactersRoundTrip(t *testing.T) {
	passwords := []string{
		"simple",
		"p@ss/word",
		"space pass word",
		`quote"pass'word`,
		"weird:?#@/\\pass",
		`back\slash`,
		"hash#and?amp=1",
	}
	for _, pw := range passwords {
		cfg := DatabaseConfig{
			Host:     "localhost",
			Port:     5432,
			User:     "dbuser",
			Password: pw,
			Name:     "maburvm",
			SSLMode:  "disable",
		}
		dsn := cfg.DatabaseURL()
		u := mustParseDSN(t, dsn)

		gotUser := u.User.Username()
		if gotUser != "dbuser" {
			t.Errorf("user mismatch: got %q", gotUser)
		}
		gotPass, ok := u.User.Password()
		if !ok {
			t.Fatalf("password missing from DSN for %q", pw)
		}
		if gotPass != pw {
			t.Errorf("password round-trip mismatch: input %q got %q (dsn %q)", pw, gotPass, dsn)
		}
		if u.Path != "/maburvm" {
			t.Errorf("expected path /maburvm, got %q (dsn %q)", u.Path, dsn)
		}
		if u.Query().Get("sslmode") != "disable" {
			t.Errorf("expected sslmode=disable, got %q", u.Query().Get("sslmode"))
		}
	}
}

// TestDatabaseURL_IPv6Host ensures an IPv6 host is bracketed correctly and the
// DSN still round-trips.
func TestDatabaseURL_IPv6Host(t *testing.T) {
	cfg := DatabaseConfig{
		Host:     "2001:db8::1",
		Port:     5432,
		User:     "u",
		Password: "p@ss",
		Name:     "db",
		SSLMode:  "require",
	}
	dsn := cfg.DatabaseURL()
	u := mustParseDSN(t, dsn)
	if u.Host != "[2001:db8::1]:5432" {
		t.Errorf("expected bracketed IPv6 host [2001:db8::1]:5432, got %q (dsn %q)", u.Host, dsn)
	}
	gotPass, _ := u.User.Password()
	if gotPass != "p@ss" {
		t.Errorf("password mismatch: %q", gotPass)
	}
}

// TestDatabaseURL_NoSSLModeOmitted ensures sslmode is omitted from the DSN when
// not configured.
func TestDatabaseURL_NoSSLModeOmitted(t *testing.T) {
	cfg := DatabaseConfig{Host: "h", Port: 5432, User: "u", Password: "p", Name: "db"}
	dsn := cfg.DatabaseURL()
	if dsn != "postgres://u:p@h:5432/db" {
		t.Errorf("expected no sslmode param, got %q", dsn)
	}
}

// TestLoad_Base64AESResolvesToUsable32ByteKey verifies that a base64-encoded
// (44-char) AES source, supplied via env, is normalized at the secret boundary
// into exactly 32 raw bytes, passes config validation, and is directly usable by
// crypto/aes.NewCipher — without the previous false rejection of base64 input.
//
// The secret resolver is a process-wide singleton; we reset it so this test does
// not pollute (or get polluted by) sibling tests, and restore isolation by
// clearing the relevant env afterward.
func TestLoad_Base64AESResolvesToUsable32ByteKey(t *testing.T) {
	clearAll(t)
	secret.ResetForTest()
	t.Cleanup(func() {
		secret.ResetForTest()
		clearAll(t)
	})

	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_USER", "postgres")
	t.Setenv("DB_PASSWORD", "pw")
	t.Setenv("DB_NAME", "maburvm")
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY", "access")
	t.Setenv("S3_SECRET_KEY", "secret")
	t.Setenv("S3_BUCKET", "bucket")

	// Valid 44-char base64 encoding of 32 bytes.
	rawAES := []byte("ABCDEFGHIJKLMNOPQRSTUVWXYZ123456")
	t.Setenv("AES_KEY", base64.StdEncoding.EncodeToString(rawAES))
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with base64 AES must not be rejected: %v", err)
	}
	if len(cfg.JWT.AESKey) != 32 {
		t.Fatalf("resolved AES key must be 32 bytes, got %d", len(cfg.JWT.AESKey))
	}
	if cfg.JWT.AESKey != string(rawAES) {
		t.Fatalf("expected normalized AES %q, got %q", rawAES, cfg.JWT.AESKey)
	}
	if _, err := aes.NewCipher([]byte(cfg.JWT.AESKey)); err != nil {
		t.Fatalf("resolved AES key unusable by aes.NewCipher: %v", err)
	}
}

// TestLoad_InvalidBase64AESRejected verifies that an AES value which is neither a
// raw 32-byte string nor valid base64-of-32-bytes fails closed (no usable key
// handed to Config/callers).
func TestLoad_InvalidBase64AESRejected(t *testing.T) {
	clearAll(t)
	secret.ResetForTest()
	t.Cleanup(func() {
		secret.ResetForTest()
		clearAll(t)
	})

	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_USER", "postgres")
	t.Setenv("DB_PASSWORD", "pw")
	t.Setenv("DB_NAME", "maburvm")
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY", "access")
	t.Setenv("S3_SECRET_KEY", "secret")
	t.Setenv("S3_BUCKET", "bucket")

	// base64 decoding to the wrong size → must be rejected.
	t.Setenv("AES_KEY", base64.StdEncoding.EncodeToString(make([]byte, 16)))
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef")

	if _, err := Load(); err == nil {
		t.Fatal("expected rejection for AES that decodes to wrong size, got nil")
	}
}

// TestLoad_Raw32CharAESStillWorks preserves existing behavior: a raw 32-char,
// printable AES value is accepted, validated, and usable.
func TestLoad_Raw32CharAESStillWorks(t *testing.T) {
	clearAll(t)
	secret.ResetForTest()
	t.Cleanup(func() {
		secret.ResetForTest()
		clearAll(t)
	})

	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_USER", "postgres")
	t.Setenv("DB_PASSWORD", "pw")
	t.Setenv("DB_NAME", "maburvm")
	t.Setenv("S3_ENDPOINT", "http://localhost:9000")
	t.Setenv("S3_ACCESS_KEY", "access")
	t.Setenv("S3_SECRET_KEY", "secret")
	t.Setenv("S3_BUCKET", "bucket")

	raw := "0123456789abcdef0123456789abcdef"
	t.Setenv("AES_KEY", raw)
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with raw 32-char AES must pass: %v", err)
	}
	if cfg.JWT.AESKey != raw {
		t.Fatalf("expected raw AES passed through, got %q", cfg.JWT.AESKey)
	}
	if _, err := aes.NewCipher([]byte(cfg.JWT.AESKey)); err != nil {
		t.Fatalf("raw AES key unusable by aes.NewCipher: %v", err)
	}
}
