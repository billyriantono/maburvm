package service

import (
	"context"
	"crypto/tls"
	"fmt"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

// VolumeProvisioner provisions and removes real disk volumes on a node's agent.
// It is an interface so the storage service can be unit-tested with a fake.
type VolumeProvisioner interface {
	CreateVolume(ctx context.Context, node *models.Node, poolType, poolPath, name, format string, sizeGB int64) (path string, actualSizeBytes int64, err error)
	DeleteVolume(ctx context.Context, node *models.Node, poolType, path string) error
}

// agentVolumeProvisioner talks to the node agent over gRPC (TLS + Bearer token).
type agentVolumeProvisioner struct {
	agentPort int
}

// NewAgentVolumeProvisioner returns a provisioner that dials node agents on agentPort.
func NewAgentVolumeProvisioner(agentPort int) VolumeProvisioner {
	if agentPort == 0 {
		agentPort = DefaultAgentPort
	}
	return &agentVolumeProvisioner{agentPort: agentPort}
}

func (p *agentVolumeProvisioner) dial(node *models.Node) (pb.NodeAgentClient, *grpc.ClientConn, error) {
	addr := fmt.Sprintf("%s:%d", node.IPAddress, p.agentPort)
	creds := credentials.NewTLS(&tls.Config{InsecureSkipVerify: true}) // node uses a self-signed cert
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, nil, fmt.Errorf("dial agent at %s: %w", addr, err)
	}
	return pb.NewNodeAgentClient(conn), conn, nil
}

// volumeAuthContext attaches the node's Bearer token + node id, matching the
// agent's auth interceptor requirements.
func volumeAuthContext(ctx context.Context, node *models.Node) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.New(map[string]string{
		"authorization": "Bearer " + node.Token,
		"x-node-id":     node.ID,
	}))
}

func (p *agentVolumeProvisioner) CreateVolume(ctx context.Context, node *models.Node, poolType, poolPath, name, format string, sizeGB int64) (string, int64, error) {
	client, conn, err := p.dial(node)
	if err != nil {
		return "", 0, err
	}
	defer conn.Close()

	resp, err := client.CreateStorageVolume(volumeAuthContext(ctx, node), &pb.CreateStorageVolumeRequest{
		PoolPath: poolPath,
		Name:     name,
		Format:   format,
		SizeGb:   sizeGB,
		PoolType: poolType,
	})
	if err != nil {
		return "", 0, err
	}
	if !resp.Success {
		return "", 0, fmt.Errorf("agent: %s", agentErrMessage(resp.Error))
	}
	return resp.Path, resp.ActualSizeBytes, nil
}

func (p *agentVolumeProvisioner) DeleteVolume(ctx context.Context, node *models.Node, poolType, path string) error {
	client, conn, err := p.dial(node)
	if err != nil {
		return err
	}
	defer conn.Close()

	resp, err := client.DeleteStorageVolume(volumeAuthContext(ctx, node), &pb.DeleteStorageVolumeRequest{Path: path, PoolType: poolType})
	if err != nil {
		return err
	}
	if !resp.Success {
		return fmt.Errorf("agent: %s", agentErrMessage(resp.Error))
	}
	return nil
}

func agentErrMessage(e *pb.ErrorResponse) string {
	if e != nil && e.Message != "" {
		return e.Message
	}
	return "unknown error"
}
