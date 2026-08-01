package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/shared/models"
)

// setupAuthTestDB creates an in-memory SQLite DB with just the users table,
// which is all RequireAuth needs to look up the authenticated user.
func setupAuthTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared&_busy_timeout=5000"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		email TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		role TEXT DEFAULT 'client',
quota_mode TEXT NOT NULL DEFAULT 'legacy',
				two_factor_secret TEXT,
		two_factor_backup_codes TEXT,
		ip_whitelist TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME
	)`).Error)

	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		token TEXT NOT NULL,
		expires_at DATETIME NOT NULL,
		ip_address TEXT,
		user_agent TEXT,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`).Error)

	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	})

	return db
}

// TestRequireAuth_IPWhitelist verifies that RequireAuth enforces a user's
// IPWhitelist using the request's real IP (via echo's c.RealIP()), and that
// users with an empty whitelist are unaffected.
func TestRequireAuth_IPWhitelist(t *testing.T) {
	db := setupAuthTestDB(t)

	allowedUser := &models.User{
		Email:        "allowed@example.com",
		PasswordHash: "x",
		Role:         models.RoleClient,
		IPWhitelist:  []string{"203.0.113.5"},
	}
	require.NoError(t, db.Create(allowedUser).Error)

	blockedUser := &models.User{
		Email:        "blocked@example.com",
		PasswordHash: "x",
		Role:         models.RoleClient,
		IPWhitelist:  []string{"203.0.113.5"},
	}
	require.NoError(t, db.Create(blockedUser).Error)

	openUser := &models.User{
		Email:        "open@example.com",
		PasswordHash: "x",
		Role:         models.RoleClient,
		IPWhitelist:  []string{},
	}
	require.NoError(t, db.Create(openUser).Error)

	tests := []struct {
		name       string
		user       *models.User
		remoteAddr string
		wantStatus int
	}{
		{
			name:       "IP in whitelist is allowed",
			user:       allowedUser,
			remoteAddr: "203.0.113.5:4321",
			wantStatus: http.StatusOK,
		},
		{
			name:       "IP not in whitelist is forbidden",
			user:       blockedUser,
			remoteAddr: "198.51.100.9:4321",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "empty whitelist allows any IP",
			user:       openUser,
			remoteAddr: "198.51.100.9:4321",
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens, err := GenerateTokenPair(tt.user, db)
			require.NoError(t, err)

			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			handler := RequireAuth(db)(func(c echo.Context) error {
				return c.NoContent(http.StatusOK)
			})

			_ = handler(c)

			require.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
