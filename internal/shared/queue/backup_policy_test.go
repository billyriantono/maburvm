package queue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

func TestBackupWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 11, 19, h, m, 0, 0, time.UTC) }
	night := BackupPolicy{WindowStart: "22:00", WindowEnd: "06:00"}
	cases := []struct {
		p    BackupPolicy
		now  time.Time
		want time.Duration
	}{
		{BackupPolicy{}, at(12, 0), 0},
		{BackupPolicy{WindowStart: "02:00", WindowEnd: "02:00"}, at(12, 0), 0},
		{BackupPolicy{WindowStart: "nonsense", WindowEnd: "06:00"}, at(12, 0), 0},
		{night, at(23, 0), 0},
		{night, at(3, 0), 0},
		{night, at(6, 0), 16 * time.Hour},
		{night, at(21, 30), 30 * time.Minute},
		{BackupPolicy{WindowStart: "01:00", WindowEnd: "05:00"}, at(5, 0), 20 * time.Hour},
		{BackupPolicy{WindowStart: "01:00", WindowEnd: "05:00"}, at(0, 15), 45 * time.Minute},
	}
	for _, c := range cases {
		if got := c.p.UntilWindow(c.now); got != c.want {
			t.Errorf("%+v at %s: UntilWindow = %s, want %s", c.p, c.now.Format("15:04"), got, c.want)
		}
	}
	if err := (BackupPolicy{WindowStart: "25:00", WindowEnd: "06:00"}).ValidateWindow(); err == nil {
		t.Error("an impossible time must be refused")
	}
	if err := night.ValidateWindow(); err != nil {
		t.Error(err)
	}
	if got := (BackupPolicy{}).NodeLimit(); got != DefaultBackupsPerNode {
		t.Errorf("default limit = %d", got)
	}
	if got := (BackupPolicy{PerNode: 500}).NodeLimit(); got != maxBackupsPerNode {
		t.Errorf("limit is not capped: %d", got)
	}
}

func addBackupPolicy(t *testing.T, w *BackupWorker, policy string) {
	t.Helper()
	db := globalWorkerContext.DB
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`,
		`INSERT INTO system_settings (section, data) VALUES ('` + BackupPolicySection + `', '` + policy + `')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// A node at its limit queues the next backup instead of starting it: the job
// is put off without using a try, and the row says why.
func TestBackupWaitsForAFreeSlotOnItsNode(t *testing.T) {
	agent := &backupAgent{resp: &pb.BackupDiskResponse{Success: true, ObjectKey: "k", Checksum: "c"}}
	w, db := backupWorkerFixture(t, agent, nil)
	addBackupPolicy(t, w, `{"per_node":1}`)
	other := "44444444-4444-4444-4444-444444444444"
	otherVM := "55555555-5555-5555-5555-555555555555"
	for _, stmt := range []string{
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, resources, status) VALUES ('` + otherVM + `', 'u', '` + testNodeID + `', 'h2', 't', '{}', 'running')`,
		`INSERT INTO backups (id, vm_id, storage_provider, storage_path, status, started_at) VALUES ('` + other + `', '` + otherVM + `', 's3', 'k2', 'in_progress', CURRENT_TIMESTAMP)`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}

	var snooze *river.JobSnoozeError
	if err := w.Work(context.Background(), backupJob(1, 3)); !errors.As(err, &snooze) {
		t.Fatalf("got %v, want the backup queued", err)
	}
	if len(agent.asked) != 0 {
		t.Fatal("the node was asked to export past its limit")
	}
	if b := loadBackup(t, db); b.Status != models.BackupStatusPending || !strings.Contains(b.ErrorMessage, "Queued") {
		t.Fatalf("row = %s %q", b.Status, b.ErrorMessage)
	}

	// The other backup finished: this one starts.
	if err := db.Exec(`UPDATE backups SET status = 'completed' WHERE id = ?`, other).Error; err != nil {
		t.Fatal(err)
	}
	if err := w.Work(context.Background(), backupJob(1, 3)); err != nil {
		t.Fatal(err)
	}
	if b := loadBackup(t, db); b.Status != models.BackupStatusCompleted || b.ErrorMessage != "" {
		t.Fatalf("row = %s %q", b.Status, b.ErrorMessage)
	}
}

func TestScheduledBackupWaitsForTheWindow(t *testing.T) {
	agent := &backupAgent{resp: &pb.BackupDiskResponse{Success: true}}
	w, db := backupWorkerFixture(t, agent, nil)
	now := time.Now()
	start := now.Add(2 * time.Hour).Format("15:04")
	addBackupPolicy(t, w, `{"window_start":"`+start+`","window_end":"`+now.Add(3*time.Hour).Format("15:04")+`"}`)

	job := backupJob(1, 3)
	job.Args.Scheduled = true
	var snooze *river.JobSnoozeError
	if err := w.Work(context.Background(), job); !errors.As(err, &snooze) || snooze.Duration < time.Hour {
		t.Fatalf("got %v, want the backup put off until the window", err)
	}
	if b := loadBackup(t, db); !strings.Contains(b.ErrorMessage, start) || len(agent.asked) != 0 {
		t.Fatalf("row %q, asked %d", b.ErrorMessage, len(agent.asked))
	}
	// A backup someone asked for now is not held to the window.
	if err := w.Work(context.Background(), backupJob(1, 3)); err != nil {
		t.Fatal(err)
	}
}

func TestBackupOfEveryDiskIsRecorded(t *testing.T) {
	agent := &backupAgent{resp: &pb.BackupDiskResponse{Success: true, ObjectKey: "backups/x.qcow2", SizeBytes: 10, Checksum: "aa",
		Compression: "zstd", Consistency: "frozen",
		Disks: []*pb.BackupDiskFile{
			{Target: "vda", Key: "backups/x.qcow2", SizeBytes: 10, Checksum: "aa", VirtualSizeBytes: 20 << 30},
			{Target: "vdb", Key: "backups/x-vdb.qcow2", SizeBytes: 5, Checksum: "bb", VirtualSizeBytes: 50 << 30},
		}}}
	w, db := backupWorkerFixture(t, agent, nil)
	job := backupJob(1, 3)
	job.Args.Compression = "zstd"
	if err := w.Work(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	asked := agent.asked[0]
	if !asked.GetAllDisks() || asked.GetCompression() != "zstd" || !asked.GetCompress() {
		t.Fatalf("asked %+v", asked)
	}
	b := loadBackup(t, db)
	if b.Size != 15 || b.Compression != "zstd" || b.Consistency != models.BackupConsistencyFrozen || len(b.Disks) != 2 ||
		b.Disks[1] != (models.BackupDisk{Target: "vdb", Key: "backups/x-vdb.qcow2", Size: 5, Checksum: "bb", VirtualSize: 50 << 30}) {
		t.Fatalf("row = %+v", b)
	}
	if keys := b.StorageKeys(); len(keys) != 2 || keys[1] != "backups/x-vdb.qcow2" {
		t.Fatalf("keys = %v", keys)
	}
}

// An agent older than whole-VM backups copies the first disk only; the row
// says so when the VM has others.
func TestBackupFromAnOlderAgentSaysWhatIsMissing(t *testing.T) {
	agent := &backupAgent{resp: &pb.BackupDiskResponse{Success: true, ObjectKey: "b/x.qcow2", SizeBytes: 10, Checksum: "aa"}}
	w, db := backupWorkerFixture(t, agent, nil)
	for _, stmt := range []string{
		`CREATE TABLE vm_disks (id TEXT PRIMARY KEY, vm_id TEXT, device TEXT, size_gb INTEGER, path TEXT, lifecycle TEXT, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`,
		`INSERT INTO vm_disks (id, vm_id, device, size_gb, path) VALUES ('d1', '` + testVMID + `', 'vdb', 50, '/vm/d.qcow2')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Work(context.Background(), backupJob(1, 3)); err != nil {
		t.Fatal(err)
	}
	b := loadBackup(t, db)
	if b.Status != models.BackupStatusCompleted || b.Compression != "gzip" || b.Consistency != models.BackupConsistencyLive ||
		!strings.Contains(b.ErrorMessage, "Only the first disk") || len(b.Disks) != 0 {
		t.Fatalf("row = %+v", b)
	}
}

func TestRestoreRequestForEveryDisk(t *testing.T) {
	single := restoreDiskRequest("vm", RestoreJob{SourceKey: "k", Checksum: "c", Disks: []models.BackupDisk{{Target: "vda", Key: "k", Checksum: "c"}}}, nil)
	if single.GetSourceKey() != "k" || len(single.GetDisks()) != 0 {
		t.Fatalf("a one-disk backup must restore the way every agent understands: %+v", single)
	}
	multi := restoreDiskRequest("vm", RestoreJob{SourceKey: "k", Checksum: "c", Disks: []models.BackupDisk{
		{Target: "vda", Key: "k", Checksum: "c"}, {Target: "vdb", Key: "k2", Checksum: "c2"}}}, nil)
	if multi.GetSourceKey() != "" || len(multi.GetDisks()) != 2 || multi.GetDisks()[1].GetKey() != "k2" {
		t.Fatalf("multi = %+v", multi)
	}
}

// restoreAgent answers RestoreDisk the way an agent too old for several
// disks does.
type restoreAgent struct {
	backupAgent
	asked *pb.RestoreDiskRequest
}

func (a *restoreAgent) RestoreDisk(_ context.Context, req *pb.RestoreDiskRequest) (*pb.RestoreDiskResponse, error) {
	a.asked = req
	if req.GetSourceKey() == "" {
		return nil, status.Error(codes.InvalidArgument, "vm_id and source_key are required")
	}
	return &pb.RestoreDiskResponse{Success: true, BytesRestored: 1}, nil
}

func TestRestoreOfSeveralDisksOnAnOldAgentSaysToUpdateIt(t *testing.T) {
	agent := &restoreAgent{}
	_, db := backupWorkerFixture(t, &agent.backupAgent, nil)
	// backupWorkerFixture registered the embedded agent; register this one.
	agents := NewAgentClient()
	agents.clients[testNodeID] = agentClientFor(t, agent)
	globalWorkerContext.AgentClient = agents
	if err := db.Exec(`CREATE TABLE snapshots (id TEXT PRIMARY KEY, vm_id TEXT, name TEXT, status TEXT, deleted_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO snapshots (id, vm_id, name, status) VALUES ('s1', ?, 'before-update', 'complete')`, testVMID).Error; err != nil {
		t.Fatal(err)
	}
	w := NewRestoreWorker(discardLogger())
	w.agentClient = agents
	job := func(disks []models.BackupDisk) *river.Job[RestoreJob] {
		return &river.Job[RestoreJob]{JobRow: &rivertype.JobRow{Attempt: 1, MaxAttempts: 3},
			Args: RestoreJob{VMID: testVMID, NodeID: testNodeID, BackupID: testBackupID, SourceKey: "k", Checksum: "c", NodeStore: true, Disks: disks}}
	}
	err := w.Work(context.Background(), job([]models.BackupDisk{{Target: "vda", Key: "k"}, {Target: "vdb", Key: "k2"}}))
	if !cancelled(err) || !strings.Contains(err.Error(), "older than the panel") {
		t.Fatalf("got %v", err)
	}
	var n int64
	db.Table("snapshots").Where("deleted_at IS NULL").Count(&n)
	if n != 1 {
		t.Fatal("a refused restore must leave the snapshots alone")
	}

	if err := w.Work(context.Background(), job(nil)); err != nil {
		t.Fatal(err)
	}
	db.Table("snapshots").Where("deleted_at IS NULL").Count(&n)
	if n != 0 {
		t.Fatal("the snapshots of a restored disk must go with it")
	}
}

func TestRestoredDataDisksAreRecorded(t *testing.T) {
	_, db := backupWorkerFixture(t, &backupAgent{}, nil)
	if err := db.Exec(`CREATE TABLE vm_disks (id TEXT PRIMARY KEY, vm_id TEXT, device TEXT, size_gb INTEGER, path TEXT, lifecycle TEXT DEFAULT 'attached', created_at DATETIME, updated_at DATETIME, deleted_at DATETIME)`).Error; err != nil {
		t.Fatal(err)
	}
	disks := []*pb.BackupDiskFile{{Target: "vdb", Path: "/vm/x-data-vdb.qcow2", VirtualSizeBytes: 50<<30 + 1}}
	recordRestoredDisks(context.Background(), db, testVMID, disks, discardLogger())
	recordRestoredDisks(context.Background(), db, testVMID, disks, discardLogger()) // a create tried again
	var rows []models.VMDisk
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Device != "vdb" || rows[0].SizeGB != 51 || rows[0].Path != "/vm/x-data-vdb.qcow2" {
		t.Fatalf("rows = %+v", rows)
	}
	if got := restoreDisksProto([]models.BackupDisk{{Target: "vdb", Key: "k", Checksum: "c", VirtualSize: 7}},
		map[string][]models.BackupDisk{"vdb": {{Key: "full", Checksum: "f"}}}); len(got) != 1 || got[0].GetKey() != "k" || got[0].GetVirtualSizeBytes() != 7 ||
		len(got[0].GetLayers()) != 1 || got[0].GetLayers()[0].GetKey() != "full" {
		t.Fatalf("proto = %+v", got)
	}
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// agentClientFor serves agent over an in-memory connection.
func agentClientFor(t *testing.T, agent pb.NodeAgentServer) pb.NodeAgentClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterNodeAgentServer(srv, agent)
	go srv.Serve(lis)
	conn, err := grpc.NewClient("passthrough:///node",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close(); srv.Stop() })
	return pb.NewNodeAgentClient(conn)
}
