//go:build pg

package server

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// 077 applies on a database built from every earlier migration, again on top
// of itself, down and up; staff that existed before it keep full access, and
// the session, sign-in and role tables work through the models.
func TestStaffRolesSessionsMigrationOnPostgres(t *testing.T) {
	db, raw, _ := pgMigratedDB(t)
	ctx := context.Background()
	up, down := migrationSQL(t, "077_staff_roles_sessions.up.sql"), migrationSQL(t, "077_staff_roles_sessions.down.sql")

	// Accounts from before roles: two administrators and a customer.
	owner, admin, customer := uuid.New(), uuid.New(), uuid.New()
	for i, u := range []struct {
		id   uuid.UUID
		role string
	}{{owner, "admin"}, {admin, "admin"}, {customer, "client"}} {
		_, err := raw.Exec(`INSERT INTO users (id, email, password_hash, role, created_at) VALUES ($1, $2, 'x', $3, $4)`,
			u.id, u.id.String()+"@example.com", u.role, time.Now().Add(time.Duration(i)*time.Minute))
		require.NoError(t, err)
	}
	_, err := raw.Exec(up)
	require.NoError(t, err, "077 run twice")
	_, err = raw.Exec(down)
	require.NoError(t, err, "077 down")
	_, err = raw.Exec(up)
	require.NoError(t, err, "077 up again")

	load := func(id uuid.UUID) *models.User {
		var u models.User
		require.NoError(t, db.First(&u, "id = ?", id).Error)
		return &u
	}
	role, perms := middleware.ResolveStaffRole(ctx, db, load(owner))
	assert.Equal(t, middleware.StaffRoleOwner, role.ID)
	assert.True(t, middleware.Can(perms, middleware.PermStaffManage))
	role, perms = middleware.ResolveStaffRole(ctx, db, load(admin))
	assert.Equal(t, middleware.StaffRoleAdministrator, role.ID, "an administrator from before roles keeps everything")
	assert.True(t, middleware.Can(perms, "settings:update"))

	// A custom role in jsonb, names unique whatever the case.
	require.NoError(t, db.Create(&models.CustomStaffRole{ID: uuid.NewString(), Name: "Night shift", Permissions: []string{"vm:read", "vm:power"}}).Error)
	assert.Error(t, db.Create(&models.CustomStaffRole{ID: uuid.NewString(), Name: "night SHIFT"}).Error)
	var row models.CustomStaffRole
	require.NoError(t, db.Where("name = ?", "Night shift").First(&row).Error)
	require.NoError(t, db.Exec(`UPDATE users SET staff_role = ? WHERE id = ?`, row.ID, admin).Error)
	role, perms = middleware.ResolveStaffRole(ctx, db, load(admin))
	assert.Equal(t, "Night shift", role.Name)
	assert.True(t, middleware.Can(perms, "vm:power"))
	assert.False(t, middleware.Can(perms, "vm:delete"))

	// Sessions and sign-in history.
	cust := load(customer)
	_, err = middleware.IssueSession(db, cust, middleware.SessionMeta{Method: middleware.SignInPassword, IP: "203.0.113.9", UserAgent: "test"}, time.Hour)
	require.NoError(t, err)
	_, err = middleware.IssueSession(db, cust, middleware.SessionMeta{Method: middleware.SignInImpersonation, IP: "203.0.113.10", ImpersonatorID: &owner}, time.Hour)
	require.NoError(t, err)
	list, err := service.ActiveSessions(ctx, db, customer, "")
	require.NoError(t, err)
	require.Len(t, list, 2)
	emails := map[string]string{}
	for _, s := range list {
		emails[s.Method] = s.ImpersonatorEmail
	}
	assert.Equal(t, owner.String()+"@example.com", emails[middleware.SignInImpersonation])

	service.RecordSignIn(ctx, db, models.SignInEvent{Email: cust.Email, Method: middleware.SignInPassword, Note: "wrong_email_or_password", IPAddress: "203.0.113.9"})
	service.RecordSignIn(ctx, db, models.SignInEvent{Email: "nobody@example.com", Method: middleware.SignInPasswordReset, Success: true, IPAddress: "198.51.100.1"})
	history, err := service.SignInHistory(ctx, db, customer, 10)
	require.NoError(t, err)
	require.Len(t, history, 1, "attributed by email")
	since := time.Now().Add(-time.Minute)
	assert.Equal(t, int64(1), service.RecentFailures(ctx, db, "203.0.113.9", "", since))
	assert.Equal(t, int64(1), service.RecentFailures(ctx, db, "192.0.2.200", cust.Email, since))
	assert.Equal(t, int64(1), service.RecentFailures(ctx, db, "198.51.100.1", "", since), "a reset request counts")

	// Signing the staff member out everywhere ends their view as the customer.
	require.NoError(t, service.RevokeAllSessions(ctx, db, owner))
	list, err = service.ActiveSessions(ctx, db, customer, "")
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, middleware.SignInPassword, list[0].Method)

	require.NoError(t, service.RevokeAllSessions(ctx, db, customer))
	list, err = service.ActiveSessions(ctx, db, customer, "")
	require.NoError(t, err)
	assert.Empty(t, list)
}
