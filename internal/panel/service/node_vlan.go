package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// A node's bridges that carry per-VM VLANs. Kept in nodes.vlan_bridges as
// "bridge:uplink,..." and pushed to the agent, which keeps them too; NULL
// leaves it to MABURVM_VLAN_BRIDGES in the agent's environment.

// ErrVLANBridgesUnsupported is an agent too old to take the list.
var ErrVLANBridgesUnsupported = errors.New("the agent on this node is too old to take VLAN bridges from the panel; update it, or set MABURVM_VLAN_BRIDGES in its environment")

// maxVLANBridges bounds the list; a node has a handful of bridges.
const maxVLANBridges = 16

// VLANBridge is one bridge and the uplink port that carries its VLANs tagged.
type VLANBridge struct {
	Bridge string `json:"bridge"`
	Uplink string `json:"uplink"`
	// Problem says why VLANs cannot work there now (from the node); empty
	// when they can or when not asked.
	Problem string `json:"problem,omitempty"`
	// Filtering is the bridge's VLAN filtering flag as the node last said.
	Filtering bool `json:"filtering,omitempty"`
}

// NodeVLANBridges is what the node page shows.
type NodeVLANBridges struct {
	// UseEnvironment is true while the panel sets nothing and the agent's
	// environment decides.
	UseEnvironment bool         `json:"use_environment"`
	Bridges        []VLANBridge `json:"bridges"`
}

// parseVLANBridges reads the stored column.
func parseVLANBridges(s string) []VLANBridge {
	out := []VLANBridge{}
	for _, e := range models.SplitList(s) {
		if b, u, ok := strings.Cut(e, ":"); ok {
			out = append(out, VLANBridge{Bridge: b, Uplink: u})
		}
	}
	return out
}

// GetVLANBridges is what the panel has set for the node.
func (s *NodeService) GetVLANBridges(ctx context.Context, nodeID string) (*NodeVLANBridges, error) {
	var row struct{ VLANBridges *string }
	res := s.db.WithContext(ctx).Table("nodes").Select("vlan_bridges").Where("id = ? AND deleted_at IS NULL", nodeID).Scan(&row)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected == 0 {
		return nil, ErrNodeNotFound
	}
	if row.VLANBridges == nil {
		return &NodeVLANBridges{UseEnvironment: true, Bridges: []VLANBridge{}}, nil
	}
	return &NodeVLANBridges{Bridges: parseVLANBridges(*row.VLANBridges)}, nil
}

// SetVLANBridges checks the list, gives it to the node's agent and keeps it
// once the agent has it. Each bridge comes back with what the node sees there
// (a missing bridge, an uplink that is not its port). Nothing on the node's
// network changes until a VM there is given a VLAN.
func (s *NodeService) SetVLANBridges(ctx context.Context, nodeID string, want *NodeVLANBridges) (*NodeVLANBridges, error) {
	if _, err := s.repo.GetByID(ctx, nodeID); err != nil {
		return nil, ErrNodeNotFound
	}
	req := &pb.SetVLANBridgesRequest{UseEnvironment: want.UseEnvironment}
	if !want.UseEnvironment {
		if len(want.Bridges) > maxVLANBridges {
			return nil, fmt.Errorf("%w: at most %d bridges", ErrInvalidPolicy, maxVLANBridges)
		}
		seen := map[string]bool{}
		for _, b := range want.Bridges {
			b.Bridge, b.Uplink = strings.TrimSpace(b.Bridge), strings.TrimSpace(b.Uplink)
			if !models.ValidInterfaceName(b.Bridge) || !models.ValidInterfaceName(b.Uplink) {
				return nil, fmt.Errorf("%w: %q and %q must be interface names (letters, digits, '-', '_', '.', up to 15)", ErrInvalidPolicy, b.Bridge, b.Uplink)
			}
			if b.Bridge == b.Uplink {
				return nil, fmt.Errorf("%w: %s cannot be its own uplink", ErrInvalidPolicy, b.Bridge)
			}
			if seen[b.Bridge] {
				return nil, fmt.Errorf("%w: %s is named twice", ErrInvalidPolicy, b.Bridge)
			}
			seen[b.Bridge] = true
			req.Bridges = append(req.Bridges, &pb.VLANBridge{Bridge: b.Bridge, Uplink: b.Uplink})
		}
		sort.Slice(req.Bridges, func(i, j int) bool { return req.Bridges[i].Bridge < req.Bridges[j].Bridge })
	}
	if err := s.EnsureAgentRegistered(ctx, nodeID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNodeUnreachable, err)
	}
	resp, err := s.agentClient.SetVLANBridges(ctx, nodeID, req)
	if status.Code(err) == codes.Unimplemented {
		return nil, ErrVLANBridgesUnsupported
	}
	if err != nil {
		return nil, fmt.Errorf("%w: nothing was changed: %v", ErrNodeUnreachable, err)
	}
	if !resp.GetSuccess() {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPolicy, resp.GetError().GetMessage())
	}
	var value any
	if !want.UseEnvironment {
		parts := make([]string, 0, len(req.Bridges))
		for _, b := range req.Bridges {
			parts = append(parts, b.Bridge+":"+b.Uplink)
		}
		value = strings.Join(parts, ",")
	}
	if err := s.db.WithContext(ctx).Table("nodes").Where("id = ?", nodeID).Update("vlan_bridges", value).Error; err != nil {
		return nil, fmt.Errorf("the node has the change, but it could not be saved here: %w", err)
	}
	out := &NodeVLANBridges{UseEnvironment: resp.GetFromEnvironment(), Bridges: []VLANBridge{}}
	for _, b := range resp.GetBridges() {
		out.Bridges = append(out.Bridges, VLANBridge{Bridge: b.GetBridge(), Uplink: b.GetUplink(), Problem: b.GetProblem(), Filtering: b.GetFiltering()})
	}
	return out, nil
}
