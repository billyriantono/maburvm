package queue

import (
	"os"
	"testing"
)

// clearStorageEnv unsets all storage env vars used by the constructors.
func clearStorageEnv(t *testing.T) {
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
func TestNewBackupStorageClientEndpointResolution(t *testing.T) {
	clearStorageEnv(t)
	t.Setenv("STORAGE_ACCESS_KEY", "ak")
	t.Setenv("STORAGE_SECRET_KEY", "sk")
	t.Setenv("STORAGE_BUCKET", "b")

	t.Run("scheme-less force http", func(t *testing.T) {
		clearStorageEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_ENDPOINT", "minio:9000")
		t.Setenv("STORAGE_FORCE_HTTP", "true")
		t.Setenv("STORAGE_USE_PATH_STYLE", "true")

		client, err := newBackupStorageClient("minio")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client.Endpoint() != "http://minio:9000" {
			t.Fatalf("endpoint = %q; want http://minio:9000", client.Endpoint())
		}
		if !client.UsePathStyle() {
			t.Fatalf("UsePathStyle() = false; want true")
		}
		if !client.ForceHTTP() {
			t.Fatalf("ForceHTTP() = false; want true")
		}
	})

	t.Run("scheme-less default https", func(t *testing.T) {
		clearStorageEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("S3_ENDPOINT", "s3.amazonaws.com")

		client, err := newBackupStorageClient("s3")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if client.Endpoint() != "https://s3.amazonaws.com" {
			t.Fatalf("endpoint = %q; want https://s3.amazonaws.com", client.Endpoint())
		}
		// Flag omitted -> legacy MinIO default (path-style on).
		if !client.UsePathStyle() {
			t.Fatalf("UsePathStyle() = false; want true (legacy default)")
		}
	})

	t.Run("explicit https scheme", func(t *testing.T) {
		clearStorageEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_ENDPOINT", "https://minio:9000")
		t.Setenv("STORAGE_FORCE_HTTP", "true")

		client, err := newBackupStorageClient("minio")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Explicit scheme wins over ForceHTTP.
		if client.Endpoint() != "https://minio:9000" {
			t.Fatalf("endpoint = %q; want https://minio:9000", client.Endpoint())
		}
	})

	t.Run("invalid scheme rejected", func(t *testing.T) {
		clearStorageEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_ENDPOINT", "ftp://minio:9000")

		if _, err := newBackupStorageClient("minio"); err == nil {
			t.Fatalf("expected error for unsupported scheme, got nil")
		}
	})

	t.Run("whitespace endpoint rejected", func(t *testing.T) {
		clearStorageEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")

		for _, ep := range []string{"   ", "  minio:9000", "minio:9000  "} {
			t.Setenv("STORAGE_ENDPOINT", ep)
			if _, err := newBackupStorageClient("minio"); err == nil {
				t.Fatalf("endpoint %q: expected error for whitespace-contaminated endpoint, got nil", ep)
			}
		}
	})

	t.Run("high-priority whitespace endpoint does not fall through to S3", func(t *testing.T) {
		clearStorageEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_ENDPOINT", "   ")
		t.Setenv("S3_ENDPOINT", "s3.amazonaws.com")

		if _, err := newBackupStorageClient("minio"); err == nil {
			t.Fatalf("expected whitespace-only STORAGE_ENDPOINT to fail closed, not fall through to S3_ENDPOINT")
		}
	})

	t.Run("flag precedence STORAGE over S3", func(t *testing.T) {
		clearStorageEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_FORCE_HTTP", "true")
		// S3 variant set to opposite; STORAGE must win.
		t.Setenv("S3_FORCE_HTTP", "false")

		client, err := newBackupStorageClient("minio")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !client.ForceHTTP() {
			t.Fatalf("ForceHTTP() = false; STORAGE_FORCE_HTTP=true should win")
		}
	})

	t.Run("invalid bool fails closed", func(t *testing.T) {
		clearStorageEnv(t)
		t.Setenv("STORAGE_ACCESS_KEY", "ak")
		t.Setenv("STORAGE_SECRET_KEY", "sk")
		t.Setenv("STORAGE_BUCKET", "b")
		t.Setenv("STORAGE_FORCE_HTTP", "notabool")

		if _, err := newBackupStorageClient("minio"); err == nil {
			t.Fatalf("expected error for invalid bool, got nil")
		}
	})

	t.Run("missing credentials", func(t *testing.T) {
		clearStorageEnv(t)
		if _, err := newBackupStorageClient("minio"); err == nil {
			t.Fatalf("expected error for missing credentials, got nil")
		}
	})
}

func TestBoolEnvAndEnvSet(t *testing.T) {
	clearStorageEnv(t)
	t.Setenv("STORAGE_FORCE_HTTP", "true")

	if v, err := boolEnv("STORAGE_FORCE_HTTP", "S3_FORCE_HTTP"); err != nil || !v {
		t.Fatalf("boolEnv = (%v,%v); want (true,nil)", v, err)
	}
	if !envSet("STORAGE_FORCE_HTTP", "S3_FORCE_HTTP") {
		t.Fatalf("envSet = false; want true")
	}
	if v, err := boolEnv("MISSING_A", "MISSING_B"); err != nil || v {
		t.Fatalf("boolEnv missing = (%v,%v); want (false,nil)", v, err)
	}
	if envSet("MISSING_A", "MISSING_B") {
		t.Fatalf("envSet missing = true; want false")
	}
}
