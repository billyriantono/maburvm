package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// restoreFixture is a customer's VM on a node that has died, one finished
// backup of it, and a second, healthy node with addresses to give out.
type restoreFixture struct {
	db      *gorm.DB
	svc     *BackupService
	src     *models.VM
	backup  *models.Backup
	target  *models.Node
	agent   pb.NodeAgentServer
	checked int
}

func newRestoreFixture(t *testing.T, targetAgent pb.NodeAgentServer) *restoreFixture {
	t.Helper()
	db := setupVMIPAMTestDB(t)
	for _, stmt := range []string{
		`CREATE TABLE backups (id TEXT PRIMARY KEY, vm_id TEXT, storage_provider TEXT, storage_path TEXT, backup_type TEXT, status TEXT, size INTEGER DEFAULT 0, compression TEXT, checksum TEXT, error_message TEXT, started_at DATETIME, completed_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME, disks TEXT DEFAULT '[]', consistency TEXT DEFAULT '', destination_id TEXT, node_id TEXT, mode TEXT DEFAULT 'full', parent_id TEXT, chain_id TEXT, checkpoint TEXT DEFAULT '', mode_note TEXT DEFAULT '', encryption_key_id TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE firewall_rules (id TEXT PRIMARY KEY, vm_id TEXT, protocol TEXT, port_range TEXT, action TEXT, direction TEXT, source_ip TEXT, priority INTEGER, icmp_type TEXT NOT NULL DEFAULT '', log BOOLEAN NOT NULL DEFAULT 0, family TEXT NOT NULL DEFAULT '', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`,
		backupDestinationsTable,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	ctx := context.Background()
	user, dead, tmpl := seedVMIPAMDependencies(t, db)
	require.NoError(t, db.Model(dead).Update("status", models.NodeStatusOffline).Error)
	target := &models.Node{Name: "node-2", IPAddress: "192.0.2.20", Status: models.NodeStatusActive, Token: "node-2-token"}
	require.NoError(t, db.Create(target).Error)
	pool := &models.IPPool{Name: "public-v4", NodeID: &target.ID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Bridge: "br0", Gateway: "203.0.113.1"}
	require.NoError(t, db.Create(pool).Error)
	for _, addr := range []string{"203.0.113.10", "203.0.113.11", "203.0.113.12"} {
		require.NoError(t, db.Create(&models.IPAddress{PoolID: pool.ID, Address: addr, Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable}).Error)
	}

	src := &models.VM{UserID: user.ID.String(), NodeID: dead.ID, Hostname: "shop.example.test", OSTemplateID: tmpl.ID,
		Resources: models.Resources{CPU: 4, RAM: 8192, Disk: 80}, Status: models.VMStatusError}
	require.NoError(t, db.Create(src).Error)
	require.NoError(t, db.Create(&models.Network{VMID: src.ID, IPAddress: "198.51.100.7", BandwidthLimit: 200, BandwidthQuotaGB: 1000,
		OverQuotaPolicy: models.OverQuotaThrottle, ThrottleSpeedMbps: 5}).Error)
	require.NoError(t, db.Create(&models.FirewallRule{VMID: src.ID, Protocol: "tcp", PortRange: "8443", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 10}).Error)
	backup := &models.Backup{VMID: src.ID, StorageProvider: models.BackupStoreDestination, StoragePath: "prod/backups/shop.qcow2",
		Status: models.BackupStatusCompleted, Checksum: "c0ffee", Size: 1 << 30}
	require.NoError(t, db.Create(backup).Error)

	f := &restoreFixture{db: db, src: src, backup: backup, target: target, agent: targetAgent}
	client := agentClient(t, targetAgent)
	dest := NewBackupDestinations(db, func(ctx context.Context, nodeID string) (pb.NodeAgentClient, context.Context, error) {
		f.checked++
		if nodeID != target.ID {
			return nil, nil, errors.New("only the target node may be asked")
		}
		return client, ctx, nil
	}, testAESKey)
	_, err := dest.Create(ctx, validDestination())
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	vmRepo, nodeRepo := repository.NewVMRepository(db), repository.NewNodeRepository(db)
	f.svc = NewBackupService(db, repository.NewBackupRepository(db), repository.NewBackupScheduleRepository(db), vmRepo, nodeRepo, nil, dest, logger)
	f.svc.SetVMService(NewVMService(db, vmRepo, nodeRepo, repository.NewTemplateRepository(db), nil, logger))
	f.svc.objectStore = &fakeObjects{present: map[string]bool{backup.StoragePath: true}}
	return f
}

func (f *restoreFixture) vmCount(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, f.db.Model(&models.VM{}).Count(&n).Error)
	return n
}

// The case backups exist for: the original node is gone, and the backup comes
// back as a new VM somewhere else, for the same customer, at the same size.
func TestRestoreAsNewVMOnAnotherNode(t *testing.T) {
	agent := &storeAgent{}
	f := newRestoreFixture(t, agent)
	ctx := context.Background()

	resp, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
	require.NoError(t, err)

	// The target node proved it can read the destination before anything was made on it.
	require.NotNil(t, agent.asked, "the target node was not asked to test the destination")
	require.Equal(t, "vm-backups", agent.asked.GetObjectStore().GetBucket())
	require.Equal(t, "s3cr3t-value", agent.asked.GetObjectStore().GetSecretKey())

	var vm models.VM
	require.NoError(t, f.db.First(&vm, "id = ?", resp.VM.ID).Error)
	require.NotEqual(t, f.src.ID, vm.ID)
	require.Equal(t, f.src.UserID, vm.UserID, "owned by the original owner")
	require.Equal(t, f.target.ID, vm.NodeID, "on the chosen node")
	require.Equal(t, f.src.Resources, vm.Resources, "at the original size")
	require.Equal(t, f.src.OSTemplateID, vm.OSTemplateID)
	require.Equal(t, "restored-shop.example.test", vm.Hostname)
	require.Equal(t, models.VMStatusCreating, vm.Status)

	// A new address from the target node's pool, with the limits it was sold with.
	var ip models.IPAddress
	require.NoError(t, f.db.First(&ip, "vm_id = ?", vm.ID).Error)
	require.Contains(t, ip.Address, "203.0.113.")
	var network models.Network
	require.NoError(t, f.db.First(&network, "vm_id = ?", vm.ID).Error)
	require.Equal(t, ip.Address, network.IPAddress)
	require.EqualValues(t, 200, network.BandwidthLimit)
	require.EqualValues(t, 1000, network.BandwidthQuotaGB)
	require.Equal(t, 5, network.ThrottleSpeedMbps)

	// Its firewall, not the defaults.
	var rules []models.FirewallRule
	require.NoError(t, f.db.Find(&rules, "vm_id = ?", vm.ID).Error)
	require.Len(t, rules, 1)
	require.Equal(t, "8443", rules[0].PortRange)

	// The original and its backup are left as they were.
	var src models.VM
	require.NoError(t, f.db.First(&src, "id = ?", f.src.ID).Error)
	require.Equal(t, models.VMStatusError, src.Status)
	var backup models.Backup
	require.NoError(t, f.db.First(&backup, "id = ?", f.backup.ID).Error)
	require.Equal(t, models.BackupStatusCompleted, backup.Status)

	// A second restore needs its own hostname.
	_, err = f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
	require.ErrorIs(t, err, ErrHostnameExists)
	_, err = f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID, Hostname: "shop-2.example.test"})
	require.NoError(t, err)
}

// What the create job is told: the backup is the disk, used once and checked,
// read from the destination the node is sent.
func TestRestoreCreateRequest(t *testing.T) {
	backup := &models.Backup{StoragePath: "prod/backups/shop.qcow2", Checksum: "c0ffee"}
	src := &models.VM{UserID: "owner", Hostname: "shop.example.test", OSTemplateID: "tmpl", Resources: models.Resources{CPU: 2, RAM: 2048, Disk: 40}}

	req := restoreCreateRequest(backup, src, &RestoreAsNewRequest{NodeID: "node-2"}, true)
	require.Equal(t, "s3://prod/backups/shop.qcow2", req.CloneSourceRef)
	require.Equal(t, "node-2", req.NodeID)
	require.True(t, req.AutoAssignIP)
	require.True(t, req.Billed, "the owner's quota still counts the original and must not block the restore")
	params := map[string]interface{}{}
	req.Restore.apply(params)
	require.Equal(t, map[string]string{models.ImageOnceKey: "1", models.ImageSHA256Key: "c0ffee"}, params["metadata"])
	require.Equal(t, true, params["object_store"])

	// A chosen pool is used as given, and a backup in the node's own storage
	// is not read from the panel's destination.
	req = restoreCreateRequest(backup, src, &RestoreAsNewRequest{NodeID: "node-2", IPPoolID: "pool-9", Hostname: " new.example.test "}, false)
	require.Equal(t, "pool-9", req.IPPoolID)
	require.False(t, req.AutoAssignIP)
	require.Equal(t, "new.example.test", req.Hostname)
	params = map[string]interface{}{}
	req.Restore.apply(params)
	require.Equal(t, false, params["object_store"])
}

func TestRestoreAsNewVMRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("target node's agent is too old", func(t *testing.T) {
		f := newRestoreFixture(t, olderAgent{})
		before := f.vmCount(t)
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.ErrorContains(t, err, "older than the panel")
		require.Equal(t, before, f.vmCount(t), "nothing is created on a node that cannot read the backup")
	})

	t.Run("target node cannot use the destination", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{fail: "could not write a test object: AccessDenied"})
		before := f.vmCount(t)
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.ErrorContains(t, err, "AccessDenied")
		require.Equal(t, before, f.vmCount(t))
	})

	t.Run("encrypted backup, target agent cannot decrypt", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{features: []string{"backup_targets"}})
		require.NoError(t, f.db.Model(f.backup).Update("encryption_key_id", "00000000-0000-4000-8000-0000000000aa").Error)
		before := f.vmCount(t)
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.ErrorContains(t, err, "encrypted")
		require.Equal(t, before, f.vmCount(t))
	})

	t.Run("backup not finished", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{})
		require.NoError(t, f.db.Model(f.backup).Update("status", models.BackupStatusFailed).Error)
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.ErrorContains(t, err, "not completed")
	})

	t.Run("unknown node or backup", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{})
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: "00000000-0000-0000-0000-000000000000"})
		require.ErrorIs(t, err, ErrNodeNotFound)
		_, err = f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: "00000000-0000-0000-0000-000000000000", NodeID: f.target.ID})
		require.ErrorIs(t, err, ErrBackupNotFound)
	})

	// A backup an older agent kept in its node's own storage is not in the
	// destination: the target is tested against its own storage, not sent the
	// destination's keys.
	t.Run("backup in the node's own storage", func(t *testing.T) {
		agent := &storeAgent{}
		f := newRestoreFixture(t, agent)
		f.svc.objectStore = &fakeObjects{present: map[string]bool{}}
		require.NoError(t, f.db.Model(f.backup).Update("storage_provider", models.BackupStoreNode).Error)
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.NoError(t, err)
		require.NotNil(t, agent.asked, "the target node was not asked whether it has storage of its own")
		require.Empty(t, agent.asked.GetObjectStore().GetBucket())
		require.Empty(t, agent.asked.GetObjectStore().GetSecretKey())
	})

	// An agent from before restores were checked would keep the download on
	// its root filesystem, unverified. Nothing is created on it.
	t.Run("backup in the node's own storage, target agent too old", func(t *testing.T) {
		f := newRestoreFixture(t, olderAgent{})
		f.svc.objectStore = &fakeObjects{present: map[string]bool{}}
		require.NoError(t, f.db.Model(f.backup).Update("storage_provider", models.BackupStoreNode).Error)
		before := f.vmCount(t)
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.ErrorContains(t, err, "older than the panel")
		require.Equal(t, before, f.vmCount(t))
	})

	// The bucket was changed after the backup was made: the store answers, and
	// the file is not there. Refused before a VM and an address are used up.
	t.Run("file is not in the destination", func(t *testing.T) {
		agent := &storeAgent{}
		f := newRestoreFixture(t, agent)
		f.svc.objectStore = &fakeObjects{present: map[string]bool{}}
		before := f.vmCount(t)
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.ErrorContains(t, err, "not in the backup destination")
		require.Equal(t, before, f.vmCount(t))
		require.Nil(t, agent.asked)
	})

	// Unless that storage is the destination's bucket: then the file is where
	// every node can be sent to, and the target is tested like any other.
	t.Run("node's own storage is the destination's bucket", func(t *testing.T) {
		agent := &storeAgent{}
		f := newRestoreFixture(t, agent)
		f.svc.objectStore = &fakeObjects{present: map[string]bool{f.backup.StoragePath: true}}
		require.NoError(t, f.db.Model(f.backup).Update("storage_provider", models.BackupStoreNode).Error)
		_, err := f.svc.RestoreAsNewVM(ctx, &RestoreAsNewRequest{BackupID: f.backup.ID, NodeID: f.target.ID})
		require.NoError(t, err)
		require.NotNil(t, agent.asked, "the target node must prove it can read the destination")
	})
}

// A full backup in S3 is handed out as a short-lived link at the store; one
// the panel cannot hand out says why.
func TestBackupDownload(t *testing.T) {
	ctx := context.Background()
	f := newRestoreFixture(t, &storeAgent{})
	require.Empty(t, f.svc.DownloadRefusal(ctx, f.backup))
	dl, err := f.svc.OpenBackupDownload(ctx, f.backup.ID, "")
	require.NoError(t, err)
	require.Nil(t, dl.Body)
	require.Contains(t, dl.URL, "prod/backups/shop.qcow2")
	require.Contains(t, dl.URL, "X-Amz-Signature")
	require.Contains(t, dl.Filename, f.src.ID)

	_, err = f.svc.OpenBackupDownload(ctx, f.backup.ID, "vdz")
	require.ErrorIs(t, err, ErrBackupNotDownloadable)

	for _, c := range []struct {
		name, column string
		value        any
		want         string
	}{
		{"incremental", "mode", models.BackupModeIncremental, "only what changed"},
		{"node's own storage", "storage_provider", models.BackupStoreNode, "node's own storage"},
		{"not finished", "status", models.BackupStatusInProgress, "once it is completed"},
	} {
		require.NoError(t, f.db.Model(&models.Backup{}).Where("id = ?", f.backup.ID).Update(c.column, c.value).Error)
		_, err := f.svc.OpenBackupDownload(ctx, f.backup.ID, "")
		require.ErrorIs(t, err, ErrBackupNotDownloadable, c.name)
		require.ErrorContains(t, err, c.want, c.name)
		require.NoError(t, f.db.Model(&models.Backup{}).Where("id = ?", f.backup.ID).Updates(map[string]any{
			"mode": models.BackupModeFull, "storage_provider": models.BackupStoreDestination, "status": models.BackupStatusCompleted}).Error)
	}
}

func TestCopyFirewallRulesReplacesTheDefaults(t *testing.T) {
	f := newRestoreFixture(t, &storeAgent{})
	ctx := context.Background()
	dst := "44444444-4444-4444-4444-444444444444"
	require.NoError(t, f.db.Create(&models.FirewallRule{VMID: dst, Protocol: "tcp", PortRange: "22", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 10}).Error)

	require.NoError(t, f.db.Model(&models.FirewallRule{}).Where("vm_id = ?", f.src.ID).Updates(map[string]interface{}{"log": true, "family": "v6"}).Error)
	require.NoError(t, f.db.Exec(`UPDATE vms SET firewall_default = 'accept' WHERE id = ?`, f.src.ID).Error)
	require.NoError(t, f.db.Create(&models.VM{ID: dst, UserID: f.src.UserID, NodeID: f.src.NodeID, Hostname: "restored", OSTemplateID: f.src.OSTemplateID}).Error)
	require.NoError(t, f.db.Create(&models.VMFirewallGroup{VMID: f.src.ID, GroupID: "src-group", Position: models.FirewallGroupBefore, SortOrder: 3}).Error)
	require.NoError(t, f.db.Create(&models.VMFirewallGroup{VMID: dst, GroupID: "plan-default", Position: models.FirewallGroupAfter, SortOrder: 1}).Error)

	f.svc.copyFirewallRules(ctx, f.src.ID, dst)
	var links []models.VMFirewallGroup
	require.NoError(t, f.db.Find(&links, "vm_id = ?", dst).Error)
	require.Len(t, links, 1, "the original's groups replace the plan's default group")
	require.Equal(t, "src-group", links[0].GroupID)
	require.Equal(t, models.FirewallGroupBefore, links[0].Position)
	var rules []models.FirewallRule
	require.NoError(t, f.db.Find(&rules, "vm_id = ?", dst).Error)
	require.Len(t, rules, 1)
	require.Equal(t, "8443", rules[0].PortRange)
	require.True(t, rules[0].Log)
	require.Equal(t, "v6", rules[0].Family)
	var def string
	require.NoError(t, f.db.Raw(`SELECT firewall_default FROM vms WHERE id = ?`, dst).Scan(&def).Error)
	require.Equal(t, "accept", def, "an imported VM's policy comes back with it")

	// The original keeps its own.
	require.NoError(t, f.db.Find(&rules, "vm_id = ?", f.src.ID).Error)
	require.Len(t, rules, 1)
}

// fakeObjects is the storage a delete talks to.
type fakeObjects struct {
	present   map[string]bool
	deleteErr error
	deleted   []string
}

func (o *fakeObjects) Exists(_ context.Context, key string) (bool, error) { return o.present[key], nil }
func (o *fakeObjects) Delete(_ context.Context, key string) error {
	if o.deleteErr != nil {
		return o.deleteErr
	}
	o.deleted = append(o.deleted, key)
	delete(o.present, key)
	return nil
}

func TestDeleteBackupRemovesTheFileOrKeepsTheRecord(t *testing.T) {
	ctx := context.Background()
	const key = "prod/backups/shop.qcow2"
	exists := func(f *restoreFixture) bool {
		_, err := f.svc.GetBackup(ctx, f.backup.ID)
		return err == nil
	}

	t.Run("file deleted, then the record", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{})
		store := &fakeObjects{present: map[string]bool{key: true}}
		f.svc.objectStore = store
		left, err := f.svc.DeleteBackup(ctx, f.backup.ID)
		require.NoError(t, err)
		require.False(t, left)
		require.Equal(t, []string{key}, store.deleted)
		require.False(t, exists(f))
	})

	t.Run("storage refuses: the record stays so it can be retried", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{})
		store := &fakeObjects{present: map[string]bool{key: true}, deleteErr: errors.New("503 SlowDown")}
		f.svc.objectStore = store
		_, err := f.svc.DeleteBackup(ctx, f.backup.ID)
		require.ErrorIs(t, err, ErrBackupFileKept)
		require.True(t, exists(f), "the record was removed although its file is still in storage")

		store.deleteErr = nil
		_, err = f.svc.DeleteBackup(ctx, f.backup.ID)
		require.NoError(t, err)
		require.Equal(t, []string{key}, store.deleted)
		require.False(t, exists(f))
	})

	t.Run("no destination set: the record stays", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{})
		f.svc.objectStore = nil
		require.NoError(t, f.db.Exec(`DELETE FROM backup_destinations`).Error)
		f.svc.dest.invalidate()
		_, err := f.svc.DeleteBackup(ctx, f.backup.ID)
		require.ErrorIs(t, err, ErrBackupFileKept)
		require.True(t, exists(f))
	})

	// A file in the node's own storage is out of the panel's reach. The record
	// goes, and the caller is told the file did not.
	t.Run("file in the node's own storage", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{})
		require.NoError(t, f.db.Model(f.backup).Update("storage_provider", models.BackupStoreNode).Error)
		store := &fakeObjects{present: map[string]bool{}}
		f.svc.objectStore = store
		left, err := f.svc.DeleteBackup(ctx, f.backup.ID)
		require.NoError(t, err)
		require.True(t, left)
		require.Empty(t, store.deleted)
		require.False(t, exists(f))
	})

	t.Run("node's own storage is the destination's bucket", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{})
		require.NoError(t, f.db.Model(f.backup).Update("storage_provider", models.BackupStoreNode).Error)
		store := &fakeObjects{present: map[string]bool{key: true}}
		f.svc.objectStore = store
		left, err := f.svc.DeleteBackup(ctx, f.backup.ID)
		require.NoError(t, err)
		require.False(t, left)
		require.Equal(t, []string{key}, store.deleted)
	})

	// The destination's bucket was changed after this backup was made. A delete
	// of a missing file succeeds on S3: it must not be reported as a deletion.
	t.Run("file is not in the destination", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{})
		store := &fakeObjects{present: map[string]bool{}}
		f.svc.objectStore = store
		notDeleted, err := f.svc.DeleteBackup(ctx, f.backup.ID)
		require.NoError(t, err)
		require.True(t, notDeleted, "a file that was never found was reported as deleted")
		require.Empty(t, store.deleted)
		require.False(t, exists(f))
	})

	t.Run("a backup that never uploaded has no file", func(t *testing.T) {
		f := newRestoreFixture(t, &storeAgent{})
		require.NoError(t, f.db.Model(f.backup).Update("status", models.BackupStatusFailed).Error)
		store := &fakeObjects{deleteErr: errors.New("must not be called")}
		f.svc.objectStore = store
		_, err := f.svc.DeleteBackup(ctx, f.backup.ID)
		require.NoError(t, err)
		require.False(t, exists(f))
	})
}
