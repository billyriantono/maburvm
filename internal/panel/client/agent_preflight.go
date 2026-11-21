package client

import (
	"context"
	"fmt"
	"time"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// GetPreflight asks a node to check its host. Not run through
// executeWithRetry: an older agent answers Unimplemented, which is an answer,
// not a failure to retry or to count against the node's circuit breaker.
func (c *AgentClient) GetPreflight(ctx context.Context, nodeID string, pools, bridges []string) ([]*pb.PreflightCheck, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}
	pc, err := c.getConnection(ctx, node)
	if err != nil {
		return nil, err
	}
	// Each check that runs a command on the node is bounded there at 15s.
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	resp, err := pc.client.GetPreflight(c.createAuthContext(ctx, pc.token), &pb.PreflightRequest{PoolPaths: pools, Bridges: bridges})
	if err != nil {
		return nil, err
	}
	if !resp.GetSuccess() {
		return nil, fmt.Errorf("preflight failed on the node: %s", resp.GetError().GetMessage())
	}
	return resp.GetChecks(), nil
}
