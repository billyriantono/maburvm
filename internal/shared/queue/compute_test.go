package queue

import (
	"testing"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

func TestRestoredStatusIsWhatTheNodeSays(t *testing.T) {
	cases := []struct {
		state pb.VMState
		want  models.VMStatus
	}{
		{pb.VMState_VM_STATE_STOPPED, models.VMStatusStopped},
		{pb.VMState_VM_STATE_PAUSED, models.VMStatusSuspended},
		{pb.VMState_VM_STATE_RUNNING, models.VMStatusRunning},
		// An agent too old to say always started the VM again.
		{pb.VMState_VM_STATE_UNSPECIFIED, models.VMStatusRunning},
	}
	for _, c := range cases {
		if got := restoredStatus(c.state); got != c.want {
			t.Fatalf("%v: got %s, want %s", c.state, got, c.want)
		}
	}
}

func TestFailuresThatLeaveTheVMAsItWas(t *testing.T) {
	for _, op := range []VMOperationType{VMOpAttachISO, VMOpDetachISO, VMOpResetPassword, VMOpRescue, VMOpUnrescue} {
		if !keepsStateOnFailure(op) {
			t.Fatalf("%s marks the VM errored on failure", op)
		}
	}
	for _, op := range []VMOperationType{VMOpCreate, VMOpRebuild, VMOpStart} {
		if keepsStateOnFailure(op) {
			t.Fatalf("%s hides a failure", op)
		}
	}
}

func TestHardwareProto(t *testing.T) {
	if HardwareProto(nil) != nil || HardwareProto(&models.Hardware{}) != nil {
		t.Fatal("a plain VM sent a hardware profile")
	}
	p := HardwareProto(&models.Hardware{CPUModel: "Haswell", Sockets: 2, PinnedCPUs: "4-5", TPM: true, RestartOnCrash: true})
	if p.GetCpuModel() != "Haswell" || p.GetSockets() != 2 || p.GetPinnedCpus() != "4-5" || !p.GetTpm() || !p.GetRestartOnCrash() {
		t.Fatalf("%+v", p)
	}
}
