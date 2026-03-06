package config

import (
	"os"
	"testing"
	"time"
)

func TestLoad_WithRequiredEnvVars(t *testing.T) {
	os.Setenv("DB_HOST", "localhost")
	os.Setenv("DB_USER", "postgres")
	os.Setenv("DB_PASSWORD", "password")
	os.Setenv("DB_NAME", "maburvm")
	os.Setenv("JWT_SECRET_KEY", "test-secret-key")
	os.Setenv("S3_ENDPOINT", "http://localhost:9000")
	os.Setenv("S3_ACCESS_KEY", "accesskey")
	os.Setenv("S3_SECRET_KEY", "secretkey")
	os.Setenv("S3_BUCKET", "bucket")

	defer func() {
		os.Unsetenv("DB_HOST")
		os.Unsetenv("DB_USER")
		os.Unsetenv("DB_PASSWORD")
		os.Unsetenv("DB_NAME")
		os.Unsetenv("JWT_SECRET_KEY")
		os.Unsetenv("S3_ENDPOINT")
		os.Unsetenv("S3_ACCESS_KEY")
		os.Unsetenv("S3_SECRET_KEY")
		os.Unsetenv("S3_BUCKET")
	}()

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	if cfg.Database.Host != "localhost" {
		t.Errorf("Expected DB_HOST=localhost, got %s", cfg.Database.Host)
	}
	if cfg.Database.Port != 5432 {
		t.Errorf("Expected DB_PORT=5432, got %d", cfg.Database.Port)
	}
	if cfg.Database.User != "postgres" {
		t.Errorf("Expected DB_USER=postgres, got %s", cfg.Database.User)
	}
	if cfg.Database.SSLMode != "disable" {
		t.Errorf("Expected DB_SSL_MODE=disable, got %s", cfg.Database.SSLMode)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("Expected SERVER_PORT=8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.Host != "0.0.0.0" {
		t.Errorf("Expected SERVER_HOST=0.0.0.0, got %s", cfg.Server.Host)
	}

	if cfg.JWT.SecretKey != "test-secret-key" {
		t.Errorf("Expected JWT_SECRET_KEY=test-secret-key, got %s", cfg.JWT.SecretKey)
	}

	if cfg.S3.Endpoint != "http://localhost:9000" {
		t.Errorf("Expected S3_ENDPOINT=http://localhost:9000, got %s", cfg.S3.Endpoint)
	}
	if cfg.S3.Region != "us-east-1" {
		t.Errorf("Expected S3_REGION=us-east-1, got %s", cfg.S3.Region)
	}

	if cfg.VNC.TokenTTL != 5*time.Minute {
		t.Errorf("Expected VNC_TOKEN_TTL=5m, got %s", cfg.VNC.TokenTTL)
	}
}

func TestLoad_MissingRequiredFields(t *testing.T) {
	os.Unsetenv("DB_HOST")
	os.Unsetenv("DB_USER")
	os.Unsetenv("DB_PASSWORD")
	os.Unsetenv("DB_NAME")
	os.Unsetenv("JWT_SECRET_KEY")
	os.Unsetenv("S3_ENDPOINT")
	os.Unsetenv("S3_ACCESS_KEY")
	os.Unsetenv("S3_SECRET_KEY")
	os.Unsetenv("S3_BUCKET")

	_, err := Load()
	if err == nil {
		t.Fatal("Expected error for missing required fields, got nil")
	}

	t.Logf("Got expected error: %v", err)
}

func TestLoadDefault(t *testing.T) {
	cfg, err := LoadDefault()
	if err != nil {
		t.Fatalf("Expected no error from LoadDefault, got: %v", err)
	}

	if cfg.Database.Host != "localhost" {
		t.Errorf("Expected default DB_HOST=localhost, got %s", cfg.Database.Host)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("Expected default SERVER_PORT=8080, got %d", cfg.Server.Port)
	}
	if cfg.Agent.GRPCPort != 50051 {
		t.Errorf("Expected default AGENT_GRPC_PORT=50051, got %d", cfg.Agent.GRPCPort)
	}
}
