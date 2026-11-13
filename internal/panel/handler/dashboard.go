package handler

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
)

// The two dashboards. Both are "ask the database, show the answer": no node is
// contacted, so they stay fast when a node is down, which is exactly when an
// operator opens the dashboard.
//
//	GET /api/v1/dashboard/overview  staff: what needs attention, and how full each node is
//	GET /api/v1/dashboard/mine      anyone: the caller's own VMs with their health

type dashTotals struct {
	VMs       int64 `json:"vms" gorm:"column:vms"`
	Running   int64 `json:"running"`
	Stopped   int64 `json:"stopped"`
	Error     int64 `json:"error"`
	Suspended int64 `json:"suspended"`
	New7d     int64 `json:"new_7d" gorm:"column:new7d"`
	Customers int64 `json:"customers" gorm:"-"`
}

// dashNode is one node's allocation against what it physically has. Allocated
// figures are what VMs were promised, not what they use right now.
type dashNode struct {
	ID               string     `json:"id" gorm:"column:id"`
	Name             string     `json:"name"`
	Status           string     `json:"status"`
	CreationPaused   bool       `json:"creation_paused" gorm:"column:creation_paused"`
	Region           string     `json:"region"`
	MemoryTotalBytes int64      `json:"memory_total_bytes" gorm:"column:memory_total_bytes"`
	VMCount          int64      `json:"vm_count" gorm:"column:vm_count"`
	Running          int64      `json:"running"`
	VCPU             int64      `json:"vcpu_allocated" gorm:"column:vcpu"`
	RAMMB            int64      `json:"ram_allocated_mb" gorm:"column:rammb"`
	DiskGB           int64      `json:"disk_allocated_gb" gorm:"column:diskgb"`
	DiskTotal        int64      `json:"disk_total_bytes" gorm:"column:disk_total"`
	DiskUsed         int64      `json:"disk_used_bytes" gorm:"column:disk_used"`
	CPUUsage         *float64   `json:"cpu_usage" gorm:"column:cpu_usage"`
	MemoryUsage      *float64   `json:"memory_usage" gorm:"column:memory_usage"`
	ReportedAt       *time.Time `json:"reported_at" gorm:"column:reported_at"`
	ConntrackCount   *int64     `json:"conntrack_count" gorm:"column:conntrack_count"`
	ConntrackMax     *int64     `json:"conntrack_max" gorm:"column:conntrack_max"`
}

type dashPool struct {
	ID    string `json:"id" gorm:"column:id"`
	Name  string `json:"name"`
	Total int64  `json:"total"`
	Used  int64  `json:"used"`
}

type dashStorage struct {
	Name           string `json:"name"`
	Node           string `json:"node"`
	TotalSpace     int64  `json:"total_bytes" gorm:"column:total_space"`
	UsedSpace      int64  `json:"used_bytes" gorm:"column:used_space"`
	AlertThreshold int    `json:"alert_threshold" gorm:"column:alert_threshold"`
}

type dashVMRef struct {
	ID        string    `json:"id" gorm:"column:id"`
	Hostname  string    `json:"hostname"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

type dashMineVM struct {
	ID               string     `json:"id" gorm:"column:id"`
	Hostname         string     `json:"hostname"`
	Status           string     `json:"status"`
	Suspended        bool       `json:"suspended"`
	CPU              int        `json:"cpu" gorm:"column:cpu"`
	RAMMB            int        `json:"ram_mb" gorm:"column:rammb"`
	DiskGB           int        `json:"disk_gb" gorm:"column:diskgb"`
	RegionName       string     `json:"region_name" gorm:"column:region_name"`
	RegionCountry    string     `json:"region_country" gorm:"column:region_country"`
	IP               *string    `json:"ip" gorm:"column:ip"`
	TransferBytes    *int64     `json:"transfer_bytes" gorm:"column:transfer_bytes"`
	TransferQuotaGB  int64      `json:"transfer_quota_gb" gorm:"column:transfer_quota_gb"`
	TransferExceeded *bool      `json:"transfer_exceeded" gorm:"column:transfer_exceeded"`
	TransferResets   *time.Time `json:"transfer_period_end" gorm:"column:transfer_resets"`
	LastBackupStatus *string    `json:"last_backup_status" gorm:"column:last_backup_status"`
	LastBackupAt     *time.Time `json:"last_backup_at" gorm:"column:last_backup_at"`
	// The last hour, one point a minute, oldest first. Empty while the VM is off.
	CPUSeries []float64 `json:"cpu_series" gorm:"-"`
	MemoryNow *float64  `json:"memory_now" gorm:"-"`
}

// RegisterDashboardRoutes wires both dashboard endpoints.
func RegisterDashboardRoutes(e *echo.Echo, db *gorm.DB) {
	auth := middleware.RequireAuth(db)
	fail := func(c echo.Context, err error) error {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Internal Server Error", "message": err.Error()})
	}

	e.GET("/api/v1/dashboard/overview", func(c echo.Context) error {
		q := db.WithContext(c.Request().Context())

		var totals dashTotals
		if err := q.Raw(`SELECT count(*) AS vms,
				count(*) FILTER (WHERE status = 'running') AS running,
				count(*) FILTER (WHERE status = 'stopped') AS stopped,
				count(*) FILTER (WHERE status = 'error') AS error,
				count(*) FILTER (WHERE suspended) AS suspended,
				count(*) FILTER (WHERE created_at > now() - interval '7 days') AS new7d
			FROM vms WHERE deleted_at IS NULL`).Scan(&totals).Error; err != nil {
			return fail(c, err)
		}
		if err := q.Raw(`SELECT count(*) FROM users WHERE role = 'client' AND deleted_at IS NULL`).Scan(&totals.Customers).Error; err != nil {
			return fail(c, err)
		}

		nodes := []dashNode{}
		if err := q.Raw(`SELECT n.id, n.name, n.status, n.creation_paused, coalesce(r.name, '') AS region, n.memory_total_bytes,
				coalesce(v.vm_count, 0) AS vm_count, coalesce(v.running, 0) AS running,
				coalesce(v.vcpu, 0) AS vcpu, coalesce(v.rammb, 0) AS rammb, coalesce(v.diskgb, 0) AS diskgb,
				coalesce(s.total, 0) AS disk_total, coalesce(s.used, 0) AS disk_used,
				m.cpu_usage, m.memory_usage, m.recorded_at AS reported_at, m.conntrack_count, m.conntrack_max
			FROM nodes n
			LEFT JOIN regions r ON r.id = n.region_id
			LEFT JOIN (SELECT node_id, count(*) AS vm_count, count(*) FILTER (WHERE status = 'running') AS running,
					sum((resources->>'cpu')::bigint) AS vcpu, sum((resources->>'ram')::bigint) AS rammb, sum((resources->>'disk')::bigint) AS diskgb
				FROM vms WHERE deleted_at IS NULL GROUP BY node_id) v ON v.node_id = n.id
			LEFT JOIN (SELECT node_id, sum(total_space) AS total, sum(used_space) AS used
				FROM storage_pools WHERE deleted_at IS NULL GROUP BY node_id) s ON s.node_id = n.id
			-- The last time the node answered. The collector also stores a row of
			-- zeros, marked offline, when it cannot reach a node; that is not a reading.
			LEFT JOIN LATERAL (SELECT cpu_usage, memory_usage, recorded_at, conntrack_count, conntrack_max
				FROM node_metrics WHERE node_id = n.id AND status <> 'offline' ORDER BY recorded_at DESC LIMIT 1) m ON true
			WHERE n.deleted_at IS NULL ORDER BY n.name`).Scan(&nodes).Error; err != nil {
			return fail(c, err)
		}

		pools := []dashPool{}
		if err := q.Raw(`SELECT p.id, p.name, count(a.id) AS total, count(a.id) FILTER (WHERE a.status <> 'available') AS used
			FROM ip_pools p LEFT JOIN ip_addresses a ON a.pool_id = p.id AND a.deleted_at IS NULL
			WHERE p.deleted_at IS NULL AND p.family = 'ipv4' GROUP BY p.id, p.name ORDER BY p.name`).Scan(&pools).Error; err != nil {
			return fail(c, err)
		}

		// Storage that has passed the alert level its operator set.
		storage := []dashStorage{}
		if err := q.Raw(`SELECT s.name, coalesce(n.name, '') AS node, s.total_space, s.used_space, s.alert_threshold
			FROM storage_pools s LEFT JOIN nodes n ON n.id = s.node_id
			WHERE s.deleted_at IS NULL AND s.total_space > 0 AND s.alert_threshold > 0
				AND s.used_space::numeric * 100 >= s.total_space::numeric * s.alert_threshold
			ORDER BY s.name`).Scan(&storage).Error; err != nil {
			return fail(c, err)
		}

		// VMs that failed, or have sat in a build or delete for over 15 minutes.
		stuck := []dashVMRef{}
		if err := q.Raw(`SELECT id, hostname, status, updated_at FROM vms
			WHERE deleted_at IS NULL AND (status = 'error'
				OR (status IN ('creating', 'deleting') AND updated_at < now() - interval '15 minutes'))
			ORDER BY updated_at LIMIT 20`).Scan(&stuck).Error; err != nil {
			return fail(c, err)
		}
		overTransfer := []dashVMRef{}
		if err := q.Raw(`SELECT v.id, v.hostname, v.status, v.updated_at FROM bandwidth_usages b
			JOIN vms v ON v.id = b.vm_id AND v.deleted_at IS NULL
			WHERE b.deleted_at IS NULL AND b.exceeded AND current_date BETWEEN b.period_start AND b.period_end
			ORDER BY v.hostname LIMIT 20`).Scan(&overTransfer).Error; err != nil {
			return fail(c, err)
		}
		// VMs with a backup that failed in the last day, with when it last did.
		backupFailed := []dashVMRef{}
		if err := q.Raw(`SELECT v.id, v.hostname, v.status, max(b.created_at) AS updated_at FROM backups b
			JOIN vms v ON v.id = b.vm_id AND v.deleted_at IS NULL
			WHERE b.deleted_at IS NULL AND b.status = 'failed' AND b.created_at > now() - interval '24 hours'
			GROUP BY v.id, v.hostname, v.status ORDER BY v.hostname LIMIT 20`).Scan(&backupFailed).Error; err != nil {
			return fail(c, err)
		}
		var failedJobs int64
		if err := q.Raw(`SELECT count(*) FROM river_job WHERE state = 'discarded' AND finalized_at > now() - interval '24 hours'`).Scan(&failedJobs).Error; err != nil {
			return fail(c, err)
		}

		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{
			"totals":             totals,
			"nodes":              nodes,
			"address_pools":      pools,
			"storage_over_alert": storage,
			"vms_stuck":          stuck,
			"vms_over_transfer":  overTransfer,
			"vms_backup_failed":  backupFailed,
			"failed_jobs_24h":    failedJobs,
		}})
	}, auth, middleware.RequirePermission("admin:access"))

	e.GET("/api/v1/dashboard/mine", func(c echo.Context) error {
		user, ok := middleware.GetUserContext(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
		}
		q := db.WithContext(c.Request().Context())

		// Only the caller's own VMs, and nothing that names a node.
		vms := []dashMineVM{}
		if err := q.Raw(`SELECT v.id, v.hostname, v.status, v.suspended,
				(v.resources->>'cpu')::int AS cpu, (v.resources->>'ram')::int AS rammb, (v.resources->>'disk')::int AS diskgb,
				coalesce(rg.name, '') AS region_name, coalesce(rg.country::text, '') AS region_country,
				(SELECT host(n.ip_address) FROM networks n WHERE n.vm_id = v.id AND n.deleted_at IS NULL ORDER BY n.created_at LIMIT 1) AS ip,
				(SELECT coalesce(max(n.bandwidth_quota_gb), 0) FROM networks n WHERE n.vm_id = v.id AND n.deleted_at IS NULL) AS transfer_quota_gb,
				b.total_bytes AS transfer_bytes, b.exceeded AS transfer_exceeded, b.period_end AS transfer_resets,
				lb.status AS last_backup_status, lb.created_at AS last_backup_at
			FROM vms v
			LEFT JOIN nodes nd ON nd.id = v.node_id
			LEFT JOIN regions rg ON rg.id = nd.region_id
			LEFT JOIN LATERAL (SELECT total_bytes, exceeded, period_end FROM bandwidth_usages
				WHERE vm_id = v.id AND deleted_at IS NULL AND current_date BETWEEN period_start AND period_end
				ORDER BY period_start DESC LIMIT 1) b ON true
			LEFT JOIN LATERAL (SELECT status, created_at FROM backups
				WHERE vm_id = v.id AND deleted_at IS NULL ORDER BY created_at DESC LIMIT 1) lb ON true
			WHERE v.deleted_at IS NULL AND v.user_id = ?
			ORDER BY v.hostname LIMIT 100`, user.ID).Scan(&vms).Error; err != nil {
			return fail(c, err)
		}

		if len(vms) > 0 {
			ids := make([]string, len(vms))
			at := make(map[string]int, len(vms))
			for i := range vms {
				ids[i] = vms[i].ID
				at[vms[i].ID] = i
				vms[i].CPUSeries = []float64{}
			}
			var samples []struct {
				VMID        string  `gorm:"column:vm_id"`
				CPUUsage    float64 `gorm:"column:cpu_usage"`
				MemoryUsage float64 `gorm:"column:memory_usage"`
			}
			if err := q.Raw(`SELECT vm_id, cpu_usage, memory_usage FROM vm_metrics
				WHERE vm_id IN ? AND recorded_at > now() - interval '60 minutes' ORDER BY recorded_at`, ids).Scan(&samples).Error; err != nil {
				return fail(c, err)
			}
			for _, s := range samples {
				vm := &vms[at[s.VMID]]
				vm.CPUSeries = append(vm.CPUSeries, s.CPUUsage)
				mem := s.MemoryUsage
				vm.MemoryNow = &mem
			}
		}

		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"vms": vms}})
	}, auth, middleware.RequirePermission("vm:read"))
}
