package server

import (
	"os"
	"testing"
)

// clearStorageEnv unsets all storage env vars used by the constructors.
func clearBackupEnv(t *testing.T) {
	t.Helper()
	keys := []string{
		"STORAGE_ENDPOINT", "S3_ENDPOINT",
		"STORAGE_ACCESS_KEY", "S3_ACCESS_KEY",
		"STORAGE_SECRET_KEY", "S3_SECRET_KEY",
		"STORAGE_BUCKET", "S3_BUCKET",
		"STORAGE_REGION", "S3_REGION",
		"STORAGE_USE_PATH_STYLE", "S3_USE_PATH_STYLE",
		"STORAGE_FORCE_HTTP", "S3_FORCE_HTTP",
	}
	for _, k := range keys {
		os.Unsetenv(k)
	}
}

// With credentials set, the constructor builds a client whose resolved
// endpoint + flags reflect the env inputs (no network call is made).
func TestBackupStorageClientFromEnv(t *testing.T) {
	clearBackupEnv(t)
	t.Setenv("STORAGE_ACCESS_KEY", "ak")
	t.Setenv("STORAGE_SECRET_KEY", "sk")
	t.Setenv("STORAGE_BUCKET", "b")

	t.Run("scheme-less force http", func(t *testing.T) {
		clearBackupEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_ENDPOINT", "minio:9000")
		t.Setenv("STORAGE_FORCE_HTTP", "true")

		client, err := backupStorageClientFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client.Endpoint() != "http://minio:9000" {
			t.Fatalf("endpoint = %q; want http://minio:9000", client.Endpoint())
		}
		if !client.ForceHTTP() {
			t.Fatalf("ForceHTTP() = false; want true")
		}
		// Flag omitted -> legacy MinIO default (path-style on).
		if !client.UsePathStyle() {
			t.Fatalf("UsePathStyle() = false; want true (legacy default)")
		}
	})

	t.Run("scheme-less default https", func(t *testing.T) {
		clearBackupEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("S3_ENDPOINT", "s3.amazonaws.com")

		client, err := backupStorageClientFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// STORAGE_* omitted; S3_* is the fallback.
		if client.Endpoint() != "https://s3.amazonaws.com" {
			t.Fatalf("endpoint = %q; want https://s3.amazonaws.com", client.Endpoint())
		}
	})

	t.Run("explicit scheme wins over force http", func(t *testing.T) {
		clearBackupEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_ENDPOINT", "https://minio:9000")
		t.Setenv("STORAGE_FORCE_HTTP", "true")

		client, err := backupStorageClientFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client.Endpoint() != "https://minio:9000" {
			t.Fatalf("endpoint = %q; want https://minio:9000", client.Endpoint())
		}
	})

	t.Run("invalid scheme rejected", func(t *testing.T) {
		clearBackupEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_ENDPOINT", "ftp://minio:9000")

		if _, err := backupStorageClientFromEnv(); err == nil {
			t.Fatalf("expected error for unsupported scheme, got nil")
		}
	})

	t.Run("whitespace endpoint rejected", func(t *testing.T) {
		clearBackupEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")

		for _, ep := range []string{"   ", "  minio:9000", "minio:9000  "} {
			t.Setenv("STORAGE_ENDPOINT", ep)
			if _, err := backupStorageClientFromEnv(); err == nil {
				t.Fatalf("endpoint %q: expected error for whitespace-contaminated endpoint, got nil", ep)
			}
		}
	})

	t.Run("high-priority whitespace endpoint does not fall through to S3", func(t *testing.T) {
		// STORAGE_ENDPOINT is whitespace-only (set) and S3_ENDPOINT is a valid
		// scheme-less value. The whitespace must win (fail closed) and must NOT
		// silently fall through to the valid S3_ENDPOINT.
		clearBackupEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_ENDPOINT", "   ")
		t.Setenv("S3_ENDPOINT", "s3.amazonaws.com")

		if _, err := backupStorageClientFromEnv(); err == nil {
			t.Fatalf("expected whitespace-only STORAGE_ENDPOINT to fail closed, not fall through to S3_ENDPOINT")
		}
	})

	t.Run("flag precedence STORAGE over S3", func(t *testing.T) {
		clearBackupEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_FORCE_HTTP", "true")
		t.Setenv("S3_FORCE_HTTP", "false")

		client, err := backupStorageClientFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !client.ForceHTTP() {
			t.Fatalf("ForceHTTP() = false; STORAGE_FORCE_HTTP=true should win")
		}
	})

	t.Run("invalid bool fails closed", func(t *testing.T) {
		clearBackupEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_FORCE_HTTP", "notabool")

		if _, err := backupStorageClientFromEnv(); err == nil {
			t.Fatalf("expected error for invalid bool, got nil")
		}
	})

	t.Run("explicit path style false honored", func(t *testing.T) {
		clearBackupEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("S3_USE_PATH_STYLE", "false")

		client, err := backupStorageClientFromEnv()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client.UsePathStyle() {
			t.Fatalf("UsePathStyle() = true; explicit false should win")
		}
	})

	t.Run("missing credentials", func(t *testing.T) {
		clearBackupEnv(t)
		if _, err := backupStorageClientFromEnv(); err == nil {
			t.Fatalf("expected error for missing credentials, got nil")
		}
	})
}

func TestParseBoolFlag(t *testing.T) {
	clearBackupEnv(t)
	t.Setenv("STORAGE_FORCE_HTTP", "true")

	if v, err := parseBoolFlag("STORAGE_FORCE_HTTP", "S3_FORCE_HTTP"); err != nil || !v {
		t.Fatalf("parseBoolFlag = (%v,%v); want (true,nil)", v, err)
	}
	if v, err := parseBoolFlag("MISSING_A", "MISSING_B"); err != nil || v {
		t.Fatalf("parseBoolFlag missing = (%v,%v); want (false,nil)", v, err)
	}
}
