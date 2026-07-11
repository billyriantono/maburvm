package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// ManagedNetworkService manages admin-defined virtual networks. For private
// (isolated) and NAT networks bound to a node, it provisions a real libvirt
// network on that node; "bridge" networks just reference an existing host bridge.
type ManagedNetworkService struct {
	repo     *repository.ManagedNetworkRepository
	nodeRepo *repository.NodeRepository
}

// NewManagedNetworkService creates a new ManagedNetworkService.
func NewManagedNetworkService(db *gorm.DB) *ManagedNetworkService {
	return &ManagedNetworkService{
		repo:     repository.NewManagedNetworkRepository(db),
		nodeRepo: repository.NewNodeRepository(db),
	}
}

// libvirtNetworkName is the stable per-network libvirt network name on nodes.
func libvirtNetworkName(id string) string { return "maburvm-" + id }

// needsProvisioning reports whether the type is materialized by libvirt on the
// node (isolated/nat) vs. referencing a pre-existing host bridge ("bridge").
func needsProvisioning(t string) bool { return t == "isolated" || t == "nat" }

func (s *ManagedNetworkService) List(ctx context.Context) ([]models.ManagedNetwork, error) {
	return s.repo.List(ctx)
}

// Create persists a managed network and, for isolated/NAT types bound to a node,
// provisions the libvirt network there, recording the resulting bridge.
func (s *ManagedNetworkService) Create(ctx context.Context, net *models.ManagedNetwork) error {
	if net.Name == "" {
		return fmt.Errorf("network name is required")
	}
	if net.Type == "" {
		net.Type = "bridge"
	}
	if net.ID == "" {
		net.ID = uuid.NewString()
	}
	if net.NodeID != nil && *net.NodeID != "" && needsProvisioning(net.Type) {
		bridge, err := s.defineOnNode(ctx, *net.NodeID, net)
		if err != nil {
			return fmt.Errorf("failed to provision network on node: %w", err)
		}
		net.Bridge = bridge
	}
	return s.repo.Create(ctx, net)
}

// Delete unprovisions the libvirt network on its node (best-effort) and removes
// the record. A node hiccup doesn't block deleting the record.
func (s *ManagedNetworkService) Delete(ctx context.Context, id string) error {
	if net, err := s.repo.GetByID(ctx, id); err == nil &&
		net.NodeID != nil && *net.NodeID != "" && needsProvisioning(net.Type) {
		_ = s.undefineOnNode(ctx, *net.NodeID, id)
	}
	return s.repo.Delete(ctx, id)
}

func (s *ManagedNetworkService) defineOnNode(ctx context.Context, nodeID string, net *models.ManagedNetwork) (string, error) {
	client, authCtx, closeConn, err := s.agentClient(ctx, nodeID)
	if err != nil {
		return "", err
	}
	defer closeConn()
	resp, err := client.DefineNetwork(authCtx, &pb.DefineNetworkRequest{
		Name:   libvirtNetworkName(net.ID),
		Mode:   net.Type,
		Bridge: net.Bridge,
		Cidr:   net.Subnet,
		Dhcp:   net.DHCPStart != "",
	})
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("%s", agentErrorMessage(resp.Error))
	}
	return resp.Bridge, nil
}

func (s *ManagedNetworkService) undefineOnNode(ctx context.Context, nodeID, id string) error {
	client, authCtx, closeConn, err := s.agentClient(ctx, nodeID)
	if err != nil {
		return err
	}
	defer closeConn()
	resp, err := client.UndefineNetwork(authCtx, &pb.UndefineNetworkRequest{Name: libvirtNetworkName(id)})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("%s", agentErrorMessage(resp.Error))
	}
	return nil
}

// agentClient dials the node's agent (self-signed TLS) and returns the client, a
// context carrying the node's bearer token, and a close func.
func (s *ManagedNetworkService) agentClient(ctx context.Context, nodeID string) (pb.NodeAgentClient, context.Context, func(), error) {
	node, err := s.nodeRepo.GetByID(ctx, nodeID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("node not found: %w", err)
	}
	dialCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := grpc.DialContext(dialCtx, fmt.Sprintf("%s:50051", node.IPAddress),
		grpc.WithTransportCredentials(client.NodeTLSCredentials(node.ID, node.IPAddress)),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to connect to agent: %w", err)
	}
	authCtx := metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{"authorization": "Bearer " + node.Token}))
	return pb.NewNodeAgentClient(conn), authCtx, func() { _ = conn.Close() }, nil
}
