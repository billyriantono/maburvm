package authz

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

// A team member reaches the account's VM through AuthorizeVM: reads with any
// role, changes only with one that operates, and the console only that way too.
func TestAuthorizeVMTeamMember(t *testing.T) {
	db := testDB(t)
	for _, ddl := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, deleted_at DATETIME, suspended_at DATETIME)`,
		`CREATE TABLE account_members (id TEXT PRIMARY KEY, account_id TEXT, user_id TEXT, email TEXT, role TEXT, vm_ids TEXT, project_ids TEXT,
			invite_token_hash TEXT, invite_expires_at DATETIME, invited_by TEXT, accepted_at DATETIME, created_at DATETIME, updated_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}
	owner, viewer, tech := uuid.NewString(), uuid.NewString(), uuid.NewString()
	vmID := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO users (id, email) VALUES (?, 'o@example.com')`, owner).Error)
	require.NoError(t, db.Exec(`INSERT INTO vms (id, user_id, node_id, status) VALUES (?, ?, ?, 'running')`, vmID, owner, uuid.NewString()).Error)
	for _, m := range []struct{ user, role string }{{viewer, "readonly"}, {tech, "technical"}} {
		require.NoError(t, db.Exec(`INSERT INTO account_members (id, account_id, user_id, email, role, vm_ids, project_ids, accepted_at)
			VALUES (?, ?, ?, ?, ?, '[]', '[]', ?)`, uuid.NewString(), owner, m.user, m.user+"@example.com", m.role, time.Now()).Error)
	}
	a := NewAuthorizer(db)
	call := func(user, method string, operate bool) int {
		req := httptest.NewRequest(method, "/", nil)
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(user), Role: models.RoleClient})
		var ok bool
		if operate {
			ok = a.AuthorizeVMOperate(c, vmID)
		} else {
			ok = a.AuthorizeVM(c, vmID)
		}
		if ok {
			return http.StatusOK
		}
		return rec.Code
	}
	for _, c := range []struct {
		name    string
		user    string
		method  string
		operate bool
		want    int
	}{
		{"viewer reads", viewer, http.MethodGet, false, http.StatusOK},
		{"viewer changes", viewer, http.MethodPost, false, http.StatusForbidden},
		{"viewer opens the console", viewer, http.MethodGet, true, http.StatusForbidden},
		{"technical changes", tech, http.MethodPost, false, http.StatusOK},
		{"technical opens the console", tech, http.MethodGet, true, http.StatusOK},
		{"a stranger gets not found", uuid.NewString(), http.MethodGet, false, http.StatusNotFound},
	} {
		assert.Equal(t, c.want, call(c.user, c.method, c.operate), c.name)
	}
}
