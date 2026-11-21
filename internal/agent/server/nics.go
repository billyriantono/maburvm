package server

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/maburvm/panel/internal/agent/libvirt"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// ConfigureNIC lists a VM's network cards, adds or removes one, or changes a
// card's MAC (see the proto). The VM's first card is never removed here.
func (s *NodeAgentService) ConfigureNIC(ctx context.Context, req *pb.ConfigureNICRequest) (*pb.ConfigureNICResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if req.VmId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "vm_id is required")
	}
	fail := func(code pb.ErrorCode, msg string) (*pb.ConfigureNICResponse, error) {
		return &pb.ConfigureNICResponse{Error: &pb.ErrorResponse{Code: code, Message: msg}}, nil
	}
	info, err := libvirt.GetVMInfo(req.VmId)
	if err != nil {
		return nil, vmLookupError(err)
	}
	running := info.Status == libvirt.VMStatusRunning
	resp := &pb.ConfigureNICResponse{Success: true}

	switch req.Action {
	case pb.NICAction_NIC_ACTION_LIST:
	case pb.NICAction_NIC_ACTION_ATTACH:
		mac, err := models.NormalizeMAC(req.MacAddress)
		if err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err.Error())
		}
		if !models.ValidInterfaceName(req.Bridge) {
			return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "invalid bridge name")
		}
		if _, err := os.Stat(filepath.Join("/sys/class/net", req.Bridge, "bridge")); err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_NOT_FOUND, "there is no bridge "+req.Bridge+" on this node")
		}
		ip := net.ParseIP(req.IpAddress)
		if req.AntiSpoofing && (ip == nil || ip.To4() == nil) {
			return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "anti-spoofing needs the card's IPv4 address")
		}
		filter := libvirt.InterfaceFilterRef(req.AntiSpoofing, req.IpAddress, mac, "")
		live, err := libvirt.AttachNIC(req.VmId, req.Bridge, mac, filter)
		if err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_INTERNAL, "the card could not be added: "+err.Error())
		}
		resp.Live = live
		switch {
		case !running:
			resp.Note = "The card is in place for the VM's next start."
		case !live:
			resp.Note = "The VM's machine has no free slot for a card added while it runs, so the card appears when the VM is next stopped and started from the panel."
		}
		log.Printf("[NodeAgent] VM %s: card %s added on %s (live: %v)", req.VmId, mac, req.Bridge, live)
	case pb.NICAction_NIC_ACTION_DETACH:
		mac, err := models.NormalizeMAC(req.MacAddress)
		if err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err.Error())
		}
		if first, ferr := libvirt.GetVMInterfaceMAC(req.VmId); ferr == nil && strings.EqualFold(first, mac) {
			return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "the VM's first card cannot be removed")
		}
		live, err := libvirt.DetachNIC(req.VmId, mac)
		if errors.Is(err, libvirt.ErrNICNotFound) {
			return fail(pb.ErrorCode_ERROR_CODE_NOT_FOUND, err.Error())
		}
		if err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_INTERNAL, "the card could not be removed: "+err.Error())
		}
		resp.Live = live
		if running && !live {
			resp.Note = "The guest did not release the card, so it stays in the running VM until the VM is next stopped; it is gone from the VM's definition."
		}
		log.Printf("[NodeAgent] VM %s: card %s removed (live: %v)", req.VmId, mac, live)
	case pb.NICAction_NIC_ACTION_SET_MAC:
		mac, err := models.NormalizeMAC(req.MacAddress)
		if err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err.Error())
		}
		newMAC, err := models.NormalizeMAC(req.NewMacAddress)
		if err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, err.Error())
		}
		if running {
			return fail(pb.ErrorCode_ERROR_CODE_CONFLICT, "the VM is running; stop it before changing a card's MAC address")
		}
		if err := libvirt.SetNICMAC(req.VmId, mac, newMAC); errors.Is(err, libvirt.ErrNICNotFound) {
			return fail(pb.ErrorCode_ERROR_CODE_NOT_FOUND, err.Error())
		} else if err != nil {
			return fail(pb.ErrorCode_ERROR_CODE_INTERNAL, "the MAC address could not be changed: "+err.Error())
		}
		log.Printf("[NodeAgent] VM %s: card %s is now %s", req.VmId, mac, newMAC)
	default:
		return fail(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "unknown action")
	}

	nics, err := libvirt.GetVMNICs(req.VmId)
	if err != nil {
		return fail(pb.ErrorCode_ERROR_CODE_INTERNAL, "the VM's cards could not be read: "+err.Error())
	}
	for _, n := range nics {
		resp.Nics = append(resp.Nics, &pb.VMNIC{MacAddress: strings.ToLower(n.MAC), Bridge: n.Bridge, Model: n.Model, Tap: n.Tap})
	}
	return resp, nil
}

// SetVLANBridges replaces the bridges that carry per-VM VLANs (see the proto).
func (s *NodeAgentService) SetVLANBridges(ctx context.Context, req *pb.SetVLANBridgesRequest) (*pb.SetVLANBridgesResponse, error) {
	if _, authenticated := GetNodeIDFromContext(ctx); !authenticated {
		return nil, status.Errorf(codes.Unauthenticated, "not authenticated")
	}
	if s.networkMgr == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "network manager not initialized")
	}
	bridges := map[string]string{}
	for _, b := range req.Bridges {
		if _, dup := bridges[b.Bridge]; dup {
			return &pb.SetVLANBridgesResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: "bridge " + b.Bridge + " is named twice"}}, nil
		}
		bridges[b.Bridge] = b.Uplink
	}
	states, fromEnv, err := s.networkMgr.SetVLANBridges(bridges, req.UseEnvironment)
	if err != nil {
		return &pb.SetVLANBridgesResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: err.Error()}}, nil
	}
	resp := &pb.SetVLANBridgesResponse{Success: true, FromEnvironment: fromEnv}
	for _, st := range states {
		resp.Bridges = append(resp.Bridges, &pb.VLANBridgeStatus{Bridge: st.Bridge, Uplink: st.Uplink, Problem: st.Problem, Filtering: st.Filtering})
	}
	return resp, nil
}
