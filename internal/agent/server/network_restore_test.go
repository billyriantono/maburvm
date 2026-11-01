package server

import (
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

func TestNetworkConfigPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "network-config.json")
	if s := loadNetworkConfigs(path); len(s.Configs) != 0 || s.BootID != "" {
		t.Fatalf("missing file: %+v", s)
	}
	req := &pb.NetworkConfigRequest{VmId: "vm-1", ReplaceAll: true, Config: &pb.VMNetworkConfig{
		Interfaces: []*pb.NetworkInterface{{IpAddress: "203.0.113.5", AntiSpoofing: true}},
	}}
	updateNetworkConfig(path, "boot-a", "vm-1", req)
	updateNetworkConfig(path, "boot-a", "vm-2", req)
	updateNetworkConfig(path, "boot-b", "vm-2", nil)

	s := loadNetworkConfigs(path)
	if s.BootID != "boot-b" || len(s.Configs) != 1 {
		t.Fatalf("got boot %q, %d configs; want boot-b, 1", s.BootID, len(s.Configs))
	}
	got := &pb.NetworkConfigRequest{}
	if err := protojson.Unmarshal(s.Configs["vm-1"], got); err != nil {
		t.Fatal(err)
	}
	if !got.ReplaceAll || got.Config.Interfaces[0].IpAddress != "203.0.113.5" || !got.Config.Interfaces[0].AntiSpoofing {
		t.Errorf("round trip lost fields: %v", got)
	}
}
