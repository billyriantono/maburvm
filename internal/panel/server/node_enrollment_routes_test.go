package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/service"
)

// The installer's endpoints are reached through the real route table: the
// enrollment answers to its install token and not to a session, although it
// sits under /api/v1/nodes beside routes that need one.
func TestNodeInstallerRoutesThroughTheServer(t *testing.T) {
	f := newStaffFixture(t)
	for _, ddl := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, ip_address TEXT NOT NULL, status TEXT, token TEXT NOT NULL,
			cert_fingerprint TEXT NOT NULL DEFAULT '', last_report_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE node_enrollments (id TEXT PRIMARY KEY, node_id TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE, created_by TEXT,
			created_at DATETIME NOT NULL, expires_at DATETIME NOT NULL, used_at DATETIME, used_from TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO nodes (id, name, ip_address, status, token) VALUES ('11111111-1111-1111-1111-111111111111', 'node-new', '192.0.2.10', 'offline', 'old')`,
	} {
		require.NoError(t, f.db.Exec(ddl).Error)
	}
	f.s.setupNodeRoutes(f.s.echo.Group("/api/v1"))
	f.s.setupProvisionRoutes()

	enr, err := service.IssueNodeEnrollment(context.Background(), f.db, "11111111-1111-1111-1111-111111111111", "")
	require.NoError(t, err)
	call := func(method, path, bearer string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		f.s.echo.ServeHTTP(rec, req)
		return rec
	}

	rec := call(http.MethodGet, "/api/v1/nodes/enroll", enr.Token)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "node-new")

	rec = call(http.MethodGet, "/install-agent.sh", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "#!/usr/bin/env bash")

	// The install token is no session: the node's own routes refuse it.
	rec = call(http.MethodGet, "/api/v1/nodes/11111111-1111-1111-1111-111111111111", enr.Token)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = call(http.MethodPost, "/api/v1/nodes/11111111-1111-1111-1111-111111111111/install-command", enr.Token)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = call(http.MethodPost, "/api/v1/nodes/enroll", enr.Token)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"agent_token":"`)
	rec = call(http.MethodPost, "/api/v1/nodes/enroll", enr.Token)
	assert.Equal(t, http.StatusForbidden, rec.Code, "an install token works once")
}
