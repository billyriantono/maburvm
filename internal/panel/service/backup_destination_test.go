package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
)

const testAESKey = "0123456789abcdef0123456789abcdef"

func destinationStatus(t *testing.T, d *BackupDestinations) BackupDestinationStatus {
	t.Helper()
	st, err := d.Status(context.Background())
	require.NoError(t, err)
	return st
}

func newDestinationTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:dest-%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE system_settings (section TEXT PRIMARY KEY, data TEXT, updated_at DATETIME)`).Error)
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

// The secret key goes in and never comes back out: not in what an
// administrator is shown, and not in clear in the database.
func TestBackupDestinationSecretIsNeverReturned(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	d := NewBackupDestinations(db, nil, testAESKey)

	require.False(t, destinationStatus(t, d).Configured)
	require.NoError(t, d.Save(ctx, validDestination()))

	st := destinationStatus(t, d)
	require.True(t, st.Configured)
	require.True(t, st.HasSecretKey)
	require.True(t, st.UseTLS)
	require.Equal(t, "prod", st.PathPrefix)
	shown, err := json.Marshal(st)
	require.NoError(t, err)
	require.NotContains(t, string(shown), "s3cr3t-value")
	require.NotContains(t, string(shown), `"secret_key"`, "the response has no secret key field at all")

	var raw string
	require.NoError(t, db.Raw(`SELECT data FROM system_settings WHERE section = ?`, BackupDestinationSection).Scan(&raw).Error)
	require.NotContains(t, raw, "s3cr3t-value", "stored in clear")

	// The nodes, and only they, are given the key.
	store, err := d.ObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, "s3cr3t-value", store.GetSecretKey())
	require.Equal(t, "vm-backups", store.GetBucket())
	require.False(t, store.GetPlainHttp())
	require.Equal(t, "prod/backups/a", d.Key(ctx, "backups/a"))
}

func TestBackupDestinationBlankSecretKeepsTheSavedOne(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	d := NewBackupDestinations(db, nil, testAESKey)
	require.NoError(t, d.Save(ctx, validDestination()))

	// What the form sends back: everything but the key it was never given.
	in := validDestination()
	in.SecretKey, in.Region = "", "eu-1"
	require.NoError(t, d.Save(ctx, in))
	store, err := d.ObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, "s3cr3t-value", store.GetSecretKey())
	require.Equal(t, "eu-1", store.GetRegion())

	// Pointing the saved key at another server or bucket needs the key again.
	for name, change := range map[string]func(*BackupDestinationInput){
		"endpoint": func(i *BackupDestinationInput) { i.Endpoint = "collector.example.net" },
		"bucket":   func(i *BackupDestinationInput) { i.Bucket = "somebody-elses" },
		"tls off":  func(i *BackupDestinationInput) { off := false; i.UseTLS = &off },
	} {
		in := validDestination()
		in.SecretKey = ""
		change(&in)
		require.ErrorIs(t, d.Save(ctx, in), ErrBackupDestinationInvalid, name)
	}
	store, _ = d.ObjectStore(ctx)
	require.Equal(t, "s3.example.test", store.GetEndpoint(), "a refused save changed nothing")
}

func TestBackupDestinationValidation(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	d := NewBackupDestinations(db, nil, testAESKey)
	for name, change := range map[string]func(*BackupDestinationInput){
		"no bucket":        func(i *BackupDestinationInput) { i.Bucket = "" },
		"bucket is a path": func(i *BackupDestinationInput) { i.Bucket = "a/b" },
		"no access key":    func(i *BackupDestinationInput) { i.AccessKey = "" },
		"no secret key":    func(i *BackupDestinationInput) { i.SecretKey = "" },
		"endpoint path":    func(i *BackupDestinationInput) { i.Endpoint = "s3.example.test/bucket" },
		"endpoint scheme":  func(i *BackupDestinationInput) { i.Endpoint = "ftp://s3.example.test" },
		"endpoint creds":   func(i *BackupDestinationInput) { i.Endpoint = "user:pw@s3.example.test" },
		"prefix escapes":   func(i *BackupDestinationInput) { i.PathPrefix = "../other" },
	} {
		in := validDestination()
		change(&in)
		require.ErrorIs(t, d.Save(ctx, in), ErrBackupDestinationInvalid, name)
	}
	require.False(t, destinationStatus(t, d).Configured)

	// A scheme typed with the endpoint decides TLS, and is stored once.
	in := validDestination()
	in.Endpoint = "http://minio.internal.example.test:9000/"
	require.NoError(t, d.Save(ctx, in))
	st := destinationStatus(t, d)
	require.Equal(t, "minio.internal.example.test:9000", st.Endpoint)
	require.False(t, st.UseTLS)
	store, err := d.ObjectStore(ctx)
	require.NoError(t, err)
	require.True(t, store.GetPlainHttp())

	require.NoError(t, d.Clear(ctx))
	store, err = d.ObjectStore(ctx)
	require.NoError(t, err)
	require.Nil(t, store, "nothing saved means nothing is sent, and the node uses its own keys")
}

// After the panel's AES key is lost, the saved destination must be reported as
// unreadable. Sending nothing would move backups to each node's own storage
// without anyone having chosen that.
func TestBackupDestinationUnreadableIsAnErrorNotAFallback(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	require.NoError(t, NewBackupDestinations(db, nil, testAESKey).Save(ctx, validDestination()))

	rotated := NewBackupDestinations(db, nil, "ffffffffffffffffffffffffffffffff")
	_, err := rotated.ObjectStore(ctx)
	require.ErrorIs(t, err, ErrBackupDestinationUnreadable)
	_, err = rotated.Client(ctx)
	require.ErrorIs(t, err, ErrBackupDestinationUnreadable)

	// The old key, kept as a previous key, still reads it.
	withPrev := NewBackupDestinations(db, nil, "ffffffffffffffffffffffffffffffff", testAESKey)
	store, err := withPrev.ObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, "s3cr3t-value", store.GetSecretKey())
}

// A settings table that cannot be read is not "no destination": saying so
// would send backups to each node's own keys, or fail them for good.
func TestBackupDestinationReadFailureIsNotNoDestination(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	d := NewBackupDestinations(db, nil, testAESKey)
	require.NoError(t, d.Save(ctx, validDestination()))
	d.invalidate()
	require.NoError(t, db.Exec(`ALTER TABLE system_settings RENAME TO system_settings_gone`).Error)

	store, err := d.ObjectStore(ctx)
	require.Error(t, err)
	require.Nil(t, store)
	_, err = d.Status(ctx)
	require.Error(t, err)

	// Not remembered: once the table answers again, so does the destination.
	require.NoError(t, db.Exec(`ALTER TABLE system_settings_gone RENAME TO system_settings`).Error)
	store, err = d.ObjectStore(ctx)
	require.NoError(t, err)
	require.Equal(t, "vm-backups", store.GetBucket())
}

// storeAgent answers TestObjectStore the way a node does.
type storeAgent struct {
	pb.UnimplementedNodeAgentServer
	fail  string
	asked *pb.TestObjectStoreRequest
}

func (a *storeAgent) TestObjectStore(_ context.Context, req *pb.TestObjectStoreRequest) (*pb.TestObjectStoreResponse, error) {
	a.asked = req
	if a.fail != "" {
		return &pb.TestObjectStoreResponse{Error: &pb.ErrorResponse{Code: pb.ErrorCode_ERROR_CODE_PERMISSION_DENIED, Message: a.fail}}, nil
	}
	return &pb.TestObjectStoreResponse{Success: true}, nil
}

func TestBackupDestinationTestFromNodes(t *testing.T) {
	db := newDestinationTestDB(t)
	ctx := context.Background()
	good, bad := &storeAgent{}, &storeAgent{fail: "test failed: could not write a test object: AccessDenied"}
	agents := map[string]pb.NodeAgentClient{
		"n-good": agentClient(t, good),
		"n-bad":  agentClient(t, bad),
		"n-old":  agentClient(t, olderAgent{}),
	}
	d := NewBackupDestinations(db, func(ctx context.Context, nodeID string) (pb.NodeAgentClient, context.Context, error) {
		c, ok := agents[nodeID]
		if !ok {
			return nil, nil, errors.New("connection refused")
		}
		return c, ctx, nil
	}, testAESKey)

	in := validDestination()
	in.Endpoint = "http://127.0.0.1:1" // nothing listens: the panel's own test fails fast
	nodes := []models.Node{{ID: "n-good", Name: "good"}, {ID: "n-bad", Name: "bad"}, {ID: "n-old", Name: "old"}, {ID: "n-down", Name: "down"}}
	results, err := d.Test(ctx, in, nodes)
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
	require.False(t, destinationStatus(t, d).Configured, "testing saved the destination")

	_, err = d.Test(ctx, BackupDestinationInput{Bucket: "b"}, nil)
	require.ErrorIs(t, err, ErrBackupDestinationInvalid)
}
