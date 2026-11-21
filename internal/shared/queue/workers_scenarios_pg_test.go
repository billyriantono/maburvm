//go:build pg

package queue

// The jobs that drive nodes, run against the real schema and a fake node.
// Each case asks what a job leaves behind: the VM's state, the rows that
// track the work, and what the node was asked to do, on success and on the
// failures that have bitten before (a VM stuck at creating, a row left in
// progress, a retry that redoes a disk transfer).

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/fakeagent"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

const (
	fxNode  = "a0000000-0000-0000-0000-000000000001"
	fxUser  = "a0000000-0000-0000-0000-000000000002"
	fxTmpl  = "a0000000-0000-0000-0000-000000000003"
	fxVM    = "a0000000-0000-0000-0000-000000000004"
	fxPool  = "a0000000-0000-0000-0000-000000000005"
	fxToken = "node-token"
	fxIP    = "192.0.2.50"
	fxDest  = "a0000000-0000-0000-0000-000000000006"
)

type fixture struct {
	t    *testing.T
	db   *gorm.DB
	node *fakeagent.Agent
	// done receives what VMDoneHook was told.
	done chan VMOperationType
	// failed and retrying receive what the failure hooks were told, as "kind: reason".
	failed, retrying chan string
	jobID            atomic.Int64
}

// newFixture is a database holding one customer's VM on one node, with an
// address, a firewall rule and a backup schedule, and a node that holds the
// VM in the given state (none: the node does not have it).
func newFixture(t *testing.T, status models.VMStatus, onNode pb.VMState) *fixture {
	t.Helper()
	db := slotTestDB(t)
	f := &fixture{t: t, db: db, node: fakeagent.New(), done: make(chan VMOperationType, 4), failed: make(chan string, 4), retrying: make(chan string, 4)}
	f.exec(`INSERT INTO users (id, email, password_hash, role) VALUES (?, 'owner@example.test', 'x', 'client')`, fxUser)
	f.exec(`INSERT INTO nodes (id, name, ip_address, status, token) VALUES (?, 'node-1', '192.0.2.10', 'active', ?)`, fxNode, fxToken)
	f.exec(`INSERT INTO os_templates (id, name, version, image_path) VALUES (?, 'Debian', '12', '/var/lib/maburvm/templates/debian-12.qcow2')`, fxTmpl)
	f.exec(`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, resources, status) VALUES (?, ?, ?, 'web-1', ?, '{"cpu":1,"ram":1024,"disk":20}', ?)`,
		fxVM, fxUser, fxNode, fxTmpl, string(status))
	f.exec(`INSERT INTO ip_pools (id, name, node_id, cidr, gateway, bridge) VALUES (?, 'pool', ?, '192.0.2.0/24', '192.0.2.1', 'br0')`, fxPool, fxNode)
	f.exec(`INSERT INTO ip_addresses (pool_id, node_id, address, status, vm_id) VALUES (?, ?, ?, 'assigned', ?)`, fxPool, fxNode, fxIP, fxVM)
	f.exec(`INSERT INTO networks (vm_id, ip_address) VALUES (?, ?)`, fxVM, fxIP)
	f.exec(`INSERT INTO firewall_rules (vm_id, protocol, port_range, action, direction, source_ip, priority) VALUES (?, 'tcp', '22', 'allow', 'inbound', '0.0.0.0/0', 10)`, fxVM)
	f.exec(`INSERT INTO backup_schedules (vm_id, schedule, status, storage_provider) VALUES (?, 'daily', 'active', 's3')`, fxVM)
	if onNode != pb.VMState_VM_STATE_UNSPECIFIED {
		f.node.PutVM(fakeagent.VM{ID: fxVM, State: onNode, Config: &pb.VMConfig{Resources: &pb.VMResources{Vcpus: 1, MemoryMb: 1024, DiskGb: 20}}})
	}

	f.node.Token = fxToken
	client, stop, err := f.node.Dial()
	if err != nil {
		t.Fatal(err)
	}
	agents := NewAgentClient()
	agents.clients[fxNode] = client

	prevCtx, prevStore, prevDone, prevFailed, prevRetrying := globalWorkerContext, ObjectStoreHook, VMDoneHook, JobFailedHook, JobRetryingHook
	SetWorkerContext(&WorkerContext{DB: db, VMRepo: repository.NewVMRepository(db), NodeRepo: repository.NewNodeRepository(db),
		TemplateRepo: repository.NewTemplateRepository(db), NetworkRepo: repository.NewNetworkRepository(db),
		IPAMRepo: repository.NewIPAMRepository(db), AgentClient: agents})
	store := &pb.ObjectStore{Endpoint: "s3.example.test", Bucket: "backups", AccessKey: "ak", SecretKey: "sk"}
	ObjectStoreHook = func(context.Context, string) (*pb.ObjectStore, string, error) { return store, fxDest, nil }
	VMDoneHook = func(_ context.Context, _ string, op VMOperationType) { f.done <- op }
	JobFailedHook = func(_ context.Context, kind string, _ []byte, reason string) { f.failed <- kind + ": " + reason }
	JobRetryingHook = func(_ context.Context, kind string, _ []byte, reason string) { f.retrying <- kind + ": " + reason }
	t.Cleanup(func() {
		globalWorkerContext, ObjectStoreHook, VMDoneHook, JobFailedHook, JobRetryingHook = prevCtx, prevStore, prevDone, prevFailed, prevRetrying
		stop()
	})
	return f
}

func (f *fixture) exec(sql string, args ...any) {
	f.t.Helper()
	if err := f.db.Exec(sql, args...).Error; err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
}

func (f *fixture) scalar(sql string, args ...any) string {
	f.t.Helper()
	var v *string
	if err := f.db.Raw(sql, args...).Scan(&v).Error; err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	if v == nil {
		return "<null>"
	}
	return *v
}

func (f *fixture) vmStatus() string {
	return f.scalar(`SELECT status::text FROM vms WHERE id = ?`, fxVM)
}

func row(kind string, args any, attempt, maxAttempts int, id int64) *rivertype.JobRow {
	encoded, _ := json.Marshal(args)
	return &rivertype.JobRow{ID: id, Kind: kind, EncodedArgs: encoded, Attempt: attempt, MaxAttempts: maxAttempts, CreatedAt: time.Now()}
}

func (f *fixture) vmJob(op VMOperationType, params string, attempt int) *river.Job[VMOperationJob] {
	args := VMOperationJob{VMID: fxVM, NodeID: fxNode, Operation: op}
	if params != "" {
		args.Params = json.RawMessage(params)
	}
	return &river.Job[VMOperationJob]{JobRow: row(args.Kind(), args, attempt, NodeJobMaxAttempts, f.jobID.Add(1)), Args: args}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// outcome is how River will treat what a try returned.
func outcome(err error) string {
	var snooze *rivertype.JobSnoozeError
	switch {
	case err == nil:
		return "done"
	case isJobCancel(err):
		return "cancelled"
	case errors.As(err, &snooze):
		return "snoozed"
	}
	return "retry"
}

// refuse answers one VM command with the node's refusal, and lets the others
// through.
func refuse(command pb.VMCommandType, code pb.ErrorCode, msg string) fakeagent.Hook {
	return func(req proto.Message) (proto.Message, error) {
		r := req.(*pb.VMCommandRequest)
		if r.Command != command {
			return nil, nil
		}
		return &pb.VMCommandResponse{VmId: r.VmId, Command: r.Command, Error: &pb.ErrorResponse{Code: code, Message: msg}}, nil
	}
}

func unreachable(req proto.Message) (proto.Message, error) {
	return nil, status.Error(codes.Unavailable, "connection refused")
}

const createParams = `{"hostname":"web-1","image_path":"/var/lib/maburvm/templates/debian-12.qcow2","root_password":"Root-pass-1","ip_address":"192.0.2.50","gateway":"192.0.2.1","netmask":24,"bridge":"br0","pool_path":"/vm"}`

func TestVMJobsLeaveTheVMAsTheNodeHasIt(t *testing.T) {
	running, stopped, none := pb.VMState_VM_STATE_RUNNING, pb.VMState_VM_STATE_STOPPED, pb.VMState_VM_STATE_UNSPECIFIED
	cases := []struct {
		name     string
		status   models.VMStatus
		onNode   pb.VMState
		setup    func(*fixture)
		op       VMOperationType
		params   string
		attempt  int
		want     string
		wantVM   string
		commands []string
		check    func(*testing.T, *fixture)
	}{
		{
			name: "create", status: models.VMStatusCreating, onNode: none, op: VMOpCreate, params: createParams,
			want: "done", wantVM: "running", commands: []string{"CREATE"},
			check: func(t *testing.T, f *fixture) {
				vm, ok := f.node.VM(fxVM)
				if !ok || vm.State != running || vm.Config.GetResources().GetMemoryMb() != 1024 || vm.Config.GetRootPassword() != "Root-pass-1" {
					t.Fatalf("node holds %+v (%v)", vm, ok)
				}
				if got := vm.Config.GetNetworkConfig().GetInterfaces()[0]; got.GetIpAddress() != fxIP || got.GetBridgeName() != "br0" || got.GetUseDhcp() {
					t.Errorf("interface %+v", got)
				}
				if req := f.node.Calls("ExecuteVMCommand")[0].Req.(*pb.VMCommandRequest); req.PoolPath != "/vm" {
					t.Errorf("disk placed in %q, not the pool the panel chose", req.PoolPath)
				}
				// The default firewall reaches the node with the VM, not at the first change.
				applies := f.node.Calls("ApplyNetworkConfig")
				if len(applies) != 1 || len(applies[0].Req.(*pb.NetworkConfigRequest).GetConfig().GetFirewallRules()) != 1 {
					t.Errorf("post-create network applies: %d", len(applies))
				}
				if iso := f.scalar(`SELECT attached_iso FROM vms WHERE id = ?`, fxVM); iso != "" {
					t.Errorf("attached ISO %q", iso)
				}
				select {
				case op := <-f.done:
					if op != VMOpCreate {
						t.Errorf("done hook told %q", op)
					}
				case <-time.After(5 * time.Second):
					t.Error("the owner was never told the VM is ready")
				}
			},
		},
		{
			name: "create the node refuses", status: models.VMStatusCreating, onNode: none, op: VMOpCreate, params: createParams,
			setup: func(f *fixture) {
				f.node.On("ExecuteVMCommand", refuse(pb.VMCommandType_VM_COMMAND_TYPE_CREATE, pb.ErrorCode_ERROR_CODE_INTERNAL,
					"template image not found at /var/lib/maburvm/templates/debian-12.qcow2"))
			},
			want: "cancelled", wantVM: "error", commands: []string{"CREATE"},
		},
		{
			// Retried, and meanwhile still creating: not failed, not running.
			name: "create while the node is down, tries left", status: models.VMStatusCreating, onNode: none, op: VMOpCreate, params: createParams,
			setup: func(f *fixture) { f.node.On("ExecuteVMCommand", unreachable) },
			want:  "retry", wantVM: "creating", commands: []string{"CREATE"},
			check: func(t *testing.T, f *fixture) {
				if len(f.node.Calls("ApplyNetworkConfig")) != 0 || len(f.done) != 0 {
					t.Error("a create that did not happen was followed up")
				}
			},
		},
		{
			// The VM that sat at "creating" for good.
			name: "create while the node is down, last try", status: models.VMStatusCreating, onNode: none, op: VMOpCreate, params: createParams,
			setup: func(f *fixture) { f.node.On("ExecuteVMCommand", unreachable) }, attempt: NodeJobMaxAttempts,
			want: "retry", wantVM: "error", commands: []string{"CREATE"},
		},
		{name: "start", status: models.VMStatusStopped, onNode: stopped, op: VMOpStart, want: "done", wantVM: "running", commands: []string{"START"}},
		{name: "stop", status: models.VMStatusRunning, onNode: running, op: VMOpStop, want: "done", wantVM: "stopped", commands: []string{"STOP"}},
		// Queued with "force" by a force stop: the node is asked to cut the power.
		{name: "force stop", status: models.VMStatusRunning, onNode: running, op: VMOpStop, params: `{"force":true}`, want: "done", wantVM: "stopped", commands: []string{"FORCE_STOP"}},
		{
			// The node answers once the guest is off, or says it is still on:
			// the VM is then running, neither stopped nor broken.
			name: "stop a guest that has not powered off", status: models.VMStatusRunning, onNode: running, op: VMOpStop,
			setup: func(f *fixture) {
				f.node.On("ExecuteVMCommand", func(req proto.Message) (proto.Message, error) {
					r := req.(*pb.VMCommandRequest)
					return &pb.VMCommandResponse{VmId: r.VmId, Command: r.Command, State: running,
						Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: "the guest was asked to shut down and is still on after 2m0s"}}, nil
				})
			},
			want: "retry", wantVM: "running", commands: []string{"STOP"},
		},
		{
			// An agent from before stops waited for the guest says STOPPED at
			// once; its own status still has the VM on.
			name: "stop answered before the guest is off", status: models.VMStatusRunning, onNode: running, op: VMOpStop,
			setup: func(f *fixture) {
				prev := shutOffWait
				shutOffWait = 50 * time.Millisecond
				f.t.Cleanup(func() { shutOffWait = prev })
				f.node.On("ExecuteVMCommand", func(req proto.Message) (proto.Message, error) {
					r := req.(*pb.VMCommandRequest)
					return &pb.VMCommandResponse{Success: true, VmId: r.VmId, Command: r.Command, State: stopped}, nil
				})
			},
			want: "retry", wantVM: "running", commands: []string{"STOP"},
		},
		{
			// Queued before a restore began: it must not boot the VM under it.
			name: "start while a restore holds the VM", status: models.VMStatusStopped, onNode: stopped, op: VMOpStart,
			setup: func(f *fixture) {
				f.exec(`CREATE TABLE IF NOT EXISTS river_job (id bigserial PRIMARY KEY, state text, kind text, args jsonb)`)
				f.exec(`INSERT INTO river_job (state, kind, args) VALUES ('running', 'restore', jsonb_build_object('vm_id', ?::text))`, fxVM)
			},
			want: "snoozed", wantVM: "stopped",
		},
		{name: "restart", status: models.VMStatusRunning, onNode: running, op: VMOpRestart, want: "done", wantVM: "running", commands: []string{"RESTART"}},
		{
			name: "delete", status: models.VMStatusDeleting, onNode: running, op: VMOpDelete,
			want: "done", commands: []string{"DESTROY"},
			check: func(t *testing.T, f *fixture) { assertDeleted(t, f) },
		},
		{
			// Already gone from the node is what a delete wants: records are cleaned up.
			name: "delete a VM the node no longer has", status: models.VMStatusDeleting, onNode: none, op: VMOpDelete,
			want: "done", commands: []string{"DESTROY"},
			check: func(t *testing.T, f *fixture) { assertDeleted(t, f) },
		},
		{
			name: "delete the node refuses", status: models.VMStatusDeleting, onNode: running, op: VMOpDelete,
			setup: func(f *fixture) {
				f.node.On("ExecuteVMCommand", refuse(pb.VMCommandType_VM_COMMAND_TYPE_DESTROY, pb.ErrorCode_ERROR_CODE_INTERNAL, "failed to undefine domain: device busy"))
			},
			want: "retry", wantVM: "error", commands: []string{"DESTROY"},
			check: func(t *testing.T, f *fixture) {
				// Nothing released while the domain may still be running with the address.
				if s := f.scalar(`SELECT status FROM ip_addresses WHERE address = ?`, fxIP); s != "assigned" {
					t.Errorf("address %s while the VM may still hold it", s)
				}
				// The progress the page shows ends, saying why; it is not left running.
				if s := f.scalar(`SELECT status || ': ' || error FROM vm_operations WHERE vm_id = ?`, fxVM); !strings.HasPrefix(s, "failed: ") || !strings.Contains(s, "device busy") {
					t.Errorf("delete operation row: %s", s)
				}
			},
		},
		{
			name: "reinstall", status: models.VMStatusCreating, onNode: running, op: VMOpRebuild, params: createParams,
			setup: func(f *fixture) {
				f.exec(`INSERT INTO snapshots (vm_id, name, disk_path, status) VALUES (?, 'before-upgrade', '', 'completed')`, fxVM)
			},
			want: "done", wantVM: "running", commands: []string{"REBUILD", "START"},
			check: func(t *testing.T, f *fixture) {
				if n := f.scalar(`SELECT count(*)::text FROM snapshots WHERE vm_id = ? AND deleted_at IS NULL`, fxVM); n != "0" {
					t.Errorf("%s snapshot records outlived the disk they were in", n)
				}
				if f.scalar(`SELECT chain_reset_at::text FROM backup_schedules WHERE vm_id = ?`, fxVM) == "<null>" {
					t.Error("the next backup may build on a chain of the old disk")
				}
				if len(f.node.Calls("ApplyNetworkConfig")) != 1 {
					t.Error("the reinstalled VM's firewall was not applied")
				}
				if op := <-f.done; op != VMOpRebuild {
					t.Errorf("done hook told %q", op)
				}
			},
		},
		{
			// The reinstall itself worked; a VM that would not boot is stopped, not failed.
			name: "reinstall, the new system does not start", status: models.VMStatusCreating, onNode: running, op: VMOpRebuild, params: createParams,
			setup: func(f *fixture) {
				f.node.On("ExecuteVMCommand", refuse(pb.VMCommandType_VM_COMMAND_TYPE_START, pb.ErrorCode_ERROR_CODE_INTERNAL, "no bootable device"))
			},
			want: "done", wantVM: "stopped", commands: []string{"REBUILD", "START"},
		},
		{
			name: "resize, then boot again", status: models.VMStatusStopped, onNode: stopped, op: VMOpResize, params: `{"start_after":true}`,
			setup: func(f *fixture) {
				f.exec(`UPDATE vms SET resources = '{"cpu":2,"ram":2048,"disk":40}' WHERE id = ?`, fxVM)
			},
			want: "done", wantVM: "running", commands: []string{"RESIZE", "FORCE_STOP", "START"},
			check: func(t *testing.T, f *fixture) {
				vm, _ := f.node.VM(fxVM)
				if r := vm.Config.GetResources(); r.GetVcpus() != 2 || r.GetMemoryMb() != 2048 || r.GetDiskGb() != 40 {
					t.Errorf("node resized to %+v", r)
				}
			},
		},
		{
			name: "attach a customer's ISO", status: models.VMStatusRunning, onNode: running, op: VMOpAttachISO,
			params: `{"image_path":"https://images.example.test/rescue.iso"}`,
			want:   "done", wantVM: "running", commands: []string{"ATTACH_ISO"},
			check: func(t *testing.T, f *fixture) {
				// The node fetches a customer's URL under the customer's rules.
				if vm, _ := f.node.VM(fxVM); vm.ISO != models.CustomerISORef("https://images.example.test/rescue.iso") {
					t.Errorf("node mounted %q", vm.ISO)
				}
				if iso := f.scalar(`SELECT attached_iso FROM vms WHERE id = ?`, fxVM); iso != "https://images.example.test/rescue.iso" {
					t.Errorf("attached ISO recorded as %q", iso)
				}
			},
		},
		{
			name: "attach an operator's ISO", status: models.VMStatusRunning, onNode: running, op: VMOpAttachISO,
			params: `{"image_path":"/var/lib/maburvm/isos/rescue.iso","iso_source":"operator"}`,
			want:   "done", wantVM: "running", commands: []string{"ATTACH_ISO"},
			check: func(t *testing.T, f *fixture) {
				if vm, _ := f.node.VM(fxVM); vm.ISO != "/var/lib/maburvm/isos/rescue.iso" {
					t.Errorf("node mounted %q", vm.ISO)
				}
			},
		},
		{
			// One try: another would download the image again.
			name: "attach an ISO the node cannot fetch", status: models.VMStatusRunning, onNode: running, op: VMOpAttachISO,
			params: `{"image_path":"https://images.example.test/missing.iso"}`,
			setup: func(f *fixture) {
				f.node.On("ExecuteVMCommand", refuse(pb.VMCommandType_VM_COMMAND_TYPE_ATTACH_ISO, pb.ErrorCode_ERROR_CODE_INTERNAL, "download failed: 404 Not Found"))
			},
			want: "cancelled", commands: []string{"ATTACH_ISO"},
			check: func(t *testing.T, f *fixture) {
				if iso := f.scalar(`SELECT attached_iso FROM vms WHERE id = ?`, fxVM); iso != "" && iso != "<null>" {
					t.Errorf("a failed mount was recorded as %q", iso)
				}
			},
		},
		{
			name: "detach the ISO", status: models.VMStatusRunning, onNode: running, op: VMOpDetachISO,
			setup: func(f *fixture) {
				f.exec(`UPDATE vms SET attached_iso = 'https://images.example.test/rescue.iso' WHERE id = ?`, fxVM)
			},
			want: "done", wantVM: "running", commands: []string{"DETACH_ISO"},
			check: func(t *testing.T, f *fixture) {
				if iso := f.scalar(`SELECT attached_iso FROM vms WHERE id = ?`, fxVM); iso != "" {
					t.Errorf("attached ISO still recorded as %q", iso)
				}
			},
		},
		{
			// A move owns the VM: the job waits, using no try, and the node is not asked.
			name: "a job for a VM being moved waits", status: models.VMStatusRunning, onNode: running, op: VMOpStop,
			setup: func(f *fixture) {
				f.exec(`INSERT INTO vm_migrations (vm_id, source_node_id, dest_node_id, mode, status) VALUES (?, ?, ?, 'live', 'running')`, fxVM, fxNode, fxNode)
			},
			want: "snoozed", wantVM: "running",
		},
		{
			name: "a node under maintenance is not asked", status: models.VMStatusStopped, onNode: stopped, op: VMOpStart,
			setup: func(f *fixture) { f.exec(`UPDATE nodes SET status = 'maintenance' WHERE id = ?`, fxNode) },
			want:  "retry", wantVM: "stopped",
		},
		{
			name: "a job for a VM that no longer exists ends", status: models.VMStatusStopped, onNode: none, op: VMOpStart,
			setup: func(f *fixture) { f.exec(`DELETE FROM vms WHERE id = ?`, fxVM) },
			want:  "cancelled", wantVM: "<null>",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.status, c.onNode)
			if c.setup != nil {
				c.setup(f)
			}
			attempt := c.attempt
			if attempt == 0 {
				attempt = 1
			}
			err := NewVMOperationWorker(quiet()).Work(context.Background(), f.vmJob(c.op, c.params, attempt))
			if got := outcome(err); got != c.want {
				t.Fatalf("outcome %s (%v), want %s", got, err, c.want)
			}
			if c.wantVM != "" {
				if got := f.vmStatus(); got != c.wantVM {
					t.Errorf("VM status %s, want %s", got, c.wantVM)
				}
			}
			var cmds []string
			for _, cmd := range f.node.Commands() {
				cmds = append(cmds, strings.Fields(cmd)[0])
			}
			if strings.Join(cmds, ",") != strings.Join(c.commands, ",") {
				t.Errorf("node was asked %v, want %v", cmds, c.commands)
			}
			for _, call := range f.node.Calls("ExecuteVMCommand") {
				if call.Authorization != "Bearer "+fxToken {
					t.Errorf("a command went without the node's token: %q", call.Authorization)
				}
			}
			if c.check != nil {
				c.check(t, f)
			}
		})
	}
}

// assertDeleted checks what a finished delete leaves: no VM, its address free,
// and the operation the page follows marked finished.
func assertDeleted(t *testing.T, f *fixture) {
	t.Helper()
	if s := f.scalar(`SELECT count(*)::text FROM vms WHERE id = ? AND deleted_at IS NULL`, fxVM); s != "0" {
		t.Error("the VM row survived its delete")
	}
	if s := f.scalar(`SELECT status || ' ' || coalesce(vm_id::text, '-') FROM ip_addresses WHERE address = ?`, fxIP); s != "available -" {
		t.Errorf("address left as %q", s)
	}
	if s := f.scalar(`SELECT count(*)::text FROM networks WHERE vm_id = ? AND deleted_at IS NULL`, fxVM); s != "0" {
		t.Error("network rows survived")
	}
	if s := f.scalar(`SELECT status || ' ' || current_step || '/' || total_steps FROM vm_operations WHERE vm_id = ?`, fxVM); s != "completed 2/2" {
		t.Errorf("delete operation row: %s", s)
	}
	if _, ok := f.node.VM(fxVM); ok {
		t.Error("the node still has the domain")
	}
}

func (f *fixture) snapshot(name, state string) string {
	return f.scalar(`INSERT INTO snapshots (vm_id, name, disk_path, status) VALUES (?, ?, '', ?) RETURNING id::text`, fxVM, name, state)
}

func (f *fixture) snapshotJob(op SnapshotOperation, id, name string, attempt int) *river.Job[SnapshotJob] {
	args := SnapshotJob{VMID: fxVM, SnapshotID: id, Operation: op, NodeID: fxNode, Name: name}
	return &river.Job[SnapshotJob]{JobRow: row(args.Kind(), args, attempt, NodeJobMaxAttempts, f.jobID.Add(1)), Args: args}
}

func TestSnapshotJobsNeverLeaveAPendingRow(t *testing.T) {
	failCreate := func(f *fixture) {
		f.node.On("CreateSnapshot", func(proto.Message) (proto.Message, error) {
			return &pb.SnapshotResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: "guest agent did not answer"}}, nil
		})
	}
	for _, c := range []struct {
		name    string
		setup   func(*fixture)
		attempt int
		want    string
		row     string
	}{
		{name: "taken", want: "done", row: "completed"},
		{name: "failed, tries left", setup: failCreate, attempt: 1, want: "retry", row: "pending"},
		// Left pending, the row read as still being taken and refused every later snapshot.
		{name: "failed on the last try", setup: failCreate, attempt: NodeJobMaxAttempts, want: "retry", row: "failed"},
		{name: "refused for good", setup: func(f *fixture) {
			f.node.On("CreateSnapshot", func(proto.Message) (proto.Message, error) {
				return nil, status.Error(codes.FailedPrecondition, "a raw disk cannot be snapshotted")
			})
		}, attempt: 1, want: "cancelled", row: "failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
			if c.setup != nil {
				c.setup(f)
			}
			id := f.snapshot("before-upgrade", "pending")
			attempt := max(c.attempt, 1)
			err := NewSnapshotWorker(quiet()).Work(context.Background(), f.snapshotJob(SnapshotOpCreate, id, "before-upgrade", attempt))
			if got := outcome(err); got != c.want {
				t.Fatalf("outcome %s (%v), want %s", got, err, c.want)
			}
			if got := f.scalar(`SELECT status::text FROM snapshots WHERE id = ?`, id); got != c.row {
				t.Errorf("snapshot row %s, want %s", got, c.row)
			}
		})
	}
}

func TestSnapshotRestoreAndDelete(t *testing.T) {
	t.Run("restore", func(t *testing.T) {
		f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
		vm, _ := f.node.VM(fxVM)
		vm.Snapshots = []string{"before-upgrade"}
		f.node.PutVM(vm)
		id := f.snapshot("before-upgrade", "completed")
		if err := NewSnapshotWorker(quiet()).Work(context.Background(), f.snapshotJob(SnapshotOpRestore, id, "before-upgrade", 1)); err != nil {
			t.Fatal(err)
		}
		// The node is asked by the name libvirt knows the snapshot under.
		if got := f.node.Calls("CreateSnapshot")[0].Req.(*pb.SnapshotRequest).GetSnapshotId(); got != "before-upgrade" {
			t.Errorf("restore asked for %q", got)
		}
		if got := f.vmStatus(); got != "stopped" {
			t.Errorf("VM status %s after a revert", got)
		}
		if f.scalar(`SELECT chain_reset_at::text FROM backup_schedules WHERE vm_id = ?`, fxVM) == "<null>" {
			t.Error("the next backup may build on a checkpoint the revert threw away")
		}
	})

	// A revert leaves the VM as the snapshot was taken, and the panel records
	// what the node says.
	t.Run("restore of a snapshot taken running", func(t *testing.T) {
		f := newFixture(t, models.VMStatusStopped, pb.VMState_VM_STATE_STOPPED)
		vm, _ := f.node.VM(fxVM)
		vm.Snapshots = []string{"live"}
		vm.SnapshotStates = map[string]pb.VMState{"live": pb.VMState_VM_STATE_RUNNING}
		f.node.PutVM(vm)
		id := f.snapshot("live", "completed")
		if err := NewSnapshotWorker(quiet()).Work(context.Background(), f.snapshotJob(SnapshotOpRestore, id, "live", 1)); err != nil {
			t.Fatal(err)
		}
		if got := f.vmStatus(); got != "running" {
			t.Errorf("VM status %s after reverting to a snapshot taken running", got)
		}
	})

	// An older agent took the snapshots after the deleted one with it: their
	// records go, the others stay.
	t.Run("delete drops the records of what the node lost", func(t *testing.T) {
		f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
		vm, _ := f.node.VM(fxVM)
		vm.Snapshots = []string{"first", "kept"}
		f.node.PutVM(vm)
		f.snapshot("kept", "completed")
		lost := f.snapshot("lost", "completed")
		if err := NewSnapshotWorker(quiet()).Work(context.Background(), f.snapshotJob(SnapshotOpDelete, "", "first", 1)); err != nil {
			t.Fatal(err)
		}
		if vm, _ := f.node.VM(fxVM); strings.Join(vm.Snapshots, ",") != "kept" {
			t.Errorf("node keeps %v", vm.Snapshots)
		}
		if got := f.scalar(`SELECT string_agg(name, ',' ORDER BY name) FROM snapshots WHERE vm_id = ? AND deleted_at IS NULL`, fxVM); got != "kept" {
			t.Errorf("records left: %s (the lost one is %s)", got, lost)
		}
	})
}

func (f *fixture) backup(mode, state string) string {
	return f.scalar(`INSERT INTO backups (vm_id, storage_provider, storage_path, status, mode) VALUES (?, 's3', 'backups/web-1.qcow2', ?, ?) RETURNING id::text`, fxVM, state, mode)
}

func (f *fixture) backupJob(args BackupJob, attempt int) *river.Job[BackupJob] {
	args.VMID, args.BackupType, args.Destination, args.Compression = fxVM, BackupTypeFull, "backups/web-1.qcow2", "zstd"
	return &river.Job[BackupJob]{JobRow: row(args.Kind(), args, attempt, DiskJobMaxAttempts, f.jobID.Add(1)), Args: args}
}

func TestBackupJobs(t *testing.T) {
	t.Run("full backup of every disk", func(t *testing.T) {
		f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
		vm, _ := f.node.VM(fxVM)
		vm.Disks = []string{"vdb"}
		f.node.PutVM(vm)
		f.exec(`INSERT INTO vm_disks (vm_id, device, size_gb, path) VALUES (?, 'vdb', 50, '/vm/web-1-vdb.qcow2')`, fxVM)
		id := f.backup("full", "pending")
		if err := NewBackupWorker(quiet()).Work(context.Background(), f.backupJob(BackupJob{BackupID: id, Checkpoint: "maburvm-" + id}, 1)); err != nil {
			t.Fatal(err)
		}
		asked := f.node.Calls("BackupDisk")
		if len(asked) != 1 || !asked[0].Req.(*pb.BackupDiskRequest).GetAllDisks() || asked[0].Req.(*pb.BackupDiskRequest).GetObjectStore().GetBucket() != "backups" {
			t.Fatalf("node asked %d time(s): %v", len(asked), asked)
		}
		b := f.loadBackup(id)
		if b.Status != models.BackupStatusCompleted || len(b.Disks) != 2 || b.Disks[1].Target != "vdb" || b.Size != 2<<20 ||
			b.Mode != models.BackupModeFull || *b.ChainID != id || b.Checkpoint != "maburvm-"+id || *b.NodeID != fxNode || *b.DestinationID != fxDest {
			t.Fatalf("row = %+v", b)
		}
	})

	t.Run("incremental on its parent", func(t *testing.T) {
		f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
		parent := f.backup("full", "completed")
		id := f.backup("incremental", "pending")
		job := BackupJob{BackupID: id, Checkpoint: "maburvm-" + id, ParentID: parent, IncrementalFrom: "maburvm-" + parent, ChainID: parent}
		if err := NewBackupWorker(quiet()).Work(context.Background(), f.backupJob(job, 1)); err != nil {
			t.Fatal(err)
		}
		if got := f.node.Calls("BackupDisk")[0].Req.(*pb.BackupDiskRequest).GetIncrementalFrom(); got != "maburvm-"+parent {
			t.Errorf("node asked to build on %q", got)
		}
		b := f.loadBackup(id)
		if b.Mode != models.BackupModeIncremental || *b.ParentID != parent || *b.ChainID != parent {
			t.Fatalf("row = %+v", b)
		}
	})

	// Every retry exports the whole disk again: a failure that will not clear
	// ends at once, and the row says why. It is never left in progress.
	t.Run("export that cannot succeed is tried once", func(t *testing.T) {
		f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
		f.node.On("BackupDisk", func(proto.Message) (proto.Message, error) {
			return &pb.BackupDiskResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED, Message: "not enough room on /vm to export"}}, nil
		})
		id := f.backup("full", "pending")
		err := NewBackupWorker(quiet()).Work(context.Background(), f.backupJob(BackupJob{BackupID: id}, 1))
		if outcome(err) != "cancelled" || len(f.node.Calls("BackupDisk")) != 1 {
			t.Fatalf("outcome %s after %d export(s)", outcome(err), len(f.node.Calls("BackupDisk")))
		}
		if b := f.loadBackup(id); b.Status != models.BackupStatusFailed || !strings.Contains(b.ErrorMessage, "not enough room") {
			t.Fatalf("row = %s %q", b.Status, b.ErrorMessage)
		}
	})

	t.Run("transient failure frees the slot until the next try", func(t *testing.T) {
		f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
		f.node.On("BackupDisk", unreachable)
		id := f.backup("full", "pending")
		if err := NewBackupWorker(quiet()).Work(context.Background(), f.backupJob(BackupJob{BackupID: id}, 1)); outcome(err) != "retry" {
			t.Fatalf("outcome %s", outcome(err))
		}
		if b := f.loadBackup(id); b.Status != models.BackupStatusPending || !strings.Contains(b.ErrorMessage, "Will be tried again") {
			t.Fatalf("row = %s %q", b.Status, b.ErrorMessage)
		}
	})
}

func (f *fixture) loadBackup(id string) models.Backup {
	f.t.Helper()
	var b models.Backup
	if err := f.db.First(&b, "id = ?", id).Error; err != nil {
		f.t.Fatal(err)
	}
	return b
}

func TestRestoreJobs(t *testing.T) {
	disks := []models.BackupDisk{{Target: "vda", Key: "backups/inc.qcow2", Checksum: "c1"}, {Target: "vdb", Key: "backups/inc-vdb.qcow2", Checksum: "c2"}}
	layers := map[string][]models.BackupDisk{"vda": {{Key: "backups/full.qcow2", Checksum: "f1"}}, "vdb": {{Key: "backups/full-vdb.qcow2", Checksum: "f2"}}}
	job := func(f *fixture, attempt int) *river.Job[RestoreJob] {
		args := RestoreJob{VMID: fxVM, NodeID: fxNode, BackupID: "b-inc", SourceKey: "backups/inc.qcow2", Checksum: "c1", Disks: disks, Layers: layers}
		return &river.Job[RestoreJob]{JobRow: row(args.Kind(), args, attempt, DiskJobMaxAttempts, f.jobID.Add(1)), Args: args}
	}

	t.Run("an incremental backup of two disks, in place", func(t *testing.T) {
		f := newFixture(t, models.VMStatusStopped, pb.VMState_VM_STATE_STOPPED)
		f.exec(`INSERT INTO snapshots (vm_id, name, disk_path, status) VALUES (?, 'old', '', 'completed')`, fxVM)
		if err := NewRestoreWorker(quiet()).Work(context.Background(), job(f, 1)); err != nil {
			t.Fatal(err)
		}
		calls := f.node.Calls("RestoreDisk")
		if len(calls) != 1 {
			t.Fatalf("%d restore calls", len(calls))
		}
		req := calls[0].Req.(*pb.RestoreDiskRequest)
		if req.SourceKey != "" || len(req.Disks) != 2 || req.Disks[1].GetTarget() != "vdb" || req.Disks[1].GetLayers()[0].GetKey() != "backups/full-vdb.qcow2" {
			t.Fatalf("node asked %v", req)
		}
		if n := f.scalar(`SELECT count(*)::text FROM snapshots WHERE vm_id = ? AND deleted_at IS NULL`, fxVM); n != "0" {
			t.Error("snapshot records outlived the disks they were in")
		}
		if f.scalar(`SELECT chain_reset_at::text FROM backup_schedules WHERE vm_id = ?`, fxVM) == "<null>" {
			t.Error("the backup chain was not reset")
		}
		if got := f.scalar(`SELECT action || ' ' || (details->>'backup_id') FROM audit_logs WHERE resource_type = 'vm' AND resource_id = ?`, fxVM); got != "vm.restore_completed b-inc" {
			t.Errorf("VM activity: %s", got)
		}
	})

	// The panel said stopped, the guest was still shutting down: nothing is
	// written over its disks, and the VM's activity says why, not a silent cancel.
	t.Run("a VM still on is not restored over", func(t *testing.T) {
		f := newFixture(t, models.VMStatusStopped, pb.VMState_VM_STATE_RUNNING)
		prev := shutOffWait
		shutOffWait = 50 * time.Millisecond
		t.Cleanup(func() { shutOffWait = prev })
		err := NewRestoreWorker(quiet()).Work(context.Background(), job(f, 1))
		if outcome(err) != "cancelled" || len(f.node.Calls("RestoreDisk")) != 0 {
			t.Fatalf("outcome %s (%v), %d restore calls", outcome(err), err, len(f.node.Calls("RestoreDisk")))
		}
		if got := f.scalar(`SELECT action || ': ' || (details->>'reason') FROM audit_logs WHERE resource_type = 'vm' AND resource_id = ?`, fxVM); got != "vm.restore_failed: "+errVMNotOff.Error() {
			t.Errorf("VM activity: %s", got)
		}
	})

	// The panel restarted mid-restore and the node is still at it: that is
	// not a failed restore.
	t.Run("an earlier try still running on the node is waited for", func(t *testing.T) {
		f := newFixture(t, models.VMStatusStopped, pb.VMState_VM_STATE_STOPPED)
		f.node.On("RestoreDisk", func(proto.Message) (proto.Message, error) {
			return &pb.RestoreDiskResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL,
				Message: "a disk of this VM is being moved, copied or restored; try again when that has finished"}}, nil
		})
		err := NewRestoreWorker(quiet()).Work(context.Background(), job(f, 1))
		if outcome(err) != "snoozed" {
			t.Fatalf("outcome %s (%v)", outcome(err), err)
		}
		if n := f.scalar(`SELECT count(*)::text FROM audit_logs WHERE resource_type = 'vm' AND resource_id = ?`, fxVM); n != "0" {
			t.Errorf("%s activity entries for a restore that has not ended", n)
		}
	})

	// It would restore the top image alone: a broken disk.
	t.Run("an agent without incremental restores is not asked", func(t *testing.T) {
		f := newFixture(t, models.VMStatusStopped, pb.VMState_VM_STATE_STOPPED)
		f.node.Features = []string{"restore_disks", "backup_targets"}
		err := NewRestoreWorker(quiet()).Work(context.Background(), job(f, 1))
		if outcome(err) != "cancelled" || len(f.node.Calls("RestoreDisk")) != 0 {
			t.Fatalf("outcome %s, %d restore calls", outcome(err), len(f.node.Calls("RestoreDisk")))
		}
	})

	t.Run("a node that drops out is tried a few times only", func(t *testing.T) {
		f := newFixture(t, models.VMStatusStopped, pb.VMState_VM_STATE_STOPPED)
		f.node.On("RestoreDisk", unreachable)
		if err := NewRestoreWorker(quiet()).Work(context.Background(), job(f, 1)); outcome(err) != "retry" {
			t.Fatalf("first try: %s", outcome(err))
		}
		if err := NewRestoreWorker(quiet()).Work(context.Background(), job(f, DiskJobMaxAttempts)); outcome(err) != "cancelled" {
			t.Fatalf("last try: %s", outcome(err))
		}
	})

	t.Run("a backup restored as a new VM", func(t *testing.T) {
		f := newFixture(t, models.VMStatusCreating, pb.VMState_VM_STATE_UNSPECIFIED)
		params := `{"hostname":"web-1","image_path":"s3://backups/inc.qcow2","object_store":true,"metadata":{"image_once":"1"},"backup_destination_id":"a0000000-0000-0000-0000-000000000006",
			"restore_disks":[{"target":"vdb","key":"backups/inc-vdb.qcow2","checksum":"c2","size":1048576}],
			"restore_layers":[{"key":"backups/full.qcow2","checksum":"f1"}],
			"restore_disk_layers":{"vdb":[{"key":"backups/full-vdb.qcow2","checksum":"f2"}]}}`
		if err := NewVMOperationWorker(quiet()).Work(context.Background(), f.vmJob(VMOpCreate, params, 1)); err != nil {
			t.Fatal(err)
		}
		req := f.node.Calls("ExecuteVMCommand")[0].Req.(*pb.VMCommandRequest)
		cfg := req.GetConfig()
		if req.GetObjectStore().GetBucket() != "backups" || len(cfg.GetRestoreLayers()) != 1 || len(cfg.GetRestoreDisks()) != 1 ||
			cfg.GetRestoreDisks()[0].GetLayers()[0].GetKey() != "backups/full-vdb.qcow2" {
			t.Fatalf("node asked %v", req)
		}
		if got := f.scalar(`SELECT device FROM vm_disks WHERE vm_id = ?`, fxVM); got != "vdb" {
			t.Errorf("restored data disk recorded as %s", got)
		}
		if got := f.vmStatus(); got != "running" {
			t.Errorf("VM status %s", got)
		}
	})

	// The node already retried the download; trying again downloads it again.
	t.Run("a new VM from a backup the node cannot read", func(t *testing.T) {
		f := newFixture(t, models.VMStatusCreating, pb.VMState_VM_STATE_UNSPECIFIED)
		f.node.On("ExecuteVMCommand", refuse(pb.VMCommandType_VM_COMMAND_TYPE_CREATE, pb.ErrorCode_ERROR_CODE_INTERNAL, "download failed: checksum mismatch"))
		err := NewVMOperationWorker(quiet()).Work(context.Background(), f.vmJob(VMOpCreate, `{"hostname":"web-1","image_path":"s3://backups/full.qcow2","object_store":true,"metadata":{"image_once":"1"}}`, 1))
		if outcome(err) != "cancelled" || f.vmStatus() != "error" {
			t.Fatalf("outcome %s, VM %s", outcome(err), f.vmStatus())
		}
	})
}

func TestNetworkConfigJobs(t *testing.T) {
	apply := func(f *fixture, id int64, params string) error {
		args := VMOperationJob{VMID: fxVM, NodeID: fxNode, Operation: VMOpConfigureNetwork, Params: json.RawMessage(params)}
		return NewVMOperationWorker(quiet()).Work(context.Background(), &river.Job[VMOperationJob]{JobRow: row(args.Kind(), args, 1, NetworkJobMaxAttempts, id), Args: args})
	}

	// The job's copy is as old as the click that queued it; the node gets what
	// the database holds when the job runs.
	t.Run("rules and forwards are read when the job runs", func(t *testing.T) {
		f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
		netID := f.scalar(`SELECT id::text FROM networks WHERE vm_id = ?`, fxVM)
		f.exec(`INSERT INTO port_forwards (vm_id, network_id, node_id, external_port, internal_port, protocol, source_ip) VALUES (?, ?, ?, 20022, 22, 'tcp', '0.0.0.0/0')`, fxVM, netID, fxNode)
		stale := `{"ip_address":"192.0.2.50","firewall_rules":[{"protocol":"udp","port_range":"53","action":"allow","direction":"inbound"}]}`
		if err := apply(f, 10, stale); err != nil {
			t.Fatal(err)
		}
		req := f.node.Calls("ApplyNetworkConfig")[0].Req.(*pb.NetworkConfigRequest)
		rules := req.GetConfig().GetFirewallRules()
		if !req.ReplaceAll || len(rules) != 1 || rules[0].GetProtocol() != "tcp" || rules[0].GetDestPort() != "22" {
			t.Errorf("rules sent %v", rules)
		}
		if pf := req.GetConfig().GetPortForwards(); len(pf) != 1 || pf[0].GetExternalPort() != 20022 {
			t.Errorf("forwards sent %v", pf)
		}
		if req.GetConfig().GetFirewallDefault() != "drop" {
			t.Errorf("default policy %q", req.GetConfig().GetFirewallDefault())
		}
	})

	// The node replaces the whole config: an older job applied last would undo the newer change.
	t.Run("an overtaken job is dropped", func(t *testing.T) {
		f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
		if err := apply(f, 20, `{"ip_address":"192.0.2.50"}`); err != nil {
			t.Fatal(err)
		}
		if err := apply(f, 19, `{"ip_address":"192.0.2.50"}`); err != nil {
			t.Fatal(err)
		}
		if n := len(f.node.Calls("ApplyNetworkConfig")); n != 1 {
			t.Errorf("node applied %d times", n)
		}
	})

	t.Run("what the guest did not take is said on the VM", func(t *testing.T) {
		f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
		f.node.On("ApplyNetworkConfig", func(proto.Message) (proto.Message, error) {
			return &pb.NetworkConfigResponse{Success: true, GuestPending: "the guest agent did not answer", GuestRetry: true}, nil
		})
		if err := apply(f, 30, `{"ip_address":"192.0.2.50","configure_guest":true}`); err != nil {
			t.Fatal(err)
		}
		if got := f.scalar(`SELECT guest_network_note || ' ' || guest_network_pending FROM vms WHERE id = ?`, fxVM); got != "the guest agent did not answer true" {
			t.Errorf("VM says %q", got)
		}
	})

	t.Run("a node that does not answer is tried again", func(t *testing.T) {
		f := newFixture(t, models.VMStatusRunning, pb.VMState_VM_STATE_RUNNING)
		f.node.On("ApplyNetworkConfig", unreachable)
		if err := apply(f, 40, `{"ip_address":"192.0.2.50"}`); outcome(err) != "retry" {
			t.Fatalf("outcome %s", outcome(err))
		}
	})
}

// The queue's middleware decides when a job has failed for good, and only
// then are staff alerted and the customer told; a try with tries left is
// reported as retrying.
func TestFailureHooksFireOnceAndOnlyAtTheEnd(t *testing.T) {
	run := func(f *fixture, job *river.Job[VMOperationJob]) error {
		return (&jobFailureReporter{}).Work(context.Background(), job.JobRow, func(ctx context.Context) error {
			return NewVMOperationWorker(quiet()).Work(ctx, job)
		})
	}
	wait := func(ch chan string) string {
		select {
		case s := <-ch:
			return s
		case <-time.After(5 * time.Second):
			return ""
		}
	}

	t.Run("refused create", func(t *testing.T) {
		f := newFixture(t, models.VMStatusCreating, pb.VMState_VM_STATE_UNSPECIFIED)
		f.node.On("ExecuteVMCommand", refuse(pb.VMCommandType_VM_COMMAND_TYPE_CREATE, pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "unknown CPU model"))
		if err := run(f, f.vmJob(VMOpCreate, createParams, 1)); !isJobCancel(err) {
			t.Fatalf("got %v", err)
		}
		if got := wait(f.failed); got != "vm_operation: VM command failed: unknown CPU model" {
			t.Errorf("failure hook told %q", got)
		}
		if len(f.retrying) != 0 {
			t.Error("a job that failed for good was also reported as retrying")
		}
	})

	t.Run("node down with tries left", func(t *testing.T) {
		f := newFixture(t, models.VMStatusCreating, pb.VMState_VM_STATE_UNSPECIFIED)
		f.node.On("ExecuteVMCommand", unreachable)
		if err := run(f, f.vmJob(VMOpCreate, createParams, 1)); err == nil || isJobCancel(err) {
			t.Fatalf("got %v", err)
		}
		if got := wait(f.retrying); !strings.HasPrefix(got, "vm_operation: retryable error") {
			t.Errorf("retrying hook told %q", got)
		}
		if len(f.failed) != 0 {
			t.Error("staff were alerted about a job that will be tried again")
		}
	})

	// A job queued before the limits existed carries River's 25 tries; it
	// still ends at the node limit, and the VM with it.
	t.Run("legacy job at the node limit", func(t *testing.T) {
		f := newFixture(t, models.VMStatusCreating, pb.VMState_VM_STATE_UNSPECIFIED)
		f.node.On("ExecuteVMCommand", unreachable)
		job := f.vmJob(VMOpCreate, createParams, NodeJobMaxAttempts)
		job.MaxAttempts = 25
		if err := run(f, job); !isJobCancel(err) {
			t.Fatalf("got %v", err)
		}
		if got := wait(f.failed); !strings.Contains(got, "gave up after 5 tries") {
			t.Errorf("failure hook told %q", got)
		}
		if got := f.vmStatus(); got != "error" {
			t.Errorf("VM left %s", got)
		}
	})
}
