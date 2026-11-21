package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

func TestPortalSettingsNormalize(t *testing.T) {
	p := PortalSettings{BillingURL: " https://billing.example.com ", ISOs: []PortalISO{{Name: " Debian ", URL: "https://example.com/d.iso"}}}
	require.NoError(t, p.Normalize())
	require.Equal(t, "https://billing.example.com", p.BillingURL)
	require.Equal(t, "Debian", p.ISOs[0].Name)
	_, err := uuid.Parse(p.ISOs[0].ID)
	require.NoError(t, err, "every ISO gets an id")
	id := p.ISOs[0].ID
	require.NoError(t, p.Normalize())
	require.Equal(t, id, p.ISOs[0].ID, "an id stays once given")

	for _, bad := range []PortalSettings{
		{StatusURL: "javascript:alert(1)"},
		{IPRequests: "always"},
		{GraphHistoryDays: 3},
		{GraphHistoryDays: 5000},
		{ISOs: []PortalISO{{Name: "x", URL: "ftp://example.com/x.iso"}}},
		{ISOs: []PortalISO{{Name: "", URL: "https://example.com/x.iso"}}},
	} {
		require.Error(t, bad.Normalize(), "%+v", bad)
	}

	var defaults PortalSettings
	require.True(t, defaults.ISOURLsAllowed(), "own ISO addresses stay allowed until switched off")
	require.Equal(t, IPRequestsApproval, defaults.IPRequestMode())
	require.Equal(t, DefaultGraphHistoryDays, defaults.GraphDays())
	off := false
	require.False(t, PortalSettings{ISOURLAllowed: &off}.ISOURLsAllowed())

	clean, err := ValidatePortalSettings([]byte(`{"statusUrl":"https://status.example.com","ipRequests":"auto","isos":[]}`))
	require.NoError(t, err)
	var back PortalSettings
	require.NoError(t, json.Unmarshal(clean, &back))
	require.Equal(t, IPRequestsAuto, back.IPRequestMode())
}

func TestProfileColumns(t *testing.T) {
	s := func(v string) *string { return &v }
	cols, err := ProfileInput{Name: s("  Dewi   Lestari "), Phone: s("+62 812-0000"), Timezone: s("Asia/Jakarta"), Locale: s("id-ID")}.columns()
	require.NoError(t, err)
	require.Equal(t, map[string]any{"name": "Dewi Lestari", "phone": "+62 812-0000", "timezone": "Asia/Jakarta", "locale": "id-ID"}, cols)
	cols, err = ProfileInput{}.columns()
	require.NoError(t, err)
	require.Empty(t, cols, "nothing given, nothing written")
	for _, bad := range []ProfileInput{{Phone: s("call me")}, {Timezone: s("Mars/Olympus")}, {Timezone: s("Local")}, {Locale: s("english please")}} {
		_, err := bad.columns()
		require.Error(t, err, "%+v", bad)
	}
}

// allowanceDB adds plans, images and addresses to the team schema.
func allowanceDB(t *testing.T) *gorm.DB {
	db := teamTestDB(t)
	for _, ddl := range []string{
		`CREATE TABLE plans (id TEXT PRIMARY KEY, name TEXT, cpu INTEGER, ram INTEGER, disk INTEGER, max_extra_ipv4 INTEGER, max_extra_ipv6 INTEGER,
			max_images INTEGER, max_floating_ips INTEGER, max_extra_disks INTEGER, addon_prices TEXT, deleted_at DATETIME)`,
		`CREATE TABLE networks (id TEXT PRIMARY KEY, vm_id TEXT, deleted_at DATETIME)`,
		`CREATE TABLE ip_requests (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, user_id TEXT NOT NULL, family TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'pending', address TEXT NOT NULL DEFAULT '', note TEXT NOT NULL DEFAULT '', decided_by TEXT, decided_at DATETIME,
			created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE user_notifications (id TEXT PRIMARY KEY, user_id TEXT, kind TEXT, title TEXT, body TEXT, resource_type TEXT,
			resource_id TEXT, read_at DATETIME, created_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(ddl).Error)
	}
	return db
}

func planWith(t *testing.T, db *gorm.DB, col string, v *int) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, db.Exec(`INSERT INTO plans (id, name, cpu, ram, disk, `+col+`) VALUES (?, ?, 1, 1024, 10, ?)`, id, "p-"+id[:4], v).Error)
	return id
}

func TestAccountAllowance(t *testing.T) {
	db := allowanceDB(t)
	ctx := context.Background()
	acct := teamUser(t, db, "a@example.com")
	one, two := 1, 2

	max, err := accountAllowance(ctx, db, acct, planMaxFloatingIPs)
	require.NoError(t, err)
	require.Nil(t, max, "an account with no VMs is not limited by plans")

	a := teamVM(t, db, acct, nil)
	b := teamVM(t, db, acct, nil)
	pOne, pTwo := planWith(t, db, "max_floating_ips", &one), planWith(t, db, "max_floating_ips", &two)
	require.NoError(t, db.Exec(`UPDATE vms SET plan_id = ? WHERE id = ?`, pOne, a.ID).Error)
	max, err = accountAllowance(ctx, db, acct, planMaxFloatingIPs)
	require.NoError(t, err)
	require.Nil(t, max, "a VM on no plan lifts the limit")

	require.NoError(t, db.Exec(`UPDATE vms SET plan_id = ? WHERE id = ?`, pTwo, b.ID).Error)
	max, err = accountAllowance(ctx, db, acct, planMaxFloatingIPs)
	require.NoError(t, err)
	require.Equal(t, 3, *max, "each VM brings its plan's figure")
	require.NoError(t, checkFloatingIPAllowance(ctx, db, acct, 2))
	var allowance *AllowanceError
	require.ErrorAs(t, checkFloatingIPAllowance(ctx, db, acct, 3), &allowance)

	pNone := planWith(t, db, "max_floating_ips", nil)
	require.NoError(t, db.Exec(`UPDATE vms SET plan_id = ? WHERE id = ?`, pNone, b.ID).Error)
	max, err = accountAllowance(ctx, db, acct, planMaxFloatingIPs)
	require.NoError(t, err)
	require.Nil(t, max, "a plan with no limit lifts it")
}

func TestIPRequests(t *testing.T) {
	db := allowanceDB(t)
	ctx := context.Background()
	owner := teamUser(t, db, "owner@example.com")
	staff := teamUser(t, db, "staff@example.com")
	vm := teamVM(t, db, owner, nil)
	require.NoError(t, db.Exec(`INSERT INTO networks (id, vm_id) VALUES (?, ?)`, uuid.NewString(), vm.ID).Error)
	svc := NewIPRequestService(db, nil)
	setMode := func(mode string) {
		require.NoError(t, db.Exec(`DELETE FROM system_settings WHERE section = 'portal'`).Error)
		require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data, updated_at) VALUES ('portal', ?, ?)`,
			`{"ipRequests":"`+mode+`"}`, time.Now()).Error)
	}

	setMode(IPRequestsOff)
	_, err := svc.Request(ctx, vm, owner, IPRequestIPv4, "")
	require.ErrorIs(t, err, ErrIPRequestsOff)

	setMode(IPRequestsApproval)
	_, err = svc.Request(ctx, vm, owner, "ipx", "")
	require.Error(t, err)
	r, err := svc.Request(ctx, vm, owner, IPRequestIPv4, "a second site")
	require.NoError(t, err)
	require.Equal(t, IPRequestPending, r.Status)
	_, err = svc.Request(ctx, vm, owner, IPRequestIPv4, "")
	require.ErrorIs(t, err, ErrIPRequestOpen, "one open request per VM and kind")

	rejected, err := svc.Reject(ctx, r.ID, staff, "Use a floating IP instead")
	require.NoError(t, err)
	require.Equal(t, IPRequestRejected, rejected.Status)
	_, err = svc.Approve(ctx, r.ID, staff)
	require.ErrorIs(t, err, ErrIPRequestDecided, "a decided request is not decided again")
	var notices int64
	db.Table("user_notifications").Where("user_id = ?", owner).Count(&notices)
	require.EqualValues(t, 1, notices, "the owner is told")

	r2, err := svc.Request(ctx, vm, owner, IPRequestIPv4, "")
	require.NoError(t, err)
	require.NoError(t, svc.Cancel(ctx, vm.ID, r2.ID))
	require.ErrorIs(t, svc.Cancel(ctx, vm.ID, r2.ID), ErrIPRequestNotFound)

	// Granted at once only within a plan that sets a number: without one, a
	// customer could empty the pool a request at a time.
	setMode(IPRequestsAuto)
	waiting, err := svc.Request(ctx, vm, owner, IPRequestIPv4, "")
	require.NoError(t, err)
	require.Equal(t, IPRequestPending, waiting.Status)
	require.Contains(t, waiting.Note, "Waiting for staff")
	require.NoError(t, svc.Cancel(ctx, vm.ID, waiting.ID))
	two := 2
	limited := planWith(t, db, "max_extra_ipv4", &two)
	require.NoError(t, db.Exec(`UPDATE vms SET plan_id = ? WHERE id = ?`, limited, vm.ID).Error)
	vm.PlanID = &limited

	// An automatic grant that cannot be made waits for staff, saying why.
	r3, err := svc.Request(ctx, vm, owner, IPRequestIPv4, "")
	require.Error(t, err)
	require.NotNil(t, r3)
	require.Equal(t, IPRequestPending, r3.Status)
	require.Contains(t, r3.Note, "Could not be assigned")

	list, total, err := svc.List(ctx, IPRequestPending, 10, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, r3.ID, list[0].ID)
	require.Equal(t, "owner@example.com", list[0].Email)

	// The plan's allowance is applied before anyone waits.
	zero := 0
	plan := planWith(t, db, "max_extra_ipv4", &zero)
	require.NoError(t, db.Exec(`UPDATE vms SET plan_id = ? WHERE id = ?`, plan, vm.ID).Error)
	vm.PlanID = &plan
	setMode(IPRequestsApproval)
	other := teamVM(t, db, owner, nil)
	require.NoError(t, db.Exec(`UPDATE vms SET plan_id = ? WHERE id = ?`, plan, other.ID).Error)
	other.PlanID = &plan
	require.NoError(t, db.Exec(`INSERT INTO networks (id, vm_id) VALUES (?, ?)`, uuid.NewString(), other.ID).Error)
	var allowance *AllowanceError
	_, err = svc.Request(ctx, other, owner, IPRequestIPv4, "")
	require.ErrorAs(t, err, &allowance)
}

// A plan's extra allowances and add-on prices are checked, saved and read
// back; a plan saved without them leaves the columns alone.
func TestPlanExtras(t *testing.T) {
	db := setupPlanTestDB(t)
	require.NoError(t, db.Exec(`ALTER TABLE plans ADD COLUMN max_images INTEGER`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE plans ADD COLUMN max_extra_disks INTEGER`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE plans ADD COLUMN max_floating_ips INTEGER`).Error)
	require.NoError(t, db.Exec(`ALTER TABLE plans ADD COLUMN addon_prices TEXT NOT NULL DEFAULT '{}'`).Error)
	svc := NewPlanService(repository.NewPlanRepository(db))
	ctx := context.Background()
	two := 2

	_, err := svc.CreatePlan(ctx, &PlanRequest{Name: "A", CPU: 1, RAM: 1024, Disk: 10, AddonPrices: map[string]float64{"snapshot": 1}})
	require.Error(t, err, "a price needs a currency")
	_, err = svc.CreatePlan(ctx, &PlanRequest{Name: "A", CPU: 1, RAM: 1024, Disk: 10, Currency: "USD", AddonPrices: map[string]float64{"gold": 1}})
	require.Error(t, err, "an unknown extra")
	_, err = svc.CreatePlan(ctx, &PlanRequest{Name: "A", CPU: 1, RAM: 1024, Disk: 10, MaxExtraDisks: &[]int{99}[0]})
	require.Error(t, err)

	plan, err := svc.CreatePlan(ctx, &PlanRequest{Name: "A", CPU: 1, RAM: 1024, Disk: 10, Currency: "USD",
		MaxImages: &two, MaxFloatingIPs: &two, AddonPrices: map[string]float64{models.AddonPriceSnapshot: 0.5, models.AddonPriceFloatingIP: 3}})
	require.NoError(t, err)
	got, err := svc.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, 2, *got.MaxImages)
	require.Nil(t, got.MaxExtraDisks)
	require.Equal(t, map[string]float64{"snapshot": 0.5, "floating_ip": 3}, got.AddonPrices)

	_, err = svc.UpdatePlan(ctx, plan.ID, &PlanRequest{Name: "A", CPU: 1, RAM: 1024, Disk: 10})
	require.NoError(t, err)
	got, err = svc.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Nil(t, got.MaxImages, "replaced as a whole, like the other allowances")
	require.Empty(t, got.AddonPrices)
}
