package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/storage"
)

const testAESKey = "0123456789abcdef0123456789abcdef"

const backupDestinationsTable = `CREATE TABLE backup_destinations (id TEXT PRIMARY KEY, name TEXT, kind TEXT, config TEXT,
	secret_enc TEXT DEFAULT '', is_default BOOLEAN DEFAULT 0, created_at DATETIME, updated_at DATETIME)`

func newDestinationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:dest-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	for _, stmt := range []string{
		`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`,
		backupDestinationsTable,
		`CREATE TABLE IF NOT EXISTS backups (id TEXT PRIMARY KEY, vm_id TEXT, storage_provider TEXT, storage_path TEXT, backup_type TEXT, status TEXT, size INTEGER DEFAULT 0, compression TEXT, checksum TEXT, error_message TEXT, started_at DATETIME, completed_at DATETIME, created_at DATETIME, updated_at DATETIME, deleted_at DATETIME, disks TEXT DEFAULT '[]', consistency TEXT DEFAULT '', destination_id TEXT, node_id TEXT, mode TEXT DEFAULT 'full', parent_id TEXT, chain_id TEXT, checkpoint TEXT DEFAULT '', mode_note TEXT DEFAULT '')`,
	} {
		require.NoError(t, db.Exec(stmt).Error)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	return db
}

func validDestination() BackupDestinationInput {
	return BackupDestinationInput{Endpoint: "s3.example.test", Region: "auto", Bucket: "vm-backups", PathPrefix: "/prod/", AccessKey: "AKIDEXAMPLE", SecretKey: "s3cr3t-value"}
}

func testHostKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	k, err := ssh.NewPublicKey(pub)
	require.NoError(t, err)
	return storage.HostKeyLine(k)
}

func validSFTP(t *testing.T) BackupDestinationInput {
	return BackupDestinationInput{Kind: "sftp", Name: "Storage box", Host: "backup.example.test", User: "u123", Password: "box-pass", HostKey: testHostKey(t), Path: "maburvm/"}
}

func createDest(t *testing.T, d *BackupDestinations, in BackupDestinationInput) *BackupDestinationView {
	t.Helper()
	v, err := d.Create(context.Background(), in)
	require.NoError(t, err)
	return v
}

// Secrets go in and never come back out: not in what an administrator is
// shown, and not in clear in the database.
func TestBackupDestinationSecretIsNeverReturned(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	d := NewBackupDestinations(db, nil, testAESKey)

	store, _, err := d.ObjectStore(ctx, "")
	require.NoError(t, err)
	require.Nil(t, store, "nothing saved means nothing is sent, and the node uses its own keys")

	s3 := createDest(t, d, validDestination())
	require.True(t, s3.IsDefault, "the first destination is the default")
	box := createDest(t, d, validSFTP(t))
	require.False(t, box.IsDefault)

	list, err := d.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, s3.ID, list[0].ID, "the default comes first")
	require.True(t, list[0].HasSecret && list[1].HasSecret)
	require.Equal(t, "prod", list[0].PathPrefix)
	require.Equal(t, "maburvm", list[1].Path)
	require.True(t, strings.HasPrefix(list[1].HostKeyFingerprint, "SHA256:"))
	shown, err := json.Marshal(list)
	require.NoError(t, err)
	for _, secret := range []string{"s3cr3t-value", "box-pass", `"secret_key":`, `"password":`, `"private_key":`} {
		require.NotContains(t, string(shown), secret)
	}
	var raw []string
	require.NoError(t, db.Raw(`SELECT config || secret_enc FROM backup_destinations`).Scan(&raw).Error)
	require.NotContains(t, strings.Join(raw, ""), "s3cr3t-value", "stored in clear")
	require.NotContains(t, strings.Join(raw, ""), "box-pass", "stored in clear")

	// The nodes, and only they, are given the secret.
	store, id, err := d.ObjectStore(ctx, "")
	require.NoError(t, err)
	require.Equal(t, s3.ID, id)
	require.Equal(t, "s3cr3t-value", store.GetSecretKey())
	require.Equal(t, "vm-backups", store.GetBucket())
	r, err := d.Resolve(ctx, s3.ID)
	require.NoError(t, err)
	require.Equal(t, "prod/backups/a", r.Key("backups/a"))

	store, _, err = d.ObjectStore(ctx, box.ID)
	require.NoError(t, err)
	require.Equal(t, "sftp", store.GetKind())
	require.Equal(t, "box-pass", store.GetSftp().GetPassword())
	require.Equal(t, int32(22), store.GetSftp().GetPort())

	_, err = d.Resolve(ctx, "00000000-0000-4000-8000-000000000000")
	require.ErrorIs(t, err, ErrBackupDestinationNotFound)
}

func TestBackupDestinationBlankSecretKeepsTheSavedOne(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	d := NewBackupDestinations(db, nil, testAESKey)
	s3 := createDest(t, d, validDestination())

	// What the form sends back: everything but the key it was never given.
	in := validDestination()
	in.SecretKey, in.Region = "", "eu-1"
	_, err := d.Update(ctx, s3.ID, in)
	require.NoError(t, err)
	store, _, err := d.ObjectStore(ctx, s3.ID)
	require.NoError(t, err)
	require.Equal(t, "s3cr3t-value", store.GetSecretKey())
	require.Equal(t, "eu-1", store.GetRegion())

	// Pointing the saved key at another server or bucket needs the key again.
	type change struct {
		name string
		fn   func(*BackupDestinationInput)
	}
	for _, c := range []change{
		{"endpoint", func(i *BackupDestinationInput) { i.Endpoint = "collector.example.net" }},
		{"bucket", func(i *BackupDestinationInput) { i.Bucket = "somebody-elses" }},
		{"tls off", func(i *BackupDestinationInput) { off := false; i.UseTLS = &off }},
	} {
		in := validDestination()
		in.SecretKey = ""
		c.fn(&in)
		_, err := d.Update(ctx, s3.ID, in)
		require.ErrorIs(t, err, ErrBackupDestinationInvalid, c.name)
	}
	store, _, _ = d.ObjectStore(ctx, s3.ID)
	require.Equal(t, "s3.example.test", store.GetEndpoint(), "a refused save changed nothing")

	// The same for an SFTP server: its password goes nowhere new.
	sftpIn := validSFTP(t)
	box := createDest(t, d, sftpIn)
	sftpIn.Password = ""
	_, err = d.Update(ctx, box.ID, sftpIn)
	require.NoError(t, err)
	for _, c := range []change{
		{"host", func(i *BackupDestinationInput) { i.Host = "elsewhere.example.net" }},
		{"host key", func(i *BackupDestinationInput) { i.HostKey = testHostKey(t) }},
		{"user", func(i *BackupDestinationInput) { i.User = "other" }},
	} {
		in := sftpIn
		c.fn(&in)
		_, err := d.Update(ctx, box.ID, in)
		require.ErrorIs(t, err, ErrBackupDestinationInvalid, c.name)
	}
	// A kind does not change in place.
	_, err = d.Update(ctx, box.ID, validDestination())
	require.ErrorIs(t, err, ErrBackupDestinationInvalid)
}

func TestBackupDestinationValidation(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	d := NewBackupDestinations(db, nil, testAESKey)
	type change struct {
		name string
		in   BackupDestinationInput
	}
	s3 := func(fn func(*BackupDestinationInput)) BackupDestinationInput {
		in := validDestination()
		fn(&in)
		return in
	}
	sf := func(fn func(*BackupDestinationInput)) BackupDestinationInput { in := validSFTP(t); fn(&in); return in }
	for _, c := range []change{
		{"no bucket", s3(func(i *BackupDestinationInput) { i.Bucket = "" })},
		{"bucket is a path", s3(func(i *BackupDestinationInput) { i.Bucket = "a/b" })},
		{"no access key", s3(func(i *BackupDestinationInput) { i.AccessKey = "" })},
		{"no secret key", s3(func(i *BackupDestinationInput) { i.SecretKey = "" })},
		{"endpoint path", s3(func(i *BackupDestinationInput) { i.Endpoint = "s3.example.test/bucket" })},
		{"endpoint scheme", s3(func(i *BackupDestinationInput) { i.Endpoint = "ftp://s3.example.test" })},
		{"endpoint creds", s3(func(i *BackupDestinationInput) { i.Endpoint = "user:pw@s3.example.test" })},
		{"prefix escapes", s3(func(i *BackupDestinationInput) { i.PathPrefix = "../other" })},
		{"sftp no host key", sf(func(i *BackupDestinationInput) { i.HostKey = "" })},
		{"sftp bad host key", sf(func(i *BackupDestinationInput) { i.HostKey = "trust me" })},
		{"sftp no password", sf(func(i *BackupDestinationInput) { i.Password = "" })},
		{"sftp bad key", sf(func(i *BackupDestinationInput) { i.Password, i.PrivateKey = "", "not a key" })},
		{"sftp no user", sf(func(i *BackupDestinationInput) { i.User = "" })},
		{"sftp path escapes", sf(func(i *BackupDestinationInput) { i.Path = "../../etc" })},
		{"path relative", BackupDestinationInput{Kind: "path", Path: "mnt/backups"}},
		{"path root", BackupDestinationInput{Kind: "path", Path: "/"}},
		{"path escapes", BackupDestinationInput{Kind: "path", Path: "/mnt/../etc"}},
		{"unknown kind", BackupDestinationInput{Kind: "ftp"}},
	} {
		_, err := d.Create(ctx, c.in)
		require.ErrorIs(t, err, ErrBackupDestinationInvalid, c.name)
	}
	list, err := d.List(ctx)
	require.NoError(t, err)
	require.Empty(t, list)

	// A scheme typed with the endpoint decides TLS, and is stored once.
	in := validDestination()
	in.Endpoint = "http://minio.internal.example.test:9000/"
	v := createDest(t, d, in)
	require.Equal(t, "minio.internal.example.test:9000", v.Endpoint)
	require.False(t, v.UseTLS)
	store, _, err := d.ObjectStore(ctx, "")
	require.NoError(t, err)
	require.True(t, store.GetPlainHttp())

	// A mounted directory needs no secret.
	p := createDest(t, d, BackupDestinationInput{Kind: "path", Path: "/mnt/backups/"})
	require.Equal(t, "/mnt/backups", p.Name)
	store, _, err = d.ObjectStore(ctx, p.ID)
	require.NoError(t, err)
	require.Equal(t, "path", store.GetKind())
	require.Equal(t, "/mnt/backups", store.GetPath())
}

func TestBackupDestinationDefaultAndRemoval(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	d := NewBackupDestinations(db, nil, testAESKey)
	a := createDest(t, d, validDestination())
	b := createDest(t, d, BackupDestinationInput{Kind: "path", Path: "/mnt/backups"})

	require.ErrorIs(t, d.Delete(ctx, a.ID), ErrBackupDestinationInvalid, "the default goes last")
	require.NoError(t, d.SetDefault(ctx, b.ID))
	_, id, err := d.ObjectStore(ctx, "")
	require.NoError(t, err)
	require.Equal(t, b.ID, id)

	// Backups kept there hold it; failed ones do not.
	require.NoError(t, db.Exec(`INSERT INTO backups (id, vm_id, status, size, destination_id) VALUES ('b1', 'vm', 'completed', 10, ?), ('b2', 'vm', 'failed', 0, ?)`, a.ID, a.ID).Error)
	require.ErrorIs(t, d.Delete(ctx, a.ID), ErrBackupDestinationInUse)

	// Its bucket cannot move from under the backups kept in it; its keys can change.
	moved := validDestination()
	moved.Bucket = "other-bucket"
	_, err = d.Update(ctx, a.ID, moved)
	require.ErrorIs(t, err, ErrBackupDestinationInvalid)
	rekeyed := validDestination()
	rekeyed.SecretKey = "rotated"
	_, err = d.Update(ctx, a.ID, rekeyed)
	require.NoError(t, err)
	// An empty destination may point anywhere.
	_, err = d.Update(ctx, b.ID, BackupDestinationInput{Path: "/mnt/other"})
	require.NoError(t, err)
	list, err := d.List(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), list[1].Backups)
	require.Equal(t, int64(10), list[1].BackupBytes)

	require.NoError(t, db.Exec(`UPDATE backups SET deleted_at = CURRENT_TIMESTAMP WHERE id = 'b1'`).Error)
	require.NoError(t, d.Delete(ctx, a.ID))
	require.NoError(t, d.Delete(ctx, b.ID), "the last one may go, default or not")
}

// After the panel's AES key is lost, a saved destination must be reported as
// unreadable. Sending nothing would move backups to each node's own storage
// without anyone having chosen that.
func TestBackupDestinationUnreadableIsAnErrorNotAFallback(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	createDest(t, NewBackupDestinations(db, nil, testAESKey), validDestination())

	rotated := NewBackupDestinations(db, nil, "ffffffffffffffffffffffffffffffff")
	_, _, err := rotated.ObjectStore(ctx, "")
	require.ErrorIs(t, err, ErrBackupDestinationUnreadable)

	// The old key, kept as a previous key, still reads it.
	withPrev := NewBackupDestinations(db, nil, "ffffffffffffffffffffffffffffffff", testAESKey)
	store, _, err := withPrev.ObjectStore(ctx, "")
	require.NoError(t, err)
	require.Equal(t, "s3cr3t-value", store.GetSecretKey())
}

// A table that cannot be read is not "no destination": saying so would send
// backups to each node's own keys, or fail them for good.
func TestBackupDestinationReadFailureIsNotNoDestination(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	d := NewBackupDestinations(db, nil, testAESKey)
	createDest(t, d, validDestination())
	d.invalidate()
	require.NoError(t, db.Exec(`ALTER TABLE backup_destinations RENAME TO backup_destinations_gone`).Error)

	store, _, err := d.ObjectStore(ctx, "")
	require.Error(t, err)
	require.Nil(t, store)

	// Not remembered: once the table answers again, so does the destination.
	require.NoError(t, db.Exec(`ALTER TABLE backup_destinations_gone RENAME TO backup_destinations`).Error)
	store, _, err = d.ObjectStore(ctx, "")
	require.NoError(t, err)
	require.Equal(t, "vm-backups", store.GetBucket())
}

// storeAgent answers TestObjectStore the way a node does, and says which
// features it has.
type storeAgent struct {
	pb.UnimplementedNodeAgentServer
	fail     string
	asked    *pb.TestObjectStoreRequest
	features []string
}

func (a *storeAgent) TestObjectStore(_ context.Context, req *pb.TestObjectStoreRequest) (*pb.TestObjectStoreResponse, error) {
	a.asked = req
	if a.fail != "" {
		return &pb.TestObjectStoreResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED, Message: a.fail}}, nil
	}
	return &pb.TestObjectStoreResponse{Success: true}, nil
}

func (a *storeAgent) GetNodeInfo(context.Context, *pb.GetNodeInfoRequest) (*pb.GetNodeInfoResponse, error) {
	return &pb.GetNodeInfoResponse{Success: true, Features: a.features}, nil
}

func dialerFor(agents map[string]pb.NodeAgentClient) AgentDialer {
	return func(ctx context.Context, nodeID string) (pb.NodeAgentClient, context.Context, error) {
		c, ok := agents[nodeID]
		if !ok {
			return nil, nil, errors.New("connection refused")
		}
		return c, ctx, nil
	}
}

func TestBackupDestinationTestFromNodes(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	good, bad := &storeAgent{}, &storeAgent{fail: "test failed: could not write a test object: AccessDenied"}
	d := NewBackupDestinations(db, dialerFor(map[string]pb.NodeAgentClient{
		"n-good": agentClient(t, good),
		"n-bad":  agentClient(t, bad),
		"n-old":  agentClient(t, olderAgent{}),
	}), testAESKey)

	in := validDestination()
	in.Endpoint = "http://127.0.0.1:1" // nothing listens: the panel's own test fails fast
	nodes := []models.Node{{ID: "n-good", Name: "good"}, {ID: "n-bad", Name: "bad"}, {ID: "n-old", Name: "old"}, {ID: "n-down", Name: "down"}}
	results, err := d.Test(ctx, "", in, nodes)
	require.NoError(t, err)
	require.Len(t, results, 5)

	require.Equal(t, "Panel", results[0].Name)
	require.Equal(t, DestinationTestFailed, results[0].Status)
	require.Equal(t, DestinationTestOK, results[1].Status)
	require.Equal(t, DestinationTestFailed, results[2].Status)
	require.Contains(t, results[2].Message, "AccessDenied")
	// An agent without the call is not a broken destination, and not "ok".
	require.Equal(t, DestinationTestNeedsUpdate, results[3].Status)
	require.Equal(t, DestinationTestUnreachable, results[4].Status)

	// The node is tested with what was typed, under the path prefix, unsaved.
	require.Equal(t, "s3cr3t-value", good.asked.GetObjectStore().GetSecretKey())
	require.True(t, good.asked.GetObjectStore().GetPlainHttp())
	require.True(t, strings.HasPrefix(good.asked.GetKey(), "prod/.destination-test-"), good.asked.GetKey())
	list, _ := d.List(ctx)
	require.Empty(t, list, "testing saved the destination")

	_, err = d.Test(ctx, "", BackupDestinationInput{Bucket: "b"}, nil)
	require.ErrorIs(t, err, ErrBackupDestinationInvalid)
}

// An agent older than SFTP and mounted directories would ignore the kind and
// test its own store: it must be reported as needing an update, not "ok".
func TestBackupDestinationTestNewKindsNeedNewAgents(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	current := &storeAgent{features: []string{"restore_disks", featureBackupTargets}}
	previous := &storeAgent{features: []string{"restore_disks"}}
	d := NewBackupDestinations(db, dialerFor(map[string]pb.NodeAgentClient{
		"n-new": agentClient(t, current),
		"n-old": agentClient(t, previous),
	}), testAESKey)
	nodes := []models.Node{{ID: "n-new", Name: "new"}, {ID: "n-old", Name: "old"}}

	results, err := d.Test(ctx, "", BackupDestinationInput{Kind: "path", Path: "/mnt/backups"}, nodes)
	require.NoError(t, err)
	require.Equal(t, DestinationTestSkipped, results[0].Status, "the panel mounts nothing")
	require.Equal(t, DestinationTestOK, results[1].Status)
	require.Equal(t, DestinationTestNeedsUpdate, results[2].Status)
	require.Nil(t, previous.asked, "the old agent was never sent the destination")
	require.Equal(t, "/mnt/backups", current.asked.GetObjectStore().GetPath())

	// Editing a saved SFTP destination tests it with the saved password.
	box := createDest(t, d, validSFTP(t))
	in := validSFTP(t)
	in.Password, in.HostKey = "", box.HostKey
	_, err = d.Test(ctx, box.ID, in, nodes[:1])
	require.NoError(t, err)
	require.Equal(t, "sftp", current.asked.GetObjectStore().GetKind())
	require.Equal(t, "box-pass", current.asked.GetObjectStore().GetSftp().GetPassword())
}

// deleteAgent deletes the files it holds and reports the rest missing.
type deleteAgent struct {
	pb.UnimplementedNodeAgentServer
	holds map[string]bool
}

func (a *deleteAgent) GetNodeInfo(context.Context, *pb.GetNodeInfoRequest) (*pb.GetNodeInfoResponse, error) {
	return &pb.GetNodeInfoResponse{Success: true, Features: []string{featureBackupTargets}}, nil
}

func (a *deleteAgent) DeleteBackupFiles(_ context.Context, req *pb.DeleteBackupFilesRequest) (*pb.DeleteBackupFilesResponse, error) {
	out := &pb.DeleteBackupFilesResponse{}
	for _, k := range req.GetKeys() {
		if a.holds[k] {
			delete(a.holds, k)
			out.Deleted = append(out.Deleted, k)
		} else {
			out.Missing = append(out.Missing, k)
		}
	}
	return out, nil
}

// A disk mounted on one node holds that node's backups only: another node not
// finding them must not count as their being gone.
func TestDeleteOnNodesBelievesOnlyTheWriter(t *testing.T) {
	ctx := context.Background()
	key := "backups/vm/a.qcow2"
	writer, other := &deleteAgent{holds: map[string]bool{key: true}}, &deleteAgent{holds: map[string]bool{}}
	agents := map[string]pb.NodeAgentClient{"n-other": agentClient(t, other)}
	d := NewBackupDestinations(newDestinationTestDB(t), dialerFor(agents), testAESKey)
	r := &ResolvedDestination{Kind: storage.KindPath, Target: storage.Target{Kind: storage.KindPath, Path: "/mnt/backups"}}

	// The writer is down: the other node's "not found" is an error, not a delete.
	_, err := d.DeleteOnNodes(ctx, r, "n-writer", []string{"n-other"}, []string{key})
	require.ErrorContains(t, err, "did not answer")

	// Back up: it deletes the file.
	agents["n-writer"] = agentClient(t, writer)
	missing, err := d.DeleteOnNodes(ctx, r, "n-writer", []string{"n-other"}, []string{key})
	require.NoError(t, err)
	require.Empty(t, missing)
	require.Empty(t, writer.holds)

	// The writer removed from the panel: the VM's node is believed.
	missing, err = d.DeleteOnNodes(ctx, r, "", []string{"n-other"}, []string{key})
	require.NoError(t, err)
	require.Equal(t, []string{key}, missing)
}
