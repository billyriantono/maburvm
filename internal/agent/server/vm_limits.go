package server

import (
	"context"
	"log"

	"github.com/maburvm/panel/internal/agent/domtune"
	"github.com/maburvm/panel/internal/agent/libvirt"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetVMLimits applies a VM's CPU and disk caps to its definition and, when it
// is running, to the running guest.
func (s *NodeAgentService) SetVMLimits(ctx context.Context, req *pb.SetVMLimitsRequest) (*pb.SetVMLimitsResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if req.GetVmId() == "" {
		return &pb.SetVMLimitsResponse{
			Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: "vm_id is required"},
		}, nil
	}
	restart, liveErr, err := libvirt.SetLimits(req.GetVmId(), domtune.FromProto(req.GetLimits()))
	if err != nil {
		return &pb.SetVMLimitsResponse{
			Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: err.Error()},
		}, nil
	}
	resp := &pb.SetVMLimitsResponse{Success: true, RestartRequired: restart}
	if liveErr != nil {
		resp.Message = liveErr.Error()
	}
	log.Printf("[NodeAgent] limits for %s set (restart_required=%v %s)", req.GetVmId(), restart, resp.Message)
	return resp, nil
}
