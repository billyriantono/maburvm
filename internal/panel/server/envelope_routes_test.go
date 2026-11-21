package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/apidoc"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

// Every error any documented route answers, to anyone, is the one envelope:
// success false, error.code and error.message, and message repeated at the
// top for the clients that read it there. Paginated lists carry meta and take
// per_page.
func TestEveryRouteAnswersInTheEnvelope(t *testing.T) {
	f, s := openAPIServer(t)
	seq := 0
	// A fresh Administrator per request keeps one probe's rate limit or
	// sign-out out of the next.
	administrator := func() string {
		seq++
		u := &models.User{ID: uuid.New(), Email: "envelope" + itoa(seq) + "@example.com", Role: models.RoleAdmin}
		later := time.Now().Add(time.Hour)
		require.NoError(t, f.db.Exec(`INSERT INTO users (id, email, password_hash, role, staff_role, created_at, updated_at) VALUES (?, ?, 'x', ?, ?, ?, ?)`,
			u.ID.String(), u.Email, string(u.Role), middleware.StaffRoleAdministrator, later, later).Error)
		return mintToken(t, u)
	}
	call := func(r apidoc.Route, path, token string) *httptest.ResponseRecorder {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req := httptest.NewRequest(r.Method, path, strings.NewReader(`{}`)).WithContext(ctx)
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if token != "" {
			if r.BrowserOnly {
				req.AddCookie(&http.Cookie{Name: middleware.AccessTokenCookieName, Value: token})
			} else {
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+token)
			}
		}
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		return rec
	}

	errors := 0
	for _, r := range apidoc.Routes() {
		if r.Path == "/api/v1/events/vm-status" { // a stream that never ends
			continue
		}
		path := r.Path
		for _, seg := range strings.Split(path, "/") {
			if strings.HasPrefix(seg, ":") {
				path = strings.Replace(path, seg, "00000000-0000-0000-0000-000000000001", 1)
			}
		}
		for _, token := range []string{"", administrator()} {
			rec := call(r, path, token)
			if rec.Code < 400 || !strings.HasPrefix(rec.Header().Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
				continue
			}
			name := r.Method + " " + r.Path
			var body map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), "%s: %s", name, rec.Body.String())
			if _, rpc := body["jsonrpc"]; rpc {
				continue // MCP answers in JSON-RPC's own shape
			}
			errors++
			assert.Equal(t, false, body["success"], "%s %d: %s", name, rec.Code, rec.Body.String())
			obj, ok := body["error"].(map[string]any)
			if !assert.True(t, ok, "%s %d: error is not an object: %s", name, rec.Code, rec.Body.String()) {
				continue
			}
			code, _ := obj["code"].(string)
			message, _ := obj["message"].(string)
			assert.NotEmpty(t, code, "%s %d: no error.code", name, rec.Code)
			assert.NotEmpty(t, message, "%s %d: no error.message", name, rec.Code)
			assert.Equal(t, message, body["message"], "%s %d: the top-level message is error.message", name, rec.Code)
		}
	}
	assert.Greater(t, errors, 300, "too few error answers were seen to mean anything")

	rec := call(apidoc.Route{Method: http.MethodGet}, "/api/v1/users?per_page=2", administrator())
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var list struct {
		Success  bool              `json:"success"`
		Data     []json.RawMessage `json:"data"`
		Meta     map[string]int64  `json:"meta"`
		PageSize int               `json:"page_size"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.True(t, list.Success)
	assert.Len(t, list.Data, 2)
	assert.Equal(t, int64(1), list.Meta["page"])
	assert.Equal(t, int64(2), list.Meta["per_page"])
	assert.Greater(t, list.Meta["total"], int64(2))
	assert.Equal(t, 2, list.PageSize, "the older page_size field stays")
}
