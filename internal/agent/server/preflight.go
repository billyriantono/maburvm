package server

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/maburvm/panel/internal/agent/preflight"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// Preflight checks this host for the panel's node page and the installer.
// No pools checks the default image directory, where disks go then.
func Preflight(ctx context.Context, pools, bridges []string) []preflight.Check {
	return preflight.Run(ctx, preflight.Options{
		Pools:       pools,
		DefaultPool: defaultImageDir,
		Bridges:     bridges,
		LibvirtURI:  getEnvOrDefault("LIBVIRT_URI", "qemu:///system"),
	})
}

// GetPreflight answers the panel's preflight request.
func (s *NodeAgentService) GetPreflight(ctx context.Context, req *pb.PreflightRequest) (*pb.PreflightResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	o := preflight.Options{
		Pools:       req.GetPoolPaths(),
		DefaultPool: defaultImageDir,
		Bridges:     req.GetBridges(),
		LibvirtURI:  getEnvOrDefault("LIBVIRT_URI", "qemu:///system"),
	}
	if s.networkMgr != nil {
		vlan, _ := s.networkMgr.VLANBridges()
		o.VLANBridges = map[string]string{}
		for _, b := range vlan {
			o.VLANBridges[b.Bridge] = b.Uplink
		}
		o.DHCP = s.networkMgr.DHCPStatus()
	}
	checks := preflight.Run(ctx, o)
	out := make([]*pb.PreflightCheck, len(checks))
	for i, c := range checks {
		out[i] = &pb.PreflightCheck{Id: c.ID, Name: c.Name, Status: c.Status, Detail: c.Detail, Fix: c.Fix}
	}
	return &pb.PreflightResponse{Success: true, Checks: out}, nil
}
