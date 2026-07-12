package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
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
		description TEXT,
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
