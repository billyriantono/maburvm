package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// newPolicyTestDB is a database of its own per test: these tests add settings
// and audit rows that must not leak into the tests sharing one in-memory file.
func newPolicyTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:policy-%s?mode=memory&cache=shared&_busy_timeout=5000", name)),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, s := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', email TEXT, password_hash TEXT, role TEXT DEFAULT 'client',
			quota_mode TEXT NOT NULL DEFAULT 'legacy', two_factor_secret TEXT, two_factor_enabled BOOLEAN NOT NULL DEFAULT 0,
			two_factor_backup_codes TEXT, ip_whitelist TEXT, expires_at DATETIME, support_note TEXT NOT NULL DEFAULT '', staff_role TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, token_revoked_at DATETIME, suspended_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, node_id TEXT NOT NULL, hostname TEXT,
			os_template_id TEXT, resources TEXT, status TEXT DEFAULT 'stopped', suspended BOOLEAN NOT NULL DEFAULT 0,
			source_migration TEXT, vnc_port INTEGER, vnc_password TEXT, console_enabled BOOLEAN DEFAULT 1, rescue_mode BOOLEAN DEFAULT 0,
			plan_id TEXT, billing_ref TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE audit_logs (id TEXT PRIMARY KEY, user_id TEXT, action TEXT NOT NULL, resource_type TEXT, resource_id TEXT,
			ip_address TEXT, user_agent TEXT, details TEXT, before_snapshot TEXT, after_snapshot TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(s).Error)
	}
	t.Cleanup(func() {
		if sqlDB, _ := db.DB(); sqlDB != nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

// reqAs builds a request context for the given user with a real method and
// body, since the guards read the method.
func reqAs(t *testing.T, method, userID string, role models.UserRole, body interface{}) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, "/", r)
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(userID), Role: role})
	return c, rec
}

func suspend(t *testing.T, db *gorm.DB, vmID string) {
	t.Helper()
	require.NoError(t, db.Table("vms").Where("id = ?", vmID).Update("suspended", true).Error)
}

func auditActions(t *testing.T, db *gorm.DB) []string {
	t.Helper()
	var actions []string
	require.NoError(t, db.Model(&models.AuditLog{}).Order("created_at, action").Pluck("action", &actions).Error)
	return actions
}

// SEC-02: the owner of a suspended VM cannot clone it, save it as an image,
// add or remove disks, or make or restore backups. Reading and deleting stay.
func TestSuspendedVM_OwnerCannotMakeAnythingOutOfIt(t *testing.T) {
	db := newPolicyTestDB(t)
	h := newTestVMHandler(t, db)
	owner := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, owner)
	suspend(t, db, vmID)

	vmRoute := func(method string, call func(echo.Context) error, body interface{}, params ...string) int {
		c, rec := reqAs(t, method, owner, models.RoleClient, body)
		c.SetParamNames(append([]string{"id"}, params[:len(params)/2]...)...)
		c.SetParamValues(append([]string{vmID}, params[len(params)/2:]...)...)
		require.NoError(t, call(c))
		return rec.Code
	}
	assert.Equal(t, http.StatusForbidden, vmRoute(http.MethodPost, h.CloneVM, map[string]string{"hostname": "copy"}), "clone")
	assert.Equal(t, http.StatusForbidden, vmRoute(http.MethodPost, h.AttachVMDisk, map[string]int{"size_gb": 10}), "attach disk")
	assert.Equal(t, http.StatusForbidden, vmRoute(http.MethodDelete, h.DetachVMDisk, nil, "device", "vdb"), "detach disk")
	assert.Equal(t, http.StatusForbidden, vmRoute(http.MethodPut, h.UpdateVM, map[string]string{"hostname": "renamed"}), "update")

	// The guard itself: any method but a read is a change.
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		c, rec := reqAs(t, m, owner, models.RoleClient, nil)
		assert.False(t, h.authorizeVM(c, vmID), m)
		assert.Equal(t, http.StatusForbidden, rec.Code, m)
		assert.Contains(t, rec.Body.String(), "suspended")
	}
	c, _ := reqAs(t, http.MethodGet, owner, models.RoleClient, nil)
	assert.True(t, h.authorizeVM(c, vmID), "reading stays allowed")
	c, _ = reqAs(t, http.MethodDelete, owner, models.RoleClient, nil)
	assert.True(t, h.authorizeVMOwner(c, vmID), "deleting the VM stays allowed")
	c, _ = reqAs(t, http.MethodPost, admin, models.RoleAdmin, nil)
	assert.True(t, h.authorizeVM(c, vmID), "an administrator is not held back")

	// Somebody else's suspended VM is still just "not found".
	stranger := seedUser(t, db, models.RoleClient)
	c, rec := reqAs(t, http.MethodPost, stranger, models.RoleClient, nil)
	assert.False(t, h.authorizeVM(c, vmID))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestSuspendedVM_BackupsAndImagesRefused(t *testing.T) {
	db := newPolicyTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, owner)
	suspend(t, db, vmID)

	backups := NewBackupHandler(service.NewBackupService(db, repository.NewBackupRepository(db), repository.NewBackupScheduleRepository(db),
		repository.NewVMRepository(db), repository.NewNodeRepository(db), nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil))))
	for name, call := range map[string]func(echo.Context) error{"create backup": backups.CreateBackup, "restore backup": backups.RestoreBackup} {
		c, rec := reqAs(t, http.MethodPost, owner, models.RoleClient, map[string]string{})
		c.SetParamNames("id", "backupId")
		c.SetParamValues(vmID, uuid.NewString())
		require.NoError(t, call(c))
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.Contains(t, rec.Body.String(), "suspended", name)
	}
	c, _ := reqAs(t, http.MethodGet, owner, models.RoleClient, nil)
	assert.True(t, backups.authorizeVM(c, vmID), "listing backups stays allowed")
	c, _ = reqAs(t, http.MethodPost, admin, models.RoleAdmin, nil)
	assert.True(t, backups.authorizeVM(c, vmID), "an administrator is not held back")

	images := NewImageHandler(service.NewImageService(db, nil, nil))
	c, rec := reqAs(t, http.MethodPost, owner, models.RoleClient, map[string]string{"vm_id": vmID, "name": "copy"})
	require.NoError(t, images.CreateImage(c))
	assert.Equal(t, http.StatusForbidden, rec.Code, "image")
	assert.Contains(t, rec.Body.String(), "suspended")
}

// BIL-24: a customer orders by plan. With no plan the order is refused; with
// one, the size they sent is dropped and the plan is binding.
func TestVMCreate_ClientMustOrderByPlan(t *testing.T) {
	h := NewVMHandler(nil, nil, nil, nil, nil, nil)
	owner := uuid.New()
	uc := &middleware.UserContext{ID: owner, Role: models.RoleClient}

	c, rec := reqAs(t, http.MethodPost, owner.String(), models.RoleClient, nil)
	req := CreateVMRequest{Hostname: "vm", OSTemplateID: uuid.NewString(), Resources: models.Resources{CPU: 128, RAM: 131072, Disk: 9000}}
	createReq := &service.CreateVMRequest{Resources: req.Resources}
	require.True(t, h.applyClientVMPolicy(c, uc, &req, createReq), "an order with no plan is refused")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "plan_required")

	c, _ = reqAs(t, http.MethodPost, owner.String(), models.RoleClient, nil)
	req.PlanID = uuid.NewString()
	createReq = &service.CreateVMRequest{Resources: req.Resources, PlanID: req.PlanID}
	require.False(t, h.applyClientVMPolicy(c, uc, &req, createReq))
	assert.True(t, createReq.PlanRequired, "the service must take the size from the plan")
	assert.Equal(t, models.Resources{}, createReq.Resources, "the size the customer sent is dropped")

	// An administrator still sizes a VM by hand.
	admin := &middleware.UserContext{ID: uuid.New(), Role: models.RoleAdmin}
	req.PlanID = ""
	createReq = &service.CreateVMRequest{Resources: req.Resources}
	require.False(t, h.applyClientVMPolicy(c, admin, &req, createReq))
	assert.False(t, createReq.PlanRequired)
	assert.Equal(t, req.Resources, createReq.Resources)
}

// BIL-06: the operator can stop customers ordering, cloning and adding disks
// on their own. Administrators are not affected.
func TestSelfServiceSwitchedOff(t *testing.T) {
	db := newPolicyTestDB(t)
	h := newTestVMHandler(t, db)
	owner := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, owner)

	routes := map[string]struct {
		call func(echo.Context) error
		body interface{}
	}{
		"order":    {h.CreateVM, map[string]string{"hostname": "vm", "os_template_id": uuid.NewString(), "plan_id": uuid.NewString()}},
		"clone":    {h.CloneVM, map[string]string{"hostname": "copy"}},
		"add disk": {h.AttachVMDisk, map[string]int{"size_gb": 10}},
	}
	run := func(name, user string, role models.UserRole) (int, string) {
		c, rec := reqAs(t, http.MethodPost, user, role, routes[name].body)
		c.SetParamNames("id")
		c.SetParamValues(vmID)
		require.NoError(t, routes[name].call(c))
		return rec.Code, rec.Body.String()
	}

	// Unset means allowed: nothing here is refused as switched off.
	for name := range routes {
		_, body := run(name, owner, models.RoleClient)
		assert.NotContains(t, body, "self_service_disabled", name)
	}

	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('quotas', ?)`,
		`{"clientOrderingEnabled":false,"clientCloneEnabled":false,"clientExtraDisksEnabled":false}`).Error)
	for name := range routes {
		code, body := run(name, owner, models.RoleClient)
		assert.Equal(t, http.StatusForbidden, code, name)
		assert.Contains(t, body, "self_service_disabled", name)

		_, body = run(name, admin, models.RoleAdmin)
		assert.NotContains(t, body, "self_service_disabled", "administrator: "+name)
	}
}

// BIL-25: a billing event acts only on a VM of the user it names.
func TestBillingEventMustNameTheVMsOwner(t *testing.T) {
	db := newPolicyTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	other := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &BillingHandler{
		vmService: service.NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil, log),
		logger:    log, db: db,
	}
	ctx := httptest.NewRequest(http.MethodPost, "/", nil).Context()

	require.NoError(t, h.checkEventOwner(ctx, EventVMDestroy, &WebhookData{VMID: vmID, UserID: owner}))
	require.NoError(t, h.checkEventOwner(ctx, EventVMDestroy, &WebhookData{VMID: vmID, UserID: " " + strings.ToUpper(owner) + " "}))
	require.ErrorIs(t, h.checkEventOwner(ctx, EventVMDestroy, &WebhookData{VMID: vmID, UserID: other}), errBillingOwnerMismatch)
	require.ErrorIs(t, h.checkEventOwner(ctx, EventVMSuspend, &WebhookData{VMID: uuid.NewString(), UserID: owner}), service.ErrVMNotFound)
	require.Error(t, h.checkEventOwner(ctx, EventVMDestroy, &WebhookData{VMID: vmID}), "an event that changes a VM must say whose it is")
	require.NoError(t, h.checkEventOwner(ctx, EventVMUsage, &WebhookData{VMID: vmID}), "the usage sync names no user and changes nothing")
	require.ErrorIs(t, h.checkEventOwner(ctx, EventVMUsage, &WebhookData{VMID: vmID, UserID: other}), errBillingOwnerMismatch)

	// End to end: every event that would change the VM is refused with 403,
	// leaves the VM alone and leaves an audit row.
	events := []WebhookEventType{EventVMDestroy, EventVMSuspend, EventVMUnsuspend, EventVMResize, EventVMResetPassword, EventVMStart, EventVMStop, EventVMRestart}
	for _, ev := range events {
		body, _ := json.Marshal(WebhookPayload{Event: ev, Timestamp: time.Now(), Data: WebhookData{
			VMID: vmID, UserID: other, ExternalRef: "whmcs-77", Password: "long-enough-pw", VMSpecs: VMSpecs{CPU: 64, Memory: 65536, Disk: 900},
		}})
		req := httptest.NewRequest(http.MethodPost, "/webhooks/billing", bytes.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(IdempotencyKeyHeader, "whmcs-"+string(ev))
		rec := httptest.NewRecorder()
		require.NoError(t, h.HandleWebhook(echo.New().NewContext(req, rec)))
		assert.Equal(t, http.StatusForbidden, rec.Code, string(ev))
		assert.Contains(t, rec.Body.String(), "does not belong", string(ev))
	}
	var vm models.VM
	require.NoError(t, db.First(&vm, "id = ?", vmID).Error, "the VM is still there")
	assert.False(t, vm.Suspended)
	assert.Equal(t, owner, vm.UserID)

	var rows []models.AuditLog
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, len(events))
	assert.Equal(t, "billing.owner_mismatch", rows[0].Action)
	assert.Equal(t, vmID, *rows[0].ResourceID)
	assert.Equal(t, other, rows[0].Details["claimed_user_id"])
	assert.Equal(t, "whmcs-77", rows[0].Details["external_ref"])
}

// ADM-42: only the founding administrator opens support access; a support
// account can neither open nor end one; creating, ending and the refusal of a
// longer lifetime are all enforced; create and end are on record.
func TestSupportAccessIsTheFoundingAdministratorsAlone(t *testing.T) {
	if os.Getenv("JWT_SECRET_KEY") == "" {
		os.Setenv("JWT_SECRET_KEY", "test-jwt-secret-key-for-vm-route-tests")
	}
	if os.Getenv("AES_KEY") == "" {
		os.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	}
	db := newPolicyTestDB(t)
	now := time.Now()
	account := func(email string, age time.Duration, expires *time.Time) *models.User {
		u := &models.User{ID: uuid.New(), Email: email, PasswordHash: "x", Role: models.RoleAdmin, CreatedAt: now.Add(-age)}
		require.NoError(t, db.Create(u).Error)
		if expires != nil {
			require.NoError(t, db.Table("users").Where("id = ?", u.ID).Update("expires_at", *expires).Error)
		}
		return u
	}
	later := now.Add(2 * time.Hour)
	// The support account is the oldest administrator on purpose: age alone
	// must not make it the founding one.
	support := account("support-old@support.invalid", 72*time.Hour, &later)
	founder := account("founder@example.test", 48*time.Hour, nil)
	peer := account("peer@example.test", 24*time.Hour, nil)

	e := echo.New()
	RegisterOpsRoutes(e, db, nil, nil)
	call := func(u *models.User, method, path, body string) *httptest.ResponseRecorder {
		token, err := middleware.GenerateAccessToken(u)
		require.NoError(t, err)
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	const path = "/api/v1/security/support-access"
	grant := `{"hours":4,"note":"vendor engineer, ticket 4411"}`
	admins := func() int64 {
		var n int64
		require.NoError(t, db.Table("users").Where("role = 'admin' AND deleted_at IS NULL").Count(&n).Error)
		return n
	}

	for name, u := range map[string]*models.User{"another administrator": peer, "a support account": support} {
		rec := call(u, http.MethodPost, path, grant)
		assert.Equal(t, http.StatusForbidden, rec.Code, name)
		assert.Contains(t, rec.Body.String(), "only_the_founding_administrator_may_grant_support_access", name)
	}
	assert.EqualValues(t, 3, admins(), "nobody was created")
	assert.Empty(t, auditActions(t, db))

	// No lifetime beyond seven days, and none without a reason.
	assert.Equal(t, http.StatusBadRequest, call(founder, http.MethodPost, path, `{"hours":169,"note":"too long"}`).Code)
	assert.Equal(t, http.StatusBadRequest, call(founder, http.MethodPost, path, `{"hours":4,"note":" "}`).Code)

	rec := call(founder, http.MethodPost, path, grant)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		Data struct {
			Grant    SupportGrant `json:"grant"`
			Password string       `json:"password"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.NotEmpty(t, created.Data.Password)
	assert.WithinDuration(t, now.Add(4*time.Hour), created.Data.Grant.ExpiresAt, time.Minute)
	assert.Equal(t, []string{"support_access.create"}, auditActions(t, db))
	var row models.AuditLog
	require.NoError(t, db.First(&row).Error)
	assert.Equal(t, founder.ID.String(), *row.UserID)
	assert.Equal(t, created.Data.Grant.ID, *row.ResourceID)
	assert.NotContains(t, fmt.Sprint(row.Details), created.Data.Password, "the password is never written to the audit log")

	// The new account is itself a support account: it cannot open another.
	var minted models.User
	require.NoError(t, db.First(&minted, "id = ?", created.Data.Grant.ID).Error)
	require.NotNil(t, minted.ExpiresAt)
	assert.Equal(t, http.StatusForbidden, call(&minted, http.MethodPost, path, grant).Code)

	// Ending access: not by a support account, yes by any permanent administrator.
	assert.Equal(t, http.StatusForbidden, call(support, http.MethodDelete, path+"/"+created.Data.Grant.ID, "").Code)
	assert.Equal(t, http.StatusOK, call(peer, http.MethodDelete, path+"/"+created.Data.Grant.ID, "").Code)
	assert.Equal(t, []string{"support_access.create", "support_access.revoke"}, auditActions(t, db))
	// There is no route that extends one.
	for _, m := range []string{http.MethodPut, http.MethodPatch} {
		code := call(founder, m, path+"/"+support.ID.String(), `{"hours":168}`).Code
		assert.Contains(t, []int{http.StatusNotFound, http.StatusMethodNotAllowed}, code, m)
	}
}
