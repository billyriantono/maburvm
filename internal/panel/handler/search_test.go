package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

func TestSearchScopesToTheCaller(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:search?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, ddl := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT, name TEXT, role TEXT, deleted_at DATETIME)`,
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT, ip_address TEXT, status TEXT, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, user_id TEXT, hostname TEXT, status TEXT, deleted_at DATETIME)`,
		`CREATE TABLE networks (id TEXT PRIMARY KEY, vm_id TEXT, ip_address TEXT, deleted_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}
	alice, bob := uuid.New(), uuid.New()
	require.NoError(t, db.Exec(`INSERT INTO users (id,email,name,role) VALUES (?,?,?,?),(?,?,?,?)`,
		alice.String(), "alice@example.com", "Alice", "client", bob.String(), "bob@example.com", "Bob", "client").Error)
	require.NoError(t, db.Exec(`INSERT INTO nodes (id,name,ip_address,status) VALUES ('n1','web-node','192.0.2.1','active')`).Error)
	require.NoError(t, db.Exec(`INSERT INTO vms (id,user_id,hostname,status) VALUES ('v1',?,'web-alice','running'),('v2',?,'web-bob','stopped'),('v3',?,'100%_done','stopped')`,
		alice.String(), bob.String(), bob.String()).Error)
	require.NoError(t, db.Exec(`INSERT INTO networks (id,vm_id,ip_address) VALUES ('a','v1','203.0.113.10'),('b','v2','203.0.113.20')`).Error)

	search := func(q string, id uuid.UUID, role models.UserRole) map[string][]map[string]any {
		e := echo.New()
		rec := httptest.NewRecorder()
		c := e.NewContext(httptest.NewRequest(http.MethodGet, "/?q="+url.QueryEscape(q), nil), rec)
		c.Set(middleware.UserContextKey, &middleware.UserContext{ID: id, Role: role})
		require.NoError(t, searchHandler(db)(c))
		var body struct {
			Data map[string][]map[string]any `json:"data"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		return body.Data
	}

	admin := search("web", uuid.New(), models.RoleAdmin)
	require.Len(t, admin["vms"], 2)
	require.Len(t, admin["nodes"], 1)
	require.Equal(t, "alice@example.com", admin["vms"][0]["owner_email"])

	// A customer sees only their own VM, and no users or nodes at all.
	mine := search("web", alice, models.RoleClient)
	require.Len(t, mine["vms"], 1)
	require.Equal(t, "web-alice", mine["vms"][0]["hostname"])
	require.Empty(t, mine["nodes"])
	require.Empty(t, mine["users"])
	require.Nil(t, mine["vms"][0]["owner_email"])
	require.Empty(t, search("bob", alice, models.RoleClient)["vms"])

	// By address, and with LIKE wildcards taken as the characters they are.
	require.Equal(t, "web-bob", search("113.20", uuid.New(), models.RoleAdmin)["vms"][0]["hostname"])
	require.Len(t, search("%", uuid.New(), models.RoleAdmin)["vms"], 0) // too short
	require.Len(t, search("0%_", uuid.New(), models.RoleAdmin)["vms"], 1)
	require.Len(t, search("__", uuid.New(), models.RoleAdmin)["vms"], 0)
	require.Len(t, search("alice@", uuid.New(), models.RoleAdmin)["users"], 1)
}
