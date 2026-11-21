//go:build pg

package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/repository"
	sharedb "github.com/maburvm/panel/internal/shared/db"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

// migrationDB is an empty database with every migration applied.
func migrationDB(t *testing.T) *gorm.DB {
	t.Helper()
	base := os.Getenv("MABURVM_TEST_PG_DSN")
	if base == "" {
		t.Skip("MABURVM_TEST_PG_DSN is not set")
	}
	name := fmt.Sprintf("maburvm_mig_%d", time.Now().UnixNano()%1_000_000_000)
	admin, err := sql.Open("pgx", base)
	require.NoError(t, err)
	_, err = admin.Exec(`CREATE DATABASE "` + name + `"`)
	admin.Close()
	require.NoError(t, err)
	t.Cleanup(func() {
		a, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer a.Close()
		_, _ = a.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '` + name + `'`)
		_, _ = a.Exec(`DROP DATABASE IF EXISTS "` + name + `"`)
	})
	dsn := base
	if q := strings.Index(dsn, "?"); q >= 0 {
		dsn = dsn[:strings.LastIndex(dsn[:q], "/")+1] + name + dsn[q:]
	} else {
		dsn = dsn[:strings.LastIndex(dsn, "/")+1] + name
	}
	raw, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer raw.Close()
	ctx := context.Background()
	require.NoError(t, sharedb.EnsureMigrationsTable(ctx, raw))
	_, err = sharedb.Up(ctx, raw, sharedb.MigrationsFS, "migrations", time.Minute, nil)
	require.NoError(t, err)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	return db
}

func testKey(t *testing.T) string {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	k, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// fakeVM is a domain on a fake node.
type fakeVM struct {
	state string
	pool  string
}

// fakeNode is a node agent that moves VMs between fakes at once.
type fakeNode struct {
	pb.UnimplementedNodeAgentServer
	mu       sync.Mutex
	name     string
	pool     string
	key      string
	hostKey  string
	cpu      string
	vms      map[string]*fakeVM
	progress map[string]*pb.GetMigrationProgressResponse
	calls    []string
	peers    []*pb.MigrationPeer
	peer     *fakeNode // where a move from here lands
	failMove string
	// lostTrack: the agent forgets the move (it restarted) while libvirt
	// carries it on: the VM still runs here and its incoming copy is on the peer.
	lostTrack bool
	// refuseCommit: the node does not confirm an arrived VM.
	refuseCommit bool
	// loseAnswer: the move starts, but the answer to the start never arrives.
	loseAnswer bool
	// onMove runs as a live move starts.
	onMove func()
}

func (f *fakeNode) record(call string) {
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}

func (f *fakeNode) called(call string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == call {
			return true
		}
	}
	return false
}

func (f *fakeNode) GetMigrationInfo(_ context.Context, req *pb.GetMigrationInfoRequest) (*pb.GetMigrationInfoResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &pb.GetMigrationInfoResponse{Success: true, MigrationPublicKey: f.key + " maburvm-migration@" + f.name, SshHostKeys: []string{f.hostKey},
		SshProxy: "virt-ssh-helper", CpuVendor: "Intel", CpuModel: f.cpu, CpuFeatures: []string{"avx2"}, LinkOk: req.PeerAddress != ""}
	if vm := f.vms[req.VmId]; req.VmId != "" && vm != nil {
		r.VmFound, r.VmState, r.VmCpuMode, r.Bridges = true, vm.state, "host-passthrough", []string{"br0"}
		r.Disks = []*pb.MigrationDiskInfo{
			{Target: "vda", Device: "disk", Path: vm.pool + "/" + req.VmId + ".qcow2", Format: "qcow2", SourceType: "file", VirtualSizeBytes: 20 << 30, FileSizeBytes: 5 << 30},
			{Target: "vdb", Device: "disk", Path: vm.pool + "/" + req.VmId + "-vdb.qcow2", Format: "qcow2", SourceType: "file", VirtualSizeBytes: 10 << 30, FileSizeBytes: 1 << 30},
			{Target: "sda", Device: "cdrom", Path: vm.pool + "/" + req.VmId + "-seed.iso", Format: "raw", SourceType: "file", Readonly: true, Seed: true, FileSizeBytes: 4096},
		}
		if req.IncludePayload {
			r.SeedImage, r.DomainXml = []byte("seed"), "<domain/>"
		}
	}
	return r, nil
}

func (f *fakeNode) SetMigrationPeers(_ context.Context, req *pb.SetMigrationPeersRequest) (*pb.SetMigrationPeersResponse, error) {
	f.mu.Lock()
	f.peers = req.Peers
	f.mu.Unlock()
	return &pb.SetMigrationPeersResponse{Success: true}, nil
}

func (f *fakeNode) PrepareMigrationTarget(_ context.Context, req *pb.PrepareMigrationTargetRequest) (*pb.PrepareMigrationTargetResponse, error) {
	if !req.DryRun {
		f.record("prepare")
	}
	paths := map[string]string{}
	for _, d := range req.Disks {
		paths[d.Target] = req.PoolPath + "/" + d.FileName
	}
	return &pb.PrepareMigrationTargetResponse{Success: true, Paths: paths, SeedPath: req.PoolPath + "/" + req.SeedFileName}, nil
}

// land moves a VM from f to its peer, as a finished copy would.
func (f *fakeNode) land(vmID, state string) {
	f.mu.Lock()
	f.vms[vmID].state = "shutoff"
	f.mu.Unlock()
	f.peer.mu.Lock()
	f.peer.vms[vmID] = &fakeVM{state: state, pool: f.peer.pool}
	f.peer.mu.Unlock()
}

func (f *fakeNode) MigrateVM(_ context.Context, req *pb.MigrateVMRequest) (*pb.MigrateVMResponse, error) {
	f.record("migrate")
	if f.onMove != nil {
		f.onMove()
	}
	f.mu.Lock()
	if f.lostTrack {
		f.mu.Unlock()
		f.peer.mu.Lock()
		f.peer.vms[req.VmId] = &fakeVM{state: "paused", pool: f.peer.pool}
		f.peer.mu.Unlock()
		return &pb.MigrateVMResponse{Success: true}, nil
	}
	fail := f.failMove
	if fail != "" {
		f.progress[req.VmId] = &pb.GetMigrationProgressResponse{State: "failed", Error: fail}
	} else {
		f.progress[req.VmId] = &pb.GetMigrationProgressResponse{State: "completed"}
	}
	f.mu.Unlock()
	if fail == "" || strings.HasPrefix(fail, "landed:") {
		f.land(req.VmId, "running")
	}
	if f.loseAnswer {
		return nil, status.Error(codes.Unavailable, "connection reset")
	}
	return &pb.MigrateVMResponse{Success: true}, nil
}

func (f *fakeNode) PullMigratedVM(_ context.Context, req *pb.PullMigratedVMRequest) (*pb.PullMigratedVMResponse, error) {
	f.record("pull")
	if req.DomainXml == "" || req.SourceAddress == "" {
		return &pb.PullMigratedVMResponse{Error: &pb.ErrorResponse{Message: "no definition"}}, nil
	}
	f.mu.Lock()
	f.vms[req.VmId] = &fakeVM{state: "shutoff", pool: f.pool}
	f.progress[req.VmId] = &pb.GetMigrationProgressResponse{State: "completed"}
	f.mu.Unlock()
	return &pb.PullMigratedVMResponse{Success: true}, nil
}

func (f *fakeNode) GetMigrationProgress(_ context.Context, req *pb.GetMigrationProgressRequest) (*pb.GetMigrationProgressResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.progress[req.VmId]; p != nil {
		return p, nil
	}
	return &pb.GetMigrationProgressResponse{State: "unknown"}, nil
}

func (f *fakeNode) FinishMigrationTarget(_ context.Context, req *pb.FinishMigrationTargetRequest) (*pb.FinishMigrationTargetResponse, error) {
	if !req.Commit {
		f.record("rollback")
		return &pb.FinishMigrationTargetResponse{Success: true}, nil
	}
	f.record("commit")
	f.mu.Lock()
	defer f.mu.Unlock()
	vm := f.vms[req.VmId]
	if vm == nil || f.refuseCommit {
		return &pb.FinishMigrationTargetResponse{Error: &pb.ErrorResponse{Message: "not here"}}, nil
	}
	return &pb.FinishMigrationTargetResponse{Success: true, VmState: vm.state}, nil
}

func (f *fakeNode) FinishMigrationSource(_ context.Context, req *pb.FinishMigrationSourceRequest) (*pb.FinishMigrationSourceResponse, error) {
	f.record("remove-source")
	f.mu.Lock()
	delete(f.vms, req.VmId)
	f.mu.Unlock()
	return &pb.FinishMigrationSourceResponse{Success: true}, nil
}

func (f *fakeNode) ApplyNetworkConfig(context.Context, *pb.NetworkConfigRequest) (*pb.NetworkConfigResponse, error) {
	f.record("network")
	return &pb.NetworkConfigResponse{Success: true}, nil
}

func (f *fakeNode) SetConsoleAccess(context.Context, *pb.SetConsoleAccessRequest) (*pb.SetConsoleAccessResponse, error) {
	f.record("console")
	return &pb.SetConsoleAccessResponse{Success: true, VncPort: 5907}, nil
}

func (f *fakeNode) SetVMLimits(context.Context, *pb.SetVMLimitsRequest) (*pb.SetVMLimitsResponse, error) {
	f.record("limits")
	return &pb.SetVMLimitsResponse{Success: true}, nil
}

type migrationFixture struct {
	db       *gorm.DB
	svc      *MigrationService
	src, dst *fakeNode
	srcID    string
	dstID    string
	dstPool  string
	user     string
	tmpl     string
}

const (
	migUser  = "11111111-1111-1111-1111-111111111111"
	migTmpl  = "22222222-2222-2222-2222-222222222222"
	migSrc   = "33333333-3333-3333-3333-333333333333"
	migDst   = "44444444-4444-4444-4444-444444444444"
	migDPool = "55555555-5555-5555-5555-555555555555"
	migPool  = "66666666-6666-6666-6666-666666666666"
)

func newMigrationFixture(t *testing.T) *migrationFixture {
	db := migrationDB(t)
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, db.Exec(q, args...).Error, q)
	}
	exec(`INSERT INTO users (id, email, password_hash) VALUES (?, 'owner@example.com', 'x')`, migUser)
	exec(`INSERT INTO os_templates (id, name, version, image_path, checksum) VALUES (?, 'debian', '12', '/x', 'c')`, migTmpl)
	exec(`INSERT INTO nodes (id, name, ip_address, token, status, cpu_count, memory_total_bytes) VALUES
		(?, 'alpha', '192.0.2.10', 'ta', 'active', 16, 68719476736), (?, 'beta', '192.0.2.20', 'tb', 'active', 16, 68719476736)`, migSrc, migDst)
	exec(`INSERT INTO storage_pools (id, name, type, status, total_space, available_space, path, is_primary, node_id) VALUES
		(gen_random_uuid(), 'vm', 'dir', 'online', 2199023255552, 1099511627776, '/vm', true, ?),
		(?, 'data', 'dir', 'online', 2199023255552, 1099511627776, '/data', true, ?)`, migSrc, migDPool, migDst)
	exec(`INSERT INTO ip_pools (id, name, cidr, gateway, bridge) VALUES (?, 'public', '198.51.100.0/24', '198.51.100.1', 'br0')`, migPool)

	src := &fakeNode{name: "alpha", pool: "/vm", key: testKey(t), hostKey: testKey(t), cpu: "Skylake", vms: map[string]*fakeVM{}, progress: map[string]*pb.GetMigrationProgressResponse{}}
	dst := &fakeNode{name: "beta", pool: "/data", key: testKey(t), hostKey: testKey(t), cpu: "Skylake", vms: map[string]*fakeVM{}, progress: map[string]*pb.GetMigrationProgressResponse{}}
	src.peer, dst.peer = dst, src

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	vmRepo, nodeRepo := repository.NewVMRepository(db), repository.NewNodeRepository(db)
	vms := NewVMService(db, vmRepo, nodeRepo, repository.NewTemplateRepository(db), nil, log)
	for id, agent := range map[string]*fakeNode{migSrc: src, migDst: dst} {
		lis := bufconn.Listen(1 << 20)
		srv := grpc.NewServer()
		pb.RegisterNodeAgentServer(srv, agent)
		go srv.Serve(lis)
		conn, err := grpc.NewClient("passthrough:///node",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
			grpc.WithTransportCredentials(insecure.NewCredentials()))
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close(); srv.Stop() })
		vms.grpcConns[id] = conn
	}
	network := NewNetworkService(db, repository.NewNetworkRepository(db), repository.NewFirewallRepository(db), vmRepo, nodeRepo, nil)
	svc := NewMigrationService(db, vms, network, nil, nil, log)
	svc.poll = 10 * time.Millisecond
	return &migrationFixture{db: db, svc: svc, src: src, dst: dst, srcID: migSrc, dstID: migDst, dstPool: migDPool, user: migUser, tmpl: migTmpl}
}

// addVM puts a VM with an address, a port forward and a data disk on the source.
func (f *migrationFixture) addVM(t *testing.T, id, host, status, addr string) {
	exec := func(q string, args ...interface{}) {
		t.Helper()
		require.NoError(t, f.db.Exec(q, args...).Error, q)
	}
	exec(`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, resources, status) VALUES (?, ?, ?, ?, ?, '{"cpu":2,"ram":2048,"disk":20}', ?)`,
		id, f.user, f.srcID, host, f.tmpl, status)
	exec(`INSERT INTO vm_disks (vm_id, device, size_gb, path) VALUES (?, 'vdb', 10, ?)`, id, "/vm/"+id+"-vdb.qcow2")
	var netID string
	require.NoError(t, f.db.Raw(`INSERT INTO networks (vm_id, ip_address) VALUES (?, ?) RETURNING id`, id, addr).Scan(&netID).Error)
	exec(`INSERT INTO ip_addresses (pool_id, node_id, address, status, vm_id) VALUES (?, ?, ?, 'assigned', ?)`, migPool, f.srcID, addr, id)
	port := 2200 + int(net.ParseIP(addr).To4()[3])
	exec(`INSERT INTO port_forwards (vm_id, network_id, node_id, external_port, internal_port, protocol) VALUES (?, ?, ?, ?, 22, 'tcp')`, id, netID, f.srcID, port)
	state := "running"
	if status == "stopped" {
		state = "shutoff"
	}
	f.src.vms[id] = &fakeVM{state: state, pool: "/vm"}
}

// queueMove records a checked move the way Start does, without the job queue.
func (f *migrationFixture) queueMove(t *testing.T, vmID string) *models.VMMigration {
	chk, err := f.svc.Check(context.Background(), MigrationRequest{VMID: vmID, DestNodeID: f.dstID})
	require.NoError(t, err)
	require.True(t, chk.CanMove, "%v", chk.Blockers)
	row := &models.VMMigration{VMID: vmID, SourceNodeID: f.srcID, DestNodeID: &chk.DestNodeID, DestPoolID: &chk.DestPoolID, Mode: chk.Mode, Status: models.MigrationQueued}
	require.NoError(t, f.db.Create(row).Error)
	return row
}

func (f *migrationFixture) migration(t *testing.T, id string) models.VMMigration {
	var m models.VMMigration
	require.NoError(t, f.db.Where("id = ?", id).First(&m).Error)
	return m
}

func TestLiveMoveMovesEverythingAndRemovesTheOldCopyLast(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "77777777-7777-7777-7777-777777777777"
	f.addVM(t, vm, "web", "running", "198.51.100.5")

	chk, err := f.svc.Check(ctx, MigrationRequest{VMID: vm})
	require.NoError(t, err)
	assert.True(t, chk.CanMove, "%v", chk.Blockers)
	assert.Equal(t, models.MigrationLive, chk.Mode)
	assert.Equal(t, f.dstID, chk.DestNodeID, "the only other node is the best fit")
	assert.Equal(t, "/data", chk.DestPoolPath)
	assert.Equal(t, 30, chk.DiskGB)
	assert.NotEmpty(t, chk.Warnings, "the port forward moving is said")

	row := f.queueMove(t, vm)
	require.NoError(t, f.svc.RunMigrationJob(ctx, row.ID))

	got := f.migration(t, row.ID)
	assert.Equal(t, models.MigrationCompleted, got.Status, got.Error)
	assert.Empty(t, got.Note)
	var moved struct {
		NodeID        string
		StoragePoolID string
		Status        string
		VNCPort       *int
	}
	require.NoError(t, f.db.Raw(`SELECT node_id, storage_pool_id, status, vnc_port FROM vms WHERE id = ?`, vm).Scan(&moved).Error)
	assert.Equal(t, f.dstID, moved.NodeID)
	assert.Equal(t, f.dstPool, moved.StoragePoolID)
	assert.Equal(t, "running", moved.Status)
	require.NotNil(t, moved.VNCPort)
	assert.Equal(t, 5907, *moved.VNCPort, "the console was set up on the new node")
	var diskPath, fwdNode, addrNode string
	require.NoError(t, f.db.Raw(`SELECT path FROM vm_disks WHERE vm_id = ?`, vm).Scan(&diskPath).Error)
	assert.Equal(t, "/data/"+vm+"-vdb.qcow2", diskPath)
	require.NoError(t, f.db.Raw(`SELECT node_id FROM port_forwards WHERE vm_id = ?`, vm).Scan(&fwdNode).Error)
	assert.Equal(t, f.dstID, fwdNode)
	require.NoError(t, f.db.Raw(`SELECT node_id FROM ip_addresses WHERE vm_id = ?`, vm).Scan(&addrNode).Error)
	assert.Equal(t, f.dstID, addrNode)

	for _, c := range []string{"prepare", "commit", "network", "console", "limits"} {
		assert.True(t, f.dst.called(c), "destination: %s", c)
	}
	assert.True(t, f.src.called("migrate"))
	assert.True(t, f.src.called("remove-source"), "the old copy goes once the VM runs on the new node")
	assert.False(t, f.dst.called("rollback"))
	// Each node trusts the other, and only the other.
	require.Len(t, f.dst.peers, 1)
	assert.Equal(t, f.srcID, f.dst.peers[0].NodeId)
	assert.Equal(t, "192.0.2.10", f.dst.peers[0].Address)
	assert.Equal(t, []string{f.src.hostKey}, f.dst.peers[0].HostKeys)
	require.Len(t, f.src.peers, 1)
	assert.Equal(t, f.dstID, f.src.peers[0].NodeId)

	link, err := f.svc.Link(ctx, f.dstID)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(link.KeyFingerprint, "SHA256:"))
	assert.Len(t, link.HostKeyFingerprints, 1)

	// A finished move is not run again.
	require.NoError(t, f.svc.RunMigrationJob(ctx, row.ID))

	// Removing a node takes its key off the others.
	require.NoError(t, f.db.Exec(`UPDATE nodes SET deleted_at = NOW() WHERE id = ?`, f.dstID).Error)
	f.svc.RevokeNode(ctx, f.dstID)
	f.src.mu.Lock()
	assert.Empty(t, f.src.peers, "a removed node is no longer trusted")
	f.src.mu.Unlock()
}

func TestFailedMoveLeavesTheVMWhereItWas(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "77777777-7777-7777-7777-777777777777"
	f.addVM(t, vm, "web", "running", "198.51.100.5")
	f.src.failMove = "unable to connect to the destination"

	row := f.queueMove(t, vm)
	require.NoError(t, f.svc.RunMigrationJob(ctx, row.ID))
	got := f.migration(t, row.ID)
	assert.Equal(t, models.MigrationFailed, got.Status)
	assert.Contains(t, got.Error, "unable to connect")
	var node string
	require.NoError(t, f.db.Raw(`SELECT node_id FROM vms WHERE id = ?`, vm).Scan(&node).Error)
	assert.Equal(t, f.srcID, node)
	assert.True(t, f.dst.called("rollback"), "what was prepared on the destination is removed")
	assert.False(t, f.src.called("remove-source"), "nothing on the source is touched")
	assert.False(t, VMMigrating(ctx, f.db, vm), "a failed move frees the VM")
}

// A move the node reports as failed after the switch-over is finished, not
// rolled back: the copy on the destination is the only current one.
func TestMoveThatSwitchedOverIsKept(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "77777777-7777-7777-7777-777777777777"
	f.addVM(t, vm, "web", "running", "198.51.100.5")
	f.src.failMove = "landed: confirm phase failed"

	row := f.queueMove(t, vm)
	require.NoError(t, f.svc.RunMigrationJob(ctx, row.ID))
	got := f.migration(t, row.ID)
	assert.Equal(t, models.MigrationCompleted, got.Status, got.Error)
	assert.Contains(t, got.Note, "runs on the destination")
	assert.False(t, f.dst.called("rollback"), "the only current copy is never removed")
	var node string
	require.NoError(t, f.db.Raw(`SELECT node_id FROM vms WHERE id = ?`, vm).Scan(&node).Error)
	assert.Equal(t, f.dstID, node)
}

// An agent restarted mid-move loses its record, but libvirt carries the move
// on: the panel keeps following where the VM is instead of calling it failed.
func TestMoveFollowedAfterTheAgentLosesTrack(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "77777777-7777-7777-7777-777777777777"
	f.addVM(t, vm, "web", "running", "198.51.100.5")
	f.src.lostTrack = true

	row := f.queueMove(t, vm)
	assert.Error(t, f.svc.RunMigrationJob(ctx, row.ID), "the job waits and tries again")
	got := f.migration(t, row.ID)
	assert.Equal(t, models.MigrationRunning, got.Status, got.Error)
	assert.False(t, f.dst.called("rollback"), "the incoming copy is left alone")

	f.src.land(vm, "running")
	require.NoError(t, f.svc.RunMigrationJob(ctx, row.ID))
	got = f.migration(t, row.ID)
	assert.Equal(t, models.MigrationCompleted, got.Status, got.Error)
	var node string
	require.NoError(t, f.db.Raw(`SELECT node_id FROM vms WHERE id = ?`, vm).Scan(&node).Error)
	assert.Equal(t, f.dstID, node)
}

// A start whose answer was lost is followed on the node, not rolled back.
func TestLostStartAnswerIsFollowed(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "77777777-7777-7777-7777-777777777777"
	f.addVM(t, vm, "web", "running", "198.51.100.5")
	f.src.loseAnswer = true

	row := f.queueMove(t, vm)
	assert.Error(t, f.svc.RunMigrationJob(ctx, row.ID), "the job waits and tries again")
	assert.False(t, f.dst.called("rollback"), "the copy may be running; nothing is removed")
	require.NoError(t, f.svc.RunMigrationJob(ctx, row.ID))
	got := f.migration(t, row.ID)
	assert.Equal(t, models.MigrationCompleted, got.Status, got.Error)
	assert.Len(t, filterCalls(f.src, "migrate"), 1, "the move is not started twice")
}

func filterCalls(f *fakeNode, call string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		if c == call {
			out = append(out, c)
		}
	}
	return out
}

// A live move the destination does not confirm removes nothing anywhere.
func TestUnconfirmedLiveMoveRemovesNothing(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "77777777-7777-7777-7777-777777777777"
	f.addVM(t, vm, "web", "running", "198.51.100.5")
	f.dst.refuseCommit = true

	row := f.queueMove(t, vm)
	require.NoError(t, f.svc.RunMigrationJob(ctx, row.ID))
	got := f.migration(t, row.ID)
	assert.Equal(t, models.MigrationFailed, got.Status)
	assert.False(t, f.src.called("remove-source"), "the old copy is kept")
	assert.False(t, f.dst.called("rollback"), "the new copy is kept")
	var node string
	require.NoError(t, f.db.Raw(`SELECT node_id FROM vms WHERE id = ?`, vm).Scan(&node).Error)
	assert.Equal(t, f.srcID, node)
}

// A move cancelled between the job reading it and starting it never starts.
func TestMoveCancelledAsItStartsNeverStarts(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "77777777-7777-7777-7777-777777777777"
	f.addVM(t, vm, "web", "running", "198.51.100.5")
	row := f.queueMove(t, vm)
	stale := *row

	_, err := f.svc.Cancel(ctx, vm)
	require.NoError(t, err)
	require.NoError(t, f.svc.run(ctx, &stale))
	assert.False(t, f.src.called("migrate"))
	assert.False(t, f.dst.called("prepare"))
	assert.Equal(t, models.MigrationCancelled, f.migration(t, row.ID).Status)
}

func TestOfflineMoveCopiesAStoppedVM(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "88888888-8888-8888-8888-888888888888"
	f.addVM(t, vm, "db", "stopped", "198.51.100.6")

	row := f.queueMove(t, vm)
	assert.Equal(t, models.MigrationOffline, row.Mode)
	require.NoError(t, f.svc.RunMigrationJob(ctx, row.ID))
	got := f.migration(t, row.ID)
	assert.Equal(t, models.MigrationCompleted, got.Status, got.Error)
	assert.True(t, f.dst.called("pull"))
	assert.False(t, f.src.called("migrate"))
	var moved struct{ NodeID, Status string }
	require.NoError(t, f.db.Raw(`SELECT node_id, status FROM vms WHERE id = ?`, vm).Scan(&moved).Error)
	assert.Equal(t, f.dstID, moved.NodeID)
	assert.Equal(t, "stopped", moved.Status, "a stopped VM stays stopped")
}

func TestCheckRefusesWhatCannotMove(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "77777777-7777-7777-7777-777777777777"
	f.addVM(t, vm, "web", "running", "198.51.100.5")
	exec := func(q string, args ...interface{}) { require.NoError(t, f.db.Exec(q, args...).Error, q) }

	exec(`INSERT INTO ip_addresses (pool_id, node_id, address, status, vm_id, delivery_mode, nat_mode) VALUES (?, ?, '198.51.100.9', 'assigned', ?, 'floating', 'inbound')`, migPool, f.srcID, vm)
	exec(`INSERT INTO ip_pool_nodes (pool_id, node_id) VALUES (?, ?)`, migPool, f.srcID)
	exec(`UPDATE nodes SET creation_paused = true WHERE id = ?`, f.dstID)
	f.dst.cpu = "EPYC"

	chk, err := f.svc.Check(ctx, MigrationRequest{VMID: vm, DestNodeID: f.dstID})
	require.NoError(t, err)
	assert.False(t, chk.CanMove)
	text := strings.Join(chk.Blockers, "\n")
	for _, want := range []string{"floating IP 198.51.100.9", "address pool public is not available on node beta", "paused for new VMs", "host-passthrough"} {
		assert.Contains(t, text, want)
	}

	// With no node to take it, best fit says so.
	chk, err = f.svc.Check(ctx, MigrationRequest{VMID: vm})
	require.NoError(t, err)
	assert.False(t, chk.CanMove)
}

// A move lands in a directory pool even when the destination's primary pool is
// LVM, never onto another VM's NAT ports, and its next backup is a full one.
func TestMoveFitsLVMNATAndBackupChains(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "77777777-7777-7777-7777-777777777777"
	const natVM = "88888888-8888-8888-8888-888888888888"
	f.addVM(t, vm, "web", "running", "198.51.100.5") // forwards 2205/tcp
	exec := func(q string, args ...interface{}) { require.NoError(t, f.db.Exec(q, args...).Error, q) }
	exec(`UPDATE storage_pools SET is_primary = false WHERE id = ?`, f.dstPool)
	exec(`INSERT INTO storage_pools (id, name, type, status, total_space, available_space, path, is_primary, node_id) VALUES
		(gen_random_uuid(), 'thin', 'lvmthin', 'online', 2199023255552, 1099511627776, 'vg0/thin', true, ?)`, f.dstID)
	exec(`INSERT INTO backup_schedules (vm_id, schedule, storage_provider, full_every_days) VALUES (?, '0 3 * * *', 'local', 7)`, vm)

	chk, err := f.svc.Check(ctx, MigrationRequest{VMID: vm})
	require.NoError(t, err)
	require.True(t, chk.CanMove, "%v", chk.Blockers)
	assert.Equal(t, "/data", chk.DestPoolPath, "the directory pool, not the LVM primary")

	exec(`INSERT INTO nat_port_blocks (vm_id, node_id, start_port, port_count) VALUES (?, ?, 2200, 20)`, natVM, f.dstID)
	chk, err = f.svc.Check(ctx, MigrationRequest{VMID: vm, DestNodeID: f.dstID})
	require.NoError(t, err)
	assert.False(t, chk.CanMove)
	assert.Contains(t, strings.Join(chk.Blockers, "\n"), "2205/tcp")

	// Taken while the VM moves: the move ends, without the forward.
	exec(`DELETE FROM nat_port_blocks`)
	row := f.queueMove(t, vm)
	f.src.onMove = func() {
		exec(`INSERT INTO nat_port_blocks (vm_id, node_id, start_port, port_count) VALUES (?, ?, 2200, 20)`, natVM, f.dstID)
	}
	require.NoError(t, f.svc.RunMigrationJob(ctx, row.ID))
	got := f.migration(t, row.ID)
	assert.Equal(t, models.MigrationCompleted, got.Status, got.Error)
	assert.Contains(t, got.Note, "2205/tcp")
	var forwards int64
	require.NoError(t, f.db.Raw(`SELECT COUNT(*) FROM port_forwards WHERE vm_id = ? AND deleted_at IS NULL`, vm).Scan(&forwards).Error)
	assert.Zero(t, forwards)
	var reset int64
	require.NoError(t, f.db.Raw(`SELECT COUNT(*) FROM backup_schedules WHERE vm_id = ? AND chain_reset_at IS NOT NULL`, vm).Scan(&reset).Error)
	assert.Equal(t, int64(1), reset, "the next scheduled backup starts a new chain")
}

func TestCancelBeforeStart(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const vm = "77777777-7777-7777-7777-777777777777"
	f.addVM(t, vm, "web", "running", "198.51.100.5")
	row := f.queueMove(t, vm)
	assert.True(t, VMMigrating(ctx, f.db, vm))
	err := f.svc.vms.DeleteVM(ctx, vm)
	assert.ErrorIs(t, err, ErrVMMigrating, "a moving VM is not deleted")

	got, err := f.svc.Cancel(ctx, vm)
	require.NoError(t, err)
	assert.Equal(t, models.MigrationCancelled, got.Status)
	require.NoError(t, f.svc.RunMigrationJob(ctx, row.ID))
	assert.False(t, f.src.called("migrate"), "a cancelled move never starts")
	_, err = f.svc.Cancel(ctx, vm)
	assert.ErrorIs(t, err, ErrNoActiveMigration)
}

func TestDrainMovesWhatItCanAndReportsTheRest(t *testing.T) {
	f := newMigrationFixture(t)
	ctx := context.Background()
	const movable, stuck = "77777777-7777-7777-7777-777777777777", "99999999-9999-9999-9999-999999999999"
	f.addVM(t, movable, "web", "running", "198.51.100.5")
	f.addVM(t, stuck, "mail", "running", "198.51.100.7")
	require.NoError(t, f.db.Exec(`UPDATE vms SET rescue_mode = true WHERE id = ?`, stuck).Error)

	plan, err := f.svc.PlanDrain(ctx, f.srcID, nil)
	require.NoError(t, err)
	require.Len(t, plan.Items, 2)
	assert.Equal(t, 1, plan.Movable)
	assert.Equal(t, 1, plan.Blocked)
	assert.Equal(t, "mail", plan.Items[0].Hostname, "sorted by name")
	assert.Contains(t, strings.Join(plan.Items[0].Blockers, " "), "rescue mode")

	d := &models.NodeDrain{NodeID: f.srcID, Status: models.DrainRunning, Concurrency: 2, Destinations: map[string]string{}}
	require.NoError(t, f.db.Create(d).Error)
	require.NoError(t, f.svc.RunDrainJob(ctx, d.ID))

	rep, err := f.svc.LatestDrain(ctx, f.srcID)
	require.NoError(t, err)
	require.NotNil(t, rep.Drain)
	assert.Equal(t, models.DrainIncomplete, rep.Drain.Status)
	assert.Equal(t, 1, rep.Moved)
	assert.Equal(t, 1, rep.Skipped)
	assert.Equal(t, 1, rep.Remaining)
	for _, it := range rep.Items {
		if it.VMID == stuck {
			assert.Equal(t, models.MigrationSkipped, it.Status)
			assert.Contains(t, it.Error, "rescue mode")
			assert.Equal(t, "mail", it.Hostname)
		} else {
			assert.Equal(t, models.MigrationCompleted, it.Status)
			assert.Equal(t, "beta", it.DestNodeName)
		}
	}
	var node string
	require.NoError(t, f.db.Raw(`SELECT node_id FROM vms WHERE id = ?`, stuck).Scan(&node).Error)
	assert.Equal(t, f.srcID, node, "a VM that cannot move stays where it is")
	assert.False(t, f.src.called("remove-source") && len(f.src.vms) == 0, "the stuck VM's domain is left alone")

	// Aborting a drain that has finished is refused; there is none running.
	_, err = f.svc.AbortDrain(ctx, f.srcID)
	assert.ErrorIs(t, err, ErrNoActiveDrain)
}
