package queue

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/riverqueue/river"
	"google.golang.org/grpc"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

type hostnameAgent struct {
	pb.NodeAgentClient
	got     *pb.NetworkConfigRequest
	hostErr string
}

func (a *hostnameAgent) ApplyNetworkConfig(_ context.Context, req *pb.NetworkConfigRequest, _ ...grpc.CallOption) (*pb.NetworkConfigResponse, error) {
	a.got = req
	return &pb.NetworkConfigResponse{Success: true, HostnameError: a.hostErr}, nil
}

// The worker reads the VM's rules, its groups' rules and its verified
// hostnames when it runs, sends them in enforcement order, and records on
// the hostnames whether the node routes them.
func TestNetworkJobSendsGroupRulesAndHostnames(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:netgroups?mode=memory&cache=shared"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	const vm = "11111111-1111-4111-8111-111111111111"
	for _, stmt := range []string{
		`CREATE TABLE vms (id TEXT PRIMARY KEY, firewall_default TEXT NOT NULL DEFAULT 'drop', smtp_blocked BOOLEAN NOT NULL DEFAULT 0, deleted_at DATETIME)`,
		`CREATE TABLE port_forwards (id TEXT PRIMARY KEY, vm_id TEXT, network_id TEXT, node_id TEXT, external_port INTEGER, internal_port INTEGER, protocol TEXT, source_ip TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE firewall_rules (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL, protocol TEXT, port_range TEXT, action TEXT, direction TEXT, source_ip TEXT, priority INTEGER, icmp_type TEXT NOT NULL DEFAULT '', log BOOLEAN NOT NULL DEFAULT 0, family TEXT NOT NULL DEFAULT '', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE firewall_group_rules (id TEXT PRIMARY KEY, group_id TEXT NOT NULL, protocol TEXT, port_range TEXT NOT NULL DEFAULT '', action TEXT, direction TEXT, source_ip TEXT, priority INTEGER, icmp_type TEXT NOT NULL DEFAULT '', log BOOLEAN NOT NULL DEFAULT 0, family TEXT NOT NULL DEFAULT '', created_at DATETIME, updated_at DATETIME)`,
		`CREATE TABLE vm_firewall_groups (vm_id TEXT, group_id TEXT, position TEXT, sort_order INTEGER, created_at DATETIME)`,
		`CREATE TABLE vm_hostnames (id TEXT PRIMARY KEY, vm_id TEXT, hostname TEXT, http_port INTEGER, https_port INTEGER, verify_token TEXT, verified_at DATETIME, node_error TEXT NOT NULL DEFAULT '', created_at DATETIME)`,
		`INSERT INTO vms (id) VALUES ('` + vm + `')`,
		`INSERT INTO firewall_rules (id, vm_id, protocol, port_range, action, direction, source_ip, priority) VALUES ('own', '` + vm + `', 'tcp', '8080', 'allow', 'inbound', '0.0.0.0/0', 10)`,
		`INSERT INTO firewall_group_rules (id, group_id, protocol, port_range, action, direction, source_ip, priority) VALUES ('grp', 'g1', 'tcp', '25', 'deny', 'inbound', '0.0.0.0/0', 900)`,
		`INSERT INTO vm_firewall_groups (vm_id, group_id, position, sort_order) VALUES ('` + vm + `', 'g1', 'before', 1)`,
		`INSERT INTO vm_hostnames (id, vm_id, hostname, http_port, https_port, verify_token, verified_at) VALUES ('h1', '` + vm + `', 'www.example.com', 8080, 0, 't', CURRENT_TIMESTAMP)`,
		`INSERT INTO vm_hostnames (id, vm_id, hostname, http_port, https_port, verify_token) VALUES ('h2', '` + vm + `', 'pending.example.com', 80, 443, 't')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(stmt, err)
		}
	}
	prev := globalWorkerContext
	SetWorkerContext(&WorkerContext{DB: db, VMRepo: repository.NewVMRepository(db)})
	t.Cleanup(func() { globalWorkerContext = prev })

	agent := &hostnameAgent{hostErr: "the node cannot open port 80"}
	raw, _ := json.Marshal(map[string]any{"ip_address": "10.20.0.5", "firewall_rules": []models.FirewallRule{{ID: "stale", Protocol: "tcp", PortRange: "1", Action: "allow", Direction: "inbound", Priority: 1}}})
	w := &VMOperationWorker{logger: slog.Default()}
	job := &river.Job[VMOperationJob]{Args: VMOperationJob{VMID: vm, Operation: VMOpConfigureNetwork, Params: raw}}
	if err := w.handleConfigureNetwork(context.Background(), agent, &models.Node{}, &models.VM{ID: vm}, job); err != nil {
		t.Fatal(err)
	}
	rules := agent.got.Config.FirewallRules
	if len(rules) != 2 || rules[0].DestPort != "25" || rules[1].DestPort != "8080" || rules[0].Priority != 1 || rules[1].Priority != 2 {
		t.Fatalf("rules = %v", rules)
	}
	hosts := agent.got.Config.HostnameForwards
	if len(hosts) != 1 || hosts[0].Hostname != "www.example.com" || hosts[0].HttpPort != 8080 || hosts[0].HttpsPort != 0 {
		t.Fatalf("hostnames = %v", hosts)
	}
	var noted string
	db.Raw(`SELECT node_error FROM vm_hostnames WHERE id = 'h1'`).Scan(&noted)
	if noted != "the node cannot open port 80" {
		t.Fatalf("node_error = %q", noted)
	}

	// A group lookup that fails must not send the job's older copy.
	if err := db.Exec(`DROP TABLE vm_firewall_groups`).Error; err != nil {
		t.Fatal(err)
	}
	agent.got = nil
	if err := w.handleConfigureNetwork(context.Background(), agent, &models.Node{}, &models.VM{ID: vm}, job); err == nil || agent.got != nil {
		t.Fatalf("err = %v, sent = %v", err, agent.got != nil)
	}
}
