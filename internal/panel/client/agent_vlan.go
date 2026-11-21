package client

import (
	"context"
	"time"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// SetVLANBridges tells a node's agent which of its bridges carry per-VM VLANs.
func (c *AgentClient) SetVLANBridges(ctx context.Context, nodeID string, req *pb.SetVLANBridgesRequest) (*pb.SetVLANBridgesResponse, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}
	pc, err := c.getConnection(ctx, node)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return pc.client.SetVLANBridges(c.createAuthContext(ctx, pc.token), req)
}
