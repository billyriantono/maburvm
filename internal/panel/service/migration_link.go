package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// The migration link: each node has its own migration key, which every other
// node accepts only from that node's address and only for a libvirt
// connection, and each node pins every other node's SSH host keys. The panel
// is the one place that knows all nodes, so it carries keys between them and
// sets each node's full list at once: a node left out of a list loses its
// access there.

// NodeRemovedHook, when set, is called after a node is deleted. The panel
// sets it to take the node's key off every other node.
var NodeRemovedHook func(ctx context.Context, nodeID string)

// EnsureLink refreshes the nodes' migration identities and sets each of them
// up to trust every other node the panel knows.
func (m *MigrationService) EnsureLink(ctx context.Context, nodeIDs ...string) error {
	nodes := make([]*models.Node, 0, len(nodeIDs))
	for _, id := range nodeIDs {
		node, err := m.vms.nodeRepo.GetByID(ctx, id)
		if err != nil {
			return fmt.Errorf("node %s cannot be read: %w", id, err)
		}
		if _, err := m.migrationInfo(ctx, node, &pb.GetMigrationInfoRequest{}); err != nil {
			if unimplemented(err) {
				return fmt.Errorf("node %s needs an agent update before VMs can move to or from it", node.Name)
			}
			return fmt.Errorf("node %s could not report its migration key: %w", node.Name, err)
		}
		nodes = append(nodes, node)
	}
	for _, node := range nodes {
		if err := m.pushPeers(ctx, node); err != nil {
			return err
		}
	}
	return nil
}

// peersFor lists every other node the panel knows a migration identity for.
func (m *MigrationService) peersFor(ctx context.Context, nodeID string) ([]*pb.MigrationPeer, error) {
	var rows []struct {
		ID                 string
		IPAddress          string
		MigrationPublicKey string
		SSHHostKeys        string `gorm:"column:ssh_host_keys"`
	}
	if err := m.db.WithContext(ctx).Table("nodes").
		Select("id, ip_address, migration_public_key, ssh_host_keys").
		Where("deleted_at IS NULL AND id <> ? AND migration_public_key <> ''", nodeID).
		Order("id").Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("failed to list the nodes' migration keys: %w", err)
	}
	out := make([]*pb.MigrationPeer, 0, len(rows))
	for _, r := range rows {
		p := &pb.MigrationPeer{NodeId: r.ID, Address: hostOnlyIP(r.IPAddress), PublicKey: r.MigrationPublicKey}
		for _, k := range strings.Split(r.SSHHostKeys, "\n") {
			if k = strings.TrimSpace(k); k != "" {
				p.HostKeys = append(p.HostKeys, k)
			}
		}
		out = append(out, p)
	}
	return out, nil
}

func (m *MigrationService) pushPeers(ctx context.Context, node *models.Node) error {
	peers, err := m.peersFor(ctx, node.ID)
	if err != nil {
		return err
	}
	client, actx, err := m.agent(ctx, node)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(actx, time.Minute)
	defer cancel()
	resp, err := client.SetMigrationPeers(cctx, &pb.SetMigrationPeersRequest{Peers: peers})
	if err != nil {
		if unimplemented(err) {
			return fmt.Errorf("node %s needs an agent update before VMs can move to or from it", node.Name)
		}
		return fmt.Errorf("node %s did not take the list of trusted nodes: %w", node.Name, err)
	}
	if !resp.Success {
		return fmt.Errorf("node %s did not take the list of trusted nodes: %s", node.Name, agentMessage(resp.Error, "refused"))
	}
	return nil
}

// RevokeNode takes a removed node's key and host keys off every node that
// has a migration link. A node that cannot be reached now keeps them until
// its list is next set.
func (m *MigrationService) RevokeNode(ctx context.Context, removedID string) {
	var nodes []models.Node
	if err := m.db.WithContext(ctx).Where("id <> ? AND migration_public_key <> ''", removedID).Find(&nodes).Error; err != nil {
		m.logger.ErrorContext(ctx, "could not list nodes to revoke a removed node's migration key", "node_id", removedID, "error", err)
		return
	}
	for i := range nodes {
		if nodes[i].Status == models.NodeStatusOffline {
			continue
		}
		if err := m.pushPeers(ctx, &nodes[i]); err != nil {
			m.logger.WarnContext(ctx, "a removed node's migration key is still accepted on a node", "removed", removedID, "node", nodes[i].Name, "error", err)
		}
	}
}

// NodeLink is a node's side of the migration link, for its page.
type NodeLink struct {
	KeyFingerprint      string     `json:"key_fingerprint"`
	HostKeyFingerprints []string   `json:"host_key_fingerprints"`
	RecordedAt          *time.Time `json:"recorded_at"`
}

// Link reports the migration identity the panel last recorded for a node.
func (m *MigrationService) Link(ctx context.Context, nodeID string) (*NodeLink, error) {
	var row struct {
		MigrationPublicKey  string
		SSHHostKeys         string `gorm:"column:ssh_host_keys"`
		MigrationIdentityAt *time.Time
	}
	if err := m.db.WithContext(ctx).Table("nodes").Select("migration_public_key, ssh_host_keys, migration_identity_at").
		Where("id = ? AND deleted_at IS NULL", nodeID).Scan(&row).Error; err != nil {
		return nil, err
	}
	out := &NodeLink{KeyFingerprint: fingerprint(row.MigrationPublicKey), HostKeyFingerprints: []string{}, RecordedAt: row.MigrationIdentityAt}
	for _, k := range strings.Split(row.SSHHostKeys, "\n") {
		if fp := fingerprint(k); fp != "" {
			out.HostKeyFingerprints = append(out.HostKeyFingerprints, strings.Fields(k)[0]+" "+fp)
		}
	}
	return out, nil
}

func fingerprint(key string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(k)
}
