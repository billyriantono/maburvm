//go:build pg

package handler

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

// The admin console extras through their routes, on the real schema:
// maintenance mode, staff notes, the logs, the job queue and bulk actions.
func TestPG_AdminExtrasRoutes(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	t.Cleanup(middleware.ForgetMaintenance)
	db := migratedPG(t)
	ctx := context.Background()

	// River's own tables, as the panel creates them at start.
	var name string
	require.NoError(t, db.Raw("SELECT current_database()").Scan(&name).Error)
	u, err := url.Parse(os.Getenv("MABURVM_TEST_PG_DSN"))
	require.NoError(t, err)
	u.Path = "/" + name
	pool, err := pgxpool.New(ctx, u.String())
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, queue.RunMigrationsWithLogger(ctx, pool, slog.New(slog.NewTextHandler(io.Discard, nil))))

	owner, admin, customer := uuid.New(), uuid.New(), uuid.New()
	node, tmpl, vm := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, s := range []string{
		`INSERT INTO users (id, email, password_hash, role, created_at) VALUES ('` + owner.String() + `', 'owner@example.test', 'x', 'admin', NOW() - INTERVAL '2 days')`,
		`INSERT INTO users (id, email, password_hash, role, created_at) VALUES ('` + admin.String() + `', 'admin@example.test', 'x', 'admin', NOW() - INTERVAL '1 day')`,
		`INSERT INTO users (id, email, password_hash, role) VALUES ('` + customer.String() + `', 'ana@example.test', 'x', 'client')`,
		`INSERT INTO nodes (id, name, ip_address, token, status) VALUES ('` + node + `', 'node-a', '192.0.2.1', 't', 'active')`,
		`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES ('` + tmpl + `', 'debian', '12', '/x', 'c')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, status) VALUES ('` + vm + `', '` + customer.String() + `', '` + node + `', 'web', '` + tmpl + `', 'stopped')`,
		`INSERT INTO ip_assignment_history (address, vm_id, vm_hostname, user_email, node_id, kind, assigned_at, released_at) VALUES
			('203.0.113.5', '` + vm + `', 'web', 'ana@example.test', '` + node + `', 'direct', NOW() - INTERVAL '3 days', NOW() - INTERVAL '1 day'),
			('203.0.113.9', '` + vm + `', 'web', 'ana@example.test', '` + node + `', 'direct', NOW() - INTERVAL '2 days', NULL)`,
		`INSERT INTO river_job (id, kind, args, state, queue, attempt, max_attempts, priority, finalized_at, errors) VALUES
			(901, 'vm_operation', '{"vm_id":"` + vm + `","operation":"rebuild","root_password":"hunter2"}', 'discarded', 'critical', 3, 3, 1, NOW(),
			 ARRAY['{"at":"2026-11-01T00:00:00Z","attempt":1,"error":"node unreachable","trace":"x"}'::jsonb, '{"at":"2026-11-01T00:01:00Z","attempt":2,"error":"disk full","trace":"y"}'::jsonb]),
			(902, 'backup', '{"vm_id":"` + uuid.NewString() + `"}', 'completed', 'batch', 1, 3, 1, NOW(), ARRAY[]::jsonb[])`,
	} {
		require.NoError(t, db.Exec(s).Error, s)
	}

	vms := service.NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	e := echo.New()
	RegisterMaintenanceRoutes(e, db, nil)
	RegisterVMMetaRoutes(e, vms, db)
	RegisterLogRoutes(e, db)
	RegisterBulkVMRoutes(e, BulkDeps{VMs: vms}, db)
	RegisterOpsRoutes(e, db, nil, nil)
	RegisterDashboardRoutes(e, db)
	RegisterAnnouncementRoutes(e, db)

	tokenFor := func(id uuid.UUID, role models.UserRole) string {
		tok, err := middleware.GenerateAccessToken(&models.User{ID: id, Role: role})
		require.NoError(t, err)
		return tok
	}
	ownerTok, adminTok, customerTok := tokenFor(owner, models.RoleAdmin), tokenFor(admin, models.RoleAdmin), tokenFor(customer, models.RoleClient)
	call := func(tok, method, path, body string) (int, map[string]any) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(echo.HeaderAuthorization, "Bearer "+tok)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	data := func(m map[string]any) map[string]any { d, _ := m["data"].(map[string]any); return d }

	t.Run("maintenance mode", func(t *testing.T) {
		code, _ := call(adminTok, http.MethodPut, "/api/v1/maintenance", `{"enabled":true}`)
		assert.Equal(t, http.StatusForbidden, code, "only the Owner switches it")
		code, out := call(ownerTok, http.MethodPut, "/api/v1/maintenance", `{"enabled":true,"message":"Back at 10:00","hold_jobs":true}`)
		require.Equal(t, http.StatusOK, code, out)
		assert.Equal(t, true, data(out)["can_switch"])

		code, out = call(customerTok, http.MethodPut, "/api/v1/vms/"+vm+"/tags", `{"tags":["web"]}`)
		assert.Equal(t, http.StatusServiceUnavailable, code)
		assert.Equal(t, "maintenance", out["code"])
		code, out = call(customerTok, http.MethodGet, "/api/v1/maintenance", "")
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, "Back at 10:00", data(out)["message"])
		assert.Nil(t, data(out)["by"], "customers do not see who switched it on")
		code, _ = call(adminTok, http.MethodPut, "/api/v1/vms/"+vm+"/tags", `{"tags":["web"]}`)
		assert.Equal(t, http.StatusOK, code, "staff carry on")

		code, _ = call(ownerTok, http.MethodPut, "/api/v1/maintenance", `{"enabled":false}`)
		require.Equal(t, http.StatusOK, code)
		code, out = call(customerTok, http.MethodPut, "/api/v1/vms/"+vm+"/lock", `{"locked":true}`)
		require.Equal(t, http.StatusOK, code, out)
		assert.Equal(t, "owner", data(out)["delete_lock"])
	})

	t.Run("the staff VM table filters by tag", func(t *testing.T) {
		code, out := call(adminTok, http.MethodGet, "/api/v1/dashboard/vms?tag=web", "")
		require.Equal(t, http.StatusOK, code, out)
		rows := data(out)["vms"].([]any)
		require.Len(t, rows, 1)
		assert.Equal(t, []any{"web"}, rows[0].(map[string]any)["tags"])
		assert.Equal(t, "owner", rows[0].(map[string]any)["delete_lock"])
		code, out = call(adminTok, http.MethodGet, "/api/v1/dashboard/vms?tag=db", "")
		require.Equal(t, http.StatusOK, code, out)
		assert.Empty(t, data(out)["vms"])
	})

	t.Run("staff notes are for staff", func(t *testing.T) {
		code, _ := call(customerTok, http.MethodGet, "/api/v1/vms/"+vm+"/notes", "")
		assert.Equal(t, http.StatusForbidden, code)
		code, out := call(adminTok, http.MethodPost, "/api/v1/vms/"+vm+"/notes", `{"body":"Asked for a refund"}`)
		require.Equal(t, http.StatusCreated, code, out)
		code, out = call(adminTok, http.MethodGet, "/api/v1/users/"+customer.String()+"/notes", "")
		require.Equal(t, http.StatusOK, code, out)
		assert.Empty(t, out["data"])
		code, out = call(adminTok, http.MethodGet, "/api/v1/vms/"+vm+"/notes", "")
		require.Equal(t, http.StatusOK, code)
		require.Len(t, out["data"], 1)
		assert.Equal(t, "admin@example.test", out["data"].([]any)[0].(map[string]any)["author_email"])
		code, _ = call(adminTok, http.MethodDelete, "/api/v1/vms/"+vm+"/notes/not-a-uuid", "")
		assert.Equal(t, http.StatusBadRequest, code)
	})

	t.Run("a customer sees an announcement, not who else it is for", func(t *testing.T) {
		_, err := service.SaveAnnouncement(ctx, db, "", service.AnnouncementInput{Title: "Reboot", Body: "Sunday",
			Audience: service.Audience{Type: service.AudienceVMs, IDs: []string{vm, uuid.NewString()}}}, admin.String())
		require.NoError(t, err)
		code, out := call(customerTok, http.MethodGet, "/api/v1/announcements/current", "")
		require.Equal(t, http.StatusOK, code, out)
		rows := out["data"].([]any)
		require.Len(t, rows, 1)
		assert.Equal(t, "Reboot", rows[0].(map[string]any)["title"])
		assert.NotContains(t, rows[0], "audience")
		assert.NotContains(t, rows[0], "created_by")
	})

	t.Run("address assignment log", func(t *testing.T) {
		code, out := call(adminTok, http.MethodGet, "/api/v1/logs/ip-allocations?q=203.0.113.5", "")
		require.Equal(t, http.StatusOK, code, out)
		events := data(out)["events"].([]any)
		require.Len(t, events, 1)
		ev := events[0].(map[string]any)
		assert.Equal(t, "203.0.113.5", ev["address"])
		assert.Equal(t, "node-a", ev["node_name"])
		assert.Equal(t, "web", ev["vm_hostname"])
		code, out = call(adminTok, http.MethodGet, "/api/v1/logs/ip-allocations?state=held", "")
		require.Equal(t, http.StatusOK, code)
		assert.Equal(t, float64(1), data(out)["total"])
	})

	t.Run("job queue search and detail", func(t *testing.T) {
		code, out := call(adminTok, http.MethodGet, "/api/v1/queue?vm_id="+vm, "")
		require.Equal(t, http.StatusOK, code, out)
		assert.Equal(t, float64(1), data(out)["total"])
		code, out = call(adminTok, http.MethodGet, "/api/v1/queue?q=disk+full", "")
		require.Equal(t, http.StatusOK, code, out)
		assert.Equal(t, float64(1), data(out)["total"], "the last error is searched")
		code, out = call(adminTok, http.MethodGet, "/api/v1/queue?q=hunter2", "")
		require.Equal(t, http.StatusOK, code, out)
		assert.Equal(t, float64(0), data(out)["total"], "arguments are not searched: they can hold credentials")
		code, out = call(adminTok, http.MethodGet, "/api/v1/queue?kind=backup&page_size=1", "")
		require.Equal(t, http.StatusOK, code, out)
		assert.Equal(t, float64(1), data(out)["total"])
		assert.ElementsMatch(t, []any{"backup", "vm_operation"}, data(out)["kinds"])

		code, out = call(adminTok, http.MethodGet, "/api/v1/queue/jobs/901", "")
		require.Equal(t, http.StatusOK, code, out)
		d := data(out)
		assert.Equal(t, "[REDACTED]", d["args"].(map[string]any)["root_password"])
		assert.Equal(t, "rebuild", d["args"].(map[string]any)["operation"])
		errs := d["errors"].([]any)
		require.Len(t, errs, 2)
		assert.Equal(t, "disk full", errs[1].(map[string]any)["error"])
		assert.Nil(t, errs[1].(map[string]any)["trace"])
		code, _ = call(adminTok, http.MethodGet, "/api/v1/queue/jobs/77777", "")
		assert.Equal(t, http.StatusNotFound, code)
	})

	t.Run("bulk preview and apply", func(t *testing.T) {
		code, out := call(customerTok, http.MethodPost, "/api/v1/vms/bulk", `{"action":"lock","vm_ids":["`+vm+`"]}`)
		assert.Equal(t, http.StatusForbidden, code, out)
		code, out = call(adminTok, http.MethodPost, "/api/v1/vms/bulk", `{"action":"lock","vm_ids":["`+vm+`","`+vm+`"],"preview":true}`)
		require.Equal(t, http.StatusOK, code, out)
		results := data(out)["results"].([]any)
		require.Len(t, results, 1, "a VM named twice is changed once")
		assert.Equal(t, true, results[0].(map[string]any)["ok"])
		var lock string
		require.NoError(t, db.Raw("SELECT delete_lock FROM vms WHERE id = ?", vm).Scan(&lock).Error)
		assert.Equal(t, "owner", lock, "a preview changes nothing")

		code, out = call(adminTok, http.MethodPost, "/api/v1/vms/bulk", `{"action":"lock","vm_ids":["`+vm+`"]}`)
		require.Equal(t, http.StatusOK, code, out)
		assert.Equal(t, float64(1), data(out)["ok"])
		require.NoError(t, db.Raw("SELECT delete_lock FROM vms WHERE id = ?", vm).Scan(&lock).Error)
		assert.Equal(t, "staff", lock)

		code, out = call(adminTok, http.MethodPost, "/api/v1/vms/bulk", `{"action":"start","vm_ids":["`+vm+`"],"preview":true}`)
		require.Equal(t, http.StatusOK, code, out)
		assert.Equal(t, "Will start", data(out)["results"].([]any)[0].(map[string]any)["message"])
		code, _ = call(adminTok, http.MethodPost, "/api/v1/vms/bulk", `{"action":"explode","vm_ids":["`+vm+`"]}`)
		assert.Equal(t, http.StatusBadRequest, code)
		code, _ = call(adminTok, http.MethodPost, "/api/v1/vms/bulk", `{"action":"apply_plan","vm_ids":["`+vm+`"]}`)
		assert.Equal(t, http.StatusBadRequest, code, "apply_plan needs the plan")
	})
}
