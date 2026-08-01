package authz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

// testDB builds an isolated in-memory SQLite DB (unique DSN per call so tests
// don't share schema/state) with the vms/networks/port_forwards/firewall_rules
// schema the Authorizer reads. Plain TEXT columns are used because SQLite cannot
// parse the Postgres-specific vm_status/user_role/cidr/inet dialect types.
func testDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + uuid.New().String() + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	ddl := []string{
		`CREATE TABLE vms (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, node_id TEXT NOT NULL, hostname TEXT,
			os_template_id TEXT, resources TEXT, status TEXT DEFAULT 'stopped',
			source_migration TEXT, vnc_port INTEGER, vnc_password TEXT,
			console_enabled BOOLEAN DEFAULT 1, rescue_mode BOOLEAN DEFAULT 0,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE networks (
			id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, ip_address TEXT, bandwidth_limit INTEGER,
			bandwidth_quota_gb INTEGER, over_quota_policy TEXT, throttle_speed_mbps INTEGER,
			throttled BOOLEAN, vlan_id INTEGER, anti_spoofing BOOLEAN, created_at DATETIME,
			updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE firewall_rules (
			id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, protocol TEXT, port_range TEXT, action TEXT,
			direction TEXT, source_ip TEXT, priority INTEGER, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME)`,
		`CREATE TABLE port_forwards (
			id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, network_id TEXT, external_port INTEGER,
			internal_port INTEGER, protocol TEXT, source_ip TEXT, created_at DATETIME,
			updated_at DATETIME, deleted_at DATETIME)`,
	}
	for _, s := range ddl {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

func ctxWith(userID string, role models.UserRole) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(userID), Role: role})
	return c
}

func ctxWithoutUser() echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}

func TestAuthorizer_AuthorizeVM(t *testing.T) {
	db := testDB(t)
	ownerID := uuid.New().String()
	otherID := uuid.New().String()
	vmID := uuid.New().String()
	require.NoError(t, db.Create(&models.VM{ID: vmID, UserID: ownerID, NodeID: uuid.New().String()}).Error)

	a := NewAuthorizer(db)

	// Owner is authorized.
	assert.True(t, a.AuthorizeVM(ctxWith(ownerID, models.RoleClient), vmID))

	// Non-owner is denied (404, not 403).
	c := ctxWith(otherID, models.RoleClient)
	assert.False(t, a.AuthorizeVM(c, vmID))
	assert.Equal(t, http.StatusNotFound, c.Response().Status)

	// Nonexistent VM maps to 404 (anti-enumeration parity with non-owner).
	c2 := ctxWith(ownerID, models.RoleClient)
	assert.False(t, a.AuthorizeVM(c2, uuid.New().String()))
	assert.Equal(t, http.StatusNotFound, c2.Response().Status)

	// Missing auth maps to 401.
	c3 := ctxWithoutUser()
	assert.False(t, a.AuthorizeVM(c3, vmID))
	assert.Equal(t, http.StatusUnauthorized, c3.Response().Status)

	// Admin bypasses ownership.
	assert.True(t, a.AuthorizeVM(ctxWith(otherID, models.RoleAdmin), vmID))
}

func TestAuthorizer_AuthorizeAdmin(t *testing.T) {
	db := testDB(t)
	a := NewAuthorizer(db)

	// Admin passes.
	assert.True(t, a.AuthorizeAdmin(ctxWith(uuid.New().String(), models.RoleAdmin)))

	// Non-admin client is forbidden (403).
	c := ctxWith(uuid.New().String(), models.RoleClient)
	assert.False(t, a.AuthorizeAdmin(c))
	assert.Equal(t, http.StatusForbidden, c.Response().Status)

	// Missing auth maps to 401.
	c2 := ctxWithoutUser()
	assert.False(t, a.AuthorizeAdmin(c2))
	assert.Equal(t, http.StatusUnauthorized, c2.Response().Status)
}

func TestAuthorizer_ResourceMismatch(t *testing.T) {
	db := testDB(t)
	vmA := uuid.New().String()
	vmB := uuid.New().String()
	require.NoError(t, db.Create(&models.VM{ID: vmA, UserID: uuid.New().String(), NodeID: uuid.New().String()}).Error)
	require.NoError(t, db.Create(&models.VM{ID: vmB, UserID: uuid.New().String(), NodeID: uuid.New().String()}).Error)

	netID := uuid.New().String()
	require.NoError(t, db.Create(&models.Network{ID: netID, VMID: vmB}).Error)

	a := NewAuthorizer(db)

	// Network belongs to vmB, route targets vmA -> mismatch -> 404.
	c := ctxWith(uuid.New().String(), models.RoleClient)
	assert.False(t, a.AuthorizeVMResource(c, vmA, vmB))
	assert.Equal(t, http.StatusNotFound, c.Response().Status)

	// Matching VM IDs pass.
	assert.True(t, a.AuthorizeVMResource(ctxWith(uuid.New().String(), models.RoleClient), vmA, vmA))
}

func TestAuthorizer_VMIDHelpers(t *testing.T) {
	db := testDB(t)
	vmID := uuid.New().String()
	require.NoError(t, db.Create(&models.VM{ID: vmID, UserID: uuid.New().String(), NodeID: uuid.New().String()}).Error)

	netID := uuid.New().String()
	pfID := uuid.New().String()
	ruleID := uuid.New().String()
	require.NoError(t, db.Create(&models.Network{ID: netID, VMID: vmID}).Error)
	require.NoError(t, db.Create(&models.PortForward{ID: pfID, VMID: vmID, NetworkID: netID}).Error)
	require.NoError(t, db.Create(&models.FirewallRule{ID: ruleID, VMID: vmID}).Error)

	a := NewAuthorizer(db)
	ctx := context.Background()

	got, err := a.NetworkVMID(ctx, netID)
	require.NoError(t, err)
	assert.Equal(t, vmID, got)

	got, err = a.PortForwardVMID(ctx, pfID)
	require.NoError(t, err)
	assert.Equal(t, vmID, got)

	got, err = a.FirewallRuleVMID(ctx, ruleID)
	require.NoError(t, err)
	assert.Equal(t, vmID, got)

	// Nonexistent resource -> ErrRecordNotFound.
	_, err = a.NetworkVMID(ctx, uuid.New().String())
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound)
}
