package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
)

// Reports: capacity and allocation over time per node, pool and region, and
// what each customer uses. The node history (metrics) is kept for days; this
// keeps one row per node and pool per day for two years.

// capacityRetention is how long daily capacity rows are kept.
const capacityRetention = 2 * 365 * 24 * time.Hour

// RecordCapacity writes today's row for every node and storage pool: what is
// allocated against what there is, and today's average use so far. Run
// hourly, the day's row ends as its last state.
func RecordCapacity(ctx context.Context, db *gorm.DB) error {
	db = db.WithContext(ctx)
	if err := db.Exec(`INSERT INTO capacity_history (day, scope, scope_id, node_id, vms, vcpu_allocated, vcpu_total,
			ram_allocated_mb, ram_total_mb, disk_allocated_gb, disk_total_gb, disk_used_gb, cpu_usage, memory_usage, updated_at)
		SELECT CURRENT_DATE, 'node', CAST(n.id AS TEXT), CAST(n.id AS TEXT), COALESCE(a.vms, 0), COALESCE(a.cpu, 0), n.cpu_count,
			COALESCE(a.ram, 0), n.memory_total_bytes / 1048576, COALESCE(a.disk, 0),
			COALESCE(p.total, 0) / 1073741824, COALESCE(p.used, 0) / 1073741824, COALESCE(m.cpu, 0), COALESCE(m.mem, 0), NOW()
		FROM nodes n
		LEFT JOIN (SELECT CAST(node_id AS TEXT) AS node_id, COUNT(*) AS vms, SUM(CAST(resources->>'cpu' AS INTEGER)) AS cpu,
				SUM(CAST(resources->>'ram' AS BIGINT)) AS ram, SUM(CAST(resources->>'disk' AS BIGINT)) AS disk
			FROM vms WHERE deleted_at IS NULL GROUP BY 1) a ON a.node_id = CAST(n.id AS TEXT)
		LEFT JOIN (SELECT CAST(node_id AS TEXT) AS node_id, SUM(total_space) AS total, SUM(used_space) AS used
			FROM storage_pools WHERE deleted_at IS NULL GROUP BY 1) p ON p.node_id = CAST(n.id AS TEXT)
		LEFT JOIN (SELECT CAST(node_id AS TEXT) AS node_id, AVG(cpu_usage) AS cpu, AVG(memory_usage) AS mem
			FROM node_metrics WHERE recorded_at >= CURRENT_DATE AND status <> 'offline' GROUP BY 1) m ON m.node_id = CAST(n.id AS TEXT)
		WHERE n.deleted_at IS NULL
		ON CONFLICT (day, scope, scope_id) DO UPDATE SET vms = EXCLUDED.vms, vcpu_allocated = EXCLUDED.vcpu_allocated,
			vcpu_total = EXCLUDED.vcpu_total, ram_allocated_mb = EXCLUDED.ram_allocated_mb, ram_total_mb = EXCLUDED.ram_total_mb,
			disk_allocated_gb = EXCLUDED.disk_allocated_gb, disk_total_gb = EXCLUDED.disk_total_gb, disk_used_gb = EXCLUDED.disk_used_gb,
			cpu_usage = EXCLUDED.cpu_usage, memory_usage = EXCLUDED.memory_usage, updated_at = NOW()`).Error; err != nil {
		return fmt.Errorf("recording node capacity: %w", err)
	}

	alloc, err := PoolAllocations(ctx, db, "")
	if err != nil {
		return err
	}
	var pools []struct {
		ID, NodeID            string
		TotalSpace, UsedSpace int64
	}
	if err := db.Table("storage_pools").Select("CAST(id AS TEXT) AS id, CAST(node_id AS TEXT) AS node_id, total_space, used_space").
		Where("deleted_at IS NULL").Scan(&pools).Error; err != nil {
		return fmt.Errorf("reading storage pools: %w", err)
	}
	for _, p := range pools {
		if err := db.Exec(`INSERT INTO capacity_history (day, scope, scope_id, node_id, disk_allocated_gb, disk_total_gb, disk_used_gb, updated_at)
			VALUES (CURRENT_DATE, 'pool', ?, ?, ?, ?, ?, NOW())
			ON CONFLICT (day, scope, scope_id) DO UPDATE SET disk_allocated_gb = EXCLUDED.disk_allocated_gb,
				disk_total_gb = EXCLUDED.disk_total_gb, disk_used_gb = EXCLUDED.disk_used_gb, updated_at = NOW()`,
			p.ID, p.NodeID, alloc[p.ID]/bytesPerGB, p.TotalSpace/bytesPerGB, p.UsedSpace/bytesPerGB).Error; err != nil {
			return fmt.Errorf("recording pool capacity: %w", err)
		}
	}
	return db.Exec("DELETE FROM capacity_history WHERE day < ?", time.Now().Add(-capacityRetention).Format("2006-01-02")).Error
}

// RunCapacityRecorder records capacity shortly after start and then hourly,
// until ctx ends.
func RunCapacityRecorder(ctx context.Context, db *gorm.DB) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := RecordCapacity(ctx, db); err != nil {
				slog.Default().Warn("capacity report: could not record today's capacity", "error", err)
			}
			t.Reset(time.Hour)
		}
	}
}

// CapacityPoint is one day of a node, pool or region.
type CapacityPoint struct {
	Day             string  `json:"day"`
	VMs             int     `json:"vms"`
	VCPUAllocated   int     `json:"vcpu_allocated" gorm:"column:vcpu_allocated"`
	VCPUTotal       int     `json:"vcpu_total" gorm:"column:vcpu_total"`
	RAMAllocatedMB  int64   `json:"ram_allocated_mb"`
	RAMTotalMB      int64   `json:"ram_total_mb"`
	DiskAllocatedGB int64   `json:"disk_allocated_gb"`
	DiskTotalGB     int64   `json:"disk_total_gb"`
	DiskUsedGB      int64   `json:"disk_used_gb"`
	CPUUsage        float64 `json:"cpu_usage"`
	MemoryUsage     float64 `json:"memory_usage"`
}

// CapacitySeries is one node, pool or region over the days asked for.
type CapacitySeries struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Points []CapacityPoint `json:"points"`
}

// Scopes of a capacity report.
const (
	CapacityByNode   = "node"
	CapacityByPool   = "pool"
	CapacityByRegion = "region"
)

// CapacityReport returns one series per node, pool or region for the last
// days, oldest day first. Regions add up their nodes as they are assigned
// now; usage is averaged over the region's nodes.
func CapacityReport(ctx context.Context, db *gorm.DB, scope string, days int) ([]CapacitySeries, error) {
	since := time.Now().UTC().AddDate(0, 0, -days).Format("2006-01-02")
	var q string
	switch scope {
	case CapacityByNode:
		q = `SELECT h.scope_id AS id, COALESCE(n.name, h.scope_id) AS name, CAST(h.day AS TEXT) AS day, h.vms, h.vcpu_allocated, h.vcpu_total,
				h.ram_allocated_mb, h.ram_total_mb, h.disk_allocated_gb, h.disk_total_gb, h.disk_used_gb, h.cpu_usage, h.memory_usage
			FROM capacity_history h LEFT JOIN nodes n ON CAST(n.id AS TEXT) = h.scope_id
			WHERE h.scope = 'node' AND h.day >= ? ORDER BY name, h.day`
	case CapacityByPool:
		q = `SELECT h.scope_id AS id, COALESCE(n.name || ' / ' || p.name, p.name, h.scope_id) AS name, CAST(h.day AS TEXT) AS day,
				h.disk_allocated_gb, h.disk_total_gb, h.disk_used_gb
			FROM capacity_history h LEFT JOIN storage_pools p ON CAST(p.id AS TEXT) = h.scope_id
			LEFT JOIN nodes n ON CAST(n.id AS TEXT) = h.node_id
			WHERE h.scope = 'pool' AND h.day >= ? ORDER BY name, h.day`
	case CapacityByRegion:
		q = `SELECT COALESCE(CAST(r.id AS TEXT), '') AS id, COALESCE(r.name, 'No region') AS name, CAST(h.day AS TEXT) AS day,
				SUM(h.vms) AS vms, SUM(h.vcpu_allocated) AS vcpu_allocated, SUM(h.vcpu_total) AS vcpu_total,
				SUM(h.ram_allocated_mb) AS ram_allocated_mb, SUM(h.ram_total_mb) AS ram_total_mb,
				SUM(h.disk_allocated_gb) AS disk_allocated_gb, SUM(h.disk_total_gb) AS disk_total_gb, SUM(h.disk_used_gb) AS disk_used_gb,
				AVG(h.cpu_usage) AS cpu_usage, AVG(h.memory_usage) AS memory_usage
			FROM capacity_history h LEFT JOIN nodes n ON CAST(n.id AS TEXT) = h.scope_id
			LEFT JOIN regions r ON r.id = n.region_id
			WHERE h.scope = 'node' AND h.day >= ? GROUP BY r.id, r.name, h.day ORDER BY name, day`
	default:
		return nil, fmt.Errorf("scope must be node, pool or region")
	}
	var rows []struct {
		ID, Name string
		CapacityPoint
	}
	if err := db.WithContext(ctx).Raw(q, since).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := []CapacitySeries{}
	index := map[string]int{}
	for _, r := range rows {
		i, seen := index[r.ID]
		if !seen {
			i = len(out)
			index[r.ID] = i
			out = append(out, CapacitySeries{ID: r.ID, Name: r.Name})
		}
		out[i].Points = append(out[i].Points, r.CapacityPoint)
	}
	return out, nil
}

// CustomerUsage is what one customer account holds now and transferred in a month.
type CustomerUsage struct {
	UserID        string `json:"user_id"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	VMs           int    `json:"vms"`
	VCPU          int    `json:"vcpu" gorm:"column:vcpu"`
	RAMMB         int64  `json:"ram_mb"`
	DiskGB        int64  `json:"disk_gb"`
	TransferBytes int64  `json:"transfer_bytes"`
}

// CustomerUsageReport lists every customer account with VMs or transfer in
// month (first day of it), largest transfer first. The VMs, vCPU, memory and
// disk are what the account holds now.
func CustomerUsageReport(ctx context.Context, db *gorm.DB, month time.Time) ([]CustomerUsage, error) {
	start := time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	out := []CustomerUsage{}
	err := db.WithContext(ctx).Raw(`SELECT CAST(u.id AS TEXT) AS user_id, u.email, COALESCE(u.name, '') AS name,
			COALESCE(a.vms, 0) AS vms, COALESCE(a.cpu, 0) AS vcpu, COALESCE(a.ram, 0) AS ram_mb, COALESCE(a.disk, 0) AS disk_gb,
			COALESCE(t.bytes, 0) AS transfer_bytes
		FROM users u
		LEFT JOIN (SELECT CAST(user_id AS TEXT) AS user_id, COUNT(*) AS vms, SUM(CAST(resources->>'cpu' AS INTEGER)) AS cpu,
				SUM(CAST(resources->>'ram' AS BIGINT)) AS ram, SUM(CAST(resources->>'disk' AS BIGINT)) AS disk
			FROM vms WHERE deleted_at IS NULL GROUP BY 1) a ON a.user_id = CAST(u.id AS TEXT)
		LEFT JOIN (SELECT CAST(v.user_id AS TEXT) AS user_id, SUM(b.total_bytes) AS bytes
			FROM bandwidth_usages b JOIN vms v ON CAST(v.id AS TEXT) = CAST(b.vm_id AS TEXT)
			WHERE b.deleted_at IS NULL AND b.period_start < ? AND b.period_end >= ? GROUP BY 1) t ON t.user_id = CAST(u.id AS TEXT)
		WHERE u.deleted_at IS NULL AND u.role = 'client' AND (a.vms > 0 OR t.bytes > 0)
		ORDER BY transfer_bytes DESC, u.email`, end, start).Scan(&out).Error
	return out, err
}
