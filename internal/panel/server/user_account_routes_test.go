package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// accountServer is the user routes with a working user service (it needs the
// AES key) and an audit table, so the tests can read what was recorded.
func accountServer(t *testing.T, users ...*models.User) (*Server, *gorm.DB) {
	t.Helper()
	db := setupUserContainmentDB(t, users...)
	require.NoError(t, db.Exec(`CREATE TABLE audit_logs (id TEXT PRIMARY KEY, user_id TEXT, action TEXT NOT NULL, resource_type TEXT, resource_id TEXT,
		ip_address TEXT, user_agent TEXT, details TEXT, before_snapshot TEXT, after_snapshot TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT)`).Error)
	s := NewServer(db, &config.Config{JWT: config.JWTConfig{AESKey: "0123456789abcdef0123456789abcdef", SecretKey: testJWTSecret}})
	s.setupUserRoutes(s.echo.Group("/api/v1"))
	return s, db
}

func auditCount(t *testing.T, db *gorm.DB, action string, actor, target *models.User) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Table("audit_logs").Where("action = ? AND user_id = ? AND resource_id = ?", action, actor.ID.String(), target.ID.String()).Count(&n).Error)
	return n
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var out struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &out), string(body))
	return out.Error
}

// ADM-43: any administrator deletes a customer; deleting staff stays with the
// founding administrator.
func TestUserRoutes_PeerAdminDeletesCustomersNotStaff(t *testing.T) {
	founding, peer, other := adminUser("founding@example.com"), adminUser("peer@example.com"), adminUser("other@example.com")
	client := clientUser("client@example.com")
	s, db := accountServer(t, founding, peer, other, client)
	tok := mintToken(t, peer)

	rec := doUserReq(t, s, http.MethodDelete, "/api/v1/users/"+client.ID.String(), tok, "")
	assert.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, int64(0), countUsersByRole(t, db, models.RoleClient))
	assert.Equal(t, int64(1), auditCount(t, db, "user.delete", peer, client))

	for _, staff := range []*models.User{other, founding} {
		rec = doUserReq(t, s, http.MethodDelete, "/api/v1/users/"+staff.ID.String(), tok, "")
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Equal(t, "only_the_founding_administrator_may_delete_admins", errorCode(t, rec.Body.Bytes()))
	}
	assert.Equal(t, int64(3), countUsersByRole(t, db, models.RoleAdmin))

	rec = doUserReq(t, s, http.MethodDelete, "/api/v1/users/"+client.ID.String(), tok, "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "already gone")
}

// ADM-18: an account that owns VMs is deleted only with a choice for them;
// without one nothing changes and the VMs are listed.
func TestUserRoutes_DeleteRefusedWhileTheAccountOwnsVMs(t *testing.T) {
	founding, client := adminUser("founding@example.com"), clientUser("client@example.com")
	s, db := accountServer(t, founding, client)
	tok := mintToken(t, founding)
	require.NoError(t, db.Exec(`INSERT INTO vms (id, user_id, node_id, hostname, status) VALUES ('vm-1', ?, 'n1', 'web-1', 'running')`, client.ID.String()).Error)
	path := "/api/v1/users/" + client.ID.String()

	rec := doUserReq(t, s, http.MethodDelete, path, tok, "")
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var out struct {
		Data struct {
			VMs []service.OwnedVM `json:"vms"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	require.Len(t, out.Data.VMs, 1)
	assert.Equal(t, "web-1", out.Data.VMs[0].Hostname)
	assert.Equal(t, int64(1), countUsersByRole(t, db, models.RoleClient), "nothing deleted")
	assert.Equal(t, int64(0), auditCount(t, db, "user.delete", founding, client))

	for _, q := range []string{"?vms=keep", "?vms=reassign", "?vms=reassign&to=" + client.ID.String()} {
		rec = doUserReq(t, s, http.MethodDelete, path+q, tok, "")
		assert.Equal(t, http.StatusBadRequest, rec.Code, q+": "+rec.Body.String())
	}
	assert.Equal(t, int64(1), countUsersByRole(t, db, models.RoleClient), "a bad choice deletes nothing")
}

// SEC-14: another administrator's email, allow-list, name, password, 2FA and
// lock are the founding administrator's to change, the founder's own included.
func TestUserRoutes_OnlyFoundingAdminChangesStaffAccounts(t *testing.T) {
	founding, peer, other := adminUser("founding@example.com"), adminUser("peer@example.com"), adminUser("other@example.com")
	s, db := accountServer(t, founding, peer, other)
	peerTok, foundingTok := mintToken(t, peer), mintToken(t, founding)
	const refused = "only_the_founding_administrator_may_change_admin_accounts"

	for _, target := range []*models.User{founding, other} {
		path := "/api/v1/users/" + target.ID.String()
		for _, body := range []string{`{"email":"taken-over@example.com"}`, `{"ip_whitelist":["203.0.113.9"]}`, `{"name":"Mallory"}`} {
			rec := doUserReq(t, s, http.MethodPut, path, peerTok, body)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", body, rec.Body.String())
			assert.Equal(t, refused, errorCode(t, rec.Body.Bytes()))
		}
		for _, call := range []string{"/reset-password", "/reset-2fa", "/unlock"} {
			rec := doUserReq(t, s, http.MethodPost, path+call, peerTok, `{"method":"temporary"}`)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", call, rec.Body.String())
			assert.Equal(t, refused, errorCode(t, rec.Body.Bytes()))
		}
		var row models.User
		require.NoError(t, db.First(&row, "id = ?", target.ID).Error)
		assert.Equal(t, target.Email, row.Email)
		assert.Empty(t, row.IPWhitelist)
		assert.Equal(t, "hashed", row.PasswordHash)
	}

	// The founding administrator may, and it is recorded.
	rec := doUserReq(t, s, http.MethodPut, "/api/v1/users/"+other.ID.String(), foundingTok, `{"email":"Other.New@Example.com","ip_whitelist":["203.0.113.9"]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var row models.User
	require.NoError(t, db.First(&row, "id = ?", other.ID).Error)
	assert.Equal(t, "other.new@example.com", row.Email, "stored lower-case")
	assert.Equal(t, []string{"203.0.113.9"}, row.IPWhitelist)
	assert.Equal(t, int64(1), auditCount(t, db, "user.update", founding, other))

	// An administrator keeps their own allow-list and name, but their own email
	// moves under Profile, where the current password is asked for.
	rec = doUserReq(t, s, http.MethodPut, "/api/v1/users/"+peer.ID.String(), peerTok, `{"name":"Peer","ip_whitelist":[]}`)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, tc := range []struct {
		who *models.User
		tok string
	}{{peer, peerTok}, {founding, foundingTok}} {
		rec = doUserReq(t, s, http.MethodPut, "/api/v1/users/"+tc.who.ID.String(), tc.tok, `{"email":"me-moved@example.com"}`)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Equal(t, "change_own_email_under_profile", errorCode(t, rec.Body.Bytes()))
		for _, call := range []string{"/reset-password", "/reset-2fa", "/unlock"} {
			rec = doUserReq(t, s, http.MethodPost, "/api/v1/users/"+tc.who.ID.String()+call, tc.tok, `{"method":"temporary"}`)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s: %s", call, rec.Body.String())
			assert.Equal(t, "own_account", errorCode(t, rec.Body.Bytes()))
		}
	}
}

// ADM-06: name and email of a customer, with the email checked, recorded, and
// the old address told.
func TestUserRoutes_AdminEditsCustomerNameAndEmail(t *testing.T) {
	founding, peer := adminUser("founding@example.com"), adminUser("peer@example.com")
	client, taken := clientUser("client@example.com"), clientUser("taken@example.com")
	s, db := accountServer(t, founding, peer, client, taken)
	tok := mintToken(t, peer)
	path := "/api/v1/users/" + client.ID.String()

	var told [][2]string
	orig := service.NotifyEmailChanged
	service.NotifyEmailChanged = func(_ *gorm.DB, oldEmail, newEmail, _ string) { told = append(told, [2]string{oldEmail, newEmail}) }
	t.Cleanup(func() { service.NotifyEmailChanged = orig })

	rec := doUserReq(t, s, http.MethodPut, path, tok, `{"name":"  Ada Lovelace "}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var row models.User
	require.NoError(t, db.First(&row, "id = ?", client.ID).Error)
	assert.Equal(t, "Ada Lovelace", row.Name)
	assert.Empty(t, told, "a name change tells nobody")

	// In order: the email changes once, at ada@.
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"email":"not-an-address"}`, http.StatusBadRequest},
		{`{"email":"Ada <ada@example.com>"}`, http.StatusBadRequest},
		{`{"email":"TAKEN@example.com"}`, http.StatusConflict},
		{`{"name":"` + longName() + `"}`, http.StatusBadRequest},
		{`{"email":"client@example.com"}`, http.StatusOK}, // unchanged: nothing to do
		{`{"email":" CLIENT@example.com "}`, http.StatusOK},
		{`{"email":"ada@example.com"}`, http.StatusOK},
		{`{"email":"","name":"Ada Lovelace"}`, http.StatusOK},
		{`{"email":"ada@example.com","x":"y"}`, http.StatusOK},
	} {
		rec = doUserReq(t, s, http.MethodPut, path, tok, tc.body)
		assert.Equal(t, tc.want, rec.Code, "%s: %s", tc.body, rec.Body.String())
	}
	require.NoError(t, db.First(&row, "id = ?", client.ID).Error)
	assert.Equal(t, "ada@example.com", row.Email)
	assert.Equal(t, models.RoleClient, row.Role)
	assert.Equal(t, [][2]string{{"client@example.com", "ada@example.com"}}, told, "the old address is told once, of the one real change")
	assert.Equal(t, int64(2), auditCount(t, db, "user.update", peer, client), "the name change and the email change")
}

func longName() string {
	b := make([]byte, 256)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

// ADM-06: a temporary password shown once, two-factor taken off, a lock lifted.
func TestUserRoutes_AdminRecoversACustomerAccount(t *testing.T) {
	founding, peer, client := adminUser("founding@example.com"), adminUser("peer@example.com"), clientUser("client@example.com")
	s, db := accountServer(t, founding, peer, client)
	tok := mintToken(t, peer)
	path := "/api/v1/users/" + client.ID.String()
	row := func() models.User {
		var u models.User
		require.NoError(t, db.First(&u, "id = ?", client.ID).Error)
		return u
	}

	// A customer cannot reach any of it.
	for _, call := range []string{"/reset-password", "/reset-2fa", "/unlock"} {
		rec := doUserReq(t, s, http.MethodPost, path+call, mintToken(t, client), `{"method":"temporary"}`)
		assert.Equal(t, http.StatusForbidden, rec.Code, call)
	}

	rec := doUserReq(t, s, http.MethodPost, path+"/reset-password", tok, `{"method":"shout"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// No mail server: the link is not sent, and the answer says so.
	t.Setenv("PANEL_PUBLIC_URL", "https://panel.example.com")
	rec = doUserReq(t, s, http.MethodPost, path+"/reset-password", tok, `{"method":"email"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "email_unavailable", errorCode(t, rec.Body.Bytes()))
	assert.Equal(t, "hashed", row().PasswordHash)

	require.NoError(t, db.Exec("UPDATE users SET locked_until = ?, failed_login_count = 4 WHERE id = ?", time.Now().Add(time.Hour), client.ID.String()).Error)
	rec = doUserReq(t, s, http.MethodPost, path+"/reset-password", tok, `{"method":"temporary"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var reset struct {
		Success bool `json:"success"`
		Data    struct {
			TemporaryPassword string `json:"temporary_password"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reset))
	require.True(t, reset.Success)
	require.GreaterOrEqual(t, len(reset.Data.TemporaryPassword), 16)
	after := row()
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(after.PasswordHash), []byte(reset.Data.TemporaryPassword)))
	assert.NotNil(t, after.TokenRevokedAt, "their open sessions end")
	assert.False(t, after.SignInLocked(), "and a lock is lifted with it")
	assert.Equal(t, int64(1), auditCount(t, db, "user.password_reset", peer, client))
	var details string
	require.NoError(t, db.Raw("SELECT details FROM audit_logs WHERE action = 'user.password_reset'").Scan(&details).Error)
	assert.NotContains(t, details, reset.Data.TemporaryPassword, "the password itself is never recorded")

	// Two-factor: nothing to reset, then reset.
	rec = doUserReq(t, s, http.MethodPost, path+"/reset-2fa", tok, "")
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	require.NoError(t, db.Exec("UPDATE users SET two_factor_secret = 'enc', two_factor_backup_codes = 'enc', two_factor_enabled = 1 WHERE id = ?", client.ID.String()).Error)
	rec = doUserReq(t, s, http.MethodGet, path, tok, "")
	assert.Contains(t, rec.Body.String(), `"two_factor_enabled":true`, "ADM-19: the user page is told")
	rec = doUserReq(t, s, http.MethodPost, path+"/reset-2fa", tok, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	after = row()
	assert.False(t, after.TwoFactorEnabled)
	assert.Empty(t, after.TwoFactorSecret)
	assert.Empty(t, after.TwoFactorBackupCodes)
	assert.Equal(t, int64(1), auditCount(t, db, "user.two_factor_reset", peer, client))

	// Unlock.
	require.NoError(t, db.Exec("UPDATE users SET locked_until = ?, failed_login_count = 2 WHERE id = ?", time.Now().Add(time.Hour), client.ID.String()).Error)
	rec = doUserReq(t, s, http.MethodGet, path, tok, "")
	assert.Contains(t, rec.Body.String(), `"locked_until"`)
	rec = doUserReq(t, s, http.MethodPost, path+"/unlock", tok, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	after = row()
	assert.Nil(t, after.LockedUntil)
	assert.Equal(t, 0, after.FailedLoginCount)
	assert.Equal(t, int64(1), auditCount(t, db, "user.unlock", peer, client))
}
