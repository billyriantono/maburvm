package service

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

func newPolicyVMService(db *gorm.DB) *VMService {
	return NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db),
		repository.NewTemplateRepository(db), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func setQuotaSettings(t *testing.T, db *gorm.DB, jsonData string) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`DELETE FROM system_settings WHERE section = 'quotas'`).Error)
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('quotas', ?)`, jsonData).Error)
}

// The owner of a suspended VM may read it and delete it, and nothing else.
func TestOwnerAccess(t *testing.T) {
	const owner, stranger = "owner", "stranger"
	flagged := &models.VM{UserID: owner, Suspended: true, Status: models.VMStatusStopped}
	byStatus := &models.VM{UserID: owner, Status: models.VMStatusSuspended}
	normal := &models.VM{UserID: owner, Status: models.VMStatusStopped}

	for _, vm := range []*models.VM{flagged, byStatus} {
		require.ErrorIs(t, OwnerAccess(vm, owner, false, true), ErrVMSuspended)
		require.NoError(t, OwnerAccess(vm, owner, false, false), "reading and deleting stay allowed")
		require.NoError(t, OwnerAccess(vm, "anyone", true, true), "administrators are not held back")
	}
	require.NoError(t, OwnerAccess(normal, owner, false, true))
	require.ErrorIs(t, OwnerAccess(normal, stranger, false, false), ErrVMNotFound)
	require.ErrorIs(t, OwnerAccess(flagged, stranger, false, true), ErrVMNotFound, "a stranger never learns the VM is suspended")
}

// An image of a suspended VM would be a way to run the same disk again.
func TestCreateImageFromSuspendedVMRefused(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	require.NoError(t, db.Exec(`ALTER TABLE vms ADD COLUMN suspended BOOLEAN NOT NULL DEFAULT 0`).Error)
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "held.example.test", OSTemplateID: tmpl.ID, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(vm).Error)
	require.NoError(t, db.Table("vms").Where("id = ?", vm.ID).Update("suspended", true).Error)

	svc := NewImageService(db, nil, nil)
	_, err := svc.CreateImageFromVM(context.Background(), user.ID, false, vm.ID, "copy")
	require.ErrorIs(t, err, ErrVMSuspended)
	_, err = svc.CreateImageFromVM(context.Background(), uuid.New(), false, vm.ID, "copy")
	require.ErrorIs(t, err, ErrVMNotFound)
}

func seedPlan(t *testing.T, db *gorm.DB, active bool) *models.Plan {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS plans (id TEXT PRIMARY KEY, name TEXT NOT NULL, cpu INTEGER, ram INTEGER, disk INTEGER, bandwidth_mbps INTEGER DEFAULT 0, data_quota_gb INTEGER DEFAULT 0, over_quota_policy TEXT DEFAULT 'throttle', throttle_speed_mbps INTEGER DEFAULT 0, smtp_policy TEXT NOT NULL DEFAULT 'allowed', limits TEXT NOT NULL DEFAULT '{}', price_monthly REAL, price_hourly REAL, currency TEXT NOT NULL DEFAULT '', max_snapshots INTEGER, max_backups INTEGER, max_extra_ipv4 INTEGER, max_extra_ipv6 INTEGER, description TEXT, default_firewall_group_id TEXT, network_type TEXT NOT NULL DEFAULT 'public', nat_ports INTEGER NOT NULL DEFAULT 0, nat_hostnames INTEGER NOT NULL DEFAULT 0, is_active BOOLEAN, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)
	plan := &models.Plan{Name: "small-" + uuid.NewString(), CPU: 2, RAM: 2048, Disk: 40, BandwidthMbps: 100, DataQuotaGB: 500, OverQuotaPolicy: models.OverQuotaSuspend, ThrottleSpeedMbps: 5, IsActive: true}
	require.NoError(t, db.Create(plan).Error)
	if !active { // gorm writes the column default for a false bool
		require.NoError(t, db.Model(plan).Update("is_active", false).Error)
	}
	return plan
}

// A customer order is sized by its plan alone, with the plan's speed cap and
// transfer quota, whatever else the request carries.
func TestCreateVMCustomerOrderTakesEverythingFromThePlan(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	pool := &models.IPPool{Name: "public-v4", NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Bridge: "viifbr0", Gateway: "203.0.113.1"}
	require.NoError(t, db.Create(pool).Error)
	require.NoError(t, db.Create(&models.IPAddress{PoolID: pool.ID, Address: "203.0.113.10", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable}).Error)
	plan := seedPlan(t, db, true)
	svc := newPolicyVMService(db)

	_, err := svc.CreateVM(ctx, &CreateVMRequest{
		UserID: user.ID.String(), Hostname: "noplan.example.test", OSTemplateID: tmpl.ID,
		Resources: models.Resources{CPU: 64, RAM: 131072, Disk: 4000}, NodeID: node.ID, PlanRequired: true,
	})
	require.ErrorIs(t, err, ErrPlanRequired)

	resp, err := svc.CreateVM(ctx, &CreateVMRequest{
		UserID: user.ID.String(), Hostname: "planned.example.test", OSTemplateID: tmpl.ID,
		Resources:     models.Resources{CPU: 64, RAM: 131072, Disk: 4000},
		BandwidthMbps: 10000, DataQuotaGB: 999999, OverQuotaPolicy: models.OverQuotaOverage, ThrottleSpeedMbps: 9999,
		NodeID: node.ID, IPPoolID: pool.ID, PlanID: plan.ID, PlanRequired: true,
	})
	require.NoError(t, err)
	require.Equal(t, models.Resources{CPU: 2, RAM: 2048, Disk: 40}, resp.VM.Resources)

	var network models.Network
	require.NoError(t, db.Where("vm_id = ?", resp.VM.ID).First(&network).Error)
	require.EqualValues(t, 100, network.BandwidthLimit)
	require.EqualValues(t, 500, network.BandwidthQuotaGB)
	require.Equal(t, models.OverQuotaSuspend, network.OverQuotaPolicy)
	require.Equal(t, 5, network.ThrottleSpeedMbps)
}

func TestCreateVMRefusesUnknownAndRetiredPlans(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	retired := seedPlan(t, db, false)
	svc := newPolicyVMService(db)
	order := func(planID string) error {
		_, err := svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: "p-" + uuid.NewString()[:8] + ".example.test",
			OSTemplateID: tmpl.ID, NodeID: node.ID, PlanID: planID, PlanRequired: true})
		return err
	}
	require.ErrorIs(t, order(retired.ID), ErrPlanInactive)
	require.ErrorIs(t, order(uuid.NewString()), ErrPlanNotFound)
}

// The account quota limits what a customer adds on their own. An order the
// billing system placed has been paid for and is not held back by it.
func TestAccountQuotaHoldsCustomerNotBilling(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	require.NoError(t, db.Create(&models.UserQuota{UserID: user.ID.String(), MaxVMs: 1, MaxVCPU: 1}).Error)
	svc := newPolicyVMService(db)
	req := func(host string, billed bool) *CreateVMRequest {
		return &CreateVMRequest{UserID: user.ID.String(), Hostname: host, OSTemplateID: tmpl.ID,
			Resources: models.Resources{CPU: 2, RAM: 512, Disk: 10}, NodeID: node.ID, Billed: billed}
	}

	_, err := svc.CreateVM(ctx, req("self.example.test", false))
	require.ErrorIs(t, err, ErrQuotaExceeded)

	resp, err := svc.CreateVM(ctx, req("billed.example.test", true))
	require.NoError(t, err)

	// A clone is the customer's own doing and counts like any other server.
	require.NoError(t, db.Model(&models.VM{}).Where("id = ?", resp.VM.ID).Update("status", models.VMStatusStopped).Error)
	_, err = svc.CloneVM(ctx, &CloneVMRequest{SourceVMID: resp.VM.ID})
	require.ErrorIs(t, err, ErrQuotaExceeded)

	// So does a resize asked for any way but through billing.
	grown := models.Resources{CPU: 4, RAM: 1024, Disk: 10}
	_, err = svc.UpdateVM(ctx, &UpdateVMRequest{VMID: resp.VM.ID, Resources: &grown})
	require.ErrorIs(t, err, ErrQuotaExceeded)
}

// A managed account whose quota cannot be resolved stays closed, billed or not.
func TestBilledOrderDoesNotOpenAManagedAccount(t *testing.T) {
	require.False(t, billedOverQuota(true, ErrQuotaNotAvailable))
	require.False(t, billedOverQuota(false, ErrQuotaExceeded))
	require.True(t, billedOverQuota(true, ErrQuotaExceeded))
}

// A clone keeps the speed cap and transfer quota its source was sold with.
func TestCloneKeepsSourceNetworkLimits(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	src := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "src.example.test", OSTemplateID: tmpl.ID,
		Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(src).Error)
	require.NoError(t, db.Create(&models.Network{VMID: src.ID, IPAddress: "203.0.113.9", BandwidthLimit: 100, BandwidthQuotaGB: 500, OverQuotaPolicy: models.OverQuotaSuspend, ThrottleSpeedMbps: 5}).Error)

	svc := newPolicyVMService(db)
	req := &CreateVMRequest{}
	svc.inheritNetworkLimits(ctx, src.ID, req)
	require.Equal(t, 100, req.BandwidthMbps)
	require.EqualValues(t, 500, req.DataQuotaGB)
	require.Equal(t, models.OverQuotaSuspend, req.OverQuotaPolicy)
	require.Equal(t, 5, req.ThrottleSpeedMbps)

	resp, err := svc.CloneVM(ctx, &CloneVMRequest{SourceVMID: src.ID})
	require.NoError(t, err)
	require.Equal(t, src.Resources, resp.VM.Resources)
}

func TestSelfServiceSwitches(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()

	// No settings table, no row, an unset field: all allowed, as before.
	require.True(t, SelfServiceAllowed(ctx, db, SelfServiceOrder))
	setQuotaSettings(t, db, `{"vpcMaxPerUser":5}`)
	for _, a := range []SelfServiceAction{SelfServiceOrder, SelfServiceClone, SelfServiceExtraDisk} {
		require.True(t, SelfServiceAllowed(ctx, db, a), string(a))
	}

	setQuotaSettings(t, db, `{"clientOrderingEnabled":false,"clientCloneEnabled":true,"clientExtraDisksEnabled":false}`)
	require.Equal(t, map[SelfServiceAction]bool{SelfServiceOrder: false, SelfServiceClone: true, SelfServiceExtraDisk: false}, SelfServiceSwitches(ctx, db))
}

// A new customer account starts with a finite quota, never the unlimited one
// an account with no quota row has.
func TestNewClientQuota(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, _, _ := seedVMIPAMDependencies(t, db)

	def := NewClientQuota(ctx, db)
	require.Equal(t, SetQuotaRequest{MaxVMs: DefaultNewClientMaxVMs, MaxVCPU: DefaultNewClientMaxVCPU, MaxRAMMB: DefaultNewClientMaxRAMMB, MaxDiskGB: DefaultNewClientMaxDiskGB}, def)
	require.Positive(t, def.MaxVMs)
	require.Positive(t, def.MaxVCPU)
	require.Positive(t, def.MaxRAMMB)
	require.Positive(t, def.MaxDiskGB)

	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return ApplyNewClientQuota(ctx, tx, user.ID.String()) }))
	q, err := NewQuotaService(db, repository.NewVMRepository(db)).GetQuota(ctx, user.ID.String())
	require.NoError(t, err)
	require.Equal(t, DefaultNewClientMaxVMs, q.MaxVMs)
	require.Equal(t, models.QuotaModeLegacy, q.QuotaMode)

	// The operator's numbers win, and 0 stays "no limit" when they ask for it.
	setQuotaSettings(t, db, `{"newClientMaxVms":7,"newClientMaxVcpu":0}`)
	got := NewClientQuota(ctx, db)
	require.Equal(t, 7, got.MaxVMs)
	require.Equal(t, 0, got.MaxVCPU)
	require.Equal(t, DefaultNewClientMaxRAMMB, got.MaxRAMMB)
}

func supportTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := setupVMIPAMTestDB(t)
	require.NoError(t, db.Exec(`ALTER TABLE users ADD COLUMN expires_at DATETIME`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE audit_logs (id TEXT PRIMARY KEY, user_id TEXT, action TEXT NOT NULL, resource_type TEXT, resource_id TEXT, ip_address TEXT, user_agent TEXT, details TEXT, before_snapshot TEXT, after_snapshot TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`).Error)
	return db
}

func seedAccount(t *testing.T, db *gorm.DB, email string, role models.UserRole, created time.Time, expires *time.Time, password string) uuid.UUID {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	id := uuid.New()
	require.NoError(t, db.Exec(`INSERT INTO users (id, email, password_hash, role, created_at, updated_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id.String(), email, string(hash), string(role), created, created, expires).Error)
	return id
}

// A support account is never the founding administrator, even when it is the
// oldest administrator left.
func TestFoundingAdminIsNeverASupportAccount(t *testing.T) {
	db := supportTestDB(t)
	ctx := context.Background()
	now := time.Now()
	later := now.Add(time.Hour)

	support := seedAccount(t, db, "support-1@support.invalid", models.RoleAdmin, now.Add(-3*time.Hour), &later, "x")
	require.Equal(t, uuid.Nil, FoundingAdminID(ctx, db), "a support account alone is no founding administrator")

	founder := seedAccount(t, db, "founder@example.test", models.RoleAdmin, now.Add(-2*time.Hour), nil, "x")
	peer := seedAccount(t, db, "peer@example.test", models.RoleAdmin, now.Add(-time.Hour), nil, "x")
	require.Equal(t, founder, FoundingAdminID(ctx, db))

	require.True(t, IsTemporaryAccount(ctx, db, support))
	require.False(t, IsTemporaryAccount(ctx, db, peer))
	require.True(t, IsTemporaryAccount(ctx, db, uuid.New()), "an unknown account is refused, not waved through")
}

// Every sign-in with a support account is on record; ordinary sign-ins are not
// written here.
func TestSupportAccountSignInIsAudited(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	db := supportTestDB(t)
	now := time.Now()
	later := now.Add(time.Hour)
	support := seedAccount(t, db, "support-2@support.invalid", models.RoleAdmin, now, &later, "support-pass")
	seedAccount(t, db, "founder@example.test", models.RoleAdmin, now, nil, "founder-pass")
	svc := &UserService{db: db}

	_, err := svc.Login(&LoginRequest{Email: "founder@example.test", Password: "founder-pass", ClientIP: "198.51.100.7"})
	require.NoError(t, err)
	var rows []models.AuditLog
	require.NoError(t, db.Find(&rows).Error)
	require.Empty(t, rows)

	_, err = svc.Login(&LoginRequest{Email: "support-2@support.invalid", Password: "support-pass", ClientIP: "198.51.100.7"})
	require.NoError(t, err)
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "support_access.sign_in", rows[0].Action)
	require.Equal(t, support.String(), *rows[0].UserID)
	require.Equal(t, "198.51.100.7", rows[0].IPAddress)
}

// Retrying a capture makes the image again, so the same rule holds: not from a
// suspended VM, and not from a VM that is no longer the caller's.
func TestRetryCaptureOfSuspendedVMRefused(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Exec(`ALTER TABLE vms ADD COLUMN suspended BOOLEAN NOT NULL DEFAULT 0`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE images (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, name TEXT, source_vm_id TEXT, os_template_id TEXT, object_key TEXT, size_bytes INTEGER DEFAULT 0, checksum TEXT DEFAULT '', status TEXT, error_message TEXT DEFAULT '', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "held.example.test", OSTemplateID: tmpl.ID, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(vm).Error)
	vmID := uuid.MustParse(vm.ID)
	img := &models.Image{ID: uuid.New(), UserID: user.ID, Name: "copy", SourceVMID: &vmID, ObjectKey: "images/copy.qcow2", Status: models.ImageStatusFailed}
	require.NoError(t, db.Create(img).Error)
	svc := NewImageService(db, nil, nil)

	require.NoError(t, db.Table("vms").Where("id = ?", vm.ID).Update("suspended", true).Error)
	_, err := svc.RetryCapture(ctx, img.ID.String(), user.ID, false)
	require.ErrorIs(t, err, ErrVMSuspended)

	require.NoError(t, db.Table("vms").Where("id = ?", vm.ID).Updates(map[string]any{"suspended": false, "user_id": uuid.NewString()}).Error)
	_, err = svc.RetryCapture(ctx, img.ID.String(), user.ID, false)
	require.ErrorIs(t, err, ErrVMNotFound, "the VM now belongs to someone else")

	var after models.Image
	require.NoError(t, db.First(&after, "id = ?", img.ID).Error)
	require.Equal(t, models.ImageStatusFailed, after.Status, "a refused retry starts nothing")
}

// A support account that signs in through SSO is on record too, and one that
// has run out does not sign in at all.
func TestSupportAccountSSOSignIn(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	db := supportTestDB(t)
	now := time.Now()
	later, past := now.Add(time.Hour), now.Add(-time.Hour)
	support := seedAccount(t, db, "support-3@support.invalid", models.RoleAdmin, now, &later, "x")
	seedAccount(t, db, "support-gone@support.invalid", models.RoleAdmin, now, &past, "x")
	seedAccount(t, db, "founder@example.test", models.RoleAdmin, now, nil, "x")
	svc := &UserService{db: db}

	_, token, _, err := svc.SSOLogin("founder@example.test", "198.51.100.8", "")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	var rows []models.AuditLog
	require.NoError(t, db.Find(&rows).Error)
	require.Empty(t, rows)

	_, token, _, err = svc.SSOLogin("support-3@support.invalid", "198.51.100.8", "")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "support_access.sign_in", rows[0].Action)
	require.Equal(t, support.String(), *rows[0].UserID)

	_, _, _, err = svc.SSOLogin("support-gone@support.invalid", "198.51.100.8", "")
	require.ErrorIs(t, err, ErrNoSSOAccount)
}
