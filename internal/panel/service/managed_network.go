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

// needsProvisioning reports whether the type is materialized on the node
// (isolated/nat via libvirt, vpc via a router namespace) vs. referencing a
// pre-existing host bridge ("bridge").
func needsProvisioning(t string) bool { return t == "isolated" || t == "nat" || t == NetworkTypeVPC }

// NetworkTypeVPC is a tenant VPC: a guest bridge plus a router network
// namespace holding the gateway. Unlike the libvirt types, two VPCs may carry
// the SAME subnet — the namespace is what keeps them apart, so tenants can pick
// their own ranges without coordinating.
const NetworkTypeVPC = "vpc"

func (s *ManagedNetworkService) List(ctx context.Context) ([]models.ManagedNetwork, error) {
	return s.repo.List(ctx)
}

// Create persists a managed network and, for isolated/NAT types bound to a node,
// provisions the libvirt network there, recording the resulting bridge.
// CreateTx is Create, persisting through a caller-supplied transaction so the
// row and the node-side provisioning succeed or fail together.
func (s *ManagedNetworkService) CreateTx(ctx context.Context, tx *gorm.DB, net *models.ManagedNetwork) error {
	if err := s.provision(ctx, net); err != nil {
		return err
	}
	return tx.WithContext(ctx).Create(net).Error
}

// provision fills in defaults and materialises the network on its node.
func (s *ManagedNetworkService) provision(ctx context.Context, net *models.ManagedNetwork) error {
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
	return nil
}

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
		_ = s.undefineTypedOnNode(ctx, *net.NodeID, id, net.Type)
	}
	return s.repo.Delete(ctx, id)
}

func (s *ManagedNetworkService) defineOnNode(ctx context.Context, nodeID string, net *models.ManagedNetwork) (string, error) {
	client, authCtx, closeConn, err := s.agentClient(ctx, nodeID)
	if err != nil {
		return "", err
	}
	defer closeConn()
	if net.Type == NetworkTypeVPC {
		resp, err := client.ConfigureVPC(authCtx, &pb.VPCRequest{
			VpcId:   net.ID,
			Subnet:  net.Subnet,
			Gateway: net.Gateway,
			Create:  true,
		})
		if err != nil {
			return "", err
		}
		if !resp.Success {
			return "", fmt.Errorf("%s", agentErrorMessage(resp.Error))
		}
		return resp.Bridge, nil
	}
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
	return s.undefineTypedOnNode(ctx, nodeID, id, "")
}

func (s *ManagedNetworkService) undefineTypedOnNode(ctx context.Context, nodeID, id, netType string) error {
	client, authCtx, closeConn, err := s.agentClient(ctx, nodeID)
	if err != nil {
		return err
	}
	defer closeConn()
	if netType == NetworkTypeVPC {
		resp, err := client.ConfigureVPC(authCtx, &pb.VPCRequest{VpcId: id, Create: false})
		if err != nil {
			return err
		}
		if !resp.Success {
			return fmt.Errorf("%s", agentErrorMessage(resp.Error))
		}
		return nil
	}
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
