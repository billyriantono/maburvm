package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
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

// Routes the API reference leaves out, and why.
var openAPIUndocumented = []struct{ route, why string }{
	{"GET /ws/vnc", "console websocket, opened with the token from GET /api/v1/vms/{id}/vnc"},
	{"GET /ws/ssh", "SSH console websocket, opened with the token from POST /api/v1/vms/{id}/ssh/token"},
}

func openAPIServer(t *testing.T) (*staffFixture, *Server) {
	t.Helper()
	f := newStaffFixture(t)
	s := NewServer(f.db, f.s.cfg)
	s.SetupRoutes()
	return f, s
}

// Every route the panel serves is in internal/panel/apidoc/routes.go, and
// nothing listed there is gone.
func TestOpenAPICoversEveryRoute(t *testing.T) {
	_, s := openAPIServer(t)
	skip := map[string]bool{}
	for _, u := range openAPIUndocumented {
		skip[u.route] = true
	}
	documented := map[string]bool{}
	for _, r := range apidoc.Routes() {
		key := r.Method + " " + r.Path
		assert.False(t, documented[key], "%s is listed twice", key)
		documented[key] = true
	}
	served := map[string]bool{}
	var missing []string
	for _, r := range s.echo.Routes() {
		if r.Method == "echo_route_not_found" {
			continue
		}
		key := r.Method + " " + r.Path
		served[key] = true
		if !documented[key] && !skip[key] {
			missing = append(missing, key)
		}
	}
	var stale []string
	for key := range documented {
		if !served[key] {
			stale = append(stale, key)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	assert.Empty(t, missing, "add these to internal/panel/apidoc/routes.go (or, for an internal route, to openAPIUndocumented with the reason)")
	assert.Empty(t, stale, "these are documented but no longer served")
	assert.Greater(t, len(served), 300, "the route walk saw too few routes to mean anything")
}

// The document is served without credentials and is OpenAPI 3.1.
func TestOpenAPIServed(t *testing.T) {
	_, s := openAPIServer(t)
	rec := httptest.NewRecorder()
	s.echo.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var doc struct {
		OpenAPI string                    `json:"openapi"`
		Paths   map[string]map[string]any `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	assert.Equal(t, "3.1.0", doc.OpenAPI)
	assert.Contains(t, doc.Paths, "/api/v1/vms/{id}")
}

// Each documented route's access is what its middleware enforces: its
// permissions let a staff account through, each of them is needed, an account
// without them is refused, and customers get in exactly where it says so.
func TestOpenAPIAccessMatchesMiddleware(t *testing.T) {
	f, s := openAPIServer(t)
	seq := 0
	// A fresh account per request keeps rate limits and sign-outs from one
	// probe out of the next.
	account := func(role models.UserRole, staffRole string) *models.User {
		seq++
		u := &models.User{ID: uuid.New(), Email: "probe" + itoa(seq) + "@example.com", Role: role}
		var sr any
		if staffRole != "" {
			sr = staffRole
		}
		later := time.Now().Add(time.Hour)
		require.NoError(t, f.db.Exec(`INSERT INTO users (id, email, password_hash, role, staff_role, created_at, updated_at) VALUES (?, ?, 'x', ?, ?, ?, ?)`,
			u.ID.String(), u.Email, string(role), sr, later, later).Error)
		return u
	}
	roles := map[string]bool{}
	// staffWith is an account holding exactly perms (plus the admin area), or
	// an Administrator when one of them is outside the custom role catalog.
	staffWith := func(perms []string) *models.User {
		for _, p := range perms {
			if !middleware.InCatalog(p) {
				return account(models.RoleAdmin, middleware.StaffRoleAdministrator)
			}
		}
		sort.Strings(perms)
		id := "probe:" + strings.Join(perms, ",")
		if !roles[id] {
			roles[id] = true
			b, _ := json.Marshal(append([]string{}, perms...))
			require.NoError(t, f.db.Exec(`INSERT INTO staff_roles (id, name, permissions) VALUES (?, ?, ?)`, id, id, string(b)).Error)
		}
		return account(models.RoleAdmin, id)
	}
	nobody := func() *models.User { return staffWith([]string{}) }
	// A customer probes with a VM of their own, and that VM's interface, so a
	// refusal comes from the route and not from someone else's VM being hidden.
	require.NoError(t, f.db.Exec(`CREATE TABLE networks (id TEXT PRIMARY KEY, vm_id TEXT, deleted_at DATETIME)`).Error)
	customer := func() (*models.User, string) {
		u := account(models.RoleClient, "")
		vm := uuid.New().String()
		require.NoError(t, f.db.Exec(`INSERT INTO vms (id, user_id, node_id, hostname, status) VALUES (?, ?, ?, 'probe', 'stopped')`,
			vm, u.ID.String(), uuid.New().String()).Error)
		require.NoError(t, f.db.Exec(`INSERT INTO networks (id, vm_id) VALUES (?, ?)`, vm, vm).Error)
		return u, vm
	}

	call := func(r apidoc.Route, u *models.User, vmID string) (int, string) {
		path := r.Path
		for _, seg := range strings.Split(path, "/") {
			if strings.HasPrefix(seg, ":") {
				v := "00000000-0000-0000-0000-000000000001"
				if (seg == ":id" || seg == ":network_id") && vmID != "" && strings.HasPrefix(r.Path, "/api/v1/vms/:id") {
					v = vmID // the probe VM's interface shares its id
				}
				path = strings.Replace(path, seg, v, 1)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		req := httptest.NewRequest(r.Method, path, strings.NewReader(`{}`)).WithContext(ctx)
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		if u != nil {
			if r.BrowserOnly {
				req.AddCookie(&http.Cookie{Name: middleware.AccessTokenCookieName, Value: mintToken(t, u)})
			} else {
				req.Header.Set(echo.HeaderAuthorization, "Bearer "+mintToken(t, u))
			}
		}
		rec := httptest.NewRecorder()
		s.echo.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	refused := func(code int) bool { return code == http.StatusUnauthorized || code == http.StatusForbidden }

	checked := 0
	for _, r := range apidoc.Routes() {
		name := r.Method + " " + r.Path
		if r.Path == "/api/v1/events/vm-status" { // a stream that never ends; gated like the VM list
			continue
		}
		checked++
		code, body := call(r, nil, "")
		switch r.Access {
		case "public":
			assert.NotContains(t, body, "Access token required", "%s is documented as public", name)
			continue
		case "token", "api-key", "webhook", "metrics":
			assert.GreaterOrEqual(t, code, 400, "%s answered %d without its credential", name, code)
			continue
		}
		assert.True(t, refused(code), "%s answered %d without credentials: %s", name, code, body)

		cust, vm := customer()
		code, body = call(r, cust, vm)
		if r.Customer || r.Access == "signed-in" {
			assert.False(t, refused(code), "%s is documented for customers but refused one: %d %s", name, code, body)
		} else {
			assert.True(t, refused(code), "%s is documented as staff only but a customer got %d: %s", name, code, body)
		}

		code, body = call(r, nobody(), "")
		switch r.Access {
		case "signed-in", "staff":
			assert.False(t, refused(code), "%s refused a staff account with no permissions: %d %s", name, code, body)
			continue
		case "owner":
			code, body = call(r, staffWith([]string{middleware.PermAll}), "")
			assert.True(t, refused(code), "%s is documented as the Owner's but an Administrator got %d: %s", name, code, body)
			code, body = call(r, f.owner, "")
			assert.False(t, refused(code), "%s refused the Owner: %d %s", name, code, body)
			continue
		}
		assert.True(t, refused(code), "%s let in a staff account with no permissions: %d %s", name, code, body)

		var terms [][]string
		for _, term := range strings.Fields(r.Access) {
			terms = append(terms, strings.Split(term, "|"))
		}
		firsts := func(swap int, alt string, drop int) []string {
			var out []string
			for i, alts := range terms {
				switch {
				case i == drop:
				case i == swap:
					out = append(out, alt)
				default:
					out = append(out, alts[0])
				}
			}
			return out
		}
		for i, alts := range terms {
			for _, alt := range alts {
				code, body = call(r, staffWith(firsts(i, alt, -1)), "")
				assert.False(t, refused(code), "%s refused staff holding %v: %d %s", name, firsts(i, alt, -1), code, body)
			}
			if len(terms) > 1 || middleware.InCatalog(alts[0]) {
				without := firsts(-1, "", i)
				code, body = call(r, staffWith(without), "")
				assert.True(t, refused(code), "%s let in staff without %v (holding %v): %d %s", name, alts, without, code, body)
			}
		}
	}
	assert.Greater(t, checked, 300)
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
