package queue

import (
	"fmt"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// bytesPerMB is the MB a throughput limit is given in.
const bytesPerMB = 1000000

// LimitsProto is the node's form of a VM's limits; nil when there are none.
func LimitsProto(l models.VMLimits) *pb.VMLimits {
	if l.IsZero() {
		return nil
	}
	return &pb.VMLimits{
		CpuWeight:     int32(l.CPUWeight),
		CpuCapPercent: int32(l.CPUCapPercent),
		TotalIopsSec:  int64(l.IOPS), ReadIopsSec: int64(l.ReadIOPS), WriteIopsSec: int64(l.WriteIOPS),
		TotalBytesSec: int64(l.MBps) * bytesPerMB, ReadBytesSec: int64(l.ReadMBps) * bytesPerMB, WriteBytesSec: int64(l.WriteMBps) * bytesPerMB,
	}
}

// LimitsNote says why a node does not have a VM's limits after it was asked
// to apply them, for the VM page; nil when it has them.
func LimitsNote(nodeName string, resp *pb.SetVMLimitsResponse, err error) *string {
	var s string
	switch {
	case status.Code(err) == codes.Unimplemented:
		s = fmt.Sprintf("Not applied: the agent on node %s is older than the panel and cannot apply limits. Update it under Nodes, then apply them again.", nodeName)
	case err != nil:
		s = fmt.Sprintf("Not applied: node %s did not answer (%v). Apply them again once it is back.", nodeName, err)
	case !resp.GetSuccess():
		s = fmt.Sprintf("Not applied: node %s refused them: %s", nodeName, resp.GetError().GetMessage())
	case resp.GetRestartRequired():
		s = fmt.Sprintf("Saved on node %s, but the running VM could not take them (%s). They apply once it is powered off and on.", nodeName, resp.GetMessage())
	default:
		return nil
	}
	return &s
}

// HardwareProto is a VM's hardware profile for its node; nil for a plain VM.
func HardwareProto(h *models.Hardware) *pb.VMHardware {
	if h == nil || h.IsZero() {
		return nil
	}
	return &pb.VMHardware{
		CpuModel: h.CPUModel, Sockets: int32(h.Sockets), Threads: int32(h.Threads), PinnedCpus: h.PinnedCPUs,
		Numa: h.NUMA, Hugepages: h.Hugepages, Watchdog: h.Watchdog, RestartOnCrash: h.RestartOnCrash,
		SecureBoot: h.SecureBoot, Tpm: h.TPM,
	}
}
