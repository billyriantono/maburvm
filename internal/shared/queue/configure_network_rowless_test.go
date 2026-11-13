package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"google.golang.org/grpc"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

type okAgent struct{ pb.NodeAgentClient }

func (okAgent) ApplyNetworkConfig(context.Context, *pb.NetworkConfigRequest, ...grpc.CallOption) (*pb.NetworkConfigResponse, error) {
	return &pb.NetworkConfigResponse{Success: true}, nil
}

// The post-create step applies network config with a job it builds by hand,
// which has no database row. Reading its ID used to crash the worker and left
// every new VM stuck at "creating".
func TestConfigureNetworkWithRowlessJob(t *testing.T) {
	w := &VMOperationWorker{logger: slog.Default()}
	params, _ := json.Marshal(map[string]any{"ip_address": "192.0.2.10"})
	job := &river.Job[VMOperationJob]{Args: VMOperationJob{VMID: "vm-1", Operation: VMOpConfigureNetwork, Params: params}}
	if err := w.handleConfigureNetwork(context.Background(), okAgent{}, &models.Node{}, &models.VM{ID: "vm-1"}, job); err != nil {
		t.Fatal(err)
	}
}
