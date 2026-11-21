//go:build pg

package service

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	vmimport "github.com/maburvm/panel/internal/agent/import"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

func waitImportRead(t *testing.T, s *VirtualizorImportService, id string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for s.readState(id).running {
		require.True(t, time.Now().Before(deadline), "the read did not finish")
		time.Sleep(10 * time.Millisecond)
	}
	require.Empty(t, s.readState(id).err)
}

// seedImportPanel is a panel that already holds some of what the fixture
// master holds: the state fixtureState describes, in tables.
func seedImportPanel(t *testing.T, db *gorm.DB) {
	t.Helper()
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, db.Exec(q, args...).Error, q)
	}
	exec(`INSERT INTO nodes (id, name, ip_address, token) VALUES (?, 'jkt-1', '192.0.2.10', 't1'), (?, 'node-b', '192.0.2.99', 't2')`, fxNodeA, fxNodeB)
	exec(`INSERT INTO os_templates (id, name, version, image_path) VALUES (?, 'Ubuntu', '22.04', '/u.qcow2'), (?, 'Debian', '12', '/d.qcow2'), (?, 'AlmaLinux', '8', '/a.qcow2')`,
		fxUbuntu, fxDebian, fxAlma)
	exec(`INSERT INTO users (id, email, password_hash, role, name) VALUES (?, 'budi@example.com', 'x', 'client', 'Budi'),
		(?, 'staff@example.com', 'x', 'admin', 'Staff'), (?, 'holder@example.com', 'x', 'client', 'Holder')`, fxBudi, fxStaff, fxHolder)
	exec(`INSERT INTO plans (id, name, cpu, ram, disk) VALUES (?, 'Starter', 2, 2048, 40)`, fxStarter)
	exec(`INSERT INTO ip_pools (id, name, family, cidr, gateway) VALUES (?, 'Existing', 'ipv4', '198.51.100.0/24', '198.51.100.1'),
		(?, 'Shared here', 'ipv4', '192.0.2.128/26', '192.0.2.129')`, fxPoolOld, fxPoolSame)
	exec(`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, resources, source_migration) VALUES
		(?, ?, ?, 'app.example.com', ?, '{"cpu":1,"ram":1024,"disk":20}', 'virtualizor'),
		(?, ?, ?, 'other.example.com', ?, '{"cpu":1,"ram":1024,"disk":20}', '')`,
		fxAdopted, fxHolder, fxNodeA, fxAlma, fxOther, fxBudi, fxNodeA, fxUbuntu)
	exec(`UPDATE vms SET billing_ref = 'whmcs-900' WHERE id = ?`, fxOther)
	exec(`INSERT INTO ip_addresses (pool_id, address, family, status, vm_id) VALUES (?, '198.51.100.20', 'ipv4', 'assigned', ?)`, fxPoolOld, fxOther)
}

func newImportServiceForTest(db *gorm.DB) *VirtualizorImportService {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	imports := NewImportService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db),
		repository.NewNetworkRepository(db), nil, logger)
	s := NewVirtualizorImportService(db, imports)
	s.logger = logger
	s.scan = func(_ context.Context, nodeID string) ([]DiscoveredVM, error) {
		for _, sc := range fixtureScans() {
			if sc.NodeID == nodeID {
				out := make([]DiscoveredVM, 0, len(sc.Domains))
				for i := range sc.Domains {
					d := sc.Domains[i]
					if d.Name == "v1001" {
						d.VNC = &vmimport.VNCInfo{Port: 5901, Password: "vncpw-37"}
					}
					out = append(out, DiscoveredVM{Candidate: &d, Valid: true})
				}
				return out, nil
			}
		}
		return nil, nil
	}
	return s
}

func refs(pairs ...string) []ImportItemRef {
	out := make([]ImportItemRef, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, ImportItemRef{Kind: pairs[i], Key: pairs[i+1]})
	}
	return out
}

func resultsByItem(results []ImportApplyResult) map[string]ImportApplyResult {
	out := map[string]ImportApplyResult{}
	for _, r := range results {
		out[linkKey(r.Kind, r.Key)] = r
	}
	return out
}

// The whole import against the recorded master on Postgres: preview, apply in
// parts, the VMs' owners, plans, addresses and billing links, and a second run
// that changes nothing.
func TestVirtualizorImportOnPostgres(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	models.SetSecretKeys("0123456789abcdef0123456789abcdef")
	t.Cleanup(func() { models.SetSecretKeys("") })
	seedImportPanel(t, db)
	master := newVZFixtureMaster(t)
	svc := newImportServiceForTest(db)
	actor := ImportActor{UserID: fxStaff, IP: "192.0.2.50", UserAgent: "test"}

	src, err := svc.CreateSource(ctx, VirtualizorSourceInput{Name: "Old panel", BaseURL: master.URL, APIKey: master.key, APIPass: master.pass})
	require.NoError(t, err)
	var stored struct{ APIKey, APIPass string }
	require.NoError(t, db.Raw(`SELECT api_key, api_pass FROM import_sources WHERE id = ?`, src.ID).Scan(&stored).Error)
	assert.True(t, models.IsSealed(stored.APIKey) && models.IsSealed(stored.APIPass), "the credentials are stored encrypted")
	assert.NotContains(t, stored.APIPass, master.pass)
	_, err = svc.UpdateSource(ctx, src.ID, VirtualizorSourceInput{Name: "Old panel", BaseURL: "https://elsewhere.example.com:4085"})
	require.ErrorIs(t, err, ErrInvalidImportSource, "the saved credentials are not sent to another host")
	_, err = svc.UpdateSource(ctx, src.ID, VirtualizorSourceInput{Name: "Old panel", BaseURL: master.URL + "/index.php"})
	require.NoError(t, err, "the same host keeps them")
	require.NoError(t, db.Raw(`SELECT api_key, api_pass FROM import_sources WHERE id = ?`, src.ID).Scan(&stored).Error)
	assert.True(t, models.IsSealed(stored.APIKey) && models.IsSealed(stored.APIPass), "still encrypted after a change")

	// An unattached floating IP is a customer's: the planner sees it held.
	require.NoError(t, db.Exec(`INSERT INTO ip_addresses (pool_id, address, family, status, delivery_mode) VALUES (?, '198.51.100.30', 'ipv4', 'reserved', 'floating')`, fxPoolOld).Error)
	loaded, err := svc.loadState(ctx, src.ID)
	require.NoError(t, err)
	floating, held := loaded.assigned["198.51.100.30"]
	assert.True(t, held && floating.VMID == "", "%+v", floating)
	assert.Equal(t, fxOther, loaded.assigned["198.51.100.20"].VMID)

	test, err := svc.TestSource(ctx, src.ID)
	require.NoError(t, err)
	assert.True(t, test.OK, test.Message)
	assert.Equal(t, 3, test.Servers)

	_, err = svc.Apply(ctx, src.ID, refs(importKindVM, "37"), actor)
	require.ErrorIs(t, err, ErrImportNoPreview)

	opts := fixtureOptions()
	plan, err := svc.Preview(ctx, src.ID, true, &opts)
	require.NoError(t, err)
	assert.True(t, plan.Reading, "the master is read in the background")
	waitImportRead(t, svc, src.ID)
	plan, err = svc.Preview(ctx, src.ID, false, nil)
	require.NoError(t, err)
	require.True(t, plan.Ready)
	assert.False(t, plan.Reading)
	assert.Equal(t, opts.BillingRefs, plan.Options.BillingRefs, "the options are kept with the source")

	// The preview wrote nothing but the source's own row.
	var users, vms int64
	db.Table("users").Count(&users)
	db.Table("vms").Count(&vms)
	assert.Equal(t, int64(3), users)
	assert.Equal(t, int64(2), vms)

	// First part: the pools and plans. Second: VMs, with one owner left out
	// of the request, which comes along with its VMs.
	first, err := svc.Apply(ctx, src.ID, refs(importKindPool, "1", importKindPool, "2", importKindPool, "4", importKindPlan, "1", importKindPlan, "2"), actor)
	require.NoError(t, err)
	got := resultsByItem(first)
	assert.Equal(t, "created", got["pool|1"].Status, got["pool|1"].Message)
	assert.Equal(t, "created", got["pool|2"].Status, got["pool|2"].Message)
	assert.Equal(t, "linked", got["pool|4"].Status, got["pool|4"].Message)
	assert.Equal(t, "created", got["plan|1"].Status, got["plan|1"].Message)
	assert.Equal(t, "linked", got["plan|2"].Status, got["plan|2"].Message)
	kvmPlan := got["plan|1"].TargetID

	second, err := svc.Apply(ctx, src.ID, refs(importKindVM, "40", importKindVM, "37", importKindVM, "38", importKindUser, "56", importKindUser, "60",
		importKindVM, "39", importKindVM, "77"), actor)
	require.NoError(t, err)
	got = resultsByItem(second)
	assert.Equal(t, importKindUser, second[0].Kind, "owners go in before their VMs")
	ana := got["user|55"]
	assert.Equal(t, "created", ana.Status, "a chosen VM brings its owner")
	assert.Equal(t, "linked", got["user|56"].Status)
	assert.Equal(t, fxBudi, got["user|56"].TargetID)
	assert.Equal(t, "created", got["user|60"].Status)
	assert.Equal(t, "created", got["vm|37"].Status, got["vm|37"].Message)
	assert.Equal(t, "created", got["vm|38"].Status, got["vm|38"].Message)
	assert.Equal(t, "updated", got["vm|40"].Status, got["vm|40"].Message)
	assert.Equal(t, "failed", got["vm|39"].Status)
	assert.Contains(t, got["vm|39"].Message, "198.51.100.20")
	assert.Equal(t, "failed", got["vm|77"].Status)
	assert.Contains(t, got["vm|77"].Message, "not in the preview")

	// The new account: a customer nobody knows the password of, not emailed.
	var acct struct {
		Role, PasswordHash, Name string
	}
	require.NoError(t, db.Raw(`SELECT role, password_hash, name FROM users WHERE id = ?`, ana.TargetID).Scan(&acct).Error)
	assert.Equal(t, "client", acct.Role)
	assert.Equal(t, "Ana Putri", acct.Name)
	assert.Error(t, bcrypt.CompareHashAndPassword([]byte(acct.PasswordHash), []byte("")))
	var mails int64
	db.Table("email_log").Count(&mails)
	assert.Zero(t, mails, "nobody is emailed until the invitations are sent")

	type vmRow struct {
		UserID, NodeID, OSTemplateID, SourceMigration, FirewallDefault string
		PlanID, BillingRef                                             *string
		Suspended                                                      bool
	}
	readVM := func(id string) vmRow {
		var r vmRow
		require.NoError(t, db.Raw(`SELECT user_id, node_id, os_template_id, source_migration, firewall_default, plan_id, billing_ref, suspended
			FROM vms WHERE id = ?`, id).Scan(&r).Error)
		return r
	}
	web := readVM(fxDomainUUID("37"))
	assert.Equal(t, ana.TargetID, web.UserID, "the VM goes to its real owner")
	assert.Equal(t, fxNodeA, web.NodeID)
	assert.Equal(t, fxUbuntu, web.OSTemplateID)
	assert.Equal(t, "virtualizor", web.SourceMigration)
	assert.Equal(t, "accept", web.FirewallDefault)
	require.NotNil(t, web.PlanID)
	assert.Equal(t, kvmPlan, *web.PlanID)
	require.NotNil(t, web.BillingRef)
	assert.Equal(t, "whmcs-1234", *web.BillingRef)
	var vncSealed, snapshot string
	require.NoError(t, db.Raw(`SELECT vnc_password FROM vms WHERE id = ?`, fxDomainUUID("37")).Scan(&vncSealed).Error)
	vncPlain, err := models.OpenSecret(vncSealed)
	require.NoError(t, err)
	assert.Equal(t, "vncpw-37", vncPlain, "the console opens with the password the running domain has")
	require.NoError(t, db.Raw(`SELECT CAST(snapshot AS TEXT) FROM import_sources WHERE id = ?`, src.ID).Scan(&snapshot).Error)
	assert.NotContains(t, snapshot, "vncpw-37", "the snapshot keeps it sealed")

	dbVM := readVM(fxDomainUUID("38"))
	assert.Equal(t, fxBudi, dbVM.UserID)
	assert.Equal(t, fxNodeB, dbVM.NodeID)
	assert.True(t, dbVM.Suspended)
	assert.Equal(t, fxStarter, *dbVM.PlanID)
	assert.Equal(t, "whmcs-77", *dbVM.BillingRef)

	app := readVM(fxAdopted)
	assert.Equal(t, ana.TargetID, app.UserID, "a VM the node import brought in under a placeholder owner moves to its owner")
	assert.Equal(t, kvmPlan, *app.PlanID)

	// Addresses: the VMs' own are assigned to them; a held one whose VM did not
	// come is reserved; free ones are offered; the gateway and an address
	// outside the network are left out.
	var nets []struct {
		IPAddress        string
		BandwidthLimit   int64
		BandwidthQuotaGB int64
	}
	require.NoError(t, db.Raw(`SELECT CAST(ip_address AS TEXT) AS ip_address, bandwidth_limit, bandwidth_quota_gb FROM networks
		WHERE vm_id = ? AND deleted_at IS NULL ORDER BY ip_address`, fxDomainUUID("37")).Scan(&nets).Error)
	require.Len(t, nets, 2)
	assert.Equal(t, int64(100), nets[0].BandwidthLimit)
	assert.Equal(t, int64(1000), nets[0].BandwidthQuotaGB)
	addr := func(ip string) (status, vmID, note string) {
		var r struct {
			Status, Note string
			VMID         *string
		}
		res := db.Raw(`SELECT status, COALESCE(note, '') AS note, CAST(vm_id AS TEXT) AS vm_id FROM ip_addresses WHERE address = ? AND deleted_at IS NULL`, ip).Scan(&r)
		require.NoError(t, res.Error)
		if res.RowsAffected == 0 {
			return "", "", ""
		}
		if r.VMID != nil {
			vmID = *r.VMID
		}
		return r.Status, vmID, r.Note
	}
	st, vm, _ := addr("203.0.113.10")
	assert.Equal(t, "assigned", st)
	assert.Equal(t, fxDomainUUID("37"), vm)
	st, vm, _ = addr("2001:db8:30:1::10")
	assert.Equal(t, "assigned", st)
	assert.Equal(t, fxDomainUUID("37"), vm)
	st, _, _ = addr("203.0.113.11")
	assert.Equal(t, "available", st)
	st, _, note := addr("203.0.113.14")
	assert.Equal(t, "reserved", st, "the panel never hands out an address a source VM still holds")
	assert.Contains(t, note, "v1009")
	st, _, _ = addr("203.0.113.13")
	assert.Equal(t, "reserved", st)
	st, vm, _ = addr("192.0.2.140")
	assert.Equal(t, "assigned", st, "a held address in a linked pool goes to its VM")
	assert.Equal(t, fxAdopted, vm)
	st, _, _ = addr("192.0.2.141")
	assert.Equal(t, "", st, "a linked pool only gets the addresses source VMs hold")
	st, _, _ = addr("203.0.113.1")
	assert.Equal(t, "", st)
	st, _, _ = addr("198.18.0.5")
	assert.Equal(t, "", st)

	var links, audits int64
	db.Table("import_links").Where("source_id = ?", src.ID).Count(&links)
	db.Table("audit_logs").Where("action LIKE 'import.%'").Count(&audits)
	assert.Equal(t, int64(11), links)
	assert.Equal(t, int64(11), audits)

	// Again: everything is skipped and nothing is written.
	again, err := svc.Apply(ctx, src.ID, refs(importKindPool, "1", importKindPlan, "1", importKindUser, "55", importKindVM, "37", importKindVM, "40"), actor)
	require.NoError(t, err)
	for _, r := range again {
		assert.Equal(t, "skipped", r.Status, "%s %s", r.Kind, r.Key)
	}
	var users2, vms2, addrs, addrs2 int64
	db.Table("users").Count(&users2)
	db.Table("vms").Count(&vms2)
	assert.Equal(t, users+2, users2, "ana and cara; budi was already here")
	assert.Equal(t, vms+2, vms2)
	db.Table("ip_addresses").Count(&addrs)

	plan, err = svc.Preview(ctx, src.ID, false, nil)
	require.NoError(t, err)
	assert.Equal(t, ImportActionExists, planItem(t, plan, importKindVM, "37").Action)
	assert.Equal(t, ImportActionCreate, planItem(t, plan, importKindVM, "45").Action)
	assert.Equal(t, int64(2), plan.PendingInvites)

	// A re-read of the master changes nothing that was imported.
	calls := master.callCount()
	_, err = svc.Preview(ctx, src.ID, true, nil)
	require.NoError(t, err)
	waitImportRead(t, svc, src.ID)
	assert.Greater(t, master.callCount(), calls)
	last, err := svc.Apply(ctx, src.ID, refs(importKindVM, "45", importKindPool, "1"), actor)
	require.NoError(t, err)
	got = resultsByItem(last)
	assert.Equal(t, "skipped", got["pool|1"].Status)
	assert.Equal(t, "created", got["user|61"].Status)
	assert.Equal(t, "created", got["vm|45"].Status, got["vm|45"].Message)
	st, vm, _ = addr("203.0.113.14")
	assert.Equal(t, "assigned", st, "the reserved address goes to its VM once that VM comes")
	assert.Equal(t, fxDomainUUID("45"), vm)
	db.Table("ip_addresses").Count(&addrs2)
	assert.Equal(t, addrs, addrs2)

	// A plan the import made, deleted since, is made again and its link follows.
	require.NoError(t, db.Exec(`UPDATE plans SET deleted_at = NOW() WHERE id = ?`, kvmPlan).Error)
	redo, err := svc.Apply(ctx, src.ID, refs(importKindPlan, "1"), actor)
	require.NoError(t, err)
	require.Len(t, redo, 1)
	require.Equal(t, "created", redo[0].Status, redo[0].Message)
	assert.NotEqual(t, kvmPlan, redo[0].TargetID)
	plan, err = svc.Preview(ctx, src.ID, false, nil)
	require.NoError(t, err)
	assert.Equal(t, redo[0].TargetID, planItem(t, plan, importKindPlan, "1").TargetID)
	assert.Equal(t, ImportActionExists, planItem(t, plan, importKindPlan, "1").Action)

	// Invitations: only once email and the panel's address are set, once each.
	_, err = svc.SendInvites(ctx, src.ID, actor)
	require.ErrorIs(t, err, ErrImportNoMail)
	require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('email', ?)
		ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data`,
		`{"smtpHost":"127.0.0.1","smtpPort":1,"smtpFrom":"panel@example.com"}`).Error)
	t.Setenv("PANEL_PUBLIC_URL", "")
	_, err = svc.SendInvites(ctx, src.ID, actor)
	require.ErrorIs(t, err, ErrImportNoPanelURL)
	t.Setenv("PANEL_PUBLIC_URL", "https://panel.example.com/")
	// Two sends at once (a double click) still invite each account once.
	var wg sync.WaitGroup
	sents, errs := make([]int, 2), make([]error, 2)
	for i := range sents {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sents[i], errs[i] = svc.SendInvites(ctx, src.ID, actor)
		}(i)
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, 3, sents[0]+sents[1])
	var recipients []string
	require.NoError(t, db.Raw(`SELECT recipient FROM email_log WHERE template = ? ORDER BY recipient`, EmailAccountImported).Scan(&recipients).Error)
	assert.Equal(t, []string{"ana@example.com", "cara@example.com", "dewi@example.com"}, recipients)
	var tokens int64
	db.Table("password_reset_tokens").Count(&tokens)
	assert.Equal(t, int64(3), tokens)
	sent, err := svc.SendInvites(ctx, src.ID, actor)
	require.NoError(t, err)
	assert.Zero(t, sent, "an account is invited once")

	// The source's list shows what came from it; deleting it keeps all of that.
	list, err := svc.ListSources(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, map[string]int{"pool": 3, "plan": 2, "user": 4, "vm": 4}, list[0].Imported)
	require.NoError(t, svc.DeleteSource(ctx, src.ID))
	db.Table("vms").Count(&vms2)
	assert.Equal(t, vms+3, vms2)
}
