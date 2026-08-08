package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupMetricsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:metrics-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE node_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		node_id TEXT NOT NULL,
		cpu_usage REAL NOT NULL DEFAULT 0,
		memory_usage REAL NOT NULL DEFAULT 0,
		disk_usage REAL NOT NULL DEFAULT 0,
		network_rx_bytes_per_sec REAL NOT NULL DEFAULT 0,
		network_tx_bytes_per_sec REAL NOT NULL DEFAULT 0,
		vm_count INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT '',
		conntrack_count INTEGER NOT NULL DEFAULT 0,
		conntrack_max INTEGER NOT NULL DEFAULT 0,
		recorded_at DATETIME NOT NULL
	)`).Error)
	require.NoError(t, db.Exec(`CREATE TABLE vm_metrics (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		vm_id TEXT NOT NULL,
		cpu_usage REAL NOT NULL DEFAULT 0,
		memory_usage REAL NOT NULL DEFAULT 0,
		memory_used_bytes INTEGER NOT NULL DEFAULT 0,
		disk_read_bytes_per_sec INTEGER NOT NULL DEFAULT 0,
		disk_write_bytes_per_sec INTEGER NOT NULL DEFAULT 0,
		network_rx_bytes_per_sec INTEGER NOT NULL DEFAULT 0,
		network_tx_bytes_per_sec INTEGER NOT NULL DEFAULT 0,
		recorded_at DATETIME NOT NULL
	)`).Error)
	return db
}

func TestMetricsRepositoryNodeSamples(t *testing.T) {
	db := setupMetricsTestDB(t)
	repo := NewMetricsRepository(db)
	ctx := context.Background()
	now := time.Now()
	const nodeID = "11111111-1111-1111-1111-111111111111"

	// Four samples for our node at increasing recency, CPU encodes the age.
	for i, ts := range []time.Time{
		now.Add(-30 * time.Minute),
		now.Add(-20 * time.Minute),
		now.Add(-10 * time.Minute),
		now.Add(-2 * time.Minute),
	} {
		require.NoError(t, repo.InsertNodeSample(ctx, &models.NodeMetricSample{
			NodeID: nodeID, CPUUsage: float64(i * 10), RecordedAt: ts,
		}))
	}
	// A sample for a different node must never leak into queries for nodeID.
	require.NoError(t, repo.InsertNodeSample(ctx, &models.NodeMetricSample{
		NodeID: "22222222-2222-2222-2222-222222222222", CPUUsage: 99, RecordedAt: now,
	}))

	// Window = last 15m → only the -10m (cpu 20) and -2m (cpu 30) samples, ascending.
	recent, err := repo.ListNodeSamples(ctx, nodeID, now.Add(-15*time.Minute), 0)
	require.NoError(t, err)
	require.Len(t, recent, 2)
	require.Equal(t, 20.0, recent[0].CPUUsage, "oldest within window first")
	require.Equal(t, 30.0, recent[1].CPUUsage)

	// Cap keeps the most-recent N but still returns them oldest-first.
	capped, err := repo.ListNodeSamples(ctx, nodeID, now.Add(-60*time.Minute), 2)
	require.NoError(t, err)
	require.Len(t, capped, 2)
	require.Equal(t, 20.0, capped[0].CPUUsage)
	require.Equal(t, 30.0, capped[1].CPUUsage)

	// Prune everything older than 15m → removes the -30m and -20m samples only.
	removed, err := repo.PruneNodeSamplesBefore(ctx, now.Add(-15*time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(2), removed)

	remaining, err := repo.ListNodeSamples(ctx, nodeID, now.Add(-24*time.Hour), 0)
	require.NoError(t, err)
	require.Len(t, remaining, 2)
}

func TestMetricsRepositoryVMSamples(t *testing.T) {
	db := setupMetricsTestDB(t)
	repo := NewMetricsRepository(db)
	ctx := context.Background()
	now := time.Now()
	const vmID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"

	for i, ts := range []time.Time{
		now.Add(-30 * time.Minute),
		now.Add(-10 * time.Minute),
		now.Add(-2 * time.Minute),
	} {
		require.NoError(t, repo.InsertVMSample(ctx, &models.VMMetricSample{
			VMID: vmID, CPUUsage: float64(i * 10), MemoryUsedBytes: int64(i) * 1024, RecordedAt: ts,
		}))
	}
	// Another VM's sample must not leak.
	require.NoError(t, repo.InsertVMSample(ctx, &models.VMMetricSample{
		VMID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", CPUUsage: 99, RecordedAt: now,
	}))

	// Window = last 15m → the -10m (cpu 10) and -2m (cpu 20) samples, ascending.
	recent, err := repo.ListVMSamples(ctx, vmID, now.Add(-15*time.Minute), 0)
	require.NoError(t, err)
	require.Len(t, recent, 2)
	require.Equal(t, 10.0, recent[0].CPUUsage)
	require.Equal(t, 20.0, recent[1].CPUUsage)

	// Prune older than 15m removes the -30m sample only.
	removed, err := repo.PruneVMSamplesBefore(ctx, now.Add(-15*time.Minute))
	require.NoError(t, err)
	require.Equal(t, int64(1), removed)
}
