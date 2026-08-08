package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AbuseService tracks how fast each guest is opening new outbound connections
// and lets an operator cut one off.
//
// It exists because nothing else in the panel could see the problem it solves.
// A guest running a port scan is trivial in bytes and enormous in connections,
// so bandwidth graphs stay flat while the node's conntrack table fills and every
// tenant on that node starts losing connections. Worse, the guests that did this
// were not in the panel's VM list at all, so a per-VM view would have shown
// nothing. The data therefore comes from the node's own iptables counters,
// keyed on MAC, covering every libvirt domain the node has.
type AbuseService struct {
	db *gorm.DB
	// nodes owns the agent connection pool. Held as the service rather than the
	// bare client because reaching a node needs its address and token registered
	// into that pool first, and only the node service knows how to do that.
	nodes  *NodeService
	logger *slog.Logger
}

func NewAbuseService(db *gorm.DB, nodes *NodeService, logger *slog.Logger) *AbuseService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AbuseService{db: db, nodes: nodes, logger: logger}
}

// agentFor returns a client that can actually reach the node. Registration is
// per pool and is not implied by the node existing in the database, so every
// entry point has to do this — not just the ones that happen to run after the
// metrics collector.
func (s *AbuseService) agentFor(ctx context.Context, nodeID string) (*client.AgentClient, error) {
	if s.nodes == nil {
		return nil, fmt.Errorf("no agent connection configured")
	}
	if err := s.nodes.EnsureAgentRegistered(ctx, nodeID); err != nil {
		return nil, err
	}
	return s.nodes.AgentClient(), nil
}

// SampleNode reads one node's guest counters and folds them into the stored
// state, turning cumulative counts into a per-second rate.
//
// Called on the metrics collector's tick, so the interval between samples is
// whatever that tick is; the rate is computed from the actual elapsed time
// rather than an assumed interval so a slow or skipped tick cannot inflate it.
func (s *AbuseService) SampleNode(ctx context.Context, nodeID string) error {
	agent, err := s.agentFor(ctx, nodeID)
	if err != nil {
		return err
	}

	reports, err := agent.GetGuestConnections(ctx, nodeID)
	if err != nil {
		return err
	}

	now := time.Now()
	for _, r := range reports {
		mac := strings.ToLower(strings.TrimSpace(r.MAC))
		if mac == "" {
			continue
		}

		var prev models.GuestConnection
		err := s.db.WithContext(ctx).
			Where("node_id = ? AND mac = ?", nodeID, mac).
			First(&prev).Error
		known := err == nil

		row := models.GuestConnection{
			NodeID:        nodeID,
			MAC:           mac,
			VMID:          r.VMID,
			InterfaceName: r.InterfaceName,
			SYNTotal:      r.SYNPackets,
			Quarantined:   r.Quarantined,
			LastSeenAt:    now,
		}

		if known {
			row.ID = prev.ID
			row.PeakRate = prev.PeakRate
			row.QuarantineReason = prev.QuarantineReason
			row.FirstFlaggedAt = prev.FirstFlaggedAt

			row.SYNRate = connectionRate(prev.SYNTotal, r.SYNPackets, now.Sub(prev.LastSeenAt).Seconds())
		}

		if row.SYNRate > row.PeakRate {
			row.PeakRate = row.SYNRate
		}
		switch {
		case row.SYNRate >= models.AbuseSYNRateThreshold && row.FirstFlaggedAt == nil:
			flagged := now
			row.FirstFlaggedAt = &flagged
		case row.SYNRate < models.AbuseSYNRateThreshold && row.Quarantined:
			// A quarantined guest reads as quiet because its traffic is dropped
			// before it can be counted again. Keep it flagged, or releasing it
			// would look like the problem had gone away on its own.
		case row.SYNRate < models.AbuseSYNRateThreshold:
			row.FirstFlaggedAt = nil
		}

		// Fill the hostname for guests the panel manages; guests it does not stay
		// blank, which is itself the signal that something unmanaged is running.
		if row.VMID != "" {
			var hostname string
			if err := s.db.WithContext(ctx).Model(&models.VM{}).
				Where("id = ?", row.VMID).Pluck("hostname", &hostname).Error; err == nil {
				row.VMHostname = hostname
			}
		}

		if err := s.db.WithContext(ctx).Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "node_id"}, {Name: "mac"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"vm_id", "vm_hostname", "interface_name", "syn_total", "syn_rate",
				"peak_rate", "quarantined", "first_flagged_at", "last_seen_at",
			}),
		}).Create(&row).Error; err != nil {
			s.logger.Error("abuse: upsert guest connection failed", "node_id", nodeID, "mac", mac, "error", err)
		}
	}
	return nil
}

// connectionRate turns two cumulative counter readings into a per-second rate.
//
// It returns 0 rather than a number whenever the pair cannot be trusted: a
// counter that went backwards means the node's rules were rebuilt (an agent
// restart clears them), and a non-positive interval means the clock or the
// stored sample is not usable. Both are better reported as "no reading" than as
// a spike — a false spike quarantines an innocent customer, while one skipped
// interval costs nothing, since the next tick reads the same cumulative counter.
func connectionRate(prevTotal, currentTotal int64, elapsedSeconds float64) float64 {
	if elapsedSeconds <= 0 || currentTotal < prevTotal {
		return 0
	}
	return float64(currentTotal-prevTotal) / elapsedSeconds
}

// List returns guests ordered worst-first. flaggedOnly narrows it to those
// currently over the threshold or quarantined, which is the view an operator
// actually wants: on a healthy node every guest appears here at a rate near
// zero, and that noise buries the one row that matters.
func (s *AbuseService) List(ctx context.Context, flaggedOnly bool) ([]models.GuestConnection, error) {
	q := s.db.WithContext(ctx).
		Table("guest_connections AS gc").
		Select("gc.*, nodes.name AS node_name").
		Joins("LEFT JOIN nodes ON nodes.id = gc.node_id").
		Order("gc.quarantined DESC, gc.syn_rate DESC, gc.peak_rate DESC")

	if flaggedOnly {
		q = q.Where("gc.first_flagged_at IS NOT NULL OR gc.quarantined = TRUE")
	}

	var out []models.GuestConnection
	if err := q.Find(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// SetQuarantine cuts a guest off the network, or puts it back.
//
// The node is changed first and the panel's record updated only if that
// succeeded: a row claiming a guest is quarantined while its traffic still flows
// is worse than an error, because it stops anyone from looking further.
func (s *AbuseService) SetQuarantine(ctx context.Context, nodeID, mac, reason string, on bool) error {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if mac == "" {
		return fmt.Errorf("mac is required")
	}

	agent, err := s.agentFor(ctx, nodeID)
	if err != nil {
		return err
	}
	if _, err := agent.SetQuarantine(ctx, nodeID, mac, reason, on); err != nil {
		return fmt.Errorf("node refused the change: %w", err)
	}

	updates := map[string]any{"quarantined": on}
	if on {
		updates["quarantine_reason"] = reason
	} else {
		updates["quarantine_reason"] = ""
		// Released guests start clean, so a fresh flag means fresh abuse rather
		// than a leftover from the incident that has just been dealt with.
		updates["first_flagged_at"] = nil
		updates["peak_rate"] = 0
	}

	return s.db.WithContext(ctx).Model(&models.GuestConnection{}).
		Where("node_id = ? AND mac = ?", nodeID, mac).
		Updates(updates).Error
}
