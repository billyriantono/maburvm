package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupPlanTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:plan-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE plans (
		id TEXT PRIMARY KEY, name TEXT NOT NULL, cpu INTEGER NOT NULL, ram INTEGER NOT NULL,
		disk INTEGER NOT NULL, bandwidth_mbps INTEGER DEFAULT 0,
		data_quota_gb INTEGER DEFAULT 0, over_quota_policy TEXT DEFAULT 'throttle', throttle_speed_mbps INTEGER DEFAULT 0,
		smtp_policy TEXT NOT NULL DEFAULT 'allowed', limits TEXT NOT NULL DEFAULT '{}', price_monthly REAL, price_hourly REAL, currency TEXT NOT NULL DEFAULT '', max_snapshots INTEGER, max_backups INTEGER, max_extra_ipv4 INTEGER, max_extra_ipv6 INTEGER, description TEXT,
		default_firewall_group_id TEXT, network_type TEXT NOT NULL DEFAULT 'public', nat_ports INTEGER NOT NULL DEFAULT 0, nat_hostnames INTEGER NOT NULL DEFAULT 0,
		is_active BOOLEAN DEFAULT 1, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP, deleted_at DATETIME
	)`).Error)
	return db
}

func TestPlanServiceCRUD(t *testing.T) {
	db := setupPlanTestDB(t)
	svc := NewPlanService(repository.NewPlanRepository(db))
	ctx := context.Background()

	plan, err := svc.CreatePlan(ctx, &PlanRequest{Name: "Starter", CPU: 1, RAM: 1024, Disk: 20, BandwidthMbps: 100})
	require.NoError(t, err)
	require.NotEmpty(t, plan.ID)
	require.True(t, plan.IsActive)

	// Duplicate name rejected.
	_, err = svc.CreatePlan(ctx, &PlanRequest{Name: "Starter", CPU: 2, RAM: 2048, Disk: 40})
	require.ErrorIs(t, err, ErrPlanNameExists)

	// Invalid resources rejected.
	_, err = svc.CreatePlan(ctx, &PlanRequest{Name: "Bad", CPU: 0, RAM: 1024, Disk: 20})
	require.Error(t, err)

	got, err := svc.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, "Starter", got.Name)

	updated, err := svc.UpdatePlan(ctx, plan.ID, &PlanRequest{Name: "Starter", CPU: 2, RAM: 2048, Disk: 40})
	require.NoError(t, err)
	require.Equal(t, 2, updated.CPU)
	require.Equal(t, 2048, updated.RAM)

	plans, err := svc.ListPlans(ctx, false)
	require.NoError(t, err)
	require.Len(t, plans, 1)

	require.NoError(t, svc.DeletePlan(ctx, plan.ID))
	_, err = svc.GetPlan(ctx, plan.ID)
	require.ErrorIs(t, err, ErrPlanNotFound)
}

// ADM-15: a plan carries a display price and per-VM allowances; nil lifts a
// limit and 0 means none.
func TestPlanPriceAndAllowances(t *testing.T) {
	db := setupPlanTestDB(t)
	svc := NewPlanService(repository.NewPlanRepository(db))
	ctx := context.Background()
	monthly, hourly := 5.5, 0.0082

	plan, err := svc.CreatePlan(ctx, &PlanRequest{Name: "Priced", CPU: 1, RAM: 1024, Disk: 20,
		PriceMonthly: &monthly, PriceHourly: &hourly, Currency: " usd ", MaxSnapshots: intp(3), MaxBackups: intp(0), MaxExtraIPv4: intp(2), MaxExtraIPv6: intp(1)})
	require.NoError(t, err)
	got, err := svc.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, "USD", got.Currency)
	require.InDelta(t, 5.5, *got.PriceMonthly, 1e-9)
	require.InDelta(t, 0.0082, *got.PriceHourly, 1e-9)
	require.Equal(t, 3, *got.MaxSnapshots)
	require.Equal(t, 0, *got.MaxBackups, "none is not the same as no limit")
	require.Equal(t, 2, *got.MaxExtraIPv4)

	// An update replaces them: left out means no price and no limit.
	_, err = svc.UpdatePlan(ctx, plan.ID, &PlanRequest{Name: "Priced", CPU: 1, RAM: 1024, Disk: 20})
	require.NoError(t, err)
	got, err = svc.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Nil(t, got.PriceMonthly)
	require.Nil(t, got.MaxSnapshots)
	require.Nil(t, got.MaxBackups)

	neg := -1.0
	for name, req := range map[string]*PlanRequest{
		"price without currency": {PriceMonthly: &monthly},
		"bad currency":           {PriceMonthly: &monthly, Currency: "dollars"},
		"negative price":         {PriceMonthly: &neg, Currency: "USD"},
		"snapshots over the cap": {MaxSnapshots: intp(MaxSnapshotsPerVM + 1)},
		"negative backups":       {MaxBackups: intp(-1)},
		"two IPv6":               {MaxExtraIPv6: intp(2)},
	} {
		req.Name, req.CPU, req.RAM, req.Disk = "Bad "+name, 1, 1024, 20
		_, err := svc.CreatePlan(ctx, req)
		require.Error(t, err, name)
	}
}

// A plan's network type and default firewall group: a NAT plan gets the
// default block, only a group for everyone may be a default, and an update
// that leaves them out keeps them.
func TestPlanNetworkAndDefaultGroup(t *testing.T) {
	db := setupPlanTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE firewall_groups (id TEXT PRIMARY KEY, user_id TEXT, name TEXT, description TEXT, created_at DATETIME, updated_at DATETIME)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO firewall_groups (id, user_id, name) VALUES ('g-everyone', NULL, 'Base'), ('g-customer', 'u1', 'Mine')`).Error)
	svc := NewPlanService(repository.NewPlanRepository(db))
	ctx := context.Background()
	str := func(s string) *string { return &s }

	plan, err := svc.CreatePlan(ctx, &PlanRequest{Name: "Nat", CPU: 1, RAM: 512, Disk: 10, NetworkType: models.PlanNetworkNAT, DefaultFirewallGroupID: str("g-everyone")})
	require.NoError(t, err)
	require.Equal(t, models.PlanNetworkNAT, plan.NetworkType)
	require.Equal(t, DefaultNATPorts, plan.NATPorts)
	require.Equal(t, "g-everyone", *plan.DefaultFirewallGroupID)

	_, err = svc.CreatePlan(ctx, &PlanRequest{Name: "Other", CPU: 1, RAM: 512, Disk: 10, DefaultFirewallGroupID: str("g-customer")})
	require.ErrorContains(t, err, "for everyone")
	_, err = svc.CreatePlan(ctx, &PlanRequest{Name: "Bad", CPU: 1, RAM: 512, Disk: 10, NetworkType: "bridged"})
	require.Error(t, err)
	_, err = svc.CreatePlan(ctx, &PlanRequest{Name: "Big", CPU: 1, RAM: 512, Disk: 10, NetworkType: models.PlanNetworkNAT, NATPorts: MaxNATPorts + 1})
	require.Error(t, err)
	public, err := svc.CreatePlan(ctx, &PlanRequest{Name: "Public", CPU: 1, RAM: 512, Disk: 10})
	require.NoError(t, err)
	require.Equal(t, models.PlanNetworkPublic, public.NetworkType)
	require.Nil(t, public.DefaultFirewallGroupID)

	kept, err := svc.UpdatePlan(ctx, plan.ID, &PlanRequest{Name: "Nat", CPU: 2, RAM: 512, Disk: 10})
	require.NoError(t, err)
	require.Equal(t, models.PlanNetworkNAT, kept.NetworkType, "an update that leaves the network out keeps it")
	require.Equal(t, "g-everyone", *kept.DefaultFirewallGroupID)
	cleared, err := svc.UpdatePlan(ctx, plan.ID, &PlanRequest{Name: "Nat", CPU: 2, RAM: 512, Disk: 10, DefaultFirewallGroupID: str("")})
	require.NoError(t, err)
	require.Nil(t, cleared.DefaultFirewallGroupID)
	reread, err := svc.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Nil(t, reread.DefaultFirewallGroupID)
	require.Equal(t, DefaultNATPorts, reread.NATPorts)
}
