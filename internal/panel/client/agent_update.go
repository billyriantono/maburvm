package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// UpdateAgent streams a new agent binary to a node. Success means the node
// verified the binary and scheduled the swap; it restarts a few seconds later.
//
// Deliberately not run through executeWithRetry: a retry after a lost response
// would schedule a second swap on a node that already accepted the first.
func (c *AgentClient) UpdateAgent(ctx context.Context, nodeID, arch, commit string, binary []byte) (string, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return "", err
	}
	pc, err := c.getConnection(ctx, node)
	if err != nil {
		return "", err
	}
	stream, err := pc.client.UpdateAgent(c.createAuthContext(ctx, pc.token))
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(binary)
	const chunk = 1 << 20 // well under gRPC's 4 MiB message default
	for off := 0; off < len(binary) || off == 0; off += chunk {
		msg := &pb.AgentUpdateChunk{Data: binary[off:min(off+chunk, len(binary))]}
		if off == 0 {
			msg.Sha256, msg.Arch, msg.Commit, msg.Size = hex.EncodeToString(sum[:]), arch, commit, int64(len(binary))
		}
		if err := stream.Send(msg); err != nil {
			// The real reason is in the status CloseAndRecv returns.
			break
		}
	}
	resp, err := stream.CloseAndRecv()
	if err != nil {
		return "", err
	}
	if !resp.Success {
		return "", fmt.Errorf("%s", resp.Message)
	}
	return resp.Message, nil
}
