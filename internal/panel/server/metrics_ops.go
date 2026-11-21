package server

import (
	"context"
	"database/sql"
	"log/slog"
	"time"

	"github.com/maburvm/panel/internal/panel/service"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
)

var (
	opsUp = prometheus.NewDesc("maburvm_ops_collect_success", "1 if this group of operational gauges was read on this scrape.", []string{"group"}, nil)

	queueJobsDesc      = prometheus.NewDesc("maburvm_queue_jobs", "Jobs in the queue that have not finished, by kind and state.", []string{"kind", "state"}, nil)
	queueOldestDesc    = prometheus.NewDesc("maburvm_queue_oldest_available_job_age_seconds", "How long the oldest job ready to run has waited for a worker, by queue. A stuck queue grows this.", []string{"queue"}, nil)
	queueFinishedDesc  = prometheus.NewDesc("maburvm_queue_jobs_finished_recently", "Jobs the queue still keeps that finished without success (cancelled or discarded, kept up to 7 days), by kind and state.", []string{"kind", "state"}, nil)
	backupLastOKDesc   = prometheus.NewDesc("maburvm_backup_last_success_timestamp_seconds", "Unix time the VM's newest completed backup finished. Absent for a VM with none.", []string{"vm_id", "vm"}, nil)
	backupLastFailDesc = prometheus.NewDesc("maburvm_backup_last_failed", "1 if the VM's newest backup failed, 0 if it did not.", []string{"vm_id", "vm"}, nil)
	backupsDesc        = prometheus.NewDesc("maburvm_backups", "Backups by status.", []string{"status"}, nil)
	poolTotalDesc      = prometheus.NewDesc("maburvm_storage_pool_capacity_bytes", "Size of the storage pool as its node last reported it.", []string{"node", "pool", "pool_id"}, nil)
	poolFreeDesc       = prometheus.NewDesc("maburvm_storage_pool_available_bytes", "Free space in the storage pool as its node last reported it.", []string{"node", "pool", "pool_id"}, nil)
	poolAllocDesc      = prometheus.NewDesc("maburvm_storage_pool_allocated_bytes", "Virtual disk the panel has placed in the pool (system disks, extra disks, attaches in flight). With thin disks this can exceed the capacity.", []string{"node", "pool", "pool_id"}, nil)
	ipFreeDesc         = prometheus.NewDesc("maburvm_ip_pool_free_addresses", "Addresses in the pool free to hand out.", []string{"pool_id", "pool"}, nil)
	ipTotalDesc        = prometheus.NewDesc("maburvm_ip_pool_addresses", "Addresses in the pool, by status.", []string{"pool_id", "pool", "status"}, nil)
	alertsFiringDesc   = prometheus.NewDesc("maburvm_alerts_firing", "Operator alerts open now, by kind and severity.", []string{"kind", "severity"}, nil)
)

// opsCollector reads the queue, backups, storage pools, IP pools and alerts at
// scrape time. Each group is read on its own, so one that fails (a table an
// older database lacks) does not take the others with it.
type opsCollector struct{ db *gorm.DB }

func (o *opsCollector) Describe(chan<- *prometheus.Desc) {}

func (o *opsCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, g := range []struct {
		name string
		read func(context.Context, *gorm.DB, chan<- prometheus.Metric) error
	}{
		{"queue", collectQueue}, {"backups", collectBackups}, {"storage_pools", collectPools},
		{"ip_pools", collectIPPools}, {"alerts", collectAlerts},
	} {
		up := 1.0
		if err := g.read(ctx, o.db.WithContext(ctx), ch); err != nil {
			slog.Default().Warn("metrics: could not read a group", "group", g.name, "error", err)
			up = 0
		}
		ch <- prometheus.MustNewConstMetric(opsUp, prometheus.GaugeValue, up, g.name)
	}
}

type labelledCount struct {
	A, B  string
	Count float64
}

func collectQueue(ctx context.Context, db *gorm.DB, ch chan<- prometheus.Metric) error {
	var open, done []labelledCount
	if err := db.Raw(`SELECT kind AS a, state::text AS b, COUNT(*) AS count FROM river_job
		WHERE state IN ('available', 'scheduled', 'retryable', 'running', 'pending') GROUP BY kind, state`).Scan(&open).Error; err != nil {
		return err
	}
	for _, r := range open {
		ch <- prometheus.MustNewConstMetric(queueJobsDesc, prometheus.GaugeValue, r.Count, r.A, r.B)
	}
	if err := db.Raw(`SELECT kind AS a, state::text AS b, COUNT(*) AS count FROM river_job
		WHERE state IN ('cancelled', 'discarded') GROUP BY kind, state`).Scan(&done).Error; err != nil {
		return err
	}
	for _, r := range done {
		ch <- prometheus.MustNewConstMetric(queueFinishedDesc, prometheus.GaugeValue, r.Count, r.A, r.B)
	}
	var oldest []struct {
		Queue string
		Age   float64
	}
	if err := db.Raw(`SELECT queue, EXTRACT(EPOCH FROM (NOW() - MIN(scheduled_at))) AS age FROM river_job
		WHERE state = 'available' GROUP BY queue`).Scan(&oldest).Error; err != nil {
		return err
	}
	for _, r := range oldest {
		ch <- prometheus.MustNewConstMetric(queueOldestDesc, prometheus.GaugeValue, max(r.Age, 0), r.Queue)
	}
	return nil
}

func collectBackups(ctx context.Context, db *gorm.DB, ch chan<- prometheus.Metric) error {
	var byStatus []labelledCount
	if err := db.Raw(`SELECT status::text AS a, '' AS b, COUNT(*) AS count FROM backups WHERE deleted_at IS NULL GROUP BY status`).Scan(&byStatus).Error; err != nil {
		return err
	}
	for _, r := range byStatus {
		ch <- prometheus.MustNewConstMetric(backupsDesc, prometheus.GaugeValue, r.Count, r.A)
	}
	rows, err := db.Raw(`SELECT v.id::text, v.hostname,
			MAX(b.completed_at) FILTER (WHERE b.status = 'completed') AS last_ok,
			(array_agg(b.status::text ORDER BY b.created_at DESC))[1] AS newest
		FROM backups b JOIN vms v ON v.id = b.vm_id
		WHERE b.deleted_at IS NULL AND v.deleted_at IS NULL
		GROUP BY v.id, v.hostname`).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, host, newest string
		var lastOK sql.NullTime
		if err := rows.Scan(&id, &host, &lastOK, &newest); err != nil {
			return err
		}
		if lastOK.Valid {
			ch <- prometheus.MustNewConstMetric(backupLastOKDesc, prometheus.GaugeValue, float64(lastOK.Time.Unix()), id, host)
		}
		failed := 0.0
		if newest == "failed" {
			failed = 1
		}
		ch <- prometheus.MustNewConstMetric(backupLastFailDesc, prometheus.GaugeValue, failed, id, host)
	}
	return rows.Err()
}

func collectPools(ctx context.Context, db *gorm.DB, ch chan<- prometheus.Metric) error {
	var pools []struct {
		ID, Name, Node, Shared string
		Total, Free            float64
	}
	// A shared pool is one storage: its series is its roomiest-looking online
	// member's, under the node label "shared", so sums count it once.
	if err := db.Raw(`SELECT p.id::text AS id, p.name, n.name AS node, p.total_space AS total, p.available_space AS free,
			COALESCE(p.shared_pool_id::text, '') AS shared
		FROM storage_pools p JOIN nodes n ON n.id::text = p.node_id::text
		WHERE p.deleted_at IS NULL AND n.deleted_at IS NULL
		ORDER BY (p.status = 'online') DESC, p.total_space DESC, p.id`).Scan(&pools).Error; err != nil {
		return err
	}
	alloc, err := service.PoolAllocations(ctx, db, "")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, p := range pools {
		if p.Shared != "" {
			if seen[p.Shared] {
				continue
			}
			seen[p.Shared] = true
			a := float64(alloc[p.ID])
			p.ID, p.Node = p.Shared, "shared"
			ch <- prometheus.MustNewConstMetric(poolTotalDesc, prometheus.GaugeValue, p.Total, p.Node, p.Name, p.ID)
			ch <- prometheus.MustNewConstMetric(poolFreeDesc, prometheus.GaugeValue, p.Free, p.Node, p.Name, p.ID)
			ch <- prometheus.MustNewConstMetric(poolAllocDesc, prometheus.GaugeValue, a, p.Node, p.Name, p.ID)
			continue
		}
		ch <- prometheus.MustNewConstMetric(poolTotalDesc, prometheus.GaugeValue, p.Total, p.Node, p.Name, p.ID)
		ch <- prometheus.MustNewConstMetric(poolFreeDesc, prometheus.GaugeValue, p.Free, p.Node, p.Name, p.ID)
		ch <- prometheus.MustNewConstMetric(poolAllocDesc, prometheus.GaugeValue, float64(alloc[p.ID]), p.Node, p.Name, p.ID)
	}
	return nil
}

func collectIPPools(ctx context.Context, db *gorm.DB, ch chan<- prometheus.Metric) error {
	var pools []struct {
		ID, Name string
		Free     float64
	}
	if err := db.Raw(`SELECT p.id::text AS id, p.name, COUNT(a.id) FILTER (WHERE a.status = 'available') AS free
		FROM ip_pools p LEFT JOIN ip_addresses a ON a.pool_id = p.id AND a.deleted_at IS NULL
		WHERE p.deleted_at IS NULL GROUP BY p.id, p.name`).Scan(&pools).Error; err != nil {
		return err
	}
	for _, p := range pools {
		ch <- prometheus.MustNewConstMetric(ipFreeDesc, prometheus.GaugeValue, p.Free, p.ID, p.Name)
	}
	var byStatus []struct {
		ID, Name, Status string
		Count            float64
	}
	if err := db.Raw(`SELECT p.id::text AS id, p.name, a.status, COUNT(*) AS count
		FROM ip_addresses a JOIN ip_pools p ON p.id = a.pool_id
		WHERE a.deleted_at IS NULL AND p.deleted_at IS NULL GROUP BY p.id, p.name, a.status`).Scan(&byStatus).Error; err != nil {
		return err
	}
	for _, r := range byStatus {
		ch <- prometheus.MustNewConstMetric(ipTotalDesc, prometheus.GaugeValue, r.Count, r.ID, r.Name, r.Status)
	}
	return nil
}

func collectAlerts(ctx context.Context, db *gorm.DB, ch chan<- prometheus.Metric) error {
	var rows []labelledCount
	if err := db.Raw(`SELECT kind AS a, severity AS b, COUNT(*) AS count FROM alerts WHERE state = 'firing' GROUP BY kind, severity`).Scan(&rows).Error; err != nil {
		return err
	}
	for _, r := range rows {
		ch <- prometheus.MustNewConstMetric(alertsFiringDesc, prometheus.GaugeValue, r.Count, r.A, r.B)
	}
	return nil
}
