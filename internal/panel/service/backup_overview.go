package service

import (
	"context"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

// Protection states of a VM on the fleet backup page, worst first.
const (
	BackupStateFailing     = "failing"     // its newest finished backup failed
	BackupStateStale       = "stale"       // scheduled, but no success within the stale window
	BackupStateUnscheduled = "unscheduled" // no active schedule
	BackupStateOK          = "ok"
)

var backupStateRank = map[string]int{BackupStateFailing: 0, BackupStateStale: 1, BackupStateUnscheduled: 2, BackupStateOK: 3}

// DefaultStaleDays is when a scheduled VM's backups count as stale.
const DefaultStaleDays = 2

// BackupOverviewFilter narrows the fleet backup page.
type BackupOverviewFilter struct {
	NodeID    string
	UserID    string
	State     string
	Query     string // part of a hostname or owner email
	StaleDays int
}

// BackupOverviewRow is one VM on the fleet backup page.
type BackupOverviewRow struct {
	VMID       string     `json:"vm_id"`
	Hostname   string     `json:"hostname"`
	VMStatus   string     `json:"vm_status"`
	UserID     string     `json:"user_id"`
	OwnerEmail string     `json:"owner_email"`
	NodeID     string     `json:"node_id"`
	NodeName   string     `json:"node_name"`
	State      string     `json:"state"`
	LastOK     *time.Time `json:"last_success_at,omitempty"`
	LastOKSize int64      `json:"last_success_size"`
	LastOKMode string     `json:"last_success_mode,omitempty"`
	// LastResult is the newest finished backup's status and, when it failed, why.
	LastResult   string     `json:"last_result,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	LastResultAt *time.Time `json:"last_result_at,omitempty"`
	Running      int        `json:"running"`
	Queued       int        `json:"queued"`
	StoredBytes  int64      `json:"stored_bytes"`
	Fulls        int        `json:"full_backups"`
	Incrementals int        `json:"incremental_backups"`
	// The schedule; empty Schedule means none.
	Schedule       string     `json:"schedule,omitempty"`
	ScheduleStatus string     `json:"schedule_status,omitempty"`
	FullEveryDays  int        `json:"full_every_days"`
	NextRun        *time.Time `json:"next_run_at,omitempty"`
}

// BackupDestinationTotal is what one destination holds.
type BackupDestinationTotal struct {
	ID        string `json:"id,omitempty"` // empty: nodes' own storage
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	IsDefault bool   `json:"is_default"`
	Backups   int64  `json:"backups"`
	Bytes     int64  `json:"bytes"`
}

// BackupNodeLoad is what is backing up on one node.
type BackupNodeLoad struct {
	NodeID  string `json:"node_id"`
	Name    string `json:"name"`
	Running int    `json:"running"`
	Queued  int    `json:"queued"`
	Limit   int    `json:"limit"`
}

// BackupOverview is the fleet backup page.
type BackupOverview struct {
	Rows         []BackupOverviewRow      `json:"rows"`
	Counts       map[string]int           `json:"counts"` // by state, before the state filter
	Destinations []BackupDestinationTotal `json:"destinations"`
	Nodes        []BackupNodeLoad         `json:"nodes"`
	StaleDays    int                      `json:"stale_days"`
}

type overviewVM struct {
	ID       string
	Hostname string
	Status   string
	UserID   string
	Email    string
	NodeID   string
	NodeName string
}

type overviewBackup struct {
	ID           string
	VMID         string
	Status       models.BackupStatus
	Mode         string
	Size         int64
	ErrorMessage string
	CreatedAt    time.Time
	CompletedAt  *time.Time
	UpdatedAt    time.Time
}

// FleetBackupOverview lists every VM with how well it is backed up, failing
// and stale ones first, with what each destination holds and what is running
// on each node.
func FleetBackupOverview(ctx context.Context, db *gorm.DB, f BackupOverviewFilter, now time.Time) (*BackupOverview, error) {
	if f.StaleDays <= 0 {
		f.StaleDays = DefaultStaleDays
	}
	var vms []overviewVM
	q := db.WithContext(ctx).Table("vms").
		Select("vms.id, vms.hostname, vms.status, vms.user_id, COALESCE(users.email, '') AS email, vms.node_id, COALESCE(nodes.name, '') AS node_name").
		Joins("LEFT JOIN users ON users.id = vms.user_id").
		Joins("LEFT JOIN nodes ON nodes.id = vms.node_id").
		Where("vms.deleted_at IS NULL")
	if f.NodeID != "" {
		q = q.Where("vms.node_id = ?", f.NodeID)
	}
	if f.UserID != "" {
		q = q.Where("vms.user_id = ?", f.UserID)
	}
	if err := q.Order("vms.hostname").Scan(&vms).Error; err != nil {
		return nil, err
	}

	var backups []overviewBackup
	if err := db.WithContext(ctx).Table("backups").
		Select("id, vm_id, status, mode, size, error_message, created_at, completed_at, updated_at").
		Where("deleted_at IS NULL").Order("created_at").Scan(&backups).Error; err != nil {
		return nil, err
	}
	byVM := map[string][]overviewBackup{}
	for _, b := range backups {
		byVM[b.VMID] = append(byVM[b.VMID], b)
	}
	var schedules []models.BackupSchedule
	if err := db.WithContext(ctx).Find(&schedules).Error; err != nil {
		return nil, err
	}
	schedOf := map[string]models.BackupSchedule{}
	for _, s := range schedules {
		schedOf[s.VMID] = s
	}

	out := &BackupOverview{Counts: map[string]int{}, StaleDays: f.StaleDays, Rows: []BackupOverviewRow{}}
	staleBefore := now.Add(-time.Duration(f.StaleDays) * 24 * time.Hour)
	query := strings.ToLower(strings.TrimSpace(f.Query))
	for _, vm := range vms {
		if query != "" && !strings.Contains(strings.ToLower(vm.Hostname), query) && !strings.Contains(strings.ToLower(vm.Email), query) {
			continue
		}
		row := overviewRow(vm, byVM[vm.ID], schedOf, staleBefore)
		out.Counts[row.State]++
		if f.State == "" || f.State == row.State {
			out.Rows = append(out.Rows, row)
		}
	}
	sort.SliceStable(out.Rows, func(i, j int) bool {
		return backupStateRank[out.Rows[i].State] < backupStateRank[out.Rows[j].State]
	})

	var err error
	if out.Destinations, err = destinationTotals(ctx, db); err != nil {
		return nil, err
	}
	if out.Nodes, err = nodeBackupLoad(ctx, db); err != nil {
		return nil, err
	}
	return out, nil
}

// overviewRow sums up one VM's backups (oldest first) and schedule.
func overviewRow(vm overviewVM, backups []overviewBackup, schedOf map[string]models.BackupSchedule, staleBefore time.Time) BackupOverviewRow {
	row := BackupOverviewRow{VMID: vm.ID, Hostname: vm.Hostname, VMStatus: vm.Status, UserID: vm.UserID, OwnerEmail: vm.Email,
		NodeID: vm.NodeID, NodeName: vm.NodeName}
	active := false
	if s, ok := schedOf[vm.ID]; ok {
		row.Schedule, row.ScheduleStatus, row.FullEveryDays, row.NextRun = s.Schedule, string(s.Status), s.FullEveryDays, s.NextRunAt
		active = s.Status == models.BackupScheduleStatusActive
	}
	for i := range backups {
		b := &backups[i]
		switch b.Status {
		case models.BackupStatusInProgress:
			row.Running++
		case models.BackupStatusPending:
			row.Queued++
		case models.BackupStatusCompleted:
			row.StoredBytes += b.Size
			if b.Mode == models.BackupModeIncremental {
				row.Incrementals++
			} else {
				row.Fulls++
			}
			done := b.CreatedAt
			if b.CompletedAt != nil {
				done = *b.CompletedAt
			}
			if row.LastOK == nil || done.After(*row.LastOK) {
				row.LastOK, row.LastOKSize, row.LastOKMode = &done, b.Size, b.Mode
			}
			fallthrough
		case models.BackupStatusFailed:
			at := b.UpdatedAt
			if b.CompletedAt != nil {
				at = *b.CompletedAt
			}
			if row.LastResultAt == nil || !at.Before(*row.LastResultAt) {
				row.LastResult, row.LastResultAt, row.LastError = string(b.Status), &at, ""
				if b.Status == models.BackupStatusFailed {
					row.LastError = b.ErrorMessage
				}
			}
		}
	}
	switch {
	case row.LastResult == string(models.BackupStatusFailed):
		row.State = BackupStateFailing
	case !active:
		row.State = BackupStateUnscheduled
	case row.LastOK == nil || row.LastOK.Before(staleBefore):
		row.State = BackupStateStale
	default:
		row.State = BackupStateOK
	}
	return row
}

func destinationTotals(ctx context.Context, db *gorm.DB) ([]BackupDestinationTotal, error) {
	var rows []backupDestinationRow
	if err := db.WithContext(ctx).Order("is_default DESC, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	type total struct {
		DestinationID *string
		N             int64
		Bytes         int64
	}
	var totals []total
	if err := db.WithContext(ctx).Table("backups").
		Select("destination_id, COUNT(*) AS n, COALESCE(SUM(size), 0) AS bytes").
		Where("deleted_at IS NULL AND status = ?", models.BackupStatusCompleted).
		Group("destination_id").Scan(&totals).Error; err != nil {
		return nil, err
	}
	byID := map[string]total{}
	for _, t := range totals {
		byID[strVal(t.DestinationID)] = t
	}
	out := make([]BackupDestinationTotal, 0, len(rows)+1)
	for _, r := range rows {
		t := byID[r.ID]
		out = append(out, BackupDestinationTotal{ID: r.ID, Name: r.Name, Kind: kindOf(r.Kind), IsDefault: r.IsDefault, Backups: t.N, Bytes: t.Bytes})
	}
	if t, ok := byID[""]; ok && t.N > 0 {
		out = append(out, BackupDestinationTotal{Name: "Nodes' own storage", Kind: models.BackupStoreNode, Backups: t.N, Bytes: t.Bytes})
	}
	return out, nil
}

func nodeBackupLoad(ctx context.Context, db *gorm.DB) ([]BackupNodeLoad, error) {
	limit := queue.LoadBackupPolicy(ctx, db).NodeLimit()
	var nodes []models.Node
	if err := db.WithContext(ctx).Order("name").Find(&nodes).Error; err != nil {
		return nil, err
	}
	type load struct {
		NodeID string
		Status string
		N      int
	}
	var loads []load
	if err := db.WithContext(ctx).Table("backups").
		Select("vms.node_id AS node_id, backups.status AS status, COUNT(*) AS n").
		Joins("JOIN vms ON vms.id = backups.vm_id").
		Where("backups.deleted_at IS NULL AND backups.status IN ?", []models.BackupStatus{models.BackupStatusPending, models.BackupStatusInProgress}).
		Group("vms.node_id, backups.status").Scan(&loads).Error; err != nil {
		return nil, err
	}
	out := make([]BackupNodeLoad, len(nodes))
	idx := map[string]int{}
	for i, n := range nodes {
		out[i] = BackupNodeLoad{NodeID: n.ID, Name: n.Name, Limit: limit}
		idx[n.ID] = i
	}
	for _, l := range loads {
		i, ok := idx[l.NodeID]
		if !ok {
			continue
		}
		if l.Status == string(models.BackupStatusInProgress) {
			out[i].Running += l.N
		} else {
			out[i].Queued += l.N
		}
	}
	return out, nil
}
