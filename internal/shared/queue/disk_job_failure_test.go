package queue

import (
	"context"
	"errors"
	"fmt"
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
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/panel/repository"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

func TestPermanentDiskFailure(t *testing.T) {
	agent := func(code pb.ErrorCode, msg string) *pb.ErrorResponse {
		return &pb.ErrorResponse{Code: code, Message: msg}
	}
	cases := []struct {
		name      string
		rpcErr    error
		agentErr  *pb.ErrorResponse
		permanent bool
	}{
		// The call itself failing.
		{"node unreachable", status.Error(codes.Unavailable, "connection refused"), nil, false},
		{"could not connect at all", errors.New("failed to connect to agent at 192.0.2.1: context deadline exceeded"), nil, false},
		{"panel shutting down", status.Error(codes.Canceled, "context canceled"), nil, false},
		{"agent too old for the call", status.Error(codes.Unimplemented, "unknown method"), nil, true},
		{"bad request", status.Error(codes.InvalidArgument, "vm_id is required"), nil, true},
		{"token refused", status.Error(codes.Unauthenticated, "not authenticated"), nil, true},
		{"ran out of its own time limit", status.Error(codes.DeadlineExceeded, "deadline exceeded"), nil, true},

		// An agent that says why.
		{"no destination", nil, agent(pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "missing object storage configuration"), true},
		{"bad credentials", nil, agent(pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED, "the backup destination did not accept a test write"), true},
		{"disk gone", nil, agent(pb.ErrorCode_ERROR_CODE_NOT_FOUND, "primary disk not found"), true},
		{"no room to export", nil, agent(pb.ErrorCode_ERROR_CODE_RESOURCE_EXHAUSTED, "not enough room on /vm"), true},
		{"export failed", nil, agent(pb.ErrorCode_ERROR_CODE_INTERNAL, "disk export failed: exit status 1"), true},
		{"store unreachable", nil, &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_UNAVAILABLE, Message: "upload failed: dial tcp: i/o timeout", Retryable: true}, false},

		// An agent from before failures carried a code: everything is INTERNAL.
		{"old agent, no keys", nil, agent(pb.ErrorCode_ERROR_CODE_INTERNAL, "missing object storage configuration (set S3_ACCESS_KEY/S3_SECRET_KEY/S3_BUCKET)"), true},
		{"old agent, disk gone", nil, agent(pb.ErrorCode_ERROR_CODE_INTERNAL, "primary disk not found: domain not found"), true},
		{"old agent, keys refused", nil, agent(pb.ErrorCode_ERROR_CODE_INTERNAL, "upload failed: failed to upload object: operation error S3: PutObject, https response error StatusCode: 403, api error SignatureDoesNotMatch: no"), true},
		{"old agent, no such bucket", nil, agent(pb.ErrorCode_ERROR_CODE_INTERNAL, "upload failed: failed to upload object: api error NoSuchBucket: The specified bucket does not exist"), true},
		{"old agent, network dropped", nil, agent(pb.ErrorCode_ERROR_CODE_INTERNAL, "upload failed: failed to upload object: dial tcp 192.0.2.9:443: i/o timeout"), false},
		{"no reason given", nil, nil, true},
	}
	for _, c := range cases {
		if got := permanentDiskFailure(c.rpcErr, c.agentErr); got != c.permanent {
			t.Errorf("%s: permanent = %v, want %v", c.name, got, c.permanent)
		}
	}
}

// backupAgent answers BackupDisk the way a node does, and remembers what it
// was asked.
type backupAgent struct {
	pb.UnimplementedNodeAgentServer
	resp  *pb.BackupDiskResponse
	err   error
	asked []*pb.BackupDiskRequest

	cmdResp *pb.VMCommandResponse
	cmdErr  error
}

func (a *backupAgent) ExecuteVMCommand(context.Context, *pb.VMCommandRequest) (*pb.VMCommandResponse, error) {
	return a.cmdResp, a.cmdErr
}

// GetVMStatus says the VM is off, as a restore waits for it to be.
func (a *backupAgent) GetVMStatus(_ context.Context, req *pb.VMStatusRequest) (*pb.VMStatusResponse, error) {
	return &pb.VMStatusResponse{VmId: req.VmId, State: pb.VMState_VM_STATE_STOPPED}, nil
}

func (a *backupAgent) BackupDisk(_ context.Context, req *pb.BackupDiskRequest) (*pb.BackupDiskResponse, error) {
	a.asked = append(a.asked, req)
	return a.resp, a.err
}

const (
	testVMID     = "11111111-1111-1111-1111-111111111111"
	testNodeID   = "22222222-2222-2222-2222-222222222222"
	testBackupID = "33333333-3333-3333-3333-333333333333"
)

// backupWorkerFixture is a backup worker with a database holding one VM, its
// node and one pending backup, talking to the given agent.
func backupWorkerFixture(t *testing.T, agent pb.NodeAgentServer, store *pb.ObjectStore) (*BackupWorker, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, name TEXT, ip_address TEXT, status TEXT, token TEXT, deleted_at DATETIME)`,
		`CREATE TABLE vms (id TEXT PRIMARY KEY, user_id TEXT, node_id TEXT, hostname TEXT, os_template_id TEXT, resources TEXT, status TEXT, updated_at DATETIME, deleted_at DATETIME)`,
		`CREATE TABLE backups (id TEXT PRIMARY KEY, vm_id TEXT, storage_provider TEXT, storage_path TEXT, backup_type TEXT, status TEXT, size INTEGER DEFAULT 0, compression TEXT, checksum TEXT, error_message TEXT, started_at DATETIME, completed_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME, disks TEXT DEFAULT '[]', consistency TEXT DEFAULT '', destination_id TEXT, node_id TEXT, mode TEXT DEFAULT 'full', parent_id TEXT, chain_id TEXT, checkpoint TEXT DEFAULT '', mode_note TEXT DEFAULT '', encryption_key_id TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO nodes (id, name, ip_address, status, token) VALUES ('` + testNodeID + `', 'node-1', '192.0.2.10', 'active', 'tok')`,
		`INSERT INTO vms (id, user_id, node_id, hostname, os_template_id, resources, status) VALUES ('` + testVMID + `', 'u', '` + testNodeID + `', 'h', 't', '{"cpu":1,"ram":512,"disk":10}', 'running')`,
		`INSERT INTO backups (id, vm_id, storage_provider, storage_path, status) VALUES ('` + testBackupID + `', '` + testVMID + `', 's3', 'backups/x.qcow2', 'pending')`,
	} {
		if err := db.Exec(stmt).Error; err != nil {
			t.Fatal(err)
		}
	}

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
	agents := NewAgentClient()
	agents.clients[testNodeID] = pb.NewNodeAgentClient(conn)

	prevCtx, prevHook := globalWorkerContext, ObjectStoreHook
	SetWorkerContext(&WorkerContext{DB: db, VMRepo: repository.NewVMRepository(db), NodeRepo: repository.NewNodeRepository(db), AgentClient: agents})
	ObjectStoreHook = func(context.Context, string) (*pb.ObjectStore, string, error) { return store, "dest-1", nil }
	t.Cleanup(func() {
		globalWorkerContext, ObjectStoreHook = prevCtx, prevHook
		conn.Close()
		srv.Stop()
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	return NewBackupWorker(slog.New(slog.NewTextHandler(io.Discard, nil))), db
}

func backupJob(attempt, maxAttempts int) *river.Job[BackupJob] {
	return &river.Job[BackupJob]{
		JobRow: &rivertype.JobRow{ID: 1, Attempt: attempt, MaxAttempts: maxAttempts},
		Args:   BackupJob{VMID: testVMID, BackupType: BackupTypeFull, Destination: "backups/x.qcow2", BackupID: testBackupID},
	}
}

func loadBackup(t *testing.T, db *gorm.DB) models.Backup {
	t.Helper()
	var b models.Backup
	if err := db.First(&b, "id = ?", testBackupID).Error; err != nil {
		t.Fatal(err)
	}
	return b
}

func cancelled(err error) bool {
	var c *rivertype.JobCancelError
	return errors.As(err, &c)
}

// The failure the report was written about: a node with no usable storage.
// One attempt, not twenty-five, and the row says why.
func TestBackupWithNoDestinationIsCancelledWithItsReason(t *testing.T) {
	agent := &backupAgent{resp: &pb.BackupDiskResponse{Error: &pb.ErrorResponse{
		Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: "missing object storage configuration (set S3_ACCESS_KEY/S3_SECRET_KEY/S3_BUCKET)"}}}
	w, db := backupWorkerFixture(t, agent, nil)

	// A job queued before the limit existed still carries River's 25 attempts.
	err := w.Work(context.Background(), backupJob(1, 25))
	if !cancelled(err) {
		t.Fatalf("a backup that can never work was left to be retried: %v", err)
	}
	b := loadBackup(t, db)
	if b.Status != models.BackupStatusFailed || !strings.Contains(b.ErrorMessage, "missing object storage configuration") {
		t.Fatalf("row = %s %q", b.Status, b.ErrorMessage)
	}
	if len(agent.asked) != 1 || agent.asked[0].GetObjectStore() != nil {
		t.Fatalf("asked %d time(s), store %v", len(agent.asked), agent.asked[0].GetObjectStore())
	}
}

func TestBackupTransientFailureIsRetriedAFewTimesOnly(t *testing.T) {
	agent := &backupAgent{err: status.Error(codes.Unavailable, "node restarting")}
	w, db := backupWorkerFixture(t, agent, nil)

	err := w.Work(context.Background(), backupJob(1, 25))
	if err == nil || cancelled(err) {
		t.Fatalf("first transient failure: %v", err)
	}
	// Still an active backup between tries, so a second one cannot start.
	if b := loadBackup(t, db); b.Status != models.BackupStatusPending || !strings.Contains(b.ErrorMessage, "node restarting") {
		t.Fatalf("between tries: %s %q", b.Status, b.ErrorMessage)
	}

	err = w.Work(context.Background(), backupJob(DiskJobMaxAttempts, 25))
	if !cancelled(err) {
		t.Fatalf("attempt %d of a job allowed 25 was not the last: %v", DiskJobMaxAttempts, err)
	}
	if b := loadBackup(t, db); b.Status != models.BackupStatusFailed || !strings.Contains(b.ErrorMessage, "node restarting") {
		t.Fatalf("after the last try: %s %q", b.Status, b.ErrorMessage)
	}
}

func TestBackupRecordsWhereTheFileWent(t *testing.T) {
	store := &pb.ObjectStore{Endpoint: "s3.example.test", Bucket: "b", AccessKey: "ak", SecretKey: "sk"}
	done := func(usedRequestStore bool) *pb.BackupDiskResponse {
		return &pb.BackupDiskResponse{Success: true, ObjectKey: "backups/x.qcow2", SizeBytes: 42, Checksum: "abc", UsedRequestStore: usedRequestStore}
	}

	t.Run("current agent", func(t *testing.T) {
		agent := &backupAgent{resp: done(true)}
		w, db := backupWorkerFixture(t, agent, store)
		if err := w.Work(context.Background(), backupJob(1, 3)); err != nil {
			t.Fatal(err)
		}
		if got := agent.asked[0].GetObjectStore(); got.GetBucket() != "b" || got.GetSecretKey() != "sk" {
			t.Fatalf("the node was not sent the destination: %v", got)
		}
		if b := loadBackup(t, db); b.Status != models.BackupStatusCompleted || b.StorageProvider != models.BackupStoreDestination || b.Checksum != "abc" || b.Size != 42 {
			t.Fatalf("row = %+v", b)
		}
	})

	// An agent that predates the destination ignores it and uploads with its
	// own keys. The panel must not record the file as being in its bucket.
	t.Run("older agent", func(t *testing.T) {
		w, db := backupWorkerFixture(t, &backupAgent{resp: done(false)}, store)
		if err := w.Work(context.Background(), backupJob(1, 3)); err != nil {
			t.Fatal(err)
		}
		if b := loadBackup(t, db); b.Status != models.BackupStatusCompleted || b.StorageProvider != models.BackupStoreNode {
			t.Fatalf("row = %s in %q", b.Status, b.StorageProvider)
		}
	})

	t.Run("no destination set", func(t *testing.T) {
		w, db := backupWorkerFixture(t, &backupAgent{resp: done(false)}, nil)
		if err := w.Work(context.Background(), backupJob(1, 3)); err != nil {
			t.Fatal(err)
		}
		if b := loadBackup(t, db); b.StorageProvider != models.BackupStoreNode {
			t.Fatalf("stored in %q", b.StorageProvider)
		}
	})
}

// A saved destination that cannot be decrypted stops the backup. Falling back
// to the node's own keys would put the file somewhere the operator did not choose.
func TestBackupStopsWhenTheDestinationCannotBeRead(t *testing.T) {
	agent := &backupAgent{resp: &pb.BackupDiskResponse{Success: true}}
	w, db := backupWorkerFixture(t, agent, nil)
	ObjectStoreHook = func(context.Context, string) (*pb.ObjectStore, string, error) {
		return nil, "", errors.New("cannot decrypt")
	}

	// Tried again, because the cause may be the database not answering for a
	// moment and nothing has been exported; then it fails with its reason.
	if err := w.Work(context.Background(), backupJob(1, 3)); err == nil || cancelled(err) {
		t.Fatalf("first try: %v", err)
	}
	if err := w.Work(context.Background(), backupJob(3, 3)); !cancelled(err) {
		t.Fatalf("last try: %v", err)
	}
	if len(agent.asked) != 0 {
		t.Fatal("the node was asked to back up anyway")
	}
	if b := loadBackup(t, db); b.Status != models.BackupStatusFailed || !strings.Contains(b.ErrorMessage, "cannot be read") {
		t.Fatalf("row = %s %q", b.Status, b.ErrorMessage)
	}
}

func TestDiskJobsAreInsertedWithAFewAttempts(t *testing.T) {
	for name, opts := range map[string]river.InsertOpts{
		"backup": BackupJob{}.InsertOpts(), "restore": RestoreJob{}.InsertOpts(), "image": ImageJob{}.InsertOpts(),
	} {
		if opts.MaxAttempts != DiskJobMaxAttempts {
			t.Errorf("%s jobs are inserted with MaxAttempts %d, want %d", name, opts.MaxAttempts, DiskJobMaxAttempts)
		}
	}
}

func TestAFailedRestoreAsNewIsNotDownloadedAgain(t *testing.T) {
	if !restoresBackup(&pb.VMConfig{Metadata: map[string]string{models.ImageOnceKey: "1", "hostname": "h"}}) {
		t.Error("a create from a backup must be recognised")
	}
	if restoresBackup(&pb.VMConfig{Metadata: map[string]string{"hostname": "h"}}) || restoresBackup(nil) {
		t.Error("an ordinary create keeps its retries")
	}
}

// restoreCreateJob is the create a restore as a new VM queues, as its nth try.
func restoreCreateJob(failedTries int, age time.Duration) *river.Job[VMOperationJob] {
	return &river.Job[VMOperationJob]{
		JobRow: &rivertype.JobRow{ID: 2, Attempt: failedTries + 1, MaxAttempts: 25, CreatedAt: time.Now().Add(-age),
			Errors: make([]rivertype.AttemptError, failedTries)},
		Args: VMOperationJob{VMID: testVMID, NodeID: testNodeID, Operation: VMOpCreate,
			Params: []byte(`{"image_path":"s3://backups/x.qcow2","resources":{"cpu":1,"ram":512,"disk":10},"metadata":{"image_once":"1","image_sha256":"abc"},"object_store":true}`)},
	}
}

func vmStatus(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var s string
	if err := db.Raw(`SELECT status FROM vms WHERE id = ?`, testVMID).Scan(&s).Error; err != nil {
		t.Fatal(err)
	}
	return s
}

// A create that restores a backup downloads a whole disk before the node
// answers: it gets a restore's time, not an ordinary create's hour.
func TestRestoreCreateIsGivenARestoresTime(t *testing.T) {
	w := &VMOperationWorker{}
	if got := w.Timeout(restoreCreateJob(0, 0)); got < diskTransferTimeout {
		t.Errorf("a create from a backup is cut off after %s", got)
	}
	plain := &river.Job[VMOperationJob]{Args: VMOperationJob{Operation: VMOpCreate, Params: []byte(`{"image_path":"/img.qcow2"}`)}}
	if got := w.Timeout(plain); got != 65*time.Minute {
		t.Errorf("an ordinary create's limit changed to %s", got)
	}
}

func TestRestoreCreateFailures(t *testing.T) {
	store := &pb.ObjectStore{Bucket: "b", AccessKey: "ak", SecretKey: "sk"}
	worker := func(t *testing.T, agent *backupAgent) (*VMOperationWorker, *gorm.DB) {
		_, db := backupWorkerFixture(t, agent, store)
		return NewVMOperationWorker(slog.New(slog.NewTextHandler(io.Discard, nil))), db
	}
	var snooze *river.JobSnoozeError

	// The panel restarted mid-download and asks again: the node is still at
	// work. The VM must not be failed under it, and no try is used up.
	t.Run("node still downloading", func(t *testing.T) {
		busy := &pb.VMCommandResponse{Message: "a create for VM " + testVMID + " is already running on this node"}
		w, db := worker(t, &backupAgent{cmdResp: busy})
		err := w.Work(context.Background(), restoreCreateJob(1, time.Hour))
		if !errors.As(err, &snooze) {
			t.Fatalf("got %v, want the job put off", err)
		}
		if got := vmStatus(t, db); got != "running" {
			t.Fatalf("VM marked %q while the node is still restoring it", got)
		}

		// Not for ever: a node that never finishes ends the job.
		if err := w.Work(context.Background(), restoreCreateJob(1, 3*diskTransferTimeout)); !cancelled(err) {
			t.Fatalf("got %v, want the job ended", err)
		}
		if got := vmStatus(t, db); got != string(models.VMStatusError) {
			t.Fatalf("VM left %q", got)
		}
	})

	t.Run("node refuses", func(t *testing.T) {
		w, db := worker(t, &backupAgent{cmdResp: &pb.VMCommandResponse{Message: "checksum mismatch: got a, want b"}})
		if err := w.Work(context.Background(), restoreCreateJob(0, 0)); !cancelled(err) {
			t.Fatalf("got %v, want one try only", err)
		}
		if got := vmStatus(t, db); got != string(models.VMStatusError) {
			t.Fatalf("VM left %q", got)
		}
	})

	t.Run("node unreachable", func(t *testing.T) {
		w, db := worker(t, &backupAgent{cmdErr: status.Error(codes.Unavailable, "node restarting")})
		err := w.Work(context.Background(), restoreCreateJob(0, 0))
		if err == nil || cancelled(err) || errors.As(err, &snooze) {
			t.Fatalf("first failure: %v, want another try", err)
		}
		if got := vmStatus(t, db); got != "running" {
			t.Fatalf("VM marked %q before the last try", got)
		}
		// River would allow 25; the third failure is the last.
		if err := w.Work(context.Background(), restoreCreateJob(DiskJobMaxAttempts-1, 0)); !cancelled(err) {
			t.Fatalf("third failure: %v, want the job ended", err)
		}
		if got := vmStatus(t, db); got != string(models.VMStatusError) {
			t.Fatalf("VM left %q", got)
		}
	})
}

func TestPermanentVMFailure(t *testing.T) {
	internal := func(msg string) *pb.ErrorResponse {
		return &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INTERNAL, Message: msg}
	}
	for _, tc := range []struct {
		name string
		err  *pb.ErrorResponse
		want bool
	}{
		{"template missing on the node", internal("template image not found at /var/lib/libvirt/templates/x.qcow2: no such file"), true},
		{"domain gone", internal("failed to start: virError(Code=42, Domain=10, Message='Domain not found: no domain with matching uuid')"), true},
		{"disk full", internal("failed to clone QCOW2 image: exit status 1 (output: No space left on device)"), true},
		{"refused by code", &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Message: "bad"}, true},
		{"libvirt busy", internal("cannot acquire state change lock"), false},
		{"download hiccup", internal("failed to download template image from https://example.com/x.qcow2: connection reset"), false},
		{"node says try again", &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_NOT_FOUND, Retryable: true}, false},
		{"no structured error", nil, false},
	} {
		if got := permanentVMFailure(tc.err, tc.err.GetMessage()); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
