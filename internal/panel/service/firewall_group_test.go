package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// newGroupTestService has three VMs (A and B owned by user "u", C by "v")
// with firewall rules, groups and plans.
func newGroupTestService(t *testing.T) (*FirewallGroupService, *gorm.DB) {
	t.Helper()
	net, db := newPortForwardTestService(t)
	seedPortForwardVMs(t, db)
	require.NoError(t, db.Exec(`UPDATE vms SET user_id = 'v' WHERE id = ?`, pfVMC).Error)
	for _, stmt := range []string{
		`CREATE TABLE firewall_rules (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, protocol TEXT NOT NULL, port_range TEXT, action TEXT NOT NULL,
			direction TEXT NOT NULL, source_ip TEXT DEFAULT '0.0.0.0/0', priority INTEGER DEFAULT 100,
			icmp_type TEXT NOT NULL DEFAULT '', log BOOLEAN NOT NULL DEFAULT 0, family TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME)`,
		`CREATE TABLE users (id TEXT PRIMARY KEY, email TEXT)`,
		`CREATE TABLE plans (id TEXT PRIMARY KEY, default_firewall_group_id TEXT, deleted_at DATETIME)`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	return NewFirewallGroupService(db, net), db
}

var (
	staffManager = GroupViewer{UserID: "staff", Staff: true, Manage: true}
	staffViewer  = GroupViewer{UserID: "staff2", Staff: true}
	customerU    = GroupViewer{UserID: "u"}
	customerV    = GroupViewer{UserID: "v"}
)

func tcpAllow(port string, priority int) *AddFirewallRuleRequest {
	return &AddFirewallRuleRequest{Protocol: "tcp", PortRange: port, Action: "allow", Direction: "inbound", Priority: priority}
}

// The node gets a VM's own rules and its groups' rules in one order: groups
// placed before, its own rules, groups placed after.
func TestEffectiveRulesMergeGroups(t *testing.T) {
	s, db := newGroupTestService(t)
	ctx := context.Background()
	nets := NewNetworkService(db, repository.NewNetworkRepository(db), repository.NewFirewallRepository(db), repository.NewVMRepository(db), repository.NewNodeRepository(db), nil)
	_, err := nets.AddFirewallRule(ctx, pfVMA, tcpAllow("8080", 10))
	require.NoError(t, err)

	base, err := s.Create(ctx, staffManager, "Baseline", "")
	require.NoError(t, err)
	_, err = s.AddRule(ctx, staffManager, base.ID, &AddFirewallRuleRequest{Protocol: "tcp", PortRange: "25", Action: "deny", Direction: "inbound", Priority: 500})
	require.NoError(t, err)
	web, err := s.Create(ctx, customerU, "Web", "")
	require.NoError(t, err)
	_, err = s.AddRule(ctx, customerU, web.ID, tcpAllow("80,443", 1))
	require.NoError(t, err)

	require.NoError(t, s.Attach(ctx, staffManager, pfVMA, base.ID, models.FirewallGroupBefore))
	require.NoError(t, s.Attach(ctx, customerU, pfVMA, web.ID, ""))

	rules, err := repository.NewFirewallRepository(db).ListEffectiveByVMID(ctx, pfVMA)
	require.NoError(t, err)
	require.Len(t, rules, 3)
	assert.Equal(t, []string{"25", "8080", "80,443"}, []string{rules[0].PortRange, rules[1].PortRange, rules[2].PortRange})
	assert.Equal(t, []int{1, 2, 3}, []int{rules[0].Priority, rules[1].Priority, rules[2].Priority})
	for _, r := range rules {
		assert.Equal(t, pfVMA, r.VMID)
	}
	n, err := repository.NewFirewallRepository(db).CountEffectiveByVMID(ctx, pfVMA)
	require.NoError(t, err)
	assert.EqualValues(t, 3, n)

	attached, err := s.ListForVM(ctx, pfVMA)
	require.NoError(t, err)
	require.Len(t, attached, 2)
	assert.Equal(t, base.ID, attached[0].ID, "before comes first")
	assert.True(t, attached[0].Global)
	assert.False(t, attached[1].Global)
}

// Customers see and change only their own groups, attach them only to their
// own VMs, and cannot take off a group staff placed.
func TestFirewallGroupOwnership(t *testing.T) {
	s, _ := newGroupTestService(t)
	ctx := context.Background()
	mine, err := s.Create(ctx, customerU, "Mine", "")
	require.NoError(t, err)
	require.Equal(t, "u", *mine.UserID)
	global, err := s.Create(ctx, staffManager, "Everyone", "")
	require.NoError(t, err)
	require.Nil(t, global.UserID)

	_, err = s.Create(ctx, staffViewer, "Nope", "")
	require.ErrorIs(t, err, ErrFirewallGroupForbidden, "staff without the right to manage groups")

	list, err := s.List(ctx, customerV, false)
	require.NoError(t, err)
	assert.Empty(t, list, "another customer's groups are invisible")
	_, err = s.Get(ctx, customerV, mine.ID)
	require.ErrorIs(t, err, ErrFirewallGroupNotFound)
	_, err = s.AddRule(ctx, customerV, mine.ID, tcpAllow("22", 1))
	require.ErrorIs(t, err, ErrFirewallGroupNotFound)
	require.ErrorIs(t, s.Attach(ctx, customerV, pfVMC, mine.ID, ""), ErrFirewallGroupNotFound)
	_, err = s.AddRule(ctx, customerU, global.ID, tcpAllow("22", 1))
	require.ErrorIs(t, err, ErrFirewallGroupNotFound, "a customer cannot edit a group for everyone")

	// Staff cannot mix one customer's group into another customer's VM.
	require.ErrorIs(t, s.Attach(ctx, staffManager, pfVMC, mine.ID, ""), ErrInvalidFirewallGroup)
	require.NoError(t, s.Attach(ctx, staffManager, pfVMA, mine.ID, ""))
	require.ErrorIs(t, s.Attach(ctx, customerU, pfVMA, mine.ID, ""), ErrFirewallGroupAttached)

	require.NoError(t, s.Attach(ctx, staffManager, pfVMB, global.ID, models.FirewallGroupBefore))
	require.ErrorIs(t, s.Detach(ctx, customerU, pfVMB, global.ID), ErrFirewallGroupForbidden)
	require.NoError(t, s.Detach(ctx, customerU, pfVMA, mine.ID))
	require.NoError(t, s.Detach(ctx, staffManager, pfVMB, global.ID))

	all, err := s.List(ctx, staffViewer, false)
	require.NoError(t, err)
	assert.Len(t, all, 2)
	globals, err := s.List(ctx, staffViewer, true)
	require.NoError(t, err)
	require.Len(t, globals, 1)
	assert.Equal(t, global.ID, globals[0].ID)
}

// The per-VM rule limit counts group rules: attaching a group or adding a rule
// to an attached group is refused when a VM would pass it.
func TestFirewallGroupsKeepToTheRuleLimit(t *testing.T) {
	s, db := newGroupTestService(t)
	ctx := context.Background()
	setQuotaSettings(t, db, `{"firewallRuleMaxPerVm": 3}`)
	nets := NewNetworkService(db, repository.NewNetworkRepository(db), repository.NewFirewallRepository(db), repository.NewVMRepository(db), repository.NewNodeRepository(db), nil)
	for i := 0; i < 2; i++ {
		_, err := nets.AddFirewallRule(ctx, pfVMA, tcpAllow("22", 10))
		require.NoError(t, err)
	}
	g, err := s.Create(ctx, customerU, "Two", "")
	require.NoError(t, err)
	for _, p := range []string{"80", "443"} {
		_, err := s.AddRule(ctx, customerU, g.ID, tcpAllow(p, 1))
		require.NoError(t, err)
	}
	require.ErrorIs(t, s.Attach(ctx, customerU, pfVMA, g.ID, ""), ErrFirewallRuleLimit, "2 own + 2 grouped > 3")
	require.NoError(t, s.Attach(ctx, customerU, pfVMB, g.ID, ""))
	_, err = s.AddRule(ctx, customerU, g.ID, tcpAllow("8443", 1))
	require.NoError(t, err, "B now holds 3")
	_, err = s.AddRule(ctx, customerU, g.ID, tcpAllow("9000", 1))
	require.ErrorIs(t, err, ErrFirewallRuleLimit, "B would hold 4")
	_, err = nets.AddFirewallRule(ctx, pfVMB, tcpAllow("22", 10))
	require.ErrorIs(t, err, ErrFirewallRuleLimit, "the VM's own rules count the group's")

	_, err = s.AddRule(ctx, customerU, g.ID, &AddFirewallRuleRequest{Protocol: "tcp", PortRange: "99999", Action: "allow", Direction: "inbound", Priority: 1})
	require.ErrorIs(t, err, ErrInvalidFirewallRule)
}

// A group in use, by a VM or as a plan's default, is not deleted from under it.
func TestFirewallGroupDeleteRefusedWhileInUse(t *testing.T) {
	s, db := newGroupTestService(t)
	ctx := context.Background()
	g, err := s.Create(ctx, staffManager, "Default", "")
	require.NoError(t, err)
	_, err = s.AddRule(ctx, staffManager, g.ID, tcpAllow("22", 1))
	require.NoError(t, err)
	require.NoError(t, s.Attach(ctx, staffManager, pfVMA, g.ID, ""))
	require.ErrorIs(t, s.Delete(ctx, staffManager, g.ID), ErrFirewallGroupInUse)

	require.NoError(t, s.Detach(ctx, staffManager, pfVMA, g.ID))
	require.NoError(t, db.Exec(`INSERT INTO plans (id, default_firewall_group_id) VALUES ('p1', ?)`, g.ID).Error)
	require.ErrorIs(t, s.Delete(ctx, staffManager, g.ID), ErrFirewallGroupInUse)

	require.NoError(t, db.Exec(`UPDATE plans SET default_firewall_group_id = NULL`).Error)
	require.NoError(t, s.Delete(ctx, staffManager, g.ID))
	var rules int64
	require.NoError(t, db.Model(&models.FirewallGroupRule{}).Count(&rules).Error)
	assert.Zero(t, rules)
}

// A new VM on a plan with a default group gets the group in place of the
// built-in rules, and its way in is opened if the group does not.
func TestDefaultGroupOpensTheWayIn(t *testing.T) {
	s, db := newGroupTestService(t)
	ctx := context.Background()
	g, err := s.Create(ctx, staffManager, "Web only", "")
	require.NoError(t, err)
	_, err = s.AddRule(ctx, staffManager, g.ID, tcpAllow("80,443", 1))
	require.NoError(t, err)
	vms := newPolicyVMService(db)

	vms.attachDefaultGroup(ctx, pfVMA, g.ID, models.OSTypeWindows)
	var own []models.FirewallRule
	require.NoError(t, db.Where("vm_id = ?", pfVMA).Find(&own).Error)
	require.Len(t, own, 1)
	assert.Equal(t, "3389", own[0].PortRange)

	_, err = s.AddRule(ctx, staffManager, g.ID, tcpAllow("20-30", 2))
	require.NoError(t, err)
	vms.attachDefaultGroup(ctx, pfVMB, g.ID, models.OSTypeLinux)
	var n int64
	require.NoError(t, db.Model(&models.FirewallRule{}).Where("vm_id = ?", pfVMB).Count(&n).Error)
	assert.Zero(t, n, "the group already opens 22")
}

// A VM given to another account keeps what its groups enforce, but the old
// owner's group is replaced by the new owner's copy: the old owner can no
// longer change that VM's firewall. Groups for everyone stay as they are.
func TestOwnerChangeCopiesTheOldOwnersGroups(t *testing.T) {
	s, db := newGroupTestService(t)
	ctx := context.Background()
	mine, err := s.Create(ctx, customerU, "Mine", "kept")
	require.NoError(t, err)
	_, err = s.AddRule(ctx, customerU, mine.ID, tcpAllow("8443", 7))
	require.NoError(t, err)
	global, err := s.Create(ctx, staffManager, "Everyone", "")
	require.NoError(t, err)
	require.NoError(t, s.Attach(ctx, customerU, pfVMA, mine.ID, models.FirewallGroupBefore))
	require.NoError(t, s.Attach(ctx, staffManager, pfVMA, global.ID, ""))
	require.NoError(t, s.Attach(ctx, customerU, pfVMB, mine.ID, ""))
	before, err := repository.NewFirewallRepository(db).ListEffectiveByVMID(ctx, pfVMA)
	require.NoError(t, err)

	require.NoError(t, db.Exec(`UPDATE vms SET user_id = 'v' WHERE id = ?`, pfVMA).Error)
	require.NoError(t, reownVMGroups(ctx, db, pfVMA, "v"))

	attached, err := s.ListForVM(ctx, pfVMA)
	require.NoError(t, err)
	require.Len(t, attached, 2)
	assert.NotEqual(t, mine.ID, attached[0].ID)
	assert.Equal(t, models.FirewallGroupBefore, attached[0].Position)
	assert.Equal(t, global.ID, attached[1].ID)
	copied, err := s.Get(ctx, customerV, attached[0].ID)
	require.NoError(t, err, "the new owner holds the copy")
	assert.Equal(t, "Mine", copied.Name)
	require.Len(t, copied.Rules, 1)
	assert.Equal(t, "8443", copied.Rules[0].PortRange)

	after, err := repository.NewFirewallRepository(db).ListEffectiveByVMID(ctx, pfVMA)
	require.NoError(t, err)
	require.Len(t, after, len(before))
	for i := range before {
		assert.Equal(t, [3]interface{}{before[i].PortRange, before[i].Action, before[i].Priority},
			[3]interface{}{after[i].PortRange, after[i].Action, after[i].Priority}, "enforced unchanged")
	}
	_, err = s.AddRule(ctx, customerU, mine.ID, tcpAllow("22", 1))
	require.NoError(t, err)
	n, err := repository.NewFirewallRepository(db).CountEffectiveByVMID(ctx, pfVMA)
	require.NoError(t, err)
	assert.EqualValues(t, len(before), n, "the old owner's edit no longer reaches the VM")
	stillMine, err := s.ListForVM(ctx, pfVMB)
	require.NoError(t, err)
	assert.Equal(t, mine.ID, stillMine[0].ID, "the old owner's own VM keeps the original")
}

// The owner change UpdateVM makes copies the old owner's groups in the same save.
func TestUpdateVMOwnerChangeReownsGroups(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	createGroupAndNATTables(t, db)
	ctx := context.Background()
	alice, node, tmpl := seedVMIPAMDependencies(t, db)
	bob := &models.User{Email: "bob@example.test", PasswordHash: "hash", Role: models.RoleClient}
	require.NoError(t, db.Create(bob).Error)
	svc := newPolicyVMService(db)
	resp, err := svc.CreateVM(ctx, &CreateVMRequest{UserID: alice.ID.String(), Hostname: "web1", OSTemplateID: tmpl.ID,
		Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, NodeID: node.ID})
	require.NoError(t, err)
	aliceID := alice.ID.String()
	g := &models.FirewallGroup{UserID: &aliceID, Name: "Alice"}
	require.NoError(t, db.Create(g).Error)
	require.NoError(t, db.Create(&models.VMFirewallGroup{VMID: resp.VM.ID, GroupID: g.ID}).Error)

	_, err = svc.UpdateVM(ctx, &UpdateVMRequest{VMID: resp.VM.ID, UserID: bob.ID.String()})
	require.NoError(t, err)
	var owner string
	require.NoError(t, db.Raw(`SELECT g.user_id FROM vm_firewall_groups a JOIN firewall_groups g ON g.id = a.group_id WHERE a.vm_id = ?`, resp.VM.ID).Scan(&owner).Error)
	assert.Equal(t, bob.ID.String(), owner)
}

func TestPortInSpec(t *testing.T) {
	for _, c := range []struct {
		spec string
		port int
		want bool
	}{{"", 22, true}, {"22", 22, true}, {"80,443", 443, true}, {"20-30", 22, true}, {"20-21,23", 22, false}, {"3389", 22, false}, {"x", 22, false}} {
		assert.Equal(t, c.want, portInSpec(c.spec, c.port), "%q %d", c.spec, c.port)
	}
}
