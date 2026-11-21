package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// Which nodes hold a template names the nodes: staff see it, a customer
// asking for it gets the template alone.
func TestTemplateNodeStatusIsStaffOnly(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:tmplnodes?mode=memory"), &gorm.Config{Logger: logger.Discard})
	require.NoError(t, err)
	for _, q := range []string{
		`CREATE TABLE os_templates (id TEXT PRIMARY KEY, name TEXT NOT NULL, version TEXT NOT NULL, image_path TEXT NOT NULL,
			size_bytes BIGINT NOT NULL DEFAULT 0, os_type TEXT NOT NULL DEFAULT 'linux', is_active BOOLEAN DEFAULT 1, description TEXT,
			licensed BOOLEAN NOT NULL DEFAULT 0, plan_ids TEXT, kind TEXT NOT NULL DEFAULT 'disk', checksum_sha256 TEXT NOT NULL DEFAULT '',
			checksum_url TEXT NOT NULL DEFAULT '', checksum_checked_at DATETIME, allow_http BOOLEAN NOT NULL DEFAULT 0,
			auto_refresh BOOLEAN NOT NULL DEFAULT 0, firmware TEXT NOT NULL DEFAULT 'bios', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE template_node_status (template_id TEXT, node_id TEXT, status TEXT, error_msg TEXT, synced_at DATETIME,
			checksum TEXT NOT NULL DEFAULT '', created_at DATETIME, updated_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(q).Error)
	}
	tmpl := uuid.New().String()
	require.NoError(t, db.Exec(`INSERT INTO os_templates (id, name, version, image_path) VALUES (?, 'debian', '12', 'https://example.com/d.qcow2')`, tmpl).Error)
	require.NoError(t, db.Exec(`INSERT INTO nodes (id, name) VALUES (?, 'node-secret-name')`, uuid.New().String()).Error)

	h := NewTemplateHandler(service.NewTemplateService(db, repository.NewTemplateRepository(db), repository.NewNodeRepository(db), nil))
	get := func(u *middleware.UserContext) map[string]any {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/templates/"+tmpl+"?include_nodes=true", nil)
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.SetParamNames("id")
		c.SetParamValues(tmpl)
		c.Set(middleware.UserContextKey, u)
		require.NoError(t, h.GetTemplate(c))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			Data map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body.Data
	}

	customer := get(&middleware.UserContext{ID: uuid.New(), Role: models.RoleClient, Permissions: middleware.GetPermissionsForRole(models.RoleClient)})
	require.Empty(t, customer["nodes"], "a customer must not see the nodes")
	staff := get(&middleware.UserContext{ID: uuid.New(), Role: models.RoleAdmin, Permissions: []string{"node:read", middleware.PermStaff}})
	require.Len(t, staff["nodes"], 1)
}
