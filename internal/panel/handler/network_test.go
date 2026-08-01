package handler

import (
	"bytes"
	"encoding/json"
	"io"
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

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// newAuthzTestDB builds an isolated SQLite database with the schema needed by
// the authz containment tests (no live infrastructure required). Columns use
// SQLite-compatible types (the authz helpers only read vm_id/user_id, so the
// Postgres-specific inet/cidr/user_role/vm_status types are omitted here).
func newAuthzTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY, email TEXT, password_hash TEXT, role TEXT DEFAULT 'client',
quota_mode TEXT NOT NULL DEFAULT 'legacy',
					two_factor_secret TEXT, two_factor_backup_codes TEXT, ip_whitelist TEXT,
			created_at DATETIME, updated_at DATETIME, token_revoked_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS vms (
			id TEXT PRIMARY KEY, user_id TEXT NOT NULL, node_id TEXT NOT NULL, hostname TEXT,
			os_template_id TEXT, resources TEXT, status TEXT DEFAULT 'stopped',
			source_migration TEXT, vnc_port INTEGER, vnc_password TEXT,
			console_enabled BOOLEAN DEFAULT 1, rescue_mode BOOLEAN DEFAULT 0,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS networks (
			id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, ip_address TEXT, bandwidth_limit INTEGER,
			bandwidth_quota_gb INTEGER, over_quota_policy TEXT, throttle_speed_mbps INTEGER,
			throttled BOOLEAN, vlan_id INTEGER, anti_spoofing BOOLEAN, created_at DATETIME,
			updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS firewall_rules (
			id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, protocol TEXT, port_range TEXT, action TEXT,
			direction TEXT, source_ip TEXT, priority INTEGER, created_at DATETIME, updated_at DATETIME,
			deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS port_forwards (
			id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, network_id TEXT, external_port INTEGER,
			internal_port INTEGER, protocol TEXT, source_ip TEXT, created_at DATETIME,
			updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS bandwidth_usages (
			id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, node_id TEXT NOT NULL,
			period_start DATETIME, period_end DATETIME, rx_bytes BIGINT DEFAULT 0,
			tx_bytes BIGINT DEFAULT 0, total_bytes BIGINT DEFAULT 0, quota_bytes BIGINT DEFAULT 0,
			exceeded BOOLEAN DEFAULT 0, blocked_at DATETIME, last_reported_at DATETIME,
			created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS vm_metrics (
			id INTEGER PRIMARY KEY AUTOINCREMENT, vm_id TEXT NOT NULL, cpu_usage REAL DEFAULT 0,
			memory_usage REAL DEFAULT 0, memory_used_bytes BIGINT DEFAULT 0,
			disk_read_bytes_per_sec BIGINT DEFAULT 0, disk_write_bytes_per_sec BIGINT DEFAULT 0,
			network_rx_bytes_per_sec BIGINT DEFAULT 0, network_tx_bytes_per_sec BIGINT DEFAULT 0,
			recorded_at DATETIME NOT NULL)`,
	}
	for _, s := range ddl {
		require.NoError(t, db.Exec(s).Error)
	}
	return db
}

// seedUser inserts a user with the given role and returns the UUID.
func seedUser(t *testing.T, db *gorm.DB, role models.UserRole) string {
	t.Helper()
	id := uuid.New()
	require.NoError(t, db.Create(&models.User{ID: id, Email: id.String() + "@ex.com", Role: role}).Error)
	return id.String()
}

// seedVM inserts a VM owned by ownerID and returns the VM ID.
func seedVM(t *testing.T, db *gorm.DB, ownerID string) string {
	t.Helper()
	vmID := uuid.New().String()
	require.NoError(t, db.Create(&models.VM{ID: vmID, UserID: ownerID, NodeID: uuid.New().String(), Hostname: "vm"}).Error)
	return vmID
}

func seedNetwork(t *testing.T, db *gorm.DB, vmID string) string {
	t.Helper()
	id := uuid.New().String()
	require.NoError(t, db.Create(&models.Network{ID: id, VMID: vmID, IPAddress: "10.0.0.1"}).Error)
	return id
}

func seedFirewallRule(t *testing.T, db *gorm.DB, vmID string) string {
	t.Helper()
	id := uuid.New().String()
	require.NoError(t, db.Create(&models.FirewallRule{ID: id, VMID: vmID, Protocol: "tcp", Action: "allow", Direction: "inbound", Priority: 1}).Error)
	return id
}

func seedPortForward(t *testing.T, db *gorm.DB, vmID, networkID string) string {
	t.Helper()
	id := uuid.New().String()
	require.NoError(t, db.Create(&models.PortForward{ID: id, VMID: vmID, NetworkID: networkID, ExternalPort: 80, InternalPort: 8080}).Error)
	return id
}

// ctxWithUser builds an echo context with the authenticated user set (as the
// RequireAuth middleware would), so the authz helpers resolve identity.
func ctxWithUser(t *testing.T, db *gorm.DB, userID string, role models.UserRole) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(userID), Role: role})
	return c, rec
}

// newTestNetworkService builds a NetworkService backed by the test DB so the
// owner/admin tests can assert the call reached the service layer (not just
// passed authz). The river client is nil; the service's enqueue funcs tolerate
// a nil client.
func newTestNetworkService(db *gorm.DB) *service.NetworkService {
	return service.NewNetworkService(
		db,
		repository.NewNetworkRepository(db),
		repository.NewFirewallRepository(db),
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		nil,
	)
}

// TestNetworkHandler_OwnerAuthorized verifies an owner passes the tenant
// boundary on a network read (ListNetworkInterfaces) and the service is reached.
func TestNetworkHandler_OwnerAuthorized(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := NewNetworkHandler(newTestNetworkService(db), authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	err := h.ListNetworkInterfaces(c)
	// Authz permitted the call through to the service (any non-401/404 code proves
	// the tenant boundary did not short-circuit). The service may 500 on SQLite-
	// specific IPAM enrichment, which is unrelated to authorization.
	require.NoError(t, err)
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
	assert.NotEqual(t, http.StatusNotFound, rec.Code)
}

// TestNetworkHandler_OtherUserGets404 verifies a non-owner maps to 404 (anti-
// enumeration: identical to a nonexistent VM).
func TestNetworkHandler_OtherUserGets404(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	other := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := NewNetworkHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, other, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.ListNetworkInterfaces(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "VM not found")
}

// TestNetworkHandler_NonexistentVMGets404 verifies a nonexistent VM maps to 404,
// identical to a non-owner (anti-enumeration parity).
func TestNetworkHandler_NonexistentVMGets404(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)

	h := NewNetworkHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.ListNetworkInterfaces(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestNetworkHandler_UnauthenticatedGets401 verifies missing auth maps to 401.
func TestNetworkHandler_UnauthenticatedGets401(t *testing.T) {
	db := newAuthzTestDB(t)
	vmID := seedVM(t, db, seedUser(t, db, models.RoleClient))

	h := NewNetworkHandler(nil, authz.NewAuthorizer(db))
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.ListNetworkInterfaces(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestNetworkHandler_AdminBypassesOwnership verifies an admin reaches the
// service layer for another user's VM (ownership check bypassed).
func TestNetworkHandler_AdminBypassesOwnership(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, owner)

	h := NewNetworkHandler(newTestNetworkService(db), authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, admin, models.RoleAdmin)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.ListNetworkInterfaces(c))
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
	assert.NotEqual(t, http.StatusNotFound, rec.Code)
}

// TestNetworkHandler_ResourceMismatch404 verifies a port-forward that belongs to
// a different VM than the route VM maps to 404 (confused-deputy guard).
func TestNetworkHandler_ResourceMismatch404(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmA := seedVM(t, db, owner)
	vmB := seedVM(t, db, owner)
	netA := seedNetwork(t, db, vmA)
	// Port forward belongs to vmB, but the route targets vmA (mismatch).
	pfB := seedPortForward(t, db, vmB, netA)

	h := NewNetworkHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id", "network_id", "forward_id")
	c.SetParamValues(vmA, netA, pfB)

	require.NoError(t, h.RemovePortForward(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestNetworkHandler_FirewallRuleMismatch404 verifies a firewall rule belonging
// to another VM maps to 404 on removal.
func TestNetworkHandler_FirewallRuleMismatch404(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmA := seedVM(t, db, owner)
	vmB := seedVM(t, db, owner)
	ruleB := seedFirewallRule(t, db, vmB)

	h := NewNetworkHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id", "rule_id")
	c.SetParamValues(vmA, ruleB)

	require.NoError(t, h.RemoveFirewallRule(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestNetworkHandler_VMLevelPortForwardMismatch404 verifies the VM-level port
// forward removal guards against a rule owned by another VM.
func TestNetworkHandler_VMLevelPortForwardMismatch404(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmA := seedVM(t, db, owner)
	vmB := seedVM(t, db, owner)
	netB := seedNetwork(t, db, vmB)
	pfB := seedPortForward(t, db, vmB, netB)

	h := NewNetworkHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id", "forward_id")
	c.SetParamValues(vmA, pfB)

	require.NoError(t, h.RemoveVMPortForward(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// setBody binds a JSON body onto an echo context for handler tests.
func setBodyNetwork(t *testing.T, c echo.Context, v interface{}) {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(bytes.NewReader(b)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c.SetRequest(req)
}

// TestNetworkHandler_ClientDeniedCustomNIC verifies a RoleClient is denied the
// creation of an additional/custom NIC (the request never reaches the service).
func TestNetworkHandler_ClientDeniedCustomNIC(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := NewNetworkHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)
	setBodyNetwork(t, c, map[string]interface{}{"ip_address": "10.0.0.9"})

	require.NoError(t, h.AddNetworkInterface(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "may not select VM network or infrastructure placement")
}

// TestNetworkHandler_ClientDeniedVLAN verifies a RoleClient is denied a VLAN
// change on a network.
func TestNetworkHandler_ClientDeniedVLAN(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	netID := seedNetwork(t, db, vmID)

	h := NewNetworkHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id", "network_id")
	c.SetParamValues(vmID, netID)
	setBodyNetwork(t, c, map[string]interface{}{"vlan_id": 50})

	require.NoError(t, h.SetVLAN(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "may not select VM network or infrastructure placement")
}

// TestNetworkHandler_ClientDeniedAntiSpoofing verifies a RoleClient is denied an
// anti-spoofing change.
func TestNetworkHandler_ClientDeniedAntiSpoofing(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	netID := seedNetwork(t, db, vmID)

	h := NewNetworkHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id", "network_id")
	c.SetParamValues(vmID, netID)
	setBodyNetwork(t, c, map[string]interface{}{"enabled": false})

	require.NoError(t, h.SetAntiSpoofing(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "may not select VM network or infrastructure placement")
}

// TestNetworkHandler_ClientDeniedBandwidth verifies a RoleClient (even an owner)
// is denied bandwidth configuration.
func TestNetworkHandler_ClientDeniedBandwidth(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	netID := seedNetwork(t, db, vmID)

	h := NewNetworkHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id", "network_id")
	c.SetParamValues(vmID, netID)
	setBodyNetwork(t, c, map[string]interface{}{"bandwidth_limit": 1000})

	require.NoError(t, h.SetBandwidthLimit(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "may not select VM network or infrastructure placement")
}

// TestNetworkHandler_OwnerFirewallSelfServicePreserved verifies a client owner
// can reach the firewall rule handler (self-service), proving no admin guard was
// added. The service call outcome is irrelevant; the absence of a 403 policy
// response is the assertion.
func TestNetworkHandler_OwnerFirewallSelfServicePreserved(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := NewNetworkHandler(newTestNetworkService(db), authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)
	setBodyNetwork(t, c, map[string]interface{}{"protocol": "tcp", "action": "allow", "direction": "inbound", "priority": 1})

	require.NoError(t, h.AddFirewallRule(c))
	assert.NotEqual(t, http.StatusForbidden, rec.Code)
	assert.NotContains(t, rec.Body.String(), "may not select VM network or infrastructure placement")
}

// TestNetworkHandler_OwnerPortForwardSelfServicePreserved verifies a client
// owner can reach the VM-level port-forward handler (self-service) — no admin
// guard added.
func TestNetworkHandler_OwnerPortForwardSelfServicePreserved(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := NewNetworkHandler(newTestNetworkService(db), authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)
	setBodyNetwork(t, c, map[string]interface{}{"external_port": 80, "internal_port": 8080})

	require.NoError(t, h.AddVMPortForward(c))
	assert.NotEqual(t, http.StatusForbidden, rec.Code)
	assert.NotContains(t, rec.Body.String(), "may not select VM network or infrastructure placement")
}
