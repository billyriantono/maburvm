package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// Hypervisor groups: a label on nodes (NVMe, dedicated CPU) inside or across
// regions. A plan naming a group has its VMs placed only on that group's nodes.

var nodeGroupPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ErrInvalidNodeGroup refuses a group label that is not a short name.
var ErrInvalidNodeGroup = errors.New("a node group is 1 to 64 letters, digits, dots, dashes or underscores, starting with a letter or digit")

// NormalizeNodeGroup lower-cases and checks a group label; "" is no group.
func NormalizeNodeGroup(g string) (string, error) {
	g = strings.ToLower(strings.TrimSpace(g))
	if g != "" && !nodeGroupPattern.MatchString(g) {
		return "", ErrInvalidNodeGroup
	}
	return g, nil
}

// inNodeGroup keeps the nodes of a group; "" keeps them all. A group none of
// the nodes is in refuses rather than placing the VM somewhere else.
func inNodeGroup(nodes []models.Node, group string) ([]models.Node, error) {
	if group == "" {
		return nodes, nil
	}
	out := nodes[:0:0]
	for _, n := range nodes {
		if n.NodeGroup == group {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no active node is in the node group %q this plan places VMs in", ErrNoAvailableNodes, group)
	}
	return out, nil
}

// planNodeGroup is the group a plan places VMs in; "" for none or no plan.
func planNodeGroup(ctx context.Context, db *gorm.DB, planID string) string {
	if planID == "" {
		return ""
	}
	var g string
	if err := db.WithContext(ctx).Table("plans").Select("node_group").Where("id = ?", planID).Scan(&g).Error; err != nil {
		return "" // a database without the column has no groups
	}
	return g
}

// NodeGroupSummary is one group as the admin pages list it.
type NodeGroupSummary struct {
	Name    string   `json:"name"`
	Nodes   []string `json:"nodes"`
	Regions []string `json:"regions"`
	Plans   int      `json:"plans"`
}

// ListNodeGroups returns every group in use on a node or a plan.
func ListNodeGroups(ctx context.Context, db *gorm.DB) ([]NodeGroupSummary, error) {
	var nodes []struct {
		Name, NodeGroup string
		Region          *string
	}
	if err := db.WithContext(ctx).Table("nodes").
		Select("nodes.name, nodes.node_group, regions.name AS region").
		Joins("LEFT JOIN regions ON regions.id = nodes.region_id AND regions.deleted_at IS NULL").
		Where("nodes.deleted_at IS NULL AND nodes.node_group <> ''").Order("nodes.name").Scan(&nodes).Error; err != nil {
		return nil, err
	}
	var plans []struct {
		NodeGroup string
		N         int
	}
	if err := db.WithContext(ctx).Table("plans").Select("node_group, COUNT(*) AS n").
		Where("deleted_at IS NULL AND node_group <> ''").Group("node_group").Scan(&plans).Error; err != nil {
		return nil, err
	}
	byName := map[string]*NodeGroupSummary{}
	get := func(name string) *NodeGroupSummary {
		if byName[name] == nil {
			byName[name] = &NodeGroupSummary{Name: name, Nodes: []string{}, Regions: []string{}}
		}
		return byName[name]
	}
	for _, n := range nodes {
		g := get(n.NodeGroup)
		g.Nodes = append(g.Nodes, n.Name)
		if n.Region != nil && !slices.Contains(g.Regions, *n.Region) {
			g.Regions = append(g.Regions, *n.Region)
		}
	}
	for _, p := range plans {
		get(p.NodeGroup).Plans = p.N
	}
	out := make([]NodeGroupSummary, 0, len(byName))
	for _, g := range byName {
		sort.Strings(g.Regions)
		out = append(out, *g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
