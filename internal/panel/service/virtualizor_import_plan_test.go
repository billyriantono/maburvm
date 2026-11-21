package service

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	vmimport "github.com/maburvm/panel/internal/agent/import"
)

const (
	fxNodeA    = "aaaaaaaa-0000-4000-8000-00000000000a"
	fxNodeB    = "aaaaaaaa-0000-4000-8000-00000000000b"
	fxUbuntu   = "bbbbbbbb-0000-4000-8000-000000000001"
	fxDebian   = "bbbbbbbb-0000-4000-8000-000000000002"
	fxAlma     = "bbbbbbbb-0000-4000-8000-000000000003"
	fxBudi     = "cccccccc-0000-4000-8000-000000000056"
	fxStaff    = "cccccccc-0000-4000-8000-000000000057"
	fxHolder   = "cccccccc-0000-4000-8000-000000000099"
	fxStarter  = "dddddddd-0000-4000-8000-000000000002"
	fxPoolOld  = "eeeeeeee-0000-4000-8000-000000000001"
	fxPoolSame = "eeeeeeee-0000-4000-8000-000000000002"
	// fxAdopted is a source VM brought in earlier by the node import, under a
	// placeholder owner; fxOther is a VM created in this panel.
	fxAdopted = "11111111-1111-4111-8111-000000000040"
	fxOther   = "11111111-1111-4111-8111-0000000000ff"
)

func fxDomainUUID(vpsid string) string { return "11111111-1111-4111-8111-0000000000" + vpsid }

// fixtureSnapshot is the recorded master, read the way Fetch reads it, with
// the libvirt domains found on the two panel nodes.
func fixtureSnapshot(t *testing.T) *VirtualizorSnapshot {
	t.Helper()
	raws := map[string][]json.RawMessage{}
	for _, l := range vzFixtureLists {
		b, err := os.ReadFile(filepath.Join("testdata", "virtualizor", l.act+".json"))
		require.NoError(t, err)
		var doc map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(b, &doc))
		recs, err := records(doc[l.field])
		require.NoError(t, err)
		keys := make([]string, 0, len(recs))
		for k := range recs {
			keys = append(keys, k)
		}
		sortNumeric(keys)
		for _, k := range keys {
			raws[l.act] = append(raws[l.act], recs[k])
		}
	}
	snap, err := buildVirtualizorSnapshot(raws["servers"], raws["users"], raws["plans"], raws["ippool"], raws["ips"], raws["vs"])
	require.NoError(t, err)
	snap.Scans = fixtureScans()
	return snap
}

func fxDomain(name, uuid string, cpu, ramMB int, status string) vmimport.ImportCandidate {
	return vmimport.ImportCandidate{Name: name, UUID: uuid, CPU: cpu, Memory: ramMB, Status: status,
		Disks: []vmimport.DiskInfo{{SourceFile: "/vm/" + name + ".qcow2", Format: "qcow2", Device: "disk", TargetDev: "vda", Size: 20 << 30}}}
}

func fixtureScans() []NodeScan {
	return []NodeScan{
		{NodeID: fxNodeA, Domains: []vmimport.ImportCandidate{
			fxDomain("v1001", strings.ToUpper(fxDomainUUID("37")), 1, 1024, "running"),
			fxDomain("v1003", fxDomainUUID("39"), 1, 1024, "running"),
			fxDomain("v1004", fxAdopted, 1, 1024, "running"),
			fxDomain("v1005", fxDomainUUID("41"), 1, 1024, "running"),
			fxDomain("v1009", fxDomainUUID("45"), 2, 2048, "running"),
			fxDomain("v9999", fxDomainUUID("99"), 1, 512, "shutoff"),
		}},
		{NodeID: fxNodeB, Domains: []vmimport.ImportCandidate{fxDomain("v1002", fxDomainUUID("38"), 2, 2048, "shutoff")}},
	}
}

// fixtureState is a panel that already has some of what the master holds.
func fixtureState() *importState {
	st := &importState{
		nodes:     []stateNode{{ID: fxNodeA, Name: "jkt-1", IP: "192.0.2.10"}, {ID: fxNodeB, Name: "node-b", IP: "192.0.2.99"}},
		templates: []stateTemplate{{ID: fxAlma, Name: "AlmaLinux", Version: "8"}, {ID: fxDebian, Name: "Debian", Version: "12"}, {ID: fxUbuntu, Name: "Ubuntu", Version: "22.04"}},
		users:     map[string]stateUser{}, userByID: map[string]stateUser{},
		plans: []statePlan{{ID: fxStarter, Name: "Starter", CPU: 2, RAM: 2048, Disk: 40}},
		pools: []statePool{{ID: fxPoolOld, Name: "Existing", Family: "ipv4", CIDR: "198.51.100.0/24"},
			{ID: fxPoolSame, Name: "Shared here", Family: "ipv4", CIDR: "192.0.2.128/26"}},
		assigned: map[string]stateHolder{
			"198.51.100.20": {VMID: fxOther, Hostname: "other.example.com"},
			"192.0.2.140":   {VMID: fxAdopted, Hostname: "app.example.com"},
		},
		vms:       map[string]stateVM{},
		hostnames: map[string]string{},
		links:     map[string]importLink{},
	}
	for _, u := range []stateUser{{ID: fxBudi, Email: "budi@example.com"}, {ID: fxStaff, Email: "staff@example.com", Staff: true}, {ID: fxHolder, Email: "holder@example.com"}} {
		st.users[u.Email], st.userByID[u.ID] = u, u
	}
	for _, v := range []stateVM{
		{ID: fxAdopted, UserID: fxHolder, Hostname: "app.example.com", NodeID: fxNodeA, Source: "virtualizor"},
		{ID: fxOther, UserID: fxBudi, Hostname: "other.example.com", NodeID: fxNodeA, BillingRef: "whmcs-900"},
	} {
		st.vms[v.ID] = v
		st.hostnames[hostKey(v.UserID, v.Hostname)] = v.ID
	}
	return st
}

func fixtureOptions() VirtualizorImportOptions {
	return VirtualizorImportOptions{DefaultTemplateID: fxAlma, BillingRefs: map[string]string{"37": "1234", "38": "whmcs-77"}}
}

func planItem(t *testing.T, plan *ImportPlan, kind, key string) ImportItem {
	t.Helper()
	for _, it := range plan.Items {
		if it.Kind == kind && it.Key == key {
			return it
		}
	}
	t.Fatalf("no %s item %s in the plan", kind, key)
	return ImportItem{}
}

func assertBlocked(t *testing.T, it ImportItem, why string) {
	t.Helper()
	assert.Equal(t, ImportActionBlocked, it.Action, "%s %s", it.Kind, it.Key)
	assert.Contains(t, strings.Join(it.Conflicts, " "), why, "%s %s", it.Kind, it.Key)
}

func TestPlanVirtualizorImportMapsEverything(t *testing.T) {
	plan := planVirtualizorImport(fixtureSnapshot(t), fixtureOptions(), fixtureState())

	// Servers go to nodes by address, then by name.
	require.Len(t, plan.Servers, 3)
	assert.Equal(t, ImportServerRow{ID: "0", Name: "localhost", IP: "192.0.2.10", NodeID: fxNodeA, NodeName: "jkt-1", Matched: "auto", VMs: 7}, plan.Servers[0])
	assert.Equal(t, fxNodeB, plan.Servers[1].NodeID)
	assert.Equal(t, "", plan.Servers[2].NodeID)
	assert.Contains(t, strings.Join(plan.Notes, " "), "Server xen-old has 1 VM(s) and is not matched to a node.")

	// The order an apply works in.
	var kinds []string
	for _, it := range plan.Items {
		if len(kinds) == 0 || kinds[len(kinds)-1] != it.Kind {
			kinds = append(kinds, it.Kind)
		}
	}
	assert.Equal(t, []string{importKindPool, importKindPlan, importKindUser, importKindVM}, kinds)

	// Customers.
	ana := planItem(t, plan, importKindUser, "55")
	assert.Equal(t, ImportActionCreate, ana.Action)
	assert.Equal(t, "ana@example.com", ana.Label)
	assert.Equal(t, "Ana Putri", ana.Detail)
	budi := planItem(t, plan, importKindUser, "56")
	assert.Equal(t, ImportActionLink, budi.Action)
	assert.Equal(t, fxBudi, budi.TargetID)
	assertBlocked(t, planItem(t, plan, importKindUser, "57"), "staff account")
	assertBlocked(t, planItem(t, plan, importKindUser, "58"), "no valid email")
	assertBlocked(t, planItem(t, plan, importKindUser, "59"), "User #55 on the source has the same email")
	cara := planItem(t, plan, importKindUser, "60")
	assert.Equal(t, ImportActionCreate, cara.Action)
	assert.Contains(t, strings.Join(cara.Notes, " "), "administrator on the source")
	assert.Equal(t, ImportActionCreate, planItem(t, plan, importKindUser, "61").Action)

	// Plans.
	kvm := planItem(t, plan, importKindPlan, "1")
	assert.Equal(t, ImportActionCreate, kvm.Action)
	assert.Equal(t, &ImportPlanDetail{Name: "KVM 1G", CPU: 1, RAMMB: 1024, DiskGB: 20, BandwidthMbps: 100, DataQuotaGB: 1000}, kvm.Plan)
	starter := planItem(t, plan, importKindPlan, "2")
	assert.Equal(t, ImportActionLink, starter.Action)
	assert.Equal(t, fxStarter, starter.TargetID)
	assert.Contains(t, strings.Join(starter.Notes, " "), "2 vCPU, 2048 MB RAM, 40 GB disk", "a size difference is pointed out")
	assertBlocked(t, planItem(t, plan, importKindPlan, "3"), "only KVM plans")
	assertBlocked(t, planItem(t, plan, importKindPlan, "4"), "at least 128 MB")

	// IP pools.
	v4 := planItem(t, plan, importKindPool, "1")
	assert.Equal(t, ImportActionCreate, v4.Action)
	assert.Equal(t, &ImportPoolDetail{CIDR: "203.0.113.0/24", Family: "ipv4", Gateway: "203.0.113.1", DNS: []string{"9.9.9.9", "149.112.112.112"},
		NodeIDs: []string{fxNodeA, fxNodeB}, Total: 6, InUse: 4}, v4.Pool, "offered on every node a VM holding one of its addresses runs on")
	assert.Contains(t, strings.Join(v4.Notes, " "), "1 of its addresses lie outside 203.0.113.0/24")
	v6 := planItem(t, plan, importKindPool, "2")
	assert.Equal(t, ImportActionCreate, v6.Action)
	assert.Equal(t, "2001:db8:30:1::/64", v6.Pool.CIDR)
	assert.Equal(t, "ipv6", v6.Pool.Family)
	assertBlocked(t, planItem(t, plan, importKindPool, "3"), "overlaps the panel's pool Existing (198.51.100.0/24)")
	shared := planItem(t, plan, importKindPool, "4")
	assert.Equal(t, ImportActionLink, shared.Action)
	assert.Equal(t, fxPoolSame, shared.TargetID)

	// VMs.
	web := planItem(t, plan, importKindVM, "37")
	assert.Equal(t, ImportActionCreate, web.Action, web.Conflicts)
	assert.Equal(t, &ImportVMDetail{OwnerKey: "55", OwnerLabel: "ana@example.com", PlanKey: "1", PlanLabel: "KVM 1G", NodeID: fxNodeA, NodeName: "jkt-1",
		DomainUUID: fxDomainUUID("37"), Hostname: "web.example.com", TemplateID: fxUbuntu, TemplateLabel: "Ubuntu 22.04",
		IPs: []string{"203.0.113.10", "2001:db8:30:1::10"}, BillingRef: "whmcs-1234"}, web.VM, "a bare service id becomes the reference the billing module sends")
	assert.Contains(t, strings.Join(web.Notes, " "), "Port speed 100 Mbit/s and monthly transfer 1000 GB")

	db := planItem(t, plan, importKindVM, "38")
	assert.Equal(t, ImportActionCreate, db.Action, db.Conflicts)
	assert.Equal(t, fxNodeB, db.VM.NodeID)
	assert.Equal(t, fxDebian, db.VM.TemplateID)
	assert.True(t, db.VM.Suspended)
	assert.Equal(t, "whmcs-77", db.VM.BillingRef)

	assertBlocked(t, planItem(t, plan, importKindVM, "39"), "Address 198.51.100.20 is assigned here to VM other.example.com")

	app := planItem(t, plan, importKindVM, "40")
	assert.Equal(t, ImportActionUpdate, app.Action, app.Conflicts)
	assert.Equal(t, fxAdopted, app.TargetID)
	assert.Contains(t, strings.Join(app.Notes, " "), "its owner becomes ana@example.com")
	assert.Contains(t, strings.Join(app.Notes, " "), "its plan is set to KVM 1G")

	assertBlocked(t, planItem(t, plan, importKindVM, "41"), "Its owner staff@example.com cannot be imported")
	assertBlocked(t, planItem(t, plan, importKindVM, "42"), "not matched to a node")
	assertBlocked(t, planItem(t, plan, importKindVM, "43"), "only KVM VMs")
	assertBlocked(t, planItem(t, plan, importKindVM, "44"), "No libvirt domain named v1008")

	shop := planItem(t, plan, importKindVM, "45")
	assert.Equal(t, ImportActionCreate, shop.Action, shop.Conflicts)
	assert.Equal(t, "dewi@example.com", shop.VM.OwnerLabel)
	assert.Contains(t, strings.Join(shop.Notes, " "), "It gets no plan")
}

func TestPlanVirtualizorImportChoicesAndConflicts(t *testing.T) {
	t.Run("a server mapped by hand, and one left out", func(t *testing.T) {
		opts := fixtureOptions()
		opts.NodeMap = map[string]string{"2": fxNodeB, "1": importNodeSkip}
		plan := planVirtualizorImport(fixtureSnapshot(t), opts, fixtureState())
		assert.Equal(t, "chosen", plan.Servers[2].Matched)
		assert.Equal(t, "skipped", plan.Servers[1].Matched)
		assertBlocked(t, planItem(t, plan, importKindVM, "42"), "No libvirt domain named v1006 (or with its UUID) runs on node node-b")
		assertBlocked(t, planItem(t, plan, importKindVM, "38"), "not matched to a node")
	})

	t.Run("a plan created even though one of its name exists", func(t *testing.T) {
		opts := fixtureOptions()
		opts.PlanMap = map[string]string{"2": importPlanNew, "1": fxStarter}
		plan := planVirtualizorImport(fixtureSnapshot(t), opts, fixtureState())
		starter := planItem(t, plan, importKindPlan, "2")
		assert.Equal(t, ImportActionCreate, starter.Action)
		assert.Equal(t, "Starter (imported)", starter.Plan.Name)
		kvm := planItem(t, plan, importKindPlan, "1")
		assert.Equal(t, ImportActionLink, kvm.Action)
		assert.Equal(t, fxStarter, kvm.TargetID)
	})

	t.Run("billing references held twice", func(t *testing.T) {
		opts := fixtureOptions()
		opts.BillingRefs = map[string]string{"37": "55", "45": "whmcs-55", "38": "900"}
		plan := planVirtualizorImport(fixtureSnapshot(t), opts, fixtureState())
		assert.Equal(t, ImportActionCreate, planItem(t, plan, importKindVM, "37").Action)
		assertBlocked(t, planItem(t, plan, importKindVM, "45"), "Source VM #37 has the same billing reference whmcs-55")
		assertBlocked(t, planItem(t, plan, importKindVM, "38"), "billing reference whmcs-900 is already on VM other.example.com")
	})

	t.Run("an address that is a floating IP here, and one two source VMs claim", func(t *testing.T) {
		st := fixtureState()
		st.assigned["203.0.113.12"] = stateHolder{}
		snap := fixtureSnapshot(t)
		require.Equal(t, "45", snap.VMs[8].ID)
		snap.VMs[8].IPs = append(snap.VMs[8].IPs, "203.0.113.10")
		plan := planVirtualizorImport(snap, fixtureOptions(), st)
		assertBlocked(t, planItem(t, plan, importKindVM, "38"), "Address 203.0.113.12 is a customer's floating IP here")
		assert.Equal(t, ImportActionCreate, planItem(t, plan, importKindVM, "37").Action)
		assertBlocked(t, planItem(t, plan, importKindVM, "45"), "Source VM #37 holds address 203.0.113.10 too")
	})

	t.Run("a name the owner already uses, and a VM made in this panel", func(t *testing.T) {
		st := fixtureState()
		st.hostnames[hostKey(fxBudi, "DB.example.com")] = fxOther
		snap := fixtureSnapshot(t)
		snap.Scans[0].Domains[1].UUID = fxOther // v1003 turns out to be a panel VM
		plan := planVirtualizorImport(snap, fixtureOptions(), st)
		assertBlocked(t, planItem(t, plan, importKindVM, "38"), "budi@example.com already has a VM named db.example.com")
		assertBlocked(t, planItem(t, plan, importKindVM, "39"), "was created in this panel")
	})

	t.Run("no template to record", func(t *testing.T) {
		opts := fixtureOptions()
		opts.DefaultTemplateID = ""
		st := fixtureState()
		st.templates = st.templates[:1] // AlmaLinux 8 only
		plan := planVirtualizorImport(fixtureSnapshot(t), opts, st)
		assertBlocked(t, planItem(t, plan, importKindVM, "37"), `"ubuntu-22.04-x86_64" matches no template`)
		assert.Contains(t, strings.Join(plan.Notes, " "), "Choose a default template")
	})

	t.Run("a node whose scan failed", func(t *testing.T) {
		snap := fixtureSnapshot(t)
		snap.Scans[1] = NodeScan{NodeID: fxNodeB, Error: "agent unreachable"}
		plan := planVirtualizorImport(snap, fixtureOptions(), fixtureState())
		assertBlocked(t, planItem(t, plan, importKindVM, "38"), "Node node-b could not be scanned: agent unreachable")
		assert.Equal(t, "agent unreachable", plan.Servers[1].ScanError)
	})
}

// Once imported, every record is linked and the plan has nothing left to do.
func TestPlanVirtualizorImportAfterApply(t *testing.T) {
	snap := fixtureSnapshot(t)
	st := fixtureState()
	first := planVirtualizorImport(snap, fixtureOptions(), st)
	n := 0
	for _, it := range first.Items {
		if it.Action == ImportActionBlocked {
			continue
		}
		target := it.TargetID
		switch it.Kind {
		case importKindUser:
			if target == "" {
				target = "cccccccc-1111-4000-8000-0000000000" + it.Key
				st.userByID[target] = stateUser{ID: target, Email: it.Label}
				st.users[it.Label] = st.userByID[target]
			}
		case importKindPlan:
			if target == "" {
				target = "dddddddd-1111-4000-8000-00000000000" + it.Key
				st.plans = append(st.plans, statePlan{ID: target, Name: it.Plan.Name})
			}
		case importKindPool:
			if target == "" {
				target = "eeeeeeee-1111-4000-8000-00000000000" + it.Key
				st.pools = append(st.pools, statePool{ID: target, Name: it.Label, Family: it.Pool.Family, CIDR: it.Pool.CIDR})
			}
		case importKindVM:
			target = it.VM.DomainUUID
			st.vms[target] = stateVM{ID: target, Hostname: it.VM.Hostname, Source: "virtualizor"}
		}
		st.links[linkKey(it.Kind, it.Key)] = importLink{Kind: it.Kind, SourceKey: it.Key, TargetID: target}
		n++
	}
	require.Equal(t, 13, n)

	again := planVirtualizorImport(snap, fixtureOptions(), st)
	exists := 0
	for _, it := range again.Items {
		switch it.Action {
		case ImportActionExists:
			exists++
		case ImportActionBlocked:
		default:
			t.Errorf("%s %s would be imported again: %s", it.Kind, it.Key, it.Action)
		}
	}
	assert.Equal(t, n, exists)
}

func TestSourcePoolNetwork(t *testing.T) {
	cases := []struct {
		pool         SourcePool
		cidr, family string
		err          string
	}{
		{SourcePool{Gateway: "203.0.113.1", Netmask: "255.255.255.0"}, "203.0.113.0/24", "ipv4", ""},
		{SourcePool{Gateway: "203.0.113.65", Netmask: "/26"}, "203.0.113.64/26", "ipv4", ""},
		{SourcePool{Gateway: "2001:db8:30:1::1", Netmask: "64"}, "2001:db8:30:1::/64", "ipv6", ""},
		{SourcePool{Netmask: "255.255.255.248", Addresses: []SourceAddress{{IP: "192.0.2.9"}}}, "192.0.2.8/29", "ipv4", ""},
		{SourcePool{Netmask: "24"}, "", "", "no gateway and no addresses"},
		{SourcePool{Gateway: "192.0.2.1", Netmask: "255.0.255.0"}, "", "", "not a valid mask"},
		{SourcePool{Gateway: "192.0.2.1", Netmask: "33"}, "", "", "out of range"},
		{SourcePool{Gateway: "2001:db8::1", Netmask: "255.255.255.0"}, "", "", "cannot be read"},
	}
	for _, tc := range cases {
		cidr, family, err := sourcePoolNetwork(tc.pool)
		if tc.err != "" {
			assert.ErrorContains(t, err, tc.err, "%+v", tc.pool)
			continue
		}
		require.NoError(t, err, "%+v", tc.pool)
		assert.Equal(t, tc.cidr, cidr)
		assert.Equal(t, tc.family, family)
	}
}

func TestMatchTemplate(t *testing.T) {
	templates := []stateTemplate{{ID: "u20", Name: "Ubuntu", Version: "20.04"}, {ID: "u22", Name: "Ubuntu", Version: "22.04"},
		{ID: "d12", Name: "Debian 12", Version: ""}, {ID: "win", Name: "Windows Server", Version: "2022"}, {ID: "c", Name: "CentOS", Version: ""}}
	cases := []struct{ os, want string }{
		{"ubuntu-22.04-x86_64", "u22"},
		{"Ubuntu 20.04", "u20"},
		{"debian-12-x86_64", "d12"},
		{"windows-server-2022", "win"},
		{"centos-7-x86_64", "c"}, // a template with no version takes every version
		{"ubuntu-18.04", ""},     // a versioned one only its own
		{"", ""},
	}
	for _, tc := range cases {
		got := ""
		if m := matchTemplate(tc.os, templates); m != nil {
			got = m.ID
		}
		assert.Equal(t, tc.want, got, tc.os)
	}
}

func TestImportUnits(t *testing.T) {
	for _, tc := range []struct {
		kbs  int64
		mbit int
	}{{0, 0}, {-1, 0}, {12800, 100}, {128000, 1000}, {64, 1}} {
		assert.Equal(t, tc.mbit, importSpeedMbps(tc.kbs), tc.kbs)
	}
	for _, tc := range []struct{ in, out string }{{" 1234 ", "whmcs-1234"}, {"whmcs-77", "whmcs-77"}, {"INV-9", "INV-9"}, {"", ""}} {
		assert.Equal(t, tc.out, NormalizeBillingRef(tc.in), tc.in)
	}
}

// A read that panics ends as a failed read the wizard shows, not a panel crash
// or a read left running forever.
func TestImportReadPanicBecomesError(t *testing.T) {
	s := &VirtualizorImportService{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), reads: map[string]*importRead{}}
	id := "00000000-0000-4000-8000-000000000001"
	s.startRead(id, true) // no database: getSource dereferences nil
	require.Eventually(t, func() bool { return !s.readState(id).running }, 5*time.Second, 10*time.Millisecond)
	assert.Contains(t, s.readState(id).err, "internal error")
}
