package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// PreflightCheck is one line of a node's preflight report, in its agent's words.
type PreflightCheck struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // pass, warn or fail
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// NodePreflight is a node's last preflight report. CheckedAt nil: never asked.
type NodePreflight struct {
	// Supported is false when the node's agent is too old to report one.
	Supported bool             `json:"supported"`
	Checks    []PreflightCheck `json:"checks"`
	CheckedAt *time.Time       `json:"checked_at"`
}

// AcceptsInstaller reports whether the installer may be offered for a node.
func (s *NodeService) AcceptsInstaller(ctx context.Context, nodeID string) (bool, error) {
	if s.db == nil {
		return false, nil
	}
	return NodeAcceptsInstaller(ctx, s.db, nodeID)
}

// StoredPreflight returns the report the panel last stored for a node.
func (s *NodeService) StoredPreflight(ctx context.Context, nodeID string) (*NodePreflight, error) {
	var row struct {
		Preflight   *string
		PreflightAt *time.Time
	}
	res := s.db.WithContext(ctx).Raw(`SELECT preflight, preflight_at FROM nodes WHERE id = ? AND deleted_at IS NULL`, nodeID).Scan(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNodeNotFound
	}
	out := &NodePreflight{Supported: true, Checks: []PreflightCheck{}}
	if row.Preflight == nil || row.PreflightAt == nil {
		return out, nil
	}
	if err := json.Unmarshal([]byte(*row.Preflight), out); err != nil {
		return nil, fmt.Errorf("stored preflight report is unreadable: %w", err)
	}
	out.CheckedAt = row.PreflightAt
	return out, nil
}

// RunPreflight asks the node's agent to check its host now, against the
// directory pools and bridges the panel uses there, and stores the answer.
func (s *NodeService) RunPreflight(ctx context.Context, nodeID string) (*NodePreflight, error) {
	if _, err := s.repo.GetByID(ctx, nodeID); err != nil {
		return nil, ErrNodeNotFound
	}
	if err := s.EnsureAgentRegistered(ctx, nodeID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNodeUnreachable, err)
	}
	pools, bridges := PreflightTargets(ctx, s.db, nodeID)
	checks, err := s.agentClient.GetPreflight(ctx, nodeID, pools, bridges)
	report := NodePreflight{Supported: true, Checks: []PreflightCheck{}}
	if status.Code(err) == codes.Unimplemented {
		report.Supported, err = false, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNodeUnreachable, err)
	}
	for _, c := range checks {
		report.Checks = append(report.Checks, PreflightCheck{ID: c.GetId(), Name: c.GetName(), Status: c.GetStatus(), Detail: c.GetDetail(), Fix: c.GetFix()})
	}
	if err := storePreflight(ctx, s.db, nodeID, &report, time.Now().UTC()); err != nil {
		return nil, err
	}
	return &report, nil
}

func storePreflight(ctx context.Context, db *gorm.DB, nodeID string, report *NodePreflight, at time.Time) error {
	raw, err := json.Marshal(struct {
		Supported bool             `json:"supported"`
		Checks    []PreflightCheck `json:"checks"`
	}{report.Supported, report.Checks})
	if err != nil {
		return err
	}
	if err := db.WithContext(ctx).Exec(`UPDATE nodes SET preflight = ?, preflight_at = ? WHERE id = ?`, string(raw), at, nodeID).Error; err != nil {
		return fmt.Errorf("could not store the preflight report: %w", err)
	}
	report.CheckedAt = &at
	return nil
}

// PreflightTargets are the node's directory storage pools and the bridges its
// IP pools attach VMs to: pools assigned to it and global pools, by the same
// rule as IPAMRepository.ListPoolsForNode. A failed lookup is logged and leaves
// the list short; the host checks still run.
func PreflightTargets(ctx context.Context, db *gorm.DB, nodeID string) (pools, bridges []string) {
	db = db.WithContext(ctx)
	if err := db.Raw(`SELECT DISTINCT path FROM storage_pools
		WHERE node_id = ? AND deleted_at IS NULL AND LOWER(TRIM(type)) IN ('', 'dir', 'file', 'directory') ORDER BY path`, nodeID).Scan(&pools).Error; err != nil {
		slog.Warn("preflight: could not list the node's storage pools", "node_id", nodeID, "error", err)
	}
	if err := db.Raw(`SELECT DISTINCT p.bridge FROM ip_pools p
		WHERE p.deleted_at IS NULL AND COALESCE(p.bridge, '') <> ''
		AND (p.node_id = ? OR p.id IN (SELECT pool_id FROM ip_pool_nodes WHERE node_id = ?)
			OR (p.node_id IS NULL AND p.id NOT IN (SELECT pool_id FROM ip_pool_nodes)))
		ORDER BY p.bridge`, nodeID, nodeID).Scan(&bridges).Error; err != nil {
		slog.Warn("preflight: could not list the node's bridges", "node_id", nodeID, "error", err)
	}
	return pools, bridges
}

// NodeAgentSettings is what a node's agent says it runs with.
type NodeAgentSettings struct {
	// Supported is false for an agent too old to report its settings.
	Supported bool              `json:"supported"`
	Settings  map[string]string `json:"settings"`
}

// AgentSettings asks the node's agent for its configuration, now.
func (s *NodeService) AgentSettings(ctx context.Context, nodeID string) (*NodeAgentSettings, error) {
	if _, err := s.repo.GetByID(ctx, nodeID); err != nil {
		return nil, ErrNodeNotFound
	}
	if err := s.EnsureAgentRegistered(ctx, nodeID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNodeUnreachable, err)
	}
	info, err := s.agentClient.GetNodeInfo(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNodeUnreachable, err)
	}
	out := &NodeAgentSettings{Supported: len(info.AgentSettings) > 0, Settings: info.AgentSettings}
	if out.Settings == nil {
		out.Settings = map[string]string{}
	}
	return out, nil
}
