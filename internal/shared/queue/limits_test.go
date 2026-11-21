package queue

import (
	"errors"
	"strings"
	"testing"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLimitsProto(t *testing.T) {
	if LimitsProto(models.VMLimits{}) != nil {
		t.Error("no limits must send none")
	}
	p := LimitsProto(models.VMLimits{CPUCapPercent: 40, CPUWeight: 50, ReadIOPS: 300, WriteIOPS: 100, MBps: 25})
	if p.CpuCapPercent != 40 || p.CpuWeight != 50 || p.ReadIopsSec != 300 || p.WriteIopsSec != 100 || p.TotalBytesSec != 25000000 || p.TotalIopsSec != 0 {
		t.Errorf("got %+v", p)
	}
}

func TestLimitsNote(t *testing.T) {
	if n := LimitsNote("n1", &pb.SetVMLimitsResponse{Success: true}, nil); n != nil {
		t.Errorf("applied limits got a note: %s", *n)
	}
	cases := map[string]struct {
		resp *pb.SetVMLimitsResponse
		err  error
		want string
	}{
		"old agent":   {nil, status.Error(codes.Unimplemented, "unknown method"), "older than the panel"},
		"unreachable": {nil, errors.New("connection refused"), "did not answer"},
		"refused":     {&pb.SetVMLimitsResponse{Error: &pb.ErrorResponse{Message: "domain not found"}}, nil, "domain not found"},
		"not live":    {&pb.SetVMLimitsResponse{Success: true, RestartRequired: true, Message: "no cpu controller"}, nil, "powered off and on"},
	}
	for name, c := range cases {
		n := LimitsNote("n1", c.resp, c.err)
		if n == nil || !strings.Contains(*n, c.want) || !strings.Contains(*n, "n1") {
			t.Errorf("%s: note %v, want it to say %q", name, n, c.want)
		}
	}
}
