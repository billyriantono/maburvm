//go:build pg

package service

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

const (
	axClient   = "a1111111-1111-1111-1111-111111111111"
	axOther    = "a2222222-2222-2222-2222-222222222222"
	axOptedOut = "a3333333-3333-3333-3333-333333333333"
	axRegion   = "a4444444-4444-4444-4444-444444444444"
	axNodeA    = "a5555555-5555-5555-5555-555555555555"
	axNodeB    = "a6666666-6666-6666-6666-666666666666"
	axPlan     = "a7777777-7777-7777-7777-777777777777"
	axTmpl     = "a8888888-8888-8888-8888-888888888888"
	axVMA      = "a9999999-9999-9999-9999-999999999991"
	axVMB      = "a9999999-9999-9999-9999-999999999992"
	axPool     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
)

func seedAdminExtras(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, s := range []string{
		`INSERT INTO users (id, email, name, password_hash, role) VALUES
			('` + axClient + `', 'ana@example.test', 'Ana', 'x', 'client'),
			('` + axOther + `', 'ben@example.test', 'Ben', 'x', 'client'),
			('` + axOptedOut + `', 'cy@example.test', 'Cy', 'x', 'client')`,
		`UPDATE users SET marketing_opt_out = true WHERE id = '` + axOptedOut + `'`,
		`INSERT INTO regions (id, slug, name, country) VALUES ('` + axRegion + `', 'jkt', 'Jakarta', 'ID')`,
		`INSERT INTO nodes (id, name, ip_address, token, status, region_id, cpu_count, memory_total_bytes, node_group) VALUES
			('` + axNodeA + `', 'node-a', '192.0.2.1', 'ta', 'active', '` + axRegion + `', 16, 68719476736, 'nvme'),
			('` + axNodeB + `', 'node-b', '192.0.2.2', 'tb', 'active', NULL, 8, 34359738368, '')`,
		`INSERT INTO plans (id, name, cpu, ram, disk, node_group) VALUES ('` + axPlan + `', 'fast', 2, 2048, 40, 'nvme')`,
		`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES ('` + axTmpl + `', 'debian', '12', '/x', 'c')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, resources, plan_id) VALUES
			('` + axVMA + `', '` + axClient + `', '` + axNodeA + `', 'web', '` + axTmpl + `', '{"cpu":2,"ram":2048,"disk":40}', '` + axPlan + `'),
			('` + axVMB + `', '` + axOther + `', '` + axNodeB + `', 'db', '` + axTmpl + `', '{"cpu":4,"ram":4096,"disk":80}', NULL)`,
		`INSERT INTO storage_pools (id, name, type, path, node_id, total_space, used_space) VALUES
			('` + axPool + `', 'default', 'dir', '/vm', '` + axNodeA + `', 1099511627776, 107374182400)`,
		`INSERT INTO node_metrics (node_id, cpu_usage, memory_usage, status, recorded_at) VALUES
			('` + axNodeA + `', 40, 60, 'online', NOW()), ('` + axNodeA + `', 20, 40, 'online', NOW()), ('` + axNodeA + `', 0, 0, 'offline', NOW())`,
	} {
		require.NoError(t, db.Exec(s).Error, s)
	}
}

func TestAdminExtrasOnPostgres(t *testing.T) {
	db := migrationDB(t)
	ctx := context.Background()
	seedAdminExtras(t, db)
	vms := NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db), repository.NewTemplateRepository(db), nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	t.Run("tags and the list filter", func(t *testing.T) {
		tags, err := vms.SetTags(ctx, axVMA, []string{"Web", "env:prod"})
		require.NoError(t, err)
		assert.Equal(t, []string{"env:prod", "web"}, tags)
		vm, err := vms.GetVMRecord(ctx, axVMA)
		require.NoError(t, err)
		assert.Equal(t, []string{"env:prod", "web"}, vm.Tags)
		got, err := vms.ListVMs(ctx, &ListVMsRequest{Tag: "web"})
		require.NoError(t, err)
		require.Len(t, got.VMs, 1)
		assert.Equal(t, axVMA, got.VMs[0].ID)
		got, err = vms.ListVMs(ctx, &ListVMsRequest{Tag: "we"})
		require.NoError(t, err)
		assert.Empty(t, got.VMs, "a tag matches whole")
	})

	t.Run("delete lock", func(t *testing.T) {
		lock, err := vms.SetDeleteLock(ctx, axVMA, true, false)
		require.NoError(t, err)
		assert.Equal(t, models.VMDeleteLockOwner, lock)
		assert.ErrorIs(t, vms.DeleteVM(ctx, axVMA), ErrVMDeleteLocked)
		_, err = vms.RebuildVM(ctx, &RebuildVMRequest{VMID: axVMA})
		assert.ErrorIs(t, err, ErrVMDeleteLocked)

		lock, err = vms.SetDeleteLock(ctx, axVMA, true, true)
		require.NoError(t, err)
		assert.Equal(t, models.VMDeleteLockStaff, lock)
		_, err = vms.SetDeleteLock(ctx, axVMA, false, false)
		assert.ErrorIs(t, err, ErrVMDeleteLocked, "the owner cannot lift a staff lock")
		assert.ErrorIs(t, vms.DeleteVM(AsBillingSystem(ctx), axVMA), ErrVMDeleteLocked, "a staff lock stops the billing system too")

		lock, err = vms.SetDeleteLock(ctx, axVMA, false, true)
		require.NoError(t, err)
		assert.Equal(t, "", lock)
	})

	t.Run("staff notes", func(t *testing.T) {
		n, err := AddStaffNote(ctx, db, NoteOnVM, axVMA, "  Customer asked for a reverse DNS change.  ", axOther, "staff@example.test")
		require.NoError(t, err)
		assert.Equal(t, "Customer asked for a reverse DNS change.", n.Body)
		_, err = AddStaffNote(ctx, db, NoteOnUser, axClient, "VIP", "", "")
		require.NoError(t, err)
		list, err := ListStaffNotes(ctx, db, NoteOnVM, axVMA)
		require.NoError(t, err)
		require.Len(t, list, 1)
		_, err = UpdateStaffNote(ctx, db, NoteOnUser, axClient, n.ID, "moved")
		assert.ErrorIs(t, err, ErrNoteNotFound, "a note is changed only on its own subject")
		up, err := UpdateStaffNote(ctx, db, NoteOnVM, axVMA, n.ID, "Done.")
		require.NoError(t, err)
		assert.Equal(t, "Done.", up.Body)
		require.NoError(t, DeleteStaffNote(ctx, db, NoteOnVM, axVMA, n.ID))
		assert.ErrorIs(t, DeleteStaffNote(ctx, db, NoteOnVM, axVMA, n.ID), ErrNoteNotFound)
		_, err = AddStaffNote(ctx, db, NoteOnVM, axVMA, " ", "", "")
		assert.Error(t, err)
	})

	t.Run("node groups", func(t *testing.T) {
		assert.Equal(t, "nvme", planNodeGroup(ctx, db, axPlan))
		assert.Equal(t, "", planNodeGroup(ctx, db, ""))
		groups, err := ListNodeGroups(ctx, db)
		require.NoError(t, err)
		require.Len(t, groups, 1)
		assert.Equal(t, NodeGroupSummary{Name: "nvme", Nodes: []string{"node-a"}, Regions: []string{"Jakarta"}, Plans: 1}, groups[0])

		ps := NewPlanService(repository.NewPlanRepository(db))
		empty := ""
		p, err := ps.UpdatePlan(ctx, axPlan, &PlanRequest{Name: "fast", CPU: 2, RAM: 2048, Disk: 40, NodeGroup: &empty})
		require.NoError(t, err)
		assert.Equal(t, "", p.NodeGroup)
		assert.Equal(t, "", planNodeGroup(ctx, db, axPlan))
		bad := "no spaces"
		_, err = ps.UpdatePlan(ctx, axPlan, &PlanRequest{Name: "fast", CPU: 2, RAM: 2048, Disk: 40, NodeGroup: &bad})
		assert.ErrorIs(t, err, ErrInvalidNodeGroup)

		ns := NewNodeService(repository.NewNodeRepository(db), db)
		g := "HDD"
		node, err := ns.UpdateNode(ctx, axNodeB, &UpdateNodeRequest{NodeGroup: &g})
		require.NoError(t, err)
		assert.Equal(t, "hdd", node.NodeGroup)
	})

	t.Run("capacity and usage reports", func(t *testing.T) {
		require.NoError(t, RecordCapacity(ctx, db))
		require.NoError(t, RecordCapacity(ctx, db), "a second run the same day rewrites the day's row")
		nodes, err := CapacityReport(ctx, db, CapacityByNode, 30)
		require.NoError(t, err)
		require.Len(t, nodes, 2)
		a := nodes[0]
		assert.Equal(t, "node-a", a.Name)
		require.Len(t, a.Points, 1)
		assert.Equal(t, CapacityPoint{Day: time.Now().Format("2006-01-02"), VMs: 1, VCPUAllocated: 2, VCPUTotal: 16,
			RAMAllocatedMB: 2048, RAMTotalMB: 65536, DiskAllocatedGB: 40, DiskTotalGB: 1024, DiskUsedGB: 100,
			CPUUsage: 30, MemoryUsage: 50}, a.Points[0], "the offline sample is left out of the average")

		pools, err := CapacityReport(ctx, db, CapacityByPool, 30)
		require.NoError(t, err)
		require.Len(t, pools, 1)
		assert.Equal(t, "node-a / default", pools[0].Name)
		assert.Equal(t, int64(1024), pools[0].Points[0].DiskTotalGB)

		regions, err := CapacityReport(ctx, db, CapacityByRegion, 30)
		require.NoError(t, err)
		require.Len(t, regions, 2)
		assert.Equal(t, "Jakarta", regions[0].Name)
		assert.Equal(t, "No region", regions[1].Name)

		_, err = CapacityReport(ctx, db, "planet", 30)
		assert.Error(t, err)

		month := time.Now().UTC()
		start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
		require.NoError(t, db.Exec(`INSERT INTO bandwidth_usages (vm_id, node_id, period_start, period_end, rx_bytes, tx_bytes, total_bytes)
			VALUES (?, ?, ?, ?, 100, 200, 300)`, axVMB, axNodeB, start, start.AddDate(0, 1, -1)).Error)
		usage, err := CustomerUsageReport(ctx, db, month)
		require.NoError(t, err)
		require.Len(t, usage, 2)
		assert.Equal(t, CustomerUsage{UserID: axOther, Email: "ben@example.test", Name: "Ben", VMs: 1, VCPU: 4, RAMMB: 4096, DiskGB: 80, TransferBytes: 300}, usage[0])
		assert.Equal(t, "ana@example.test", usage[1].Email)
		last, err := CustomerUsageReport(ctx, db, month.AddDate(0, -2, 0))
		require.NoError(t, err)
		assert.Equal(t, int64(0), last[0].TransferBytes, "another month's transfer is not counted")
	})

	t.Run("announcements reach their audience", func(t *testing.T) {
		onNode, err := SaveAnnouncement(ctx, db, "", AnnouncementInput{Title: "Node A reboot", Body: "Sunday 02:00.",
			Audience: Audience{Type: AudienceNodes, IDs: []string{axNodeA}}}, axOther)
		require.NoError(t, err)
		everyone, err := SaveAnnouncement(ctx, db, "", AnnouncementInput{Title: "New region", Body: "Hello {{name}}",
			Kind: AnnouncementMarketing}, "")
		require.NoError(t, err)
		later := time.Now().Add(time.Hour)
		_, err = SaveAnnouncement(ctx, db, "", AnnouncementInput{Title: "Not yet", Body: "x", StartsAt: &later}, "")
		require.NoError(t, err)

		mine, err := CurrentAnnouncements(ctx, db, axClient)
		require.NoError(t, err)
		assert.Equal(t, []string{"New region", "Node A reboot"}, titles(mine))
		theirs, err := CurrentAnnouncements(ctx, db, axOther)
		require.NoError(t, err)
		assert.Equal(t, []string{"New region"}, titles(theirs), "not on node A, not told about it")

		// A member of Ana's account runs her VM on node A, so they are told too.
		const member = "a1111111-1111-1111-1111-11111111111e"
		require.NoError(t, db.Exec(`INSERT INTO users (id, email, name, password_hash, role) VALUES (?, 'mo@example.test', 'Mo', 'x', 'client')`, member).Error)
		require.NoError(t, db.Exec(`INSERT INTO account_members (account_id, user_id, email, role, accepted_at) VALUES (?, ?, 'mo@example.test', 'technical', NOW())`, axClient, member).Error)
		theirs, err = CurrentAnnouncements(ctx, db, member)
		require.NoError(t, err)
		assert.Equal(t, []string{"New region", "Node A reboot"}, titles(theirs))
		require.NoError(t, db.Exec(`DELETE FROM users WHERE id = ?`, member).Error)

		p, err := PreviewAnnouncement(ctx, db, AnnouncementInput{Title: "x", Body: "Hi {{name}}", Kind: AnnouncementMarketing})
		require.NoError(t, err)
		assert.Equal(t, 2, p.Recipients, "the account that unsubscribed is left out of marketing")
		assert.Equal(t, []string{"ana@example.test", "ben@example.test"}, p.Sample)
		assert.Contains(t, p.Email.Text, "Hi Ana")
		p, err = PreviewAnnouncement(ctx, db, AnnouncementInput{Title: "x", Body: "y"})
		require.NoError(t, err)
		assert.Equal(t, 3, p.Recipients, "a service notice reaches everyone")

		require.NoError(t, DeleteAnnouncement(ctx, db, onNode.ID))
		mine, err = CurrentAnnouncements(ctx, db, axClient)
		require.NoError(t, err)
		assert.Equal(t, []string{everyone.Title}, titles(mine))
		assert.ErrorIs(t, DeleteAnnouncement(ctx, db, onNode.ID), ErrAnnouncementNotFound)

		_, err = EmailAnnouncement(ctx, db, everyone.ID, 60)
		assert.ErrorIs(t, err, ErrEmailNotConfigured)
		_, err = EmailAnnouncement(ctx, db, everyone.ID, 60)
		assert.ErrorIs(t, err, ErrEmailNotConfigured, "a run that stopped early lets the next one in")
		require.NoError(t, SetMarketingOptOut(ctx, db, axOptedOut, false))
	})

	t.Run("maintenance mode", func(t *testing.T) {
		t.Cleanup(middleware.ForgetMaintenance)
		m, err := SetMaintenance(ctx, db, nil, true, "Upgrading", true, "owner@example.test")
		require.NoError(t, err)
		require.NotNil(t, m.Since)
		stored := middleware.ReadMaintenance(db)
		assert.True(t, stored.Enabled)
		assert.True(t, stored.HoldJobs)
		assert.Equal(t, "Upgrading", stored.Notice())
		again, err := SetMaintenance(ctx, db, nil, true, "Still upgrading", true, "owner@example.test")
		require.NoError(t, err)
		assert.Equal(t, m.Since.Unix(), again.Since.Unix(), "switching it on again keeps when it started")
		_, err = SetMaintenance(ctx, db, nil, false, "", true, "")
		require.NoError(t, err)
		assert.False(t, middleware.ReadMaintenance(db).Enabled)
		assert.False(t, middleware.LoadMaintenance(db).Enabled)
	})

	t.Run("email log search", func(t *testing.T) {
		for _, e := range []EmailLogEntry{
			{ID: "b1111111-1111-1111-1111-111111111111", Template: "welcome", Recipient: "ana@example.test", Subject: "Welcome", Status: EmailSent, CreatedAt: time.Now()},
			{ID: "b2222222-2222-2222-2222-222222222222", Template: AnnouncementTemplate(axPlan), Recipient: "ben@example.test", Subject: "Node A reboot", Status: EmailQueued, CreatedAt: time.Now()},
		} {
			require.NoError(t, db.Create(&e).Error)
		}
		rows, total, err := SearchEmailLog(ctx, db, EmailLogFilter{Q: "REBOOT"}, 10, 0)
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		assert.Equal(t, "ben@example.test", rows[0].Recipient)
		_, total, err = SearchEmailLog(ctx, db, EmailLogFilter{Template: "welcome", Status: EmailSent}, 10, 0)
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
	})
}

func titles(as []Announcement) []string {
	out := []string{}
	for _, a := range as {
		out = append(out, a.Title)
	}
	return out
}
