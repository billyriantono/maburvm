package fakeagent

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

func TestFakeNode(t *testing.T) {
	a := New()
	a.Token = "tok"
	client, stop, err := a.Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	ctx := context.Background()
	authed := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer tok")
	cmd := func(c pb.VMCommandType) *pb.VMCommandResponse {
		t.Helper()
		resp, err := client.ExecuteVMCommand(authed, &pb.VMCommandRequest{VmId: "vm-1", Command: c,
			Config: &pb.VMConfig{Resources: &pb.VMResources{Vcpus: 2, MemoryMb: 2048}}})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Like a node, it refuses a call without the token.
	if _, err := client.GetNodeInfo(ctx, &pb.GetNodeInfoRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("call without a token: %v", err)
	}

	if r := cmd(pb.VMCommandType_VM_COMMAND_TYPE_START); r.Success || r.Error.GetCode() != pb.ErrorCode_ERROR_CODE_NOT_FOUND {
		t.Fatalf("start of a VM the node lacks: %+v", r)
	}
	cmd(pb.VMCommandType_VM_COMMAND_TYPE_CREATE)
	cmd(pb.VMCommandType_VM_COMMAND_TYPE_STOP)
	st, err := client.GetVMStatus(authed, &pb.VMStatusRequest{VmId: "vm-1"})
	if err != nil || st.State != pb.VMState_VM_STATE_STOPPED || st.ConfiguredResources.GetVcpus() != 2 {
		t.Fatalf("status %+v, %v", st, err)
	}

	// A hook answers until it hands back to the default.
	calls := 0
	a.On("ExecuteVMCommand", func(proto.Message) (proto.Message, error) {
		if calls++; calls == 1 {
			return nil, status.Error(codes.Unavailable, "rebooting")
		}
		return nil, nil
	})
	if _, err := client.ExecuteVMCommand(authed, &pb.VMCommandRequest{VmId: "vm-1", Command: pb.VMCommandType_VM_COMMAND_TYPE_START}); status.Code(err) != codes.Unavailable {
		t.Fatalf("hooked call: %v", err)
	}
	cmd(pb.VMCommandType_VM_COMMAND_TYPE_START)
	if vm, _ := a.VM("vm-1"); vm.State != pb.VMState_VM_STATE_RUNNING {
		t.Fatalf("state after the hook let go: %v", vm.State)
	}
	cmd(pb.VMCommandType_VM_COMMAND_TYPE_DESTROY)
	if got := a.Commands(); len(got) != 6 || got[5] != "DESTROY vm-1" || len(a.VMs()) != 0 {
		t.Fatalf("commands %v, VMs left %v", got, a.VMs())
	}
}
