package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// A team member reaches the account's own resources as their role allows;
// anyone else, and a billing member, does not.
func TestActingAccount(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+uuid.NewString()+"?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, ddl := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, deleted_at DATETIME, suspended_at DATETIME)`,
		`CREATE TABLE account_members (id TEXT PRIMARY KEY, account_id TEXT, user_id TEXT, email TEXT, role TEXT, vm_ids TEXT, project_ids TEXT,
			invite_token_hash TEXT, invite_expires_at DATETIME, invited_by TEXT, accepted_at DATETIME, created_at DATETIME, updated_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}
	owner, viewer, tech, biller, limited := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO users (id, email) VALUES (?, 'o@example.com')`, owner).Error)
	for _, m := range []struct{ user, role, vms string }{
		{viewer, service.MemberRoleReadOnly, "[]"}, {tech, service.MemberRoleTechnical, "[]"},
		{biller, service.MemberRoleBilling, "[]"}, {limited, service.MemberRoleTechnical, `["` + uuid.NewString() + `"]`},
	} {
		require.NoError(t, db.Exec(`INSERT INTO account_members (id, account_id, user_id, email, role, vm_ids, project_ids, accepted_at)
			VALUES (?, ?, ?, ?, ?, ?, '[]', ?)`, uuid.NewString(), owner, m.user, m.user+"@example.com", m.role, m.vms, time.Now()).Error)
	}
	call := func(user string, role models.UserRole, method, account, what string) (string, bool, int) {
		target := "/"
		if account != "" {
			target += "?account_id=" + account
		}
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(httptest.NewRequest(method, target, nil), rec)
		c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(user), Role: role})
		_, got, staff, ok := actingAccountFor(c, db, what)
		if !ok {
			return "", false, rec.Code
		}
		return got, staff, http.StatusOK
	}
	for _, c := range []struct {
		name               string
		user, method, acct string
		what               string
		want               string
		code               int
	}{
		{"own account without asking", viewer, http.MethodPost, "", "", viewer, http.StatusOK},
		{"viewer reads the team account", viewer, http.MethodGet, owner, "", owner, http.StatusOK},
		{"viewer changes nothing there", viewer, http.MethodPost, owner, "", "", http.StatusForbidden},
		{"technical changes it", tech, http.MethodDelete, owner, "", owner, http.StatusOK},
		{"limited technical does not see it", limited, http.MethodGet, owner, "", "", http.StatusForbidden},
		{"billing does not see the resources", biller, http.MethodGet, owner, "", "", http.StatusForbidden},
		{"billing sees the usage", biller, http.MethodGet, owner, service.AccountCanBilling, owner, http.StatusOK},
		{"viewer does not see the usage", viewer, http.MethodGet, owner, service.AccountCanBilling, "", http.StatusForbidden},
		{"a stranger finds nothing", uuid.NewString(), http.MethodGet, owner, "", "", http.StatusNotFound},
	} {
		got, staff, code := call(c.user, models.RoleClient, c.method, c.acct, c.what)
		assert.Equal(t, c.code, code, c.name)
		assert.Equal(t, c.want, got, c.name)
		assert.False(t, staff, c.name)
	}
	staffID := uuid.NewString()
	got, staff, code := call(staffID, models.RoleAdmin, http.MethodGet, owner, "")
	assert.Equal(t, http.StatusOK, code)
	assert.True(t, staff, "staff act as themselves")
	assert.Equal(t, staffID, got)
}
