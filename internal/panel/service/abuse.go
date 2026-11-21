package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/shared/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AbuseService watches every guest's connection and packet rates, in and out,
// records each episode over a threshold with its readings, acts on it as the
// abuse policy says, and lets staff cut a guest off, null-route an address or
// lift either.
//
// It exists because nothing else in the panel could see the problem it solves.
// A guest running a port scan is trivial in bytes and enormous in connections,
// so bandwidth graphs stay flat while the node's conntrack table fills and every
// tenant on that node starts losing connections. Worse, the guests that did this
// were not in the panel's VM list at all, so a per-VM view would have shown
// nothing. The data therefore comes from the node's own counters, keyed on MAC,
// covering every libvirt domain the node has.
type AbuseService struct {
	db *gorm.DB
	// nodes owns the agent connection pool. Held as the service rather than the
	// bare client because reaching a node needs its address and token registered
	// into that pool first, and only the node service knows how to do that.
	nodes  *NodeService
	logger *slog.Logger
	// network applies and lifts rate limits; nil leaves that action failing.
	network *NetworkService
	alerts  *AlertService
	now     func() time.Time
}

func NewAbuseService(db *gorm.DB, nodes *NodeService, logger *slog.Logger) *AbuseService {
	if logger == nil {
		logger = slog.Default()
	}
	return &AbuseService{db: db, nodes: nodes, logger: logger, now: time.Now}
}

// SetNetworkService lets the service rate-limit VMs and restore their speed.
func (s *AbuseService) SetNetworkService(n *NetworkService) { s.network = n }

// SetAlerts lets the service tell staff about abuse.
func (s *AbuseService) SetAlerts(a *AlertService) { s.alerts = a }

// maxEvidence is how many readings an event keeps: half an hour at one a
// minute, enough to show an attack's shape without growing without bound.
const maxEvidence = 30

// ErrAbuseEventNotFound is returned for an unknown event id.
var ErrAbuseEventNotFound = errors.New("abuse event not found")

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

// guestReadings are one guest's per-second rates at one sample.
type guestReadings map[string]float64

// SampleNode reads one node's guest counters and folds them into the stored
// state, turning cumulative counts into per-second rates, then checks each
// rate against the abuse policy.
//
// Called on the metrics collector's tick, so the interval between samples is
// whatever that tick is; the rate is computed from the actual elapsed time
// rather than an assumed interval so a slow or skipped tick cannot inflate it.
func (s *AbuseService) SampleNode(ctx context.Context, nodeID string) error {
	agent, err := s.agentFor(ctx, nodeID)
	if err != nil {
		return err
	}

	fetched := s.now()
	reports, routes, err := agent.GetGuestConnections(ctx, nodeID)
	if err != nil {
		return err
	}
	// Before the readings are evaluated: a null-route the policy sets below is
	// not in this answer, and must not be taken for one the node has lifted.
	s.reconcileNullRoutes(ctx, nodeID, routes, fetched)

	policy := LoadAbusePolicy(ctx, s.db)
	seen := make([]string, 0, len(reports))
	now := s.now()
	for _, r := range reports {
		mac := strings.ToLower(strings.TrimSpace(r.MAC))
		if mac == "" {
			continue
		}
		seen = append(seen, mac)

		var prev models.GuestConnection
		err := s.db.WithContext(ctx).
			Where("node_id = ? AND mac = ?", nodeID, mac).
			First(&prev).Error
		known := err == nil

		row := models.GuestConnection{
			NodeID:          nodeID,
			MAC:             mac,
			VMID:            r.VMID,
			InterfaceName:   r.InterfaceName,
			SYNTotal:        r.SYNPackets,
			SYNInTotal:      r.SYNInPackets,
			PacketsOutTotal: r.PacketsOut,
			PacketsInTotal:  r.PacketsIn,
			Quarantined:     r.Quarantined,
			LastSeenAt:      now,
		}

		if known {
			row.ID = prev.ID
			row.PeakRate = prev.PeakRate
			row.QuarantineReason = prev.QuarantineReason
			row.FirstFlaggedAt = prev.FirstFlaggedAt

			elapsed := now.Sub(prev.LastSeenAt).Seconds()
			row.SYNRate = connectionRate(prev.SYNTotal, r.SYNPackets, elapsed)
			row.SYNInRate = connectionRate(prev.SYNInTotal, r.SYNInPackets, elapsed)
			row.PPSOut = connectionRate(prev.PacketsOutTotal, r.PacketsOut, elapsed)
			row.PPSIn = connectionRate(prev.PacketsInTotal, r.PacketsIn, elapsed)
		}

		if row.SYNRate > row.PeakRate {
			row.PeakRate = row.SYNRate
		}
		readings := guestReadings{
			models.AbuseOutboundSYN: row.SYNRate,
			models.AbuseOutboundPPS: row.PPSOut,
			models.AbuseInboundSYN:  row.SYNInRate,
			models.AbuseInboundPPS:  row.PPSIn,
		}
		over := false
		for _, kind := range models.AbuseKinds {
			if rule := policy.Rule(kind); rule.Enabled && readings[kind] >= rule.Threshold {
				over = true
			}
		}
		switch {
		case over && row.FirstFlaggedAt == nil:
			flagged := now
			row.FirstFlaggedAt = &flagged
		case !over && row.Quarantined:
			// A quarantined guest reads as quiet because its traffic is dropped
			// before it can be counted again. Keep it flagged, or releasing it
			// would look like the problem had gone away on its own.
		case !over:
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
				"syn_in_total", "syn_in_rate", "packets_out_total", "packets_in_total", "pps_out", "pps_in",
			}),
		}).Create(&row).Error; err != nil {
			s.logger.Error("abuse: upsert guest connection failed", "node_id", nodeID, "mac", mac, "error", err)
			continue
		}

		if known {
			sample := models.AbuseSample{At: now, SYNOut: row.SYNRate, SYNIn: row.SYNInRate, PPSOut: row.PPSOut, PPSIn: row.PPSIn,
				SYNTotal: row.SYNTotal, SYNInTotal: row.SYNInTotal, PacketsOut: row.PacketsOutTotal, PacketsIn: row.PacketsInTotal}
			s.evaluate(ctx, &row, readings, sample, policy)
		}
	}
	s.endGoneEpisodes(ctx, nodeID, seen, now)
	return nil
}

// endGoneEpisodes ends the open episodes of guests the node no longer runs
// (stopped, deleted, moved), which no reading will ever end. One with an
// action in force stays open, so it can still be released.
func (s *AbuseService) endGoneEpisodes(ctx context.Context, nodeID string, macs []string, now time.Time) {
	q := s.db.WithContext(ctx).Model(&models.AbuseEvent{}).
		Where("node_id = ? AND ended_at IS NULL AND kind <> ? AND action_state <> ?", nodeID, models.AbuseManual, models.AbuseStateActive)
	if len(macs) > 0 {
		q = q.Where("mac NOT IN ?", macs)
	}
	if err := q.Update("ended_at", now).Error; err != nil {
		s.logger.Warn("abuse: ending episodes of gone guests failed", "node_id", nodeID, "error", err)
	}
}

// connectionRate turns two cumulative counter readings into a per-second rate.
//
// It returns 0 rather than a number whenever the pair cannot be trusted: a
// counter that went backwards means the node's rules were rebuilt (an agent
// restart clears them), and a non-positive interval means the clock or the
// stored sample is not usable. A previous reading of zero is no baseline: the
// tap packet counters run from the guest's start, so the first reading from an
// upgraded agent (or after an unreadable one) would otherwise be the guest's
// whole history over one minute, flagging every guest on the node. All of
// these are better reported as "no reading" than as a spike — a false spike
// quarantines an innocent customer, while one skipped interval costs nothing,
// since the next tick reads the same cumulative counter.
func connectionRate(prevTotal, currentTotal int64, elapsedSeconds float64) float64 {
	if elapsedSeconds <= 0 || prevTotal <= 0 || currentTotal < prevTotal {
		return 0
	}
	return float64(currentTotal-prevTotal) / elapsedSeconds
}

// abuseStep is what one reading does to the open episode of its kind.
type abuseStep int

const (
	stepNothing  abuseStep = iota
	stepOpen               // a new episode starts
	stepContinue           // the episode goes on
	stepAct                // the episode goes on and has now lasted long enough to act on
	stepEnd                // the episode is over
)

// nextAbuseStep decides what a reading does. An episode is only acted on once,
// after enough readings in a row, and only with an action other than alert;
// one whose action is in force stays open even while the reading is low,
// because a null-route hides the very traffic being measured.
func nextAbuseStep(open *models.AbuseEvent, rule AbuseRule, value float64, sustain int) abuseStep {
	over := rule.Enabled && value >= rule.Threshold
	switch {
	case open == nil && over:
		if sustain <= 1 && rule.Action != models.AbuseActionAlert {
			return stepAct
		}
		return stepOpen
	case open == nil:
		return stepNothing
	case over:
		if open.ActionState == models.AbuseStateNone && rule.Action != models.AbuseActionAlert && open.Samples+1 >= sustain {
			return stepAct
		}
		return stepContinue
	case open.ActionState == models.AbuseStateActive:
		return stepNothing
	default:
		return stepEnd
	}
}

// evaluate moves each reading's episode on by one sample.
func (s *AbuseService) evaluate(ctx context.Context, g *models.GuestConnection, readings guestReadings, sample models.AbuseSample, policy AbusePolicy) {
	for _, kind := range models.AbuseKinds {
		rule := policy.Rule(kind)
		value := readings[kind]
		open, err := s.openEvent(ctx, g.NodeID, g.MAC, kind)
		if err != nil {
			s.logger.Warn("abuse: read open event failed", "node_id", g.NodeID, "mac", g.MAC, "error", err)
			continue
		}
		step := nextAbuseStep(open, rule, value, policy.SustainSamples)
		now := sample.At
		switch step {
		case stepNothing:
			continue
		case stepEnd:
			if err := s.db.WithContext(ctx).Model(&models.AbuseEvent{}).Where("id = ?", open.ID).
				Updates(map[string]any{"ended_at": now, "metric": value}).Error; err != nil {
				s.logger.Warn("abuse: closing event failed", "id", open.ID, "error", err)
			}
			continue
		}
		if open == nil {
			open = &models.AbuseEvent{
				ID: uuid.NewString(), NodeID: g.NodeID, MAC: g.MAC, VMID: g.VMID, VMHostname: g.VMHostname, Kind: kind,
				Threshold: rule.Threshold, Action: models.AbuseActionAlert, ActionState: models.AbuseStateNone,
				StartedAt: now,
			}
			s.alertStaff(ctx, open, value)
		}
		open.Samples++
		open.Metric = value
		if value > open.Peak {
			open.Peak = value
		}
		open.LastSeenAt = now
		open.Evidence = append(open.Evidence, sample)
		if len(open.Evidence) > maxEvidence {
			open.Evidence = open.Evidence[len(open.Evidence)-maxEvidence:]
		}
		if err := s.db.WithContext(ctx).Save(open).Error; err != nil {
			s.logger.Warn("abuse: saving event failed", "node_id", g.NodeID, "mac", g.MAC, "kind", kind, "error", err)
			continue
		}
		if step == stepAct {
			reason := fmt.Sprintf("%s at %s, over %s for %d readings", abuseKindWords(kind), formatRate(kind, value),
				formatRate(kind, rule.Threshold), open.Samples)
			if err := s.act(ctx, open, rule.Action, policy, reason, "policy"); err != nil {
				s.logger.Warn("abuse: automatic action failed", "id", open.ID, "action", rule.Action, "error", err)
			}
		}
	}
}

// openEvent is the episode of a kind still open for a guest, or nil.
func (s *AbuseService) openEvent(ctx context.Context, nodeID, mac, kind string) (*models.AbuseEvent, error) {
	var rows []models.AbuseEvent
	if err := s.db.WithContext(ctx).Where("node_id = ? AND mac = ? AND kind = ? AND ended_at IS NULL", nodeID, mac, kind).
		Order("started_at DESC").Limit(1).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// act takes an action on an episode, records the outcome on it, and tells
// the owner. by is "policy" for an automatic action, or who took it.
func (s *AbuseService) act(ctx context.Context, ev *models.AbuseEvent, action string, policy AbusePolicy, reason, by string) error {
	ev.Action = action
	var actErr error
	switch action {
	case models.AbuseActionQuarantine:
		actErr = s.quarantine(ctx, ev.NodeID, ev.MAC, reason, true)
	case models.AbuseActionRateLimit:
		actErr = s.rateLimit(ctx, ev.VMID, int64(policy.RateLimitMbps))
		if actErr == nil {
			ev.Detail = fmt.Sprintf("Speed held to %d Mbps.", policy.RateLimitMbps)
		}
	case models.AbuseActionNullRoute:
		targets := strings.Split(ev.Targets, ",")
		if ev.Targets == "" {
			targets = s.vmAddresses(ctx, ev.VMID)
		}
		if len(targets) == 0 {
			actErr = errors.New("no public address is known for this guest; null-route it by hand")
			break
		}
		until := s.now().Add(time.Duration(policy.NullRouteMinutes) * time.Minute)
		var applied []string
		for _, ip := range targets {
			if err := s.nullRoute(ctx, ev.NodeID, ip, policy.NullRouteMinutes, reason, true); err != nil {
				actErr = err
				break
			}
			applied = append(applied, ip)
		}
		// A failed episode has no Release, so addresses already dropped would
		// stay dark until they expire: lift them again.
		if actErr != nil {
			for _, ip := range applied {
				if err := s.nullRoute(ctx, ev.NodeID, ip, 0, "", false); err != nil {
					s.logger.Warn("abuse: lifting a partial null-route failed", "id", ev.ID, "ip", ip, "error", err)
				}
			}
		}
		ev.Targets = strings.Join(targets, ",")
		ev.ExpiresAt = &until
		// Stamped after the node answered, so the next reconcile only judges
		// this null-route by a listing taken once it was in force.
		ev.LastSeenAt = s.now()
	default:
		return fmt.Errorf("unknown action %q", action)
	}
	if actErr != nil {
		ev.ActionState = models.AbuseStateFailed
		ev.Detail = actErr.Error()
		if ev.Kind == models.AbuseManual {
			ended := s.now() // no reading will ever end a staff action
			ev.EndedAt = &ended
		}
	} else {
		ev.ActionState = models.AbuseStateActive
	}
	if err := s.db.WithContext(ctx).Save(ev).Error; err != nil {
		s.logger.Warn("abuse: recording action failed", "id", ev.ID, "error", err)
	}
	s.logger.Warn("abuse: action taken", "id", ev.ID, "node_id", ev.NodeID, "mac", ev.MAC, "vm_id", ev.VMID,
		"action", action, "by", by, "state", ev.ActionState, "detail", ev.Detail)
	if actErr != nil {
		return actErr
	}
	if by == "policy" {
		s.alertStaff(ctx, ev, ev.Metric)
	}
	if policy.NotifyOwner {
		s.notifyOwner(ctx, ev, false)
	}
	return nil
}

// Release lifts the action of an episode. The episode stays open until the
// guest is quiet, and is not acted on again: staff decided to let it be.
func (s *AbuseService) Release(ctx context.Context, eventID, by string) (*models.AbuseEvent, error) {
	var ev models.AbuseEvent
	if err := s.db.WithContext(ctx).Where("id = ?", eventID).First(&ev).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrAbuseEventNotFound
		}
		return nil, err
	}
	if ev.ActionState != models.AbuseStateActive {
		return nil, fmt.Errorf("nothing is in force for this event")
	}
	// Marked released before lifting, so the network job that restores a
	// rate-limited VM's speed no longer sees the limit in force.
	now := s.now()
	ev.ActionState, ev.ReleasedAt, ev.ReleasedBy = models.AbuseStateReleased, &now, by
	if ev.Kind == models.AbuseManual {
		ev.EndedAt = &now
	}
	if err := s.db.WithContext(ctx).Save(&ev).Error; err != nil {
		return nil, err
	}
	if err := s.lift(ctx, &ev); err != nil {
		ev.ActionState, ev.ReleasedAt, ev.ReleasedBy, ev.EndedAt = models.AbuseStateActive, nil, "", nil
		if serr := s.db.WithContext(ctx).Save(&ev).Error; serr != nil {
			s.logger.Warn("abuse: restoring event after a failed release failed", "id", ev.ID, "error", serr)
		}
		return nil, err
	}
	if LoadAbusePolicy(ctx, s.db).NotifyOwner {
		s.notifyOwner(ctx, &ev, true)
	}
	return &ev, nil
}

// lift undoes what an episode's action did on the node.
func (s *AbuseService) lift(ctx context.Context, ev *models.AbuseEvent) error {
	switch ev.Action {
	case models.AbuseActionQuarantine:
		return s.quarantine(ctx, ev.NodeID, ev.MAC, "", false)
	case models.AbuseActionRateLimit:
		return s.restoreSpeed(ctx, ev.VMID)
	case models.AbuseActionNullRoute:
		for _, ip := range strings.Split(ev.Targets, ",") {
			if ip == "" {
				continue
			}
			if err := s.nullRoute(ctx, ev.NodeID, ip, 0, "", false); err != nil {
				return err
			}
		}
	}
	return nil
}

// reconcileNullRoutes ends null-route episodes the node no longer carries:
// expired there, or lifted by hand. The node is the authority, since it lifts
// them by itself. fetched is when routes were asked for: an episode acted on
// after that (act stamps last_seen_at) may simply not be in the answer yet.
func (s *AbuseService) reconcileNullRoutes(ctx context.Context, nodeID string, routes []client.NullRoute, fetched time.Time) {
	var active []models.AbuseEvent
	if err := s.db.WithContext(ctx).Where("node_id = ? AND action = ? AND action_state = ? AND last_seen_at < ?", nodeID,
		models.AbuseActionNullRoute, models.AbuseStateActive, fetched).Find(&active).Error; err != nil || len(active) == 0 {
		return
	}
	held := map[string]bool{}
	for _, r := range routes {
		held[r.IP] = true
	}
	now := s.now()
	for _, ev := range active {
		still := false
		for _, ip := range strings.Split(ev.Targets, ",") {
			still = still || held[ip]
		}
		if still {
			continue
		}
		// Ended with it, so an attack still going on opens a new episode and
		// is null-routed again after the sustain readings.
		if err := s.db.WithContext(ctx).Model(&models.AbuseEvent{}).Where("id = ?", ev.ID).
			Updates(map[string]any{"action_state": models.AbuseStateExpired, "ended_at": now}).Error; err != nil {
			s.logger.Warn("abuse: marking null-route expired failed", "id", ev.ID, "error", err)
		}
	}
}

// RateLimited reports whether an abuse rate limit holds a VM's speed down.
func (s *AbuseService) RateLimited(ctx context.Context, vmID string) bool {
	return s != nil && abuseSpeedCap(ctx, s.db, vmID) > 0
}

// abuseSpeedCap is the speed (Mbps) an abuse rate limit in force holds a VM
// to, or 0 when none is. A database without the table reads as none.
func abuseSpeedCap(ctx context.Context, db *gorm.DB, vmID string) int64 {
	if db == nil || vmID == "" {
		return 0
	}
	var n int64
	if err := db.WithContext(ctx).Model(&models.AbuseEvent{}).
		Where("vm_id = ? AND action = ? AND action_state = ?", vmID, models.AbuseActionRateLimit, models.AbuseStateActive).
		Count(&n).Error; err != nil || n == 0 {
		return 0
	}
	return int64(LoadAbusePolicy(ctx, db).RateLimitMbps)
}

func (s *AbuseService) rateLimit(ctx context.Context, vmID string, mbps int64) error {
	if vmID == "" {
		return errors.New("this guest is not a VM the panel manages, so its speed cannot be set; quarantine it instead")
	}
	if s.network == nil {
		return errors.New("rate limiting is not available here")
	}
	return s.network.ApplyLiveBandwidth(ctx, vmID, mbps)
}

// restoreSpeed puts a VM back to the speed it would have without the abuse
// limit: its over-quota throttle when one is on, its plan's speed otherwise.
func (s *AbuseService) restoreSpeed(ctx context.Context, vmID string) error {
	if s.network == nil {
		return errors.New("rate limiting is not available here")
	}
	n, err := s.network.networkRepo.GetByVMID(ctx, vmID)
	if err != nil {
		return err
	}
	speed := n.BandwidthLimit
	if n.Throttled {
		speed = int64(n.ThrottleSpeedMbps)
		if speed <= 0 {
			speed = defaultThrottleMbps
		}
	}
	return s.network.ApplyLiveBandwidth(ctx, vmID, speed)
}

func (s *AbuseService) nullRoute(ctx context.Context, nodeID, ip string, minutes int, reason string, on bool) error {
	agent, err := s.agentFor(ctx, nodeID)
	if err != nil {
		return err
	}
	if _, err := agent.SetNullRoute(ctx, nodeID, ip, minutes, reason, on); err != nil {
		if status.Code(err) == codes.Unimplemented {
			return errors.New("the agent on this node is too old to null-route; update it from the Nodes page")
		}
		return err
	}
	return nil
}

// vmAddresses are the public addresses a VM holds, primary first.
func (s *AbuseService) vmAddresses(ctx context.Context, vmID string) []string {
	if vmID == "" {
		return nil
	}
	var raw []string
	s.db.WithContext(ctx).Raw(`SELECT host(CAST(ip_address AS inet)) FROM networks WHERE CAST(vm_id AS TEXT) = ? AND deleted_at IS NULL
		UNION ALL SELECT host(CAST(address AS inet)) FROM ip_addresses WHERE CAST(vm_id AS TEXT) = ? AND deleted_at IS NULL`, vmID, vmID).
		Scan(&raw)
	var out []string
	seen := map[string]bool{}
	for _, a := range raw {
		ip := net.ParseIP(a)
		if ip == nil || ip.IsPrivate() || ip.IsLoopback() || seen[ip.String()] {
			continue
		}
		seen[ip.String()] = true
		out = append(out, ip.String())
	}
	return out
}

// quarantine cuts a guest off on its node, or puts it back, and mirrors the
// node's answer on the guest's row.
func (s *AbuseService) quarantine(ctx context.Context, nodeID, mac, reason string, on bool) error {
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

// List returns guests ordered worst-first. flaggedOnly narrows it to those
// currently over a threshold or quarantined, which is the view an operator
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

// ListEvents returns episodes newest first; openOnly keeps those not over yet
// or with an action still in force.
func (s *AbuseService) ListEvents(ctx context.Context, openOnly bool, limit, offset int) ([]models.AbuseEvent, int64, error) {
	q := s.db.WithContext(ctx).Table("abuse_events AS e")
	if openOnly {
		q = q.Where("e.ended_at IS NULL OR e.action_state = ?", models.AbuseStateActive)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	out := []models.AbuseEvent{}
	err := q.Select("e.*, nodes.name AS node_name").
		Joins("LEFT JOIN nodes ON CAST(nodes.id AS TEXT) = e.node_id").
		Order("e.started_at DESC").Limit(limit).Offset(offset).Find(&out).Error
	return out, total, err
}

// SetQuarantine cuts a guest off the network, or puts it back, by hand.
//
// The node is changed first and the panel's record updated only if that
// succeeded: a row claiming a guest is quarantined while its traffic still flows
// is worse than an error, because it stops anyone from looking further. The
// change shows on the timeline: cutting off opens a manual episode, putting
// back releases every quarantine in force for the guest.
func (s *AbuseService) SetQuarantine(ctx context.Context, nodeID, mac, reason string, on bool) error {
	return s.SetQuarantineBy(ctx, nodeID, mac, reason, on, "")
}

// SetQuarantineBy is SetQuarantine, naming who did it.
func (s *AbuseService) SetQuarantineBy(ctx context.Context, nodeID, mac, reason string, on bool, by string) error {
	mac = strings.ToLower(strings.TrimSpace(mac))
	if mac == "" {
		return fmt.Errorf("mac is required")
	}
	if err := s.quarantine(ctx, nodeID, mac, reason, on); err != nil {
		return err
	}
	now := s.now()
	if !on {
		inForce := func() *gorm.DB {
			return s.db.WithContext(ctx).Model(&models.AbuseEvent{}).
				Where("node_id = ? AND mac = ? AND action = ? AND action_state = ?", nodeID, mac, models.AbuseActionQuarantine, models.AbuseStateActive)
		}
		// A staff quarantine ends here: no reading ever ends a manual episode.
		if err := inForce().Where("kind = ?", models.AbuseManual).Update("ended_at", now).Error; err != nil {
			return err
		}
		return inForce().Updates(map[string]any{"action_state": models.AbuseStateReleased, "released_at": now, "released_by": by}).Error
	}
	var inForce int64
	s.db.WithContext(ctx).Model(&models.AbuseEvent{}).
		Where("node_id = ? AND mac = ? AND action = ? AND action_state = ?", nodeID, mac, models.AbuseActionQuarantine, models.AbuseStateActive).
		Count(&inForce)
	if inForce > 0 {
		return nil // already on the timeline
	}
	var g models.GuestConnection
	s.db.WithContext(ctx).Where("node_id = ? AND mac = ?", nodeID, mac).Limit(1).Find(&g)
	ev := &models.AbuseEvent{
		ID: uuid.NewString(), NodeID: nodeID, MAC: mac, VMID: g.VMID, VMHostname: g.VMHostname, Kind: models.AbuseManual,
		Metric: g.SYNRate, Peak: g.PeakRate, Action: models.AbuseActionQuarantine, ActionState: models.AbuseStateActive,
		Detail: reason, StartedAt: now, LastSeenAt: now, Evidence: []models.AbuseSample{},
	}
	if err := s.db.WithContext(ctx).Create(ev).Error; err != nil {
		s.logger.Warn("abuse: recording manual quarantine failed", "node_id", nodeID, "mac", mac, "error", err)
	}
	if LoadAbusePolicy(ctx, s.db).NotifyOwner {
		s.notifyOwner(ctx, ev, false)
	}
	return nil
}

// NullRouteInput is a null-route staff set by hand.
type NullRouteInput struct {
	NodeID  string `json:"node_id"`
	IP      string `json:"ip"`
	Minutes int    `json:"minutes"`
	Reason  string `json:"reason"`
}

// NullRoute drops all traffic to one address on a node for a while, by hand,
// and records it as a manual episode that can be released early.
func (s *AbuseService) NullRoute(ctx context.Context, in NullRouteInput, by string) (*models.AbuseEvent, error) {
	ip := net.ParseIP(strings.TrimSpace(in.IP))
	if in.NodeID == "" || ip == nil {
		return nil, fmt.Errorf("node_id and a single IP address are required")
	}
	if in.Minutes < 1 || in.Minutes > 7*24*60 {
		return nil, fmt.Errorf("minutes must be between 1 and %d", 7*24*60)
	}
	ev := &models.AbuseEvent{
		ID: uuid.NewString(), NodeID: in.NodeID, Kind: models.AbuseManual, Targets: ip.String(),
		ActionState: models.AbuseStateNone, StartedAt: s.now(), LastSeenAt: s.now(), Evidence: []models.AbuseSample{},
	}
	// Name the VM holding the address, when one does, so the owner is told and
	// the timeline reads as a server rather than an address.
	var holder struct{ ID, Hostname string }
	s.db.WithContext(ctx).Raw(`SELECT CAST(v.id AS TEXT) AS id, v.hostname FROM vms v WHERE v.deleted_at IS NULL AND CAST(v.id AS TEXT) IN (
			SELECT CAST(vm_id AS TEXT) FROM networks WHERE host(CAST(ip_address AS inet)) = ? AND deleted_at IS NULL
			UNION SELECT CAST(vm_id AS TEXT) FROM ip_addresses WHERE host(CAST(address AS inet)) = ? AND deleted_at IS NULL) LIMIT 1`,
		ip.String(), ip.String()).Scan(&holder)
	ev.VMID, ev.VMHostname = holder.ID, holder.Hostname
	if err := s.db.WithContext(ctx).Create(ev).Error; err != nil {
		return nil, err
	}
	policy := LoadAbusePolicy(ctx, s.db)
	policy.NullRouteMinutes = in.Minutes
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		reason = "null-routed by staff"
	}
	ev.Detail = reason
	if err := s.act(ctx, ev, models.AbuseActionNullRoute, policy, reason, by); err != nil {
		return ev, err
	}
	return ev, nil
}

// alertStaff tells staff about an episode: when it starts, and when the
// policy acts on it.
func (s *AbuseService) alertStaff(ctx context.Context, ev *models.AbuseEvent, value float64) {
	if s.alerts == nil {
		return
	}
	name := ev.VMHostname
	if name == "" {
		name = "unmanaged guest " + ev.MAC
	}
	var nodeName string
	s.db.WithContext(ctx).Table("nodes").Select("name").Where("CAST(id AS TEXT) = ?", ev.NodeID).Scan(&nodeName)
	title := fmt.Sprintf("%s on %s: %s at %s", name, nodeName, abuseKindWords(ev.Kind), formatRate(ev.Kind, value))
	detail := fmt.Sprintf("Over the threshold of %s. Open the Abuse page to see the readings, or to act.", formatRate(ev.Kind, ev.Threshold))
	severity := models.AlertSeverityWarning
	key := "abuse:" + ev.NodeID + ":" + ev.MAC + ":" + ev.Kind
	if ev.ActionState == models.AbuseStateActive {
		// Its own key: under the episode's, the cooldown would swallow it,
		// since the warning went out only a few readings ago.
		key += ":" + ev.Action
		severity = models.AlertSeverityCritical
		title = fmt.Sprintf("%s on %s: %s applied automatically", name, nodeName, abuseActionWords(ev.Action))
		detail = fmt.Sprintf("%s at %s, over the threshold of %s. Release it on the Abuse page.", abuseKindWords(ev.Kind),
			formatRate(ev.Kind, value), formatRate(ev.Kind, ev.Threshold))
	}
	s.alerts.Event(ctx, AlertInput{
		Key: key, Kind: models.AlertKindAbuse, Severity: severity,
		Title: title, Detail: detail, ResourceType: "vm", ResourceID: ev.VMID, ResourceName: ev.VMHostname,
	})
}

// notifyOwner emails a VM's owner that an action was taken on their server
// or lifted. Guests the panel does not manage have no owner to tell.
func (s *AbuseService) notifyOwner(ctx context.Context, ev *models.AbuseEvent, lifted bool) {
	if ev.VMID == "" {
		return
	}
	text := abuseOwnerText(ev, lifted)
	vmID := ev.VMID
	go func() {
		if err := NotifyVMOwnerWith(context.WithoutCancel(ctx), s.db, vmID, EmailAbuseAction, map[string]string{"abuse": text}); err != nil {
			s.logger.Warn("abuse email failed", "vm_id", vmID, "error", err)
		}
	}()
}

// abuseOwnerText says to an owner what happened to their server, and why.
func abuseOwnerText(ev *models.AbuseEvent, lifted bool) string {
	if lifted {
		switch ev.Action {
		case models.AbuseActionNullRoute:
			return "Traffic to your server is no longer being dropped; it is reachable again."
		case models.AbuseActionRateLimit:
			return "The speed limit we put on your server has been lifted."
		default:
			return "Your server's network traffic has been restored."
		}
	}
	seen := ""
	if ev.Kind != models.AbuseManual {
		seen = fmt.Sprintf(" We measured %s.", formatRate(ev.Kind, ev.Peak))
	}
	switch ev.Action {
	case models.AbuseActionNullRoute:
		until := ""
		if ev.ExpiresAt != nil {
			until = " until " + ev.ExpiresAt.UTC().Format("2 January 15:04 UTC")
		}
		return fmt.Sprintf("Your server was the target of a flood of traffic.%s To protect the network, all traffic to %s is being dropped%s. "+
			"The server itself keeps running; it cannot be reached from the internet until then.", seen, strings.ReplaceAll(ev.Targets, ",", ", "), until)
	case models.AbuseActionRateLimit:
		return fmt.Sprintf("Your server was sending far more traffic than a normal workload does, which usually means it has been compromised.%s "+
			"To protect other customers its speed has been limited. %s Find and stop the cause, then contact support.", seen, ev.Detail)
	default:
		return fmt.Sprintf("Your server was sending traffic that looked like an attack, which usually means it has been compromised.%s "+
			"To protect other customers its network has been cut off. The server keeps running and its console still works. "+
			"Find and stop the cause, then contact support to have its network restored.", seen)
	}
}

func abuseKindWords(kind string) string {
	switch kind {
	case models.AbuseOutboundSYN:
		return "new outbound connections"
	case models.AbuseOutboundPPS:
		return "outbound packets"
	case models.AbuseInboundSYN:
		return "new inbound connections"
	case models.AbuseInboundPPS:
		return "inbound packets"
	}
	return "manual action"
}

func abuseActionWords(action string) string {
	switch action {
	case models.AbuseActionQuarantine:
		return "quarantine"
	case models.AbuseActionRateLimit:
		return "rate limit"
	case models.AbuseActionNullRoute:
		return "null-route"
	}
	return "alert"
}

// formatRate renders a reading with its unit.
func formatRate(kind string, v float64) string {
	unit := "connections/s"
	if kind == models.AbuseOutboundPPS || kind == models.AbuseInboundPPS {
		unit = "packets/s"
	}
	return fmt.Sprintf("%.0f %s", v, unit)
}
