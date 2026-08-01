package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/shared/config"
)

// These tests exercise the liveness aliases and readiness endpoints WITHOUT a
// live database or queue. They are not run in parallel to keep process-wide
// echo/state isolated.

// newHealthTestServer builds a Server with no DB/queue wiring so health routes
// can be exercised in isolation. Dependency pings are injected per-test.
func newHealthTestServer(t *testing.T) *Server {
	t.Helper()
	s := NewServer(nil, &config.Config{})
	s.readinessCheckTimeout = 2 * time.Second
	// Register ONLY the health routes, not the DB-dependent routes, so we can
	// test liveness/readiness without a live database.
	s.setupHealthRoutes()
	return s
}

func TestHealth_LivenessAliases(t *testing.T) {
	s := newHealthTestServer(t)

	for _, path := range []string{"/health", "/healthz", "/livez"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			s.echo.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200 for %s, got %d", path, rec.Code)
			}
			var body map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if body["status"] != "healthy" {
				t.Errorf("expected status=healthy, got %v", body["status"])
			}
			if _, ok := body["timestamp"]; !ok {
				t.Errorf("expected timestamp field")
			}
		})
	}
}

func TestReadiness_DependenciesMissing(t *testing.T) {
	s := newHealthTestServer(t)
	// db and queueClient are nil → both reported "missing" → not ready.

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when deps missing, got %d", rec.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "not_ready" {
		t.Errorf("expected status=not_ready, got %v", body["status"])
	}
	deps, ok := body["dependencies"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected dependencies map, got %T", body["dependencies"])
	}
	for _, name := range []string{"database", "queue"} {
		dep, ok := deps[name].(map[string]interface{})
		if !ok {
			t.Fatalf("expected %s dependency map", name)
		}
		if dep["status"] != "missing" {
			t.Errorf("expected %s=missing, got %v", name, dep["status"])
		}
	}
}

func TestReadiness_DependenciesOK(t *testing.T) {
	s := newHealthTestServer(t)
	s.dbPing = func(ctx context.Context) error { return nil }
	s.queuePing = func(ctx context.Context) error { return nil }

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when deps ok, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ready" {
		t.Errorf("expected status=ready, got %v", body["status"])
	}
}

func TestReadiness_DependencyError(t *testing.T) {
	s := newHealthTestServer(t)
	s.dbPing = func(ctx context.Context) error { return nil }
	s.queuePing = func(ctx context.Context) error { return context.DeadlineExceeded }

	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when a dep errors, got %d", rec.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	deps := body["dependencies"].(map[string]interface{})
	queue := deps["queue"].(map[string]interface{})
	if queue["status"] != "error" {
		t.Errorf("expected queue=error, got %v", queue["status"])
	}
}
