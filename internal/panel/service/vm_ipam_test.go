package service

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestVMServiceCreateVMAllocatesIPAMAndNetwork(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()

	user, node, tmpl := seedVMIPAMDependencies(t, db)
	pool := &models.IPPool{Name: "public-v4", NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Bridge: "viifbr0", Gateway: "203.0.113.1"}
	require.NoError(t, db.Create(pool).Error)
	addr := &models.IPAddress{PoolID: pool.ID, Address: "203.0.113.10", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable}
	require.NoError(t, db.Create(addr).Error)

	svc := NewVMService(
		db,
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		repository.NewTemplateRepository(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	resp, err := svc.CreateVM(ctx, &CreateVMRequest{
		UserID:       user.ID.String(),
		Hostname:     "ipam-create.example.test",
		OSTemplateID: tmpl.ID,
		Resources:    models.Resources{CPU: 1, RAM: 512, Disk: 10},
		NodeID:       node.ID,
		IPPoolID:     pool.ID,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	var network models.Network
	require.NoError(t, db.Where("vm_id = ?", resp.VM.ID).First(&network).Error)
	require.Equal(t, "203.0.113.10", network.IPAddress)

	var updated models.IPAddress
	require.NoError(t, db.First(&updated, "id = ?", addr.ID).Error)
	require.Equal(t, models.IPAddressStatusAssigned, updated.Status)
	require.NotNil(t, updated.VMID)
	require.Equal(t, resp.VM.ID, *updated.VMID)
}

// A pool with no bridge produces an unstartable VM (falls back to the
// non-existent virbr0), so creating from it must be rejected up front.
func TestVMServiceCreateVMRejectsBridgelessPool(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()

	user, node, tmpl := seedVMIPAMDependencies(t, db)
	pool := &models.IPPool{Name: "no-bridge", NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Gateway: "203.0.113.1"}
	require.NoError(t, db.Create(pool).Error)
	require.NoError(t, db.Create(&models.IPAddress{PoolID: pool.ID, Address: "203.0.113.10", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAvailable}).Error)

	svc := NewVMService(
		db,
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		repository.NewTemplateRepository(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	_, err := svc.CreateVM(ctx, &CreateVMRequest{
		UserID:       user.ID.String(),
		Hostname:     "bridgeless.example.test",
		OSTemplateID: tmpl.ID,
		Resources:    models.Resources{CPU: 1, RAM: 512, Disk: 10},
		NodeID:       node.ID,
		IPPoolID:     pool.ID,
	})
	require.ErrorIs(t, err, ErrPoolHasNoBridge)

	// No VM row should have been created for the rejected request.
	var count int64
	require.NoError(t, db.Model(&models.VM{}).Where("hostname = ?", "bridgeless.example.test").Count(&count).Error)
	require.Zero(t, count)
}

func TestVMServiceCreateVMEnforcesQuota(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()

	user, node, tmpl := seedVMIPAMDependencies(t, db)
	// Tight quota: at most 1 vCPU total across all VMs.
	require.NoError(t, db.Create(&models.UserQuota{UserID: user.ID.String(), MaxVCPU: 1}).Error)

	svc := NewVMService(
		db,
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		repository.NewTemplateRepository(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	// Requesting 2 vCPUs exceeds the 1-vCPU quota; creation must be rejected
	// before any node/IP/job side effects.
	_, err := svc.CreateVM(ctx, &CreateVMRequest{
		UserID:       user.ID.String(),
		Hostname:     "quota-block.example.test",
		OSTemplateID: tmpl.ID,
		Resources:    models.Resources{CPU: 2, RAM: 512, Disk: 10},
		NodeID:       node.ID,
	})
	require.ErrorIs(t, err, ErrQuotaExceeded)

	// No VM row should have been created.
	var vmCount int64
	require.NoError(t, db.Model(&models.VM{}).Where("user_id = ?", user.ID.String()).Count(&vmCount).Error)
	require.Zero(t, vmCount)
}

func TestVMServiceUpdateVMEnforcesQuotaOnResize(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()

	user, node, tmpl := seedVMIPAMDependencies(t, db)
	// Existing stopped VM using 1 vCPU; quota caps total vCPU at 2.
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "resize.example.test", OSTemplateID: tmpl.ID, Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(vm).Error)
	require.NoError(t, db.Create(&models.UserQuota{UserID: user.ID.String(), MaxVCPU: 2}).Error)

	svc := NewVMService(
		db,
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		repository.NewTemplateRepository(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	// Resizing 1 -> 4 vCPU exceeds the 2-vCPU cap and must be rejected before
	// any resize job is enqueued (riverClient is nil here, so reaching it would panic).
	_, err := svc.UpdateVM(ctx, &UpdateVMRequest{
		VMID:      vm.ID,
		Resources: &models.Resources{CPU: 4, RAM: 512, Disk: 10},
	})
	require.ErrorIs(t, err, ErrQuotaExceeded)

	// The stored resources must be unchanged.
	var stored models.VM
	require.NoError(t, db.First(&stored, "id = ?", vm.ID).Error)
	require.Equal(t, 1, stored.Resources.CPU)
}

func TestVMServiceRescueRequiresISO(t *testing.T) {
	t.Setenv("RESCUE_ISO_URL", "") // no fallback configured
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()

	user, node, tmpl := seedVMIPAMDependencies(t, db)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "rescue.example.test", OSTemplateID: tmpl.ID, Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(vm).Error)

	svc := NewVMService(
		db,
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		repository.NewTemplateRepository(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	// With no iso_url and no RESCUE_ISO_URL, rescue must fail clearly (before any side effect).
	_, err := svc.RescueVM(ctx, vm.ID, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "no rescue ISO configured")

	// The VM must not have been flipped into rescue mode.
	var stored models.VM
	require.NoError(t, db.First(&stored, "id = ?", vm.ID).Error)
	require.False(t, stored.RescueMode)
}

func TestVMServiceDeleteVMReleasesIPAMAndDeletesNetwork(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()

	user, node, tmpl := seedVMIPAMDependencies(t, db)
	pool := &models.IPPool{Name: "public-v4", NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Bridge: "viifbr0", Gateway: "203.0.113.1"}
	require.NoError(t, db.Create(pool).Error)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "ipam-delete.example.test", OSTemplateID: tmpl.ID, Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(vm).Error)
	addr := &models.IPAddress{PoolID: pool.ID, NodeID: &node.ID, Address: "203.0.113.11", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAssigned, VMID: &vm.ID}
	require.NoError(t, db.Create(addr).Error)
	require.NoError(t, db.Create(&models.Network{VMID: vm.ID, IPAddress: addr.Address}).Error)

	svc := NewVMService(
		db,
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		repository.NewTemplateRepository(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	require.NoError(t, svc.DeleteVM(ctx, vm.ID))

	var updated models.IPAddress
	require.NoError(t, db.First(&updated, "id = ?", addr.ID).Error)
	require.Equal(t, models.IPAddressStatusAvailable, updated.Status)
	require.Nil(t, updated.VMID)

	var networkCount int64
	require.NoError(t, db.Model(&models.Network{}).Where("vm_id = ?", vm.ID).Count(&networkCount).Error)
	require.Zero(t, networkCount)

	var vmCount int64
	require.NoError(t, db.Model(&models.VM{}).Where("id = ?", vm.ID).Count(&vmCount).Error)
	require.Zero(t, vmCount)
}

func setupVMIPAMTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	// Unique in-memory DB per test to avoid shared-cache cross-test contamination.
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:vmipam-%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', email TEXT NOT NULL, password_hash TEXT NOT NULL, role TEXT, quota_mode TEXT NOT NULL DEFAULT 'legacy', two_factor_secret TEXT, two_factor_enabled BOOLEAN NOT NULL DEFAULT 0, two_factor_backup_codes TEXT, ip_whitelist TEXT, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, token_revoked_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT NOT NULL, ip_address TEXT NOT NULL, status TEXT, token TEXT NOT NULL, cert_fingerprint TEXT NOT NULL DEFAULT '', region_id TEXT, zone TEXT NOT NULL DEFAULT '', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE os_templates (id TEXT PRIMARY KEY, name TEXT NOT NULL, version TEXT NOT NULL, image_path TEXT NOT NULL, size_bytes BIGINT NOT NULL DEFAULT 0, is_active BOOLEAN, description TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, node_id TEXT NOT NULL, hostname TEXT NOT NULL, os_template_id TEXT NOT NULL, resources TEXT, status TEXT, source_migration TEXT, vnc_port INTEGER, vnc_password TEXT, console_enabled BOOLEAN DEFAULT 1, rescue_mode BOOLEAN DEFAULT 0, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE networks (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, ip_address TEXT NOT NULL, bandwidth_limit INTEGER DEFAULT 0, bandwidth_quota_gb INTEGER DEFAULT 0, over_quota_policy TEXT DEFAULT 'throttle', throttle_speed_mbps INTEGER DEFAULT 0, throttled BOOLEAN DEFAULT 0, vlan_id INTEGER, anti_spoofing BOOLEAN DEFAULT 1, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE ip_pools (id TEXT PRIMARY KEY, name TEXT NOT NULL, node_id TEXT, family TEXT NOT NULL, cidr TEXT, gateway TEXT, bridge TEXT, range_start TEXT, range_end TEXT, description TEXT, orderable BOOLEAN NOT NULL DEFAULT 0, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE ip_addresses (id TEXT PRIMARY KEY, pool_id TEXT NOT NULL, node_id TEXT, address TEXT NOT NULL, family TEXT NOT NULL, status TEXT NOT NULL, vm_id TEXT, delivery_mode TEXT NOT NULL DEFAULT 'direct', nat_mode TEXT NOT NULL DEFAULT '', user_id TEXT, note TEXT, rdns TEXT DEFAULT '', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE ip_pool_nodes (pool_id TEXT NOT NULL, node_id TEXT NOT NULL, PRIMARY KEY (pool_id, node_id))`,
		`CREATE TABLE user_quotas (user_id TEXT PRIMARY KEY, max_vms INTEGER DEFAULT 0, max_vcpu INTEGER DEFAULT 0, max_ram_mb INTEGER DEFAULT 0, max_disk_gb INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, quota_mode TEXT NOT NULL DEFAULT 'legacy', policy_id TEXT, policy_version INTEGER, policy_name TEXT, policy_assigned_at DATETIME, policy_assigned_by TEXT, cap_revision_id TEXT)`,
		`CREATE TABLE vm_disks (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, device TEXT NOT NULL, size_gb INTEGER NOT NULL, path TEXT NOT NULL, lifecycle TEXT NOT NULL DEFAULT 'attached', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME)`,
		`CREATE TABLE disk_quota_reservations (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, vm_id TEXT NOT NULL, size_gb INTEGER NOT NULL, status TEXT NOT NULL DEFAULT 'pending', created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, consumed_at DATETIME)`,
		`CREATE UNIQUE INDEX disk_res_one_pending_per_vm ON disk_quota_reservations(vm_id) WHERE status = 'pending'`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})
	return db
}

func seedVMIPAMDependencies(t *testing.T, db *gorm.DB) (*models.User, *models.Node, *models.OSTemplate) {
	t.Helper()
	user := &models.User{Email: "ipam-vm@example.test", PasswordHash: "hash", Role: models.RoleClient}
	require.NoError(t, db.Create(user).Error)
	node := &models.Node{Name: "node-1", IPAddress: "192.0.2.10", Status: models.NodeStatusActive, Token: "node-token"}
	require.NoError(t, db.Create(node).Error)
	tmpl := &models.OSTemplate{Name: "debian", Version: "12", ImagePath: "/images/debian.qcow2", IsActive: true}
	require.NoError(t, db.Create(tmpl).Error)
	return user, node, tmpl
}

// A floating IP outliving its VM is the entire point of the feature: deleting the
// VM must detach the address but keep it reserved for its owner, never return it
// to the pool where another tenant could take it.
func TestVMServiceDeleteVMKeepsFloatingIPForOwner(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()

	user, node, tmpl := seedVMIPAMDependencies(t, db)
	pool := &models.IPPool{Name: "public-v4", NodeID: &node.ID, Family: models.IPFamilyIPv4, CIDR: "203.0.113.0/24", Bridge: "viifbr0", Gateway: "203.0.113.1"}
	require.NoError(t, db.Create(pool).Error)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "fip-delete.example.test", OSTemplateID: tmpl.ID, Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, Status: models.VMStatusStopped}
	require.NoError(t, db.Create(vm).Error)

	ownerID := user.ID.String()
	fip := &models.IPAddress{
		PoolID: pool.ID, NodeID: &node.ID, Address: "203.0.113.50", Family: models.IPFamilyIPv4,
		Status: models.IPAddressStatusAssigned, VMID: &vm.ID,
		DeliveryMode: models.IPDeliveryFloating, NATMode: models.NATModeFull, UserID: &ownerID,
	}
	require.NoError(t, db.Create(fip).Error)
	// A plain directly-assigned address on the same VM, which SHOULD go back to the pool.
	direct := &models.IPAddress{PoolID: pool.ID, NodeID: &node.ID, Address: "203.0.113.11", Family: models.IPFamilyIPv4, Status: models.IPAddressStatusAssigned, VMID: &vm.ID}
	require.NoError(t, db.Create(direct).Error)
	require.NoError(t, db.Create(&models.Network{VMID: vm.ID, IPAddress: direct.Address}).Error)

	svc := NewVMService(
		db,
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		repository.NewTemplateRepository(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	require.NoError(t, svc.DeleteVM(ctx, vm.ID))

	var updatedFIP models.IPAddress
	require.NoError(t, db.First(&updatedFIP, "id = ?", fip.ID).Error)
	require.Equal(t, models.IPAddressStatusReserved, updatedFIP.Status, "floating IP must stay reserved for its owner, not become allocatable")
	require.Nil(t, updatedFIP.VMID)
	require.Equal(t, models.IPDeliveryFloating, updatedFIP.DeliveryMode)
	require.Empty(t, updatedFIP.NATMode)
	require.NotNil(t, updatedFIP.UserID)
	require.Equal(t, ownerID, *updatedFIP.UserID)

	var updatedDirect models.IPAddress
	require.NoError(t, db.First(&updatedDirect, "id = ?", direct.ID).Error)
	require.Equal(t, models.IPAddressStatusAvailable, updatedDirect.Status)
	require.Nil(t, updatedDirect.VMID)
}
