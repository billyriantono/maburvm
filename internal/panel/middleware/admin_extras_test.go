package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// In maintenance a customer (and their API key) can read but not change;
// staff, and staff viewing as a customer, carry on; signing out always works.
func TestMaintenanceRefusesCustomerChangesOnly(t *testing.T) {
	t.Cleanup(ForgetMaintenance)
	customer := &UserContext{ID: uuid.New(), Role: models.RoleClient}
	staff := &UserContext{ID: uuid.New(), Role: models.RoleAdmin}
	viewing := &UserContext{ID: uuid.New(), Role: models.RoleClient, ImpersonatorID: uuid.NewString()}
	try := func(u *UserContext, method, path string) int {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(method, path, nil), rec)
		if refused, _ := refuseInMaintenance(c, nil, u); refused {
			return rec.Code
		}
		return http.StatusOK
	}

	RememberMaintenance(Maintenance{})
	assert.Equal(t, http.StatusOK, try(customer, http.MethodPost, "/api/v1/vms"), "off: nothing is refused")

	RememberMaintenance(Maintenance{Enabled: true, Message: "Back at 10:00"})
	cases := []struct {
		name         string
		u            *UserContext
		method, path string
		want         int
	}{
		{"customer order", customer, http.MethodPost, "/api/v1/vms", http.StatusServiceUnavailable},
		{"customer change", customer, http.MethodPut, "/api/v1/vms/x/tags", http.StatusServiceUnavailable},
		{"customer read", customer, http.MethodGet, "/api/v1/vms", http.StatusOK},
		{"customer signs out", customer, http.MethodPost, "/api/v1/auth/logout", http.StatusOK},
		{"staff change", staff, http.MethodPost, "/api/v1/vms", http.StatusOK},
		{"staff viewing as customer", viewing, http.MethodPost, "/api/v1/vms", http.StatusOK},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, try(c.u, c.method, c.path), c.name)
	}

	e := echo.New()
	// The MCP endpoint's POST only carries tool calls, each refused on its own.
	mcp := e.NewContext(httptest.NewRequest(http.MethodPost, "/api/v1/mcp", nil), httptest.NewRecorder())
	_ = AllowReadOnlyKey(func(echo.Context) error { return nil })(mcp)
	refused, _ := refuseInMaintenance(mcp, nil, customer)
	assert.False(t, refused, "an MCP read must work in maintenance")

	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodPost, "/api/v1/vms", nil), rec)
	_, _ = refuseInMaintenance(c, nil, customer)
	assert.Equal(t, "300", rec.Header().Get("Retry-After"))
	assert.Contains(t, rec.Body.String(), `"code":"maintenance"`)
	assert.Contains(t, rec.Body.String(), "Back at 10:00")
}

func TestRequestLog(t *testing.T) {
	requestLog.Lock()
	requestLog.buf, requestLog.next, requestLog.full = make([]RequestLogEntry, 3), 0, false
	requestLog.Unlock()
	t.Cleanup(func() {
		requestLog.Lock()
		requestLog.buf, requestLog.next, requestLog.full = make([]RequestLogEntry, RequestLogSize), 0, false
		requestLog.Unlock()
	})

	e := echo.New()
	e.Use(RequestLog())
	key := &UserContext{ID: uuid.New(), Email: "bot@example.com", APIKeyID: "key-1"}
	e.GET("/api/v1/vms", func(c echo.Context) error { c.Set(UserContextKey, key); return c.NoContent(http.StatusOK) })
	e.POST("/api/v1/vms", func(c echo.Context) error { return c.NoContent(http.StatusConflict) })
	e.GET("/health", func(c echo.Context) error { return c.NoContent(http.StatusOK) })
	do := func(method, path string) {
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))
	}
	do(http.MethodGet, "/health")             // not an API request
	do(http.MethodGet, "/api/v1/vms?token=x") // the query string is never kept
	do(http.MethodPost, "/api/v1/vms")
	do(http.MethodGet, "/api/v1/logs/requests") // the log page reading itself

	all, total := RecentRequests(RequestFilter{}, 10, 0)
	require.Equal(t, 2, total)
	assert.Equal(t, http.MethodPost, all[0].Method, "newest first")
	assert.Equal(t, "/api/v1/vms", all[1].Path)
	assert.Equal(t, "key-1", all[1].APIKeyID)
	assert.Equal(t, "bot@example.com", all[1].Email)
	assert.WithinDuration(t, time.Now(), all[0].Time, time.Minute)

	got, n := RecentRequests(RequestFilter{Status: "4xx"}, 10, 0)
	assert.Equal(t, 1, n)
	assert.Equal(t, http.StatusConflict, got[0].Status)
	_, n = RecentRequests(RequestFilter{Status: "409"}, 10, 0)
	assert.Equal(t, 1, n)
	_, n = RecentRequests(RequestFilter{Q: "BOT@"}, 10, 0)
	assert.Equal(t, 1, n)
	_, n = RecentRequests(RequestFilter{Method: "delete"}, 10, 0)
	assert.Equal(t, 0, n)

	// The ring keeps the newest only.
	for i := 0; i < 4; i++ {
		do(http.MethodPost, "/api/v1/vms")
	}
	page, n := RecentRequests(RequestFilter{}, 2, 1)
	assert.Equal(t, 3, n)
	assert.Len(t, page, 2)
}
