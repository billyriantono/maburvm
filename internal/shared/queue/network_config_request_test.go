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

type captureAgent struct {
	pb.NodeAgentClient
	got *pb.NetworkConfigRequest
}

func (a *captureAgent) ApplyNetworkConfig(_ context.Context, req *pb.NetworkConfigRequest, _ ...grpc.CallOption) (*pb.NetworkConfigResponse, error) {
	a.got = req
	return &pb.NetworkConfigResponse{Success: true}, nil
}

func sendNetworkJob(t *testing.T, params map[string]any) *pb.NetworkConfigRequest {
	t.Helper()
	raw, _ := json.Marshal(params)
	agent := &captureAgent{}
	w := &VMOperationWorker{logger: slog.Default()}
	job := &river.Job[VMOperationJob]{Args: VMOperationJob{VMID: "vm-1", Operation: VMOpConfigureNetwork, Params: raw}}
	if err := w.handleConfigureNetwork(context.Background(), agent, &models.Node{}, &models.VM{ID: "vm-1"}, job); err != nil {
		t.Fatal(err)
	}
	return agent.got
}

// Adding an address sends every address the VM holds, each with its prefix,
// so the guest is given all of them rather than only the new one.
func TestNetworkJobCarriesEveryAddressAndPolicy(t *testing.T) {
	req := sendNetworkJob(t, map[string]any{
		"ip_address": "203.0.113.10", "gateway": "203.0.113.1", "netmask": 24, "configure_guest": true,
		"extra_ips":        []string{"198.51.100.20"},
		"ip_prefixes":      map[string]int{"203.0.113.10": 24, "198.51.100.20": 25},
		"firewall_default": models.FirewallDefaultAccept, "block_smtp": true,
		"firewall_rules": []models.FirewallRule{
			{Protocol: "tcp", PortRange: "22", Action: "reject", Direction: "inbound", Priority: 10, Log: true, Family: models.FirewallFamilyV4},
			{Protocol: "icmp", Action: "allow", Direction: "inbound", Priority: 20, ICMPType: "echo-request"},
		},
	})
	if !req.ConfigureGuest {
		t.Fatal("an address change must reconfigure the guest")
	}
	ifs := req.Config.Interfaces
	if len(ifs) != 2 || ifs[0].IpAddress != "203.0.113.10" || ifs[0].Netmask != 24 || ifs[0].Gateway != "203.0.113.1" ||
		ifs[1].IpAddress != "198.51.100.20" || ifs[1].Netmask != 25 {
		t.Fatalf("interfaces = %v", ifs)
	}
	if req.Config.FirewallDefault != models.FirewallDefaultAccept || !req.Config.BlockSmtp {
		t.Fatalf("policy not carried: default=%q smtp=%v", req.Config.FirewallDefault, req.Config.BlockSmtp)
	}
	r := req.Config.FirewallRules
	// An agent that predates reject sees DENY and still drops.
	if r[0].Action != pb.FirewallAction_FIREWALL_ACTION_DENY || !r[0].Reject || !r[0].Log || r[0].Family != "v4" {
		t.Fatalf("reject rule = %v", r[0])
	}
	if r[1].IcmpType != "echo-request" || r[1].Reject {
		t.Fatalf("icmp rule = %v", r[1])
	}
}

// A VM whose last address was released gets a config with no address, which
// makes the node remove its host rules; there is no guest to reconfigure.
func TestNetworkJobWithoutAddressClearsHostRules(t *testing.T) {
	req := sendNetworkJob(t, map[string]any{"ip_address": "", "configure_guest": true})
	if req.ConfigureGuest {
		t.Fatal("no address: nothing to write into the guest")
	}
	for _, i := range req.Config.Interfaces {
		if i.IpAddress != "" {
			t.Fatalf("unexpected address %q", i.IpAddress)
		}
	}
}
