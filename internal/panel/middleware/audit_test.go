package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// auditServer runs the audit middleware globally, as the panel does, and
// hands back the jobs it queued.
func auditServer(t *testing.T) (*echo.Echo, chan queue.AuditJob) {
	t.Helper()
	jobs := make(chan queue.AuditJob, 8)
	a := &Client{insert: func(_ context.Context, j queue.AuditJob) error { jobs <- j; return nil }}
	e := echo.New()
	e.Use(a.EchoMiddleware())
	return e, jobs
}

func nextJob(t *testing.T, jobs chan queue.AuditJob) *queue.AuditJob {
	t.Helper()
	select {
	case j := <-jobs:
		return &j
	case <-time.After(300 * time.Millisecond):
		return nil
	}
}

func TestAuditRecordsMutationsWithoutSecrets(t *testing.T) {
	e, jobs := auditServer(t)
	vmID := uuid.NewString()
	user := &UserContext{ID: uuid.New(), Role: models.RoleClient}
	var seen string
	e.POST("/api/v1/vms/:id/rebuild", func(c echo.Context) error {
		b, _ := io.ReadAll(c.Request().Body)
		seen = string(b)
		return c.JSON(http.StatusAccepted, map[string]any{"success": true})
	}, func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error { c.Set(UserContextKey, user); return next(c) }
	})

	body := `{"template_id":"t1","root_password":"hunter2","settings":{"smtpPassword":"x","abuseipdbToken":"y","webhook_url":"https://chat.example/hooks/T0/B0/xyz"},"user_data":"#cloud-config","code":"123456","keys":[{"private_key":"k","name":"n"}]}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vms/"+vmID+"/rebuild", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, body, seen, "the handler reads the body unchanged")
	j := nextJob(t, jobs)
	require.NotNil(t, j)
	require.Equal(t, "CREATE", j.Action)
	require.Equal(t, "VM", j.ResourceType)
	require.Equal(t, vmID, *j.ResourceID)
	require.Equal(t, user.ID.String(), *j.UserID)
	require.Equal(t, "/api/v1/vms/:id/rebuild", j.Details["route"])
	require.Equal(t, http.StatusAccepted, j.Details["http_status"])

	snap := *j.AfterSnapshot
	require.Equal(t, "t1", snap["template_id"])
	for _, k := range []string{"root_password", "user_data", "code"} {
		require.Equal(t, "[REDACTED]", snap[k], k)
	}
	nested := snap["settings"].(map[string]any)
	require.Equal(t, "[REDACTED]", nested["smtpPassword"])
	require.Equal(t, "[REDACTED]", nested["abuseipdbToken"])
	require.Equal(t, "[REDACTED]", nested["webhook_url"])
	key := snap["keys"].([]any)[0].(map[string]any)
	require.Equal(t, "[REDACTED]", key["private_key"])
	require.Equal(t, "n", key["name"])
}

func TestAuditRecordsFailedSignIn(t *testing.T) {
	e, jobs := auditServer(t)
	e.POST("/api/v1/auth/login", func(c echo.Context) error {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Invalid email or password"})
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"email":"a@example.com","password":"wrong","totpCode":"1"}`))
	req.Header.Set("Content-Type", "application/json")
	e.ServeHTTP(httptest.NewRecorder(), req)

	j := nextJob(t, jobs)
	require.NotNil(t, j)
	require.Equal(t, "LOGIN", j.Action)
	require.Equal(t, "SESSION", j.ResourceType)
	require.Nil(t, j.UserID)
	require.Equal(t, true, j.Details["failed"])
	require.Equal(t, "a@example.com", (*j.AfterSnapshot)["email"])
	require.Equal(t, "[REDACTED]", (*j.AfterSnapshot)["password"])
	require.Equal(t, "[REDACTED]", (*j.AfterSnapshot)["totpCode"])
}

func TestAuditSkipsReadsSkippedRequestsAndSelfAuditedHandlers(t *testing.T) {
	e, jobs := auditServer(t)
	db, err := gorm.Open(sqlite.Open("file:audit_self?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS audit_logs (id TEXT PRIMARY KEY, user_id TEXT, action TEXT NOT NULL, resource_type TEXT,
		resource_id TEXT, ip_address TEXT, user_agent TEXT, details TEXT, before_snapshot TEXT, after_snapshot TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`).Error)

	e.GET("/api/v1/vms", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	e.POST("/webhooks/billing", func(c echo.Context) error { SkipAudit(c); return c.NoContent(http.StatusOK) })
	e.DELETE("/api/v1/users/:id", func(c echo.Context) error {
		require.NoError(t, db.WithContext(c.Request().Context()).Create(&models.AuditLog{Action: "user.delete"}).Error)
		return c.NoContent(http.StatusOK)
	})
	e.POST("/api/v1/nodes/:id/update-agent", func(c echo.Context) error {
		return echo.NewHTTPError(http.StatusForbidden, "no")
	})
	e.POST("/api/v1/vms/:id/start", func(c echo.Context) error {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "Access token required"})
	})

	for _, r := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/v1/vms", nil),
		httptest.NewRequest(http.MethodPost, "/api/v1/no-such-route", nil),
		httptest.NewRequest(http.MethodPut, "/api/v1/vms", nil),
		httptest.NewRequest(http.MethodPost, "/webhooks/billing", nil),
		httptest.NewRequest(http.MethodDelete, "/api/v1/users/"+uuid.NewString(), nil),
		// An anonymous caller refused at the door: a flood must not become rows.
		httptest.NewRequest(http.MethodPost, "/api/v1/vms/"+uuid.NewString()+"/start", nil),
	} {
		e.ServeHTTP(httptest.NewRecorder(), r)
		require.Nil(t, nextJob(t, jobs), "%s %s must not add a generic row", r.Method, r.URL.Path)
	}

	// A refused request is still recorded, with the status the error carried.
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/nodes/n1/update-agent", nil))
	j := nextJob(t, jobs)
	require.NotNil(t, j)
	require.Equal(t, "NODE", j.ResourceType)
	require.Nil(t, j.ResourceID, "only a UUID fits the resource_id column")
	require.Equal(t, http.StatusForbidden, j.Details["http_status"])
}

// An SSO sign-in ends on a GET; where it redirects to is the outcome, and the
// two-factor ticket in the fragment is never stored.
func TestAuditRecordsSSOCallback(t *testing.T) {
	e, jobs := auditServer(t)
	e.GET("/api/v1/auth/oidc/callback", func(c echo.Context) error {
		return c.Redirect(http.StatusFound, "/login?sso=2fa#ticket=secret-ticket")
	})
	e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=c&state=s", nil))
	j := nextJob(t, jobs)
	require.NotNil(t, j)
	require.Equal(t, "LOGIN", j.Action)
	require.Equal(t, "/login?sso=2fa", j.Details["redirect"])
	require.Equal(t, "/api/v1/auth/oidc/callback", j.Details["path"], "the code and state in the query are not kept")
}
