package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func seedPublicPool(t *testing.T, db *gorm.DB, nodeID string, addresses ...string) *models.IPPool {
	t.Helper()
	pool := &models.IPPool{Name: "public-v4", NodeID: &nodeID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Bridge: "viifbr0", Gateway: "203.0.113.1"}
	require.NoError(t, db.Create(pool).Error)
	for _, a := range addresses {
		require.NoError(t, db.Create(&models.IPAddress{PoolID: pool.ID, Address: a, Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable}).Error)
	}
	return pool
}

// CLI-09: a name is unique among one owner's VMs. Two customers may use the
// same name, and neither is told about the other's.
func TestHostnamesAreUniquePerOwner(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	alice, node, tmpl := seedVMIPAMDependencies(t, db)
	bob := &models.User{Email: "bob@example.test", PasswordHash: "hash", Role: models.RoleClient}
	require.NoError(t, db.Create(bob).Error)
	svc := newPolicyVMService(db)
	order := func(userID, hostname string, anyHostname bool) (*CreateVMResponse, error) {
		return svc.CreateVM(ctx, &CreateVMRequest{UserID: userID, Hostname: hostname, OSTemplateID: tmpl.ID,
			Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, NodeID: node.ID, AnyHostname: anyHostname})
	}

	first, err := order(alice.ID.String(), "web1", false)
	require.NoError(t, err)
	_, err = order(bob.ID.String(), "web1", false)
	require.NoError(t, err, "another customer may use the same name")
	_, err = order(alice.ID.String(), "WEB1", false)
	require.ErrorIs(t, err, ErrHostnameExists, "the owner's own duplicate is refused, whatever the case")
	_, err = order(alice.ID.String(), "web1", true)
	require.NoError(t, err, "an order from billing is never refused over a name")

	other, err := order(alice.ID.String(), "db1", false)
	require.NoError(t, err)
	_, err = svc.UpdateVM(ctx, &UpdateVMRequest{VMID: other.VM.ID, Hostname: "web1"})
	require.ErrorIs(t, err, ErrHostnameExists)
	_, err = svc.UpdateVM(ctx, &UpdateVMRequest{VMID: first.VM.ID, Hostname: "web1"})
	require.NoError(t, err, "keeping its own name is not a clash")
	bobs, err := order(bob.ID.String(), "db1", false)
	require.NoError(t, err)
	_, err = svc.UpdateVM(ctx, &UpdateVMRequest{VMID: bobs.VM.ID, Hostname: "mail1"})
	require.NoError(t, err)
}

// BIL-01, BIL-12: an order from billing gets a public address and a root
// password like any other, and keeps its plan and its billing service.
func TestBilledOrderGetsAddressPasswordAndItsLinks(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	plan := seedPlan(t, db, true)
	svc := newPolicyVMService(db)
	order := func() (*CreateVMResponse, error) {
		return svc.CreateVM(ctx, &CreateVMRequest{
			UserID: user.ID.String(), Hostname: "shop.example.test", OSTemplateID: tmpl.ID, PlanID: plan.ID,
			Billed: true, BillingRef: "whmcs-41", AnyHostname: true, AutoAssignIP: true, RegeneratePassword: true,
		})
	}

	_, err := order()
	require.ErrorIs(t, err, ErrNoUsablePool, "no address to give: the order fails and says why")
	var count int64
	require.NoError(t, db.Model(&models.VM{}).Count(&count).Error)
	require.Zero(t, count, "and leaves no VM behind")

	seedPublicPool(t, db, node.ID, "203.0.113.10")
	resp, err := order()
	require.NoError(t, err)
	require.NotEmpty(t, resp.RootPassword, "a root password is generated")
	require.Equal(t, models.Resources{CPU: plan.CPU, RAM: plan.RAM, Disk: plan.Disk}, resp.VM.Resources)

	var network models.Network
	require.NoError(t, db.Where("vm_id = ?", resp.VM.ID).First(&network).Error)
	require.Equal(t, "203.0.113.10", network.IPAddress)
	require.EqualValues(t, plan.BandwidthMbps, network.BandwidthLimit)

	stored, err := svc.GetVMRecord(ctx, resp.VM.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.PlanID)
	require.Equal(t, plan.ID, *stored.PlanID)
	require.True(t, stored.BillingManaged())
	require.Equal(t, "whmcs-41", *stored.BillingRef)

	// An ordinary save of the row does not lose the link.
	_, err = svc.UpdateVM(ctx, &UpdateVMRequest{VMID: resp.VM.ID, Hostname: "renamed.example.test"})
	require.NoError(t, err)
	stored, err = svc.GetVMRecord(ctx, resp.VM.ID)
	require.NoError(t, err)
	require.Equal(t, "whmcs-41", *stored.BillingRef)

	// An operator corrects or clears it.
	_, err = svc.SetBillingLink(ctx, resp.VM.ID, nil, strPtrOrNil("whmcs-42"))
	require.NoError(t, err)
	missing := uuid.NewString()
	_, err = svc.SetBillingLink(ctx, resp.VM.ID, &missing, nil)
	require.ErrorIs(t, err, ErrPlanNotFound)
	// A plan deleted since does not stop the reference being corrected.
	require.NoError(t, db.Exec("DELETE FROM plans WHERE id = ?", plan.ID).Error)
	stored, err = svc.SetBillingLink(ctx, resp.VM.ID, &plan.ID, strPtrOrNil("whmcs-43"))
	require.NoError(t, err)
	require.Equal(t, "whmcs-43", *stored.BillingRef)
	empty := ""
	stored, err = svc.SetBillingLink(ctx, resp.VM.ID, &empty, &empty)
	require.NoError(t, err)
	require.Nil(t, stored.PlanID)
	require.False(t, stored.BillingManaged())
}

// CLI-21: a customer cannot delete a VM a billing service pays for, unless
// the operator allows it. Their other VMs, and administrators, are unaffected.
func TestOwnerCannotDeleteBillingManagedVM(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	svc := newPolicyVMService(db)
	create := func(hostname, ref string) string {
		resp, err := svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: hostname, OSTemplateID: tmpl.ID,
			Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, NodeID: node.ID, BillingRef: ref})
		require.NoError(t, err)
		return resp.VM.ID
	}
	billed, own := create("billed", "whmcs-7"), create("own", "")
	exists := func(id string) bool {
		var n int64
		require.NoError(t, db.Model(&models.VM{}).Where("id = ?", id).Count(&n).Error)
		return n == 1
	}

	require.False(t, ClientMayDeleteBilledVMs(ctx, db), "never saved means no")
	require.ErrorIs(t, svc.DeleteVMByOwner(ctx, billed, user.ID.String()), ErrBillingManaged)
	require.True(t, exists(billed))
	require.ErrorIs(t, svc.DeleteVMByOwner(ctx, billed, uuid.NewString()), ErrVMNotFound, "a stranger learns nothing")
	require.NoError(t, svc.DeleteVMByOwner(ctx, own, user.ID.String()))
	require.False(t, exists(own))

	setQuotaSettings(t, db, `{"clientDeleteBilledEnabled":false}`)
	require.ErrorIs(t, svc.DeleteVMByOwner(ctx, billed, user.ID.String()), ErrBillingManaged)
	setQuotaSettings(t, db, `{"clientDeleteBilledEnabled":true}`)
	require.NoError(t, svc.DeleteVMByOwner(ctx, billed, user.ID.String()))
	require.False(t, exists(billed))

	// The billing system and operators delete through DeleteVM, which has no such rule.
	setQuotaSettings(t, db, `{}`)
	again := create("billed-again", "whmcs-8")
	require.NoError(t, svc.DeleteVM(ctx, again))
	require.False(t, exists(again))
}

// BIL-13: a package change to a plan sets the plan's speed cap, transfer quota
// and over-quota policy, and resets this period's allowance.
func TestApplyPlanLimits(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Exec(`CREATE TABLE bandwidth_usages (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, node_id TEXT NOT NULL, period_start DATETIME NOT NULL, period_end DATETIME NOT NULL, rx_bytes INTEGER DEFAULT 0, tx_bytes INTEGER DEFAULT 0, total_bytes INTEGER DEFAULT 0, quota_bytes INTEGER DEFAULT 0, exceeded BOOLEAN DEFAULT 0, blocked_at DATETIME, last_reported_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error)
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "grow.example.test", OSTemplateID: tmpl.ID, Status: models.VMStatusRunning}
	require.NoError(t, db.Create(vm).Error)
	network := &models.Network{VMID: vm.ID, IPAddress: "203.0.113.20", BandwidthLimit: 50, BandwidthQuotaGB: 100, OverQuotaPolicy: models.OverQuotaThrottle, ThrottleSpeedMbps: 1}
	require.NoError(t, db.Create(network).Error)
	const gb = int64(1) << 30
	now := time.Now()
	period := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(&models.BandwidthUsage{VMID: vm.ID, NodeID: node.ID, PeriodStart: period, PeriodEnd: period.AddDate(0, 1, 0),
		TotalBytes: 150 * gb, QuotaBytes: 100 * gb, Exceeded: true, LastReportedAt: now}).Error)

	plan := seedPlan(t, db, true) // 100 Mbps, 500 GB, stop when over, 5 Mbps throttle
	svc := NewNetworkService(db, repository.NewNetworkRepository(db), repository.NewFirewallRepository(db),
		repository.NewVMRepository(db), repository.NewNodeRepository(db), nil)

	// No queue in this test, so the speed cannot reach the node: the limits are
	// stored all the same and the caller is told the node was not reached.
	err := svc.ApplyPlanLimits(ctx, vm.ID, plan)
	require.ErrorContains(t, err, "could not be sent to the node")

	var got models.Network
	require.NoError(t, db.First(&got, "id = ?", network.ID).Error)
	require.EqualValues(t, 100, got.BandwidthLimit)
	require.EqualValues(t, 500, got.BandwidthQuotaGB)
	require.Equal(t, models.OverQuotaSuspend, got.OverQuotaPolicy)
	require.Equal(t, 5, got.ThrottleSpeedMbps)
	var usage models.BandwidthUsage
	require.NoError(t, db.First(&usage, "vm_id = ?", vm.ID).Error)
	require.Equal(t, 500*gb, usage.QuotaBytes)
	require.False(t, usage.Exceeded, "150 GB used is inside the new 500 GB allowance")

	// A throttled VM keeps its throttle here: the collector lifts it, at the
	// new speed, once the VM is back under its allowance.
	require.NoError(t, db.Model(&models.Network{}).Where("id = ?", network.ID).Update("throttled", true).Error)
	require.NoError(t, svc.ApplyPlanLimits(ctx, vm.ID, plan))

	// A VM with no interface has nothing to limit.
	bare := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "bare.example.test", OSTemplateID: tmpl.ID}
	require.NoError(t, db.Create(bare).Error)
	require.NoError(t, svc.ApplyPlanLimits(ctx, bare.ID, plan))
}

// BIL-21: a suspended account cannot sign in, by password or SSO, until it is
// restored; its VMs follow only when asked. Administrators are not suspended.
func TestAccountSuspension(t *testing.T) {
	t.Setenv("JWT_SECRET_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("AES_KEY", "0123456789abcdef0123456789abcdef")
	db := supportTestDB(t)
	require.NoError(t, db.Exec(`ALTER TABLE vms ADD COLUMN suspended BOOLEAN NOT NULL DEFAULT 0`).Error)
	ctx := context.Background()
	_, node, tmpl := seedVMIPAMDependencies(t, db)
	now := time.Now()
	customer := seedAccount(t, db, "customer@example.test", models.RoleClient, now, nil, "correct horse")
	admin := seedAccount(t, db, "admin@example.test", models.RoleAdmin, now, nil, "x")
	vm := &models.VM{UserID: customer.String(), NodeID: node.ID, Hostname: "held.example.test", OSTemplateID: tmpl.ID, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(vm).Error)
	vms := newPolicyVMService(db)
	users := &UserService{db: db}
	signIn := func() error {
		_, err := users.Login(&LoginRequest{Email: "customer@example.test", Password: "correct horse", ClientIP: "198.51.100.8"})
		return err
	}
	require.NoError(t, signIn())

	_, err := vms.SetAccountSuspended(ctx, admin.String(), true, false)
	require.ErrorIs(t, err, ErrNotACustomerAccount)
	_, err = vms.SetAccountSuspended(ctx, uuid.NewString(), true, false)
	require.ErrorIs(t, err, ErrUserNotFound)

	result, err := vms.SetAccountSuspended(ctx, customer.String(), true, false)
	require.NoError(t, err)
	require.True(t, result.User.Suspended())
	require.Zero(t, result.VMsChanged, "the VMs are left alone unless asked")
	var stored models.User
	require.NoError(t, db.First(&stored, "id = ?", customer.String()).Error)
	require.True(t, stored.Locked())
	require.NotNil(t, stored.TokenRevokedAt, "sessions from before do not come back with the account")
	require.ErrorIs(t, signIn(), ErrAccountSuspended)
	_, err = users.Login(&LoginRequest{Email: "customer@example.test", Password: "wrong", ClientIP: "198.51.100.8"})
	require.ErrorIs(t, err, ErrInvalidCredentials, "only someone with the password is told")
	_, _, _, err = users.SSOLogin("customer@example.test", "198.51.100.8")
	require.ErrorIs(t, err, ErrAccountSuspended)

	// Asked again, with the VMs this time.
	result, err = vms.SetAccountSuspended(ctx, customer.String(), true, true)
	require.NoError(t, err)
	require.Equal(t, 1, result.VMsChanged)
	require.Empty(t, result.VMsFailed)
	held, err := vms.GetVMRecord(ctx, vm.ID)
	require.NoError(t, err)
	require.True(t, held.Suspended)

	// Restored with its VMs: the suspension is lifted even though no node is
	// there to start the VM, and the VM that did not start is named.
	short, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	result, err = vms.SetAccountSuspended(short, customer.String(), false, true)
	require.NoError(t, err)
	require.False(t, result.User.Suspended())
	require.Equal(t, 1, result.VMsChanged)
	require.Equal(t, []string{"held.example.test"}, result.VMsFailed)
	held, err = vms.GetVMRecord(ctx, vm.ID)
	require.NoError(t, err)
	require.False(t, held.Suspended)
	require.NoError(t, signIn())
}
