package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// createGroupAndNATTables adds the tables of firewall groups and NAT VMs to a
// hand-rolled test schema.
func createGroupAndNATTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS firewall_groups (id TEXT PRIMARY KEY, user_id TEXT, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS firewall_group_rules (id TEXT PRIMARY KEY, group_id TEXT NOT NULL, protocol TEXT NOT NULL, port_range TEXT NOT NULL DEFAULT '', action TEXT NOT NULL, direction TEXT NOT NULL, source_ip TEXT DEFAULT '0.0.0.0/0', priority INTEGER NOT NULL DEFAULT 100, icmp_type TEXT NOT NULL DEFAULT '', log BOOLEAN NOT NULL DEFAULT 0, family TEXT NOT NULL DEFAULT '', created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS vm_firewall_groups (vm_id TEXT NOT NULL, group_id TEXT NOT NULL, position TEXT NOT NULL DEFAULT 'after', sort_order INTEGER NOT NULL DEFAULT 0, created_at DATETIME, PRIMARY KEY (vm_id, group_id))`,
		`CREATE TABLE IF NOT EXISTS nat_port_blocks (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL UNIQUE, node_id TEXT NOT NULL, start_port INTEGER NOT NULL, port_count INTEGER NOT NULL, created_at DATETIME, UNIQUE (node_id, start_port))`,
		`CREATE TABLE IF NOT EXISTS vm_hostnames (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, hostname TEXT NOT NULL, http_port INTEGER NOT NULL DEFAULT 80, https_port INTEGER NOT NULL DEFAULT 443, verify_token TEXT NOT NULL, verified_at DATETIME, node_error TEXT NOT NULL DEFAULT '', created_at DATETIME, UNIQUE (vm_id, hostname))`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ux_vm_hostnames_verified ON vm_hostnames (hostname) WHERE verified_at IS NOT NULL`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
}

func TestFindPortBlock(t *testing.T) {
	cases := []struct {
		name         string
		lo, hi, size int
		taken        [][2]int
		want         int
		ok           bool
	}{
		{"empty range from the bottom", 20000, 20099, 10, nil, 20000, true},
		{"skips a taken block", 20000, 20099, 10, [][2]int{{20000, 20009}}, 20010, true},
		{"fills a gap that fits", 20000, 20099, 10, [][2]int{{20000, 20009}, {20020, 20029}}, 20010, true},
		{"skips a gap too small", 20000, 20099, 10, [][2]int{{20000, 20004}, {20010, 20019}}, 20020, true},
		{"a single forwarded port splits the range", 20000, 20099, 10, [][2]int{{20003, 20003}}, 20004, true},
		{"order of taken ranges does not matter", 20000, 20099, 5, [][2]int{{20005, 20009}, {20000, 20004}}, 20010, true},
		{"never across a reserved host port", 16500, 16600, 10, nil, 16515, true},
		{"never across the migration ports", 49150, 49300, 10, nil, 49216, true},
		{"full", 20000, 20019, 10, [][2]int{{20000, 20009}, {20010, 20019}}, 0, false},
		{"range smaller than the block", 20000, 20005, 10, nil, 0, false},
		{"zero size", 20000, 20099, 0, nil, 0, false},
	}
	for _, c := range cases {
		got, ok := FindPortBlock(c.lo, c.hi, c.size, c.taken)
		assert.Equal(t, c.ok, ok, c.name)
		assert.Equal(t, c.want, got, c.name)
	}
}

func TestNormalizeHostname(t *testing.T) {
	good := []struct{ in, want string }{
		{"WWW.Example.COM", "www.example.com"},
		{"shop.example.co.uk.", "shop.example.co.uk"},
		{" xn--bcher-kva.example ", "xn--bcher-kva.example"},
		{"a-b.c1.io", "a-b.c1.io"},
	}
	for _, g := range good {
		got, err := NormalizeHostname(g.in)
		require.NoError(t, err, g.in)
		assert.Equal(t, g.want, got)
	}
	for _, bad := range []string{"", "localhost", "203.0.113.5", "*.example.com", "-a.example.com", "a_b.example.com", "example.123", "a..example.com", "ex ample.com"} {
		_, err := NormalizeHostname(bad)
		assert.ErrorIs(t, err, ErrInvalidHostname, bad)
	}
}

// natTestNodes gives the port-forward test schema node rows with a NAT range
// and publish address.
func natTestNodes(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Exec(`CREATE TABLE nodes (id TEXT PRIMARY KEY, nat_port_start INTEGER NOT NULL DEFAULT 20000, nat_port_end INTEGER NOT NULL DEFAULT 29999, nat_address TEXT NOT NULL DEFAULT '', deleted_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO nodes (id, nat_address) VALUES (?, '198.51.100.7'), (?, '')`, pfNode1, pfNode2).Error)
}

// A NAT VM's block is the lowest free run of the node's range: around other
// blocks and around ports already forwarded there.
func TestAllocateNATPortBlockAvoidsWhatIsTaken(t *testing.T) {
	_, db := newPortForwardTestService(t)
	natTestNodes(t, db)
	ctx := context.Background()
	require.NoError(t, db.Exec(`UPDATE nodes SET nat_port_start = 20000, nat_port_end = 20049 WHERE id = ?`, pfNode1).Error)
	require.NoError(t, db.Exec(`INSERT INTO port_forwards (id, vm_id, network_id, node_id, external_port, internal_port, protocol) VALUES ('legacy', 'x', 'n', ?, 20012, 22, 'tcp')`, pfNode1).Error)

	a, err := allocateNATPortBlockTx(ctx, db, pfNode1, pfVMA, 10)
	require.NoError(t, err)
	assert.Equal(t, 20000, a.StartPort)
	b, err := allocateNATPortBlockTx(ctx, db, pfNode1, pfVMB, 10)
	require.NoError(t, err)
	assert.Equal(t, 20013, b.StartPort, "past A's block and the forwarded port")
	_, err = allocateNATPortBlockTx(ctx, db, pfNode1, pfVMC, 30)
	assert.ErrorIs(t, err, ErrNATPortsExhausted)

	require.NoError(t, db.Exec(`UPDATE nodes SET nat_port_start = 0, nat_port_end = 0 WHERE id = ?`, pfNode2).Error)
	_, err = allocateNATPortBlockTx(ctx, db, pfNode2, pfVMC, 10)
	assert.ErrorIs(t, err, ErrNATPortsExhausted, "a node with no range offers no NAT ports")
}

// A NAT VM forwards ports of its own block only; nobody else on the node may
// take a port inside it; another node's ports are its own.
func TestPortForwardsKeepToNATBlocks(t *testing.T) {
	svc, db := newPortForwardTestService(t)
	seedPortForwardVMs(t, db)
	ctx := context.Background()
	require.NoError(t, db.Create(&models.NATPortBlock{VMID: pfVMA, NodeID: pfNode1, StartPort: 20000, PortCount: 10}).Error)

	_, err := svc.AddPortForward(ctx, pfVMA, pfNetA, &AddPortForwardRequest{ExternalPort: 20005, InternalPort: 80})
	require.NoError(t, err)
	_, err = svc.AddPortForward(ctx, pfVMA, pfNetA, &AddPortForwardRequest{ExternalPort: 21000, InternalPort: 80})
	require.ErrorIs(t, err, ErrPortOutsideBlock)
	assert.Contains(t, err.Error(), "20000 to 20009")
	_, err = svc.AddPortForward(ctx, pfVMB, pfNetB, &AddPortForwardRequest{ExternalPort: 20003, InternalPort: 80})
	require.ErrorIs(t, err, ErrPortAlreadyInUse, "a port inside another VM's block")
	_, err = svc.AddPortForward(ctx, pfVMC, pfNetC, &AddPortForwardRequest{ExternalPort: 20003, InternalPort: 80})
	require.NoError(t, err, "the block is on node 1 only")

	// A NAT VM may use every port of its block, once per protocol, past the
	// usual per-VM limit.
	setQuotaSettings(t, db, `{"portForwardMaxPerVm": 2}`)
	for p := 20000; p <= 20009; p++ {
		if p == 20005 {
			continue
		}
		_, err = svc.AddPortForward(ctx, pfVMA, pfNetA, &AddPortForwardRequest{ExternalPort: p, InternalPort: 80})
		require.NoError(t, err, p)
	}
}

type fakeResolver struct{ txt, hosts map[string][]string }

func (f fakeResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	return f.txt[name], nil
}
func (f fakeResolver) LookupHost(_ context.Context, host string) ([]string, error) {
	return f.hosts[host], nil
}

func newNATTestService(t *testing.T) (*NATService, *gorm.DB, *fakeResolver) {
	t.Helper()
	net, db := newPortForwardTestService(t)
	seedPortForwardVMs(t, db)
	natTestNodes(t, db)
	res := &fakeResolver{txt: map[string][]string{}, hosts: map[string][]string{}}
	s := &NATService{db: db, net: net, resolver: res,
		features: func(context.Context, string) ([]string, error) { return []string{featureHostnameProxy}, nil }}
	for _, vm := range []string{pfVMA, pfVMB} {
		require.NoError(t, db.Create(&models.NATPortBlock{VMID: vm, NodeID: pfNode1, StartPort: map[string]int{pfVMA: 20000, pfVMB: 20010}[vm], PortCount: 10}).Error)
	}
	return s, db, res
}

// A hostname is recorded, proven by its TXT record or by pointing at the
// node's address, and only then routed; one VM at a time holds it verified.
func TestHostnameLifecycle(t *testing.T) {
	s, db, res := newNATTestService(t)
	ctx := context.Background()

	_, err := s.AddHostname(ctx, pfVMC, &AddHostnameRequest{Hostname: "c.example.com"})
	require.ErrorIs(t, err, ErrNotNATVM)

	h, err := s.AddHostname(ctx, pfVMA, &AddHostnameRequest{Hostname: "Shop.Example.com"})
	require.NoError(t, err)
	assert.Equal(t, "shop.example.com", h.Hostname)
	assert.Equal(t, 80, h.HTTPPort)
	assert.Equal(t, 443, h.HTTPSPort)
	assert.Len(t, h.VerifyToken, 32)
	_, err = s.AddHostname(ctx, pfVMA, &AddHostnameRequest{Hostname: "shop.example.com"})
	require.ErrorIs(t, err, ErrHostnameTaken, "the same VM twice")

	// Another VM may claim it while it is unproven: squatting a name proves nothing.
	other, err := s.AddHostname(ctx, pfVMB, &AddHostnameRequest{Hostname: "shop.example.com"})
	require.NoError(t, err)

	_, err = s.VerifyHostname(ctx, pfVMA, h.ID)
	require.ErrorIs(t, err, ErrHostnameUnproven)
	assert.Contains(t, err.Error(), HostnameVerifyPrefix+"shop.example.com")
	assert.Contains(t, err.Error(), "198.51.100.7")

	res.txt[HostnameVerifyPrefix+"shop.example.com"] = []string{"something else", "maburvm-verify=" + h.VerifyToken}
	got, err := s.VerifyHostname(ctx, pfVMA, h.ID)
	require.NoError(t, err)
	require.NotNil(t, got.VerifiedAt)

	// B's claim cannot be verified now, whatever its DNS says.
	res.hosts["shop.example.com"] = []string{"198.51.100.7"}
	_, err = s.VerifyHostname(ctx, pfVMB, other.ID)
	require.ErrorIs(t, err, ErrHostnameTaken)
	_, err = s.AddHostname(ctx, pfVMB, &AddHostnameRequest{Hostname: "shop.example.com"})
	require.ErrorIs(t, err, ErrHostnameTaken, "a verified hostname cannot be claimed again")

	// Pointing the name at the node's address proves it too.
	b2, err := s.AddHostname(ctx, pfVMB, &AddHostnameRequest{Hostname: "api.example.com"})
	require.NoError(t, err)
	res.hosts["api.example.com"] = []string{"192.0.2.1", "198.51.100.7"}
	_, err = s.VerifyHostname(ctx, pfVMB, b2.ID)
	require.NoError(t, err)

	// A VM sees and removes only its own hostnames.
	require.ErrorIs(t, s.RemoveHostname(ctx, pfVMB, h.ID), ErrHostnameNotFound)
	require.NoError(t, s.RemoveHostname(ctx, pfVMA, h.ID))
	var n int64
	require.NoError(t, db.Model(&models.VMHostname{}).Where("hostname = ?", "shop.example.com").Count(&n).Error)
	assert.EqualValues(t, 1, n, "B's unproven claim stays")
}

// NAT VMs on a node share its address, so when two accounts add the same
// hostname, pointing it there proves it for neither: only the TXT record does.
// A forward of port 80 made after the hostname was added stops its verifying.
func TestContestedHostnameNeedsTXT(t *testing.T) {
	s, db, res := newNATTestService(t)
	ctx := context.Background()
	require.NoError(t, db.Exec(`UPDATE vms SET user_id = 'other' WHERE id = ?`, pfVMB).Error)
	a, err := s.AddHostname(ctx, pfVMA, &AddHostnameRequest{Hostname: "blog.example.com"})
	require.NoError(t, err)
	b, err := s.AddHostname(ctx, pfVMB, &AddHostnameRequest{Hostname: "blog.example.com"})
	require.NoError(t, err)
	res.hosts["blog.example.com"] = []string{"198.51.100.7"}

	_, err = s.VerifyHostname(ctx, pfVMB, b.ID)
	require.ErrorIs(t, err, ErrHostnameUnproven)
	assert.Contains(t, err.Error(), "another account")
	res.txt[HostnameVerifyPrefix+"blog.example.com"] = []string{"maburvm-verify=" + a.VerifyToken}
	_, err = s.VerifyHostname(ctx, pfVMA, a.ID)
	require.NoError(t, err, "the TXT record settles it")
	_, err = s.VerifyHostname(ctx, pfVMB, b.ID)
	require.ErrorIs(t, err, ErrHostnameTaken)

	c, err := s.AddHostname(ctx, pfVMA, &AddHostnameRequest{Hostname: "www.example.com"})
	require.NoError(t, err)
	res.hosts["www.example.com"] = []string{"198.51.100.7"}
	require.NoError(t, db.Exec(`INSERT INTO port_forwards (id, vm_id, network_id, node_id, external_port, internal_port, protocol) VALUES ('web', ?, 'n', ?, 80, 80, 'tcp')`, pfVMC, pfNode1).Error)
	_, err = s.VerifyHostname(ctx, pfVMA, c.ID)
	require.ErrorIs(t, err, ErrWebPortsForwarded)
}

func TestHostnameRefusals(t *testing.T) {
	s, db, _ := newNATTestService(t)
	ctx := context.Background()
	zero, off := 0, 0
	_, err := s.AddHostname(ctx, pfVMA, &AddHostnameRequest{Hostname: "a.example.com", HTTPPort: &zero, HTTPSPort: &off})
	require.ErrorIs(t, err, ErrInvalidHostname, "both web ports off")

	// The allowance of a VM on no NAT plan.
	for i := 0; i < DefaultNATHostnames; i++ {
		_, err := s.AddHostname(ctx, pfVMA, &AddHostnameRequest{Hostname: string(rune('a'+i)) + ".example.com"})
		require.NoError(t, err)
	}
	_, err = s.AddHostname(ctx, pfVMA, &AddHostnameRequest{Hostname: "z.example.com"})
	var allowance *AllowanceError
	require.True(t, errors.As(err, &allowance), "err = %v", err)

	// A node whose agent cannot route hostnames.
	s.features = func(context.Context, string) ([]string, error) { return []string{"restore_disks"}, nil }
	_, err = s.AddHostname(ctx, pfVMB, &AddHostnameRequest{Hostname: "b.example.com"})
	require.ErrorIs(t, err, ErrNeedsAgentUpdate)

	// A node where staff forward port 80 to a VM.
	s.features = func(context.Context, string) ([]string, error) { return []string{featureHostnameProxy}, nil }
	require.NoError(t, db.Exec(`INSERT INTO port_forwards (id, vm_id, network_id, node_id, external_port, internal_port, protocol) VALUES ('web', ?, 'n', ?, 80, 80, 'tcp')`, pfVMC, pfNode1).Error)
	_, err = s.AddHostname(ctx, pfVMB, &AddHostnameRequest{Hostname: "b.example.com"})
	require.ErrorIs(t, err, ErrWebPortsForwarded)
}

// Port 80 and 443 belong to the hostname router once a hostname is routed on
// the node.
func TestWebPortsAreTheRoutersOnceHostnamesAreRouted(t *testing.T) {
	s, db, _ := newNATTestService(t)
	ctx := context.Background()
	require.NoError(t, db.Exec(`INSERT INTO vm_hostnames (id, vm_id, hostname, verify_token, verified_at) VALUES ('h1', ?, 'x.example.com', 't', CURRENT_TIMESTAMP)`, pfVMA).Error)
	_, err := s.net.AddPortForward(ctx, pfVMC, pfNetC, &AddPortForwardRequest{ExternalPort: 80, InternalPort: 80})
	require.NoError(t, err, "node 2 routes no hostname")
	require.NoError(t, db.Exec(`UPDATE vms SET node_id = ? WHERE id = ?`, pfNode1, pfVMC).Error)
	_, err = s.net.AddPortForward(ctx, pfVMC, pfNetC, &AddPortForwardRequest{ExternalPort: 443, InternalPort: 443})
	require.ErrorIs(t, err, ErrPortAlreadyInUse)
}

func TestNodeNATRange(t *testing.T) {
	s, _, _ := newNATTestService(t)
	ctx := context.Background()
	got, err := s.NodeNAT(ctx, pfNode1)
	require.NoError(t, err)
	assert.Equal(t, NodeNATSettings{PortStart: 20000, PortEnd: 29999, Address: "198.51.100.7", BlocksUsed: 2, PortsUsed: 20}, *got)

	_, err = s.SetNodeNAT(ctx, pfNode1, 20005, 29999)
	require.ErrorIs(t, err, ErrInvalidPortRange, "blocks already handed out must stay inside")
	_, err = s.SetNodeNAT(ctx, pfNode1, 80, 9000)
	require.ErrorIs(t, err, ErrInvalidPortRange)
	_, err = s.SetNodeNAT(ctx, pfNode1, 0, 0)
	require.ErrorIs(t, err, ErrInvalidPortRange, "none, while VMs hold blocks")
	got, err = s.SetNodeNAT(ctx, pfNode2, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, got.PortEnd)
	got, err = s.SetNodeNAT(ctx, pfNode1, 20000, 40000)
	require.NoError(t, err)
	assert.Equal(t, 40000, got.PortEnd)
	_, err = s.NodeNAT(ctx, "no-such-node")
	require.ErrorIs(t, err, ErrNodeNotFound)
}

// The NAT details a VM's owner sees: address, block, the forwarded way in.
func TestNATInfo(t *testing.T) {
	s, db, _ := newNATTestService(t)
	ctx := context.Background()
	require.NoError(t, db.Exec(`INSERT INTO port_forwards (id, vm_id, network_id, node_id, external_port, internal_port, protocol) VALUES ('ssh', ?, ?, ?, 20000, 22, 'tcp')`, pfVMA, pfNetA, pfNode1).Error)
	info, err := s.Info(ctx, pfVMA)
	require.NoError(t, err)
	assert.True(t, info.NAT)
	assert.Equal(t, "198.51.100.7", info.PublicAddress)
	assert.Equal(t, [4]int{20000, 20009, 20000, 22}, [4]int{info.PortStart, info.PortEnd, info.RemotePort, info.RemoteTarget})
	assert.Equal(t, DefaultNATHostnames, info.HostnameLimit)

	info, err = s.Info(ctx, pfVMC)
	require.NoError(t, err)
	assert.False(t, info.NAT)
}

// An order on a NAT plan takes a private address from the node's NAT pool, a
// block of public ports and a forward of the first one to SSH; an order on a
// public plan never takes a NAT address.
func TestCreateVMOnANATPlan(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()
	user, node, tmpl := seedVMIPAMDependencies(t, db)
	for _, stmt := range []string{
		`ALTER TABLE nodes ADD COLUMN nat_port_start INTEGER NOT NULL DEFAULT 20000`,
		`ALTER TABLE nodes ADD COLUMN nat_port_end INTEGER NOT NULL DEFAULT 29999`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	natPool := &models.IPPool{Name: "nat", NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "10.20.0.0/24", Bridge: "natbr0", Gateway: "10.20.0.1", NAT: true}
	public := &models.IPPool{Name: "public", NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Bridge: "br0", Gateway: "203.0.113.1"}
	require.NoError(t, db.Create(natPool).Error)
	require.NoError(t, db.Create(public).Error)
	require.NoError(t, db.Create(&models.IPAddress{PoolID: natPool.ID, Address: "10.20.0.10", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable}).Error)
	require.NoError(t, db.Create(&models.IPAddress{PoolID: public.ID, Address: "203.0.113.10", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable}).Error)
	natPlan := seedPlan(t, db, true)
	require.NoError(t, db.Model(natPlan).Updates(map[string]interface{}{"network_type": models.PlanNetworkNAT, "nat_ports": 10}).Error)
	publicPlan := seedPlan(t, db, true)
	svc := newPolicyVMService(db)

	// A node that offers no NAT ports is not picked for a NAT VM.
	require.NoError(t, db.Exec(`UPDATE nodes SET nat_port_start = 0, nat_port_end = 0`).Error)
	_, err := svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: "nat0.example.test", OSTemplateID: tmpl.ID,
		PlanID: natPlan.ID, PlanRequired: true, AutoAssignIP: true})
	require.ErrorIs(t, err, ErrNoNATPool)
	require.NoError(t, db.Exec(`UPDATE nodes SET nat_port_start = 20000, nat_port_end = 29999`).Error)

	resp, err := svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: "nat1.example.test", OSTemplateID: tmpl.ID,
		PlanID: natPlan.ID, PlanRequired: true, AutoAssignIP: true})
	require.NoError(t, err)
	assert.Equal(t, node.ID, resp.VM.NodeID, "picked for its NAT pool and range")
	var ip string
	require.NoError(t, db.Raw(`SELECT ip_address FROM networks WHERE vm_id = ?`, resp.VM.ID).Scan(&ip).Error)
	assert.Equal(t, "10.20.0.10", ip)
	b, err := natBlock(ctx, db, resp.VM.ID)
	require.NoError(t, err)
	require.NotNil(t, b)
	assert.Equal(t, [3]int{20000, 10, 20009}, [3]int{b.StartPort, b.PortCount, b.EndPort()})
	var fwd models.PortForward
	require.NoError(t, db.Where("vm_id = ?", resp.VM.ID).First(&fwd).Error)
	assert.Equal(t, [2]int{20000, 22}, [2]int{fwd.ExternalPort, fwd.InternalPort})
	assert.Equal(t, node.ID, fwd.NodeID)

	// The NAT pool is now full: a second NAT order is refused, it does not
	// fall back to a public address.
	_, err = svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: "nat2.example.test", OSTemplateID: tmpl.ID,
		NodeID: node.ID, PlanID: natPlan.ID, PlanRequired: true, AutoAssignIP: true})
	require.ErrorContains(t, err, "no IP address available")
	var publicTaken int64
	require.NoError(t, db.Model(&models.IPAddress{}).Where("pool_id = ? AND vm_id IS NOT NULL", public.ID).Count(&publicTaken).Error)
	assert.Zero(t, publicTaken)

	// A public order takes the public address, never the NAT pool.
	require.NoError(t, db.Model(&models.IPAddress{}).Where("pool_id = ?", natPool.ID).Updates(map[string]interface{}{"status": models.IPAddressStatusAvailable, "vm_id": nil}).Error)
	pub, err := svc.CreateVM(ctx, &CreateVMRequest{UserID: user.ID.String(), Hostname: "pub.example.test", OSTemplateID: tmpl.ID,
		NodeID: node.ID, PlanID: publicPlan.ID, PlanRequired: true, AutoAssignIP: true})
	require.NoError(t, err)
	require.NoError(t, db.Raw(`SELECT ip_address FROM networks WHERE vm_id = ?`, pub.VM.ID).Scan(&ip).Error)
	assert.Equal(t, "203.0.113.10", ip)
	b, err = natBlock(ctx, db, pub.VM.ID)
	require.NoError(t, err)
	assert.Nil(t, b)
}
