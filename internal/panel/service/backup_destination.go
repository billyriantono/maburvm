package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/storage"
)

// BackupDestinationSection is the system_settings row holding where backups
// are kept. Like the nameserver connection it holds a credential, so it is
// managed only through BackupDestinations: the generic settings endpoints
// neither return nor accept it.
const BackupDestinationSection = "backup_destination"

var (
	// ErrBackupDestinationInvalid wraps every reason a destination is refused.
	ErrBackupDestinationInvalid = errors.New("invalid backup destination")
	// ErrBackupDestinationUnreadable is returned when a destination is saved
	// but its secret key cannot be decrypted with the panel's current key.
	ErrBackupDestinationUnreadable = errors.New("the saved backup destination can no longer be read; save it again with its secret key")
)

// BackupDestinationInput is what an administrator types.
type BackupDestinationInput struct {
	Endpoint   string `json:"endpoint"`
	Region     string `json:"region"`
	Bucket     string `json:"bucket"`
	PathPrefix string `json:"path_prefix"`
	AccessKey  string `json:"access_key"`
	// SecretKey left empty means "the one already saved".
	SecretKey string `json:"secret_key"`
	UseTLS    *bool  `json:"use_tls"`
}

// BackupDestinationStatus is what an administrator may see of the saved
// destination. The secret key is never part of it, only whether one is stored.
type BackupDestinationStatus struct {
	Configured   bool   `json:"configured"`
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region"`
	Bucket       string `json:"bucket"`
	PathPrefix   string `json:"path_prefix"`
	AccessKey    string `json:"access_key"`
	HasSecretKey bool   `json:"has_secret_key"`
	UseTLS       bool   `json:"use_tls"`
}

type storedBackupDestination struct {
	Endpoint     string `json:"endpoint"`
	Region       string `json:"region"`
	Bucket       string `json:"bucket"`
	PathPrefix   string `json:"path_prefix"`
	AccessKey    string `json:"access_key"`
	SecretKeyEnc string `json:"secret_key_enc"`
	PlainHTTP    bool   `json:"plain_http"`
}

// AgentDialer returns a client for a node's agent and the context that
// authenticates to it.
type AgentDialer func(ctx context.Context, nodeID string) (pb.NodeAgentClient, context.Context, error)

// BackupDestinations is the S3-compatible store backups are kept in, as set by
// an administrator. The panel uses it to delete backups; nodes are sent it with
// each backup and restore, so they need no storage keys of their own.
//
// The secret key is stored encrypted with the panel's AES key, which lives
// outside the database: a database dump alone does not yield credentials for
// every customer's backups.
type BackupDestinations struct {
	db       *gorm.DB
	key      []byte
	prevKeys [][]byte
	agent    AgentDialer

	mu       sync.Mutex
	cached   *resolvedBackupDestination
	cachedAt time.Time
}

type resolvedBackupDestination struct {
	dest   storage.Destination
	prefix string
	err    error
}

// NewBackupDestinations builds the store. previousAESKeys are retired keys
// still accepted for decryption. agent may be nil: testing a node then reports
// that the node could not be reached.
func NewBackupDestinations(db *gorm.DB, agent AgentDialer, aesKey string, previousAESKeys ...string) *BackupDestinations {
	d := &BackupDestinations{db: db, key: []byte(aesKey), agent: agent}
	for _, k := range previousAESKeys {
		d.prevKeys = append(d.prevKeys, []byte(k))
	}
	return d
}

// load reads the saved destination; ok is false when none is saved. A failed
// read is an error, never "none saved": that would send backups to whatever
// each node has in its own environment file.
func (d *BackupDestinations) load(ctx context.Context) (s storedBackupDestination, ok bool, err error) {
	var raw string
	if err := d.db.WithContext(ctx).
		Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", BackupDestinationSection).
		Scan(&raw).Error; err != nil {
		return storedBackupDestination{}, false, fmt.Errorf("could not read the backup destination: %w", err)
	}
	if raw == "" || json.Unmarshal([]byte(raw), &s) != nil || s.Bucket == "" {
		return storedBackupDestination{}, false, nil
	}
	return s, true, nil
}

func (d *BackupDestinations) decrypt(enc string) (string, error) {
	plain, err := decryptWithKey(d.key, enc)
	for _, k := range d.prevKeys {
		if err == nil {
			break
		}
		plain, err = decryptWithKey(k, enc)
	}
	return plain, err
}

// Status reports the saved destination, without its secret key.
func (d *BackupDestinations) Status(ctx context.Context) (BackupDestinationStatus, error) {
	s, ok, err := d.load(ctx)
	if err != nil || !ok {
		return BackupDestinationStatus{UseTLS: true}, err
	}
	return BackupDestinationStatus{
		Configured: true, Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, PathPrefix: s.PathPrefix,
		AccessKey: s.AccessKey, HasSecretKey: s.SecretKeyEnc != "", UseTLS: !s.PlainHTTP,
	}, nil
}

// resolve returns the destination to use right now; ok is false when none is
// saved. Cached briefly so a burst of backups does not read and decrypt the
// setting each time; Save and Clear drop the cache.
func (d *BackupDestinations) resolve(ctx context.Context) (dest storage.Destination, prefix string, ok bool, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cached == nil || time.Since(d.cachedAt) > 30*time.Second {
		r := &resolvedBackupDestination{}
		s, saved, lerr := d.load(ctx)
		if lerr != nil {
			return storage.Destination{}, "", false, lerr // not cached: the next call reads again
		}
		if saved {
			secret, derr := d.decrypt(s.SecretKeyEnc)
			if derr != nil || secret == "" {
				// A key that cannot be read must not fall back silently to
				// whatever each node has in its own environment file.
				r.err = ErrBackupDestinationUnreadable
			}
			r.dest = storage.Destination{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket,
				AccessKey: s.AccessKey, SecretKey: secret, PlainHTTP: s.PlainHTTP}
			r.prefix = s.PathPrefix
		}
		d.cached, d.cachedAt = r, time.Now()
	}
	return d.cached.dest, d.cached.prefix, d.cached.dest.Bucket != "", d.cached.err
}

func (d *BackupDestinations) invalidate() {
	d.mu.Lock()
	d.cached = nil
	d.mu.Unlock()
}

// Client returns a client for the saved destination, or nil when none is saved.
func (d *BackupDestinations) Client(ctx context.Context) (*storage.Client, error) {
	dest, _, ok, err := d.resolve(ctx)
	if err != nil || !ok {
		return nil, err
	}
	return dest.Client()
}

// ObjectStore returns the saved destination in the form a node is sent, or nil
// when none is saved.
func (d *BackupDestinations) ObjectStore(ctx context.Context) (*pb.ObjectStore, error) {
	dest, _, ok, err := d.resolve(ctx)
	if err != nil || !ok {
		return nil, err
	}
	return objectStoreProto(dest), nil
}

func objectStoreProto(dest storage.Destination) *pb.ObjectStore {
	return &pb.ObjectStore{Endpoint: dest.Endpoint, Region: dest.Region, Bucket: dest.Bucket,
		AccessKey: dest.AccessKey, SecretKey: dest.SecretKey, PlainHttp: dest.PlainHTTP}
}

// Key puts an object key under the destination's path prefix. The result is
// stored on the backup, so a later change of prefix does not lose older ones.
func (d *BackupDestinations) Key(ctx context.Context, key string) string {
	if d == nil {
		return key
	}
	_, prefix, _, _ := d.resolve(ctx)
	return storage.JoinKey(prefix, key)
}

// normalize validates what was typed and resolves which secret key it means.
// An empty secret key keeps the saved one, which is only allowed while the
// endpoint and bucket are unchanged: otherwise anyone able to edit them could
// have the panel present the saved key to a server of their choosing.
func (d *BackupDestinations) normalize(ctx context.Context, in BackupDestinationInput) (storedBackupDestination, string, error) {
	invalid := func(msg string) (storedBackupDestination, string, error) {
		return storedBackupDestination{}, "", fmt.Errorf("%w: %s", ErrBackupDestinationInvalid, msg)
	}
	s := storedBackupDestination{
		Endpoint:   strings.TrimSuffix(strings.TrimSpace(in.Endpoint), "/"),
		Region:     strings.TrimSpace(in.Region),
		Bucket:     strings.TrimSpace(in.Bucket),
		PathPrefix: strings.Trim(strings.TrimSpace(in.PathPrefix), "/"),
		AccessKey:  strings.TrimSpace(in.AccessKey),
		PlainHTTP:  in.UseTLS != nil && !*in.UseTLS,
	}
	// A scheme typed with the endpoint is the clearer statement of intent, so
	// it decides TLS and is then dropped: one fact, kept in one place.
	lower := strings.ToLower(s.Endpoint)
	switch {
	case strings.HasPrefix(lower, "https://"):
		s.Endpoint, s.PlainHTTP = s.Endpoint[len("https://"):], false
	case strings.HasPrefix(lower, "http://"):
		s.Endpoint, s.PlainHTTP = s.Endpoint[len("http://"):], true
	case strings.Contains(lower, "://"):
		return invalid("the endpoint must be a host name, with a port if it needs one")
	}
	if strings.ContainsAny(s.Endpoint, "/ @") {
		return invalid("the endpoint must be a host name, with a port if it needs one")
	}
	if s.Bucket == "" {
		return invalid("the bucket is required")
	}
	if strings.ContainsAny(s.Bucket, "/ ") {
		return invalid("the bucket is a name, not a path; put folders in the path prefix")
	}
	if s.AccessKey == "" {
		return invalid("the access key is required")
	}
	if len(s.PathPrefix) > 200 || strings.Contains(s.PathPrefix, "..") {
		return invalid("the path prefix is too long or contains '..'")
	}

	secret := strings.TrimSpace(in.SecretKey)
	if secret == "" {
		saved, ok, lerr := d.load(ctx)
		if lerr != nil {
			return storedBackupDestination{}, "", lerr
		}
		if !ok || saved.SecretKeyEnc == "" {
			return invalid("the secret key is required")
		}
		if saved.Endpoint != s.Endpoint || saved.Bucket != s.Bucket || saved.PlainHTTP != s.PlainHTTP {
			return invalid("the secret key is required when the endpoint, the bucket or TLS changes")
		}
		var err error
		if secret, err = d.decrypt(saved.SecretKeyEnc); err != nil || secret == "" {
			return storedBackupDestination{}, "", ErrBackupDestinationUnreadable
		}
	}
	dest := storage.Destination{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, AccessKey: s.AccessKey, SecretKey: secret, PlainHTTP: s.PlainHTTP}
	if _, err := dest.Client(); err != nil {
		return invalid(err.Error())
	}
	return s, secret, nil
}

// Save stores the destination.
func (d *BackupDestinations) Save(ctx context.Context, in BackupDestinationInput) error {
	s, secret, err := d.normalize(ctx, in)
	if err != nil {
		return err
	}
	if s.SecretKeyEnc, err = encryptWithKey(d.key, secret); err != nil {
		return fmt.Errorf("could not encrypt the secret key: %w", err)
	}
	data, _ := json.Marshal(s)
	if err := d.db.WithContext(ctx).Exec(
		"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP) "+
			"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = CURRENT_TIMESTAMP",
		BackupDestinationSection, string(data),
	).Error; err != nil {
		return err
	}
	d.invalidate()
	return nil
}

// Clear removes the saved destination. Backups already made stay where they
// are; nodes go back to the store in their own environment file, if any.
func (d *BackupDestinations) Clear(ctx context.Context) error {
	err := d.db.WithContext(ctx).Exec("DELETE FROM system_settings WHERE section = ?", BackupDestinationSection).Error
	d.invalidate()
	return err
}

// Outcomes of testing the destination from one place.
const (
	DestinationTestOK          = "ok"
	DestinationTestFailed      = "failed"
	DestinationTestNeedsUpdate = "needs_agent_update"
	DestinationTestUnreachable = "unreachable"
)

// BackupDestinationTestResult is how the test went from one place: the panel
// itself (NodeID empty), or one node.
type BackupDestinationTestResult struct {
	NodeID  string `json:"node_id,omitempty"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

const destinationTestTimeout = 45 * time.Second

// Test checks a destination, without saving it, from the panel and from each
// given node: connect, write a small object, read it back, delete it. The panel
// must reach the store to delete backups; each node must reach it to write and
// restore them, and the two are often on different networks.
func (d *BackupDestinations) Test(ctx context.Context, in BackupDestinationInput, nodes []models.Node) ([]BackupDestinationTestResult, error) {
	s, secret, err := d.normalize(ctx, in)
	if err != nil {
		return nil, err
	}
	dest := storage.Destination{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, AccessKey: s.AccessKey, SecretKey: secret, PlainHTTP: s.PlainHTTP}
	key := storage.JoinKey(s.PathPrefix, fmt.Sprintf(".destination-test-%d", time.Now().UnixNano()))

	results := make([]BackupDestinationTestResult, len(nodes)+1)
	var wg sync.WaitGroup
	wg.Add(len(nodes) + 1)
	go func() {
		defer wg.Done()
		results[0] = BackupDestinationTestResult{Name: "Panel", Status: DestinationTestOK}
		tctx, cancel := context.WithTimeout(ctx, destinationTestTimeout)
		defer cancel()
		client, err := dest.Client()
		if err == nil {
			err = client.Probe(tctx, key+"-panel")
		}
		if err != nil {
			results[0].Status, results[0].Message = DestinationTestFailed, err.Error()
		}
	}()
	for i := range nodes {
		go func(i int) {
			defer wg.Done()
			results[i+1] = d.testFromNode(ctx, &nodes[i], dest, key+"-"+nodes[i].ID)
		}(i)
	}
	wg.Wait()
	return results, nil
}

// testFromNode asks one node's agent to run the test. An agent built before
// the test existed answers Unimplemented, which is not a failure of the
// destination and is not reported as one.
func (d *BackupDestinations) testFromNode(ctx context.Context, node *models.Node, dest storage.Destination, key string) BackupDestinationTestResult {
	res := BackupDestinationTestResult{NodeID: node.ID, Name: node.Name, Status: DestinationTestOK}
	fail := func(status, msg string) BackupDestinationTestResult {
		res.Status, res.Message = status, msg
		return res
	}
	if d.agent == nil {
		return fail(DestinationTestUnreachable, "the panel has no connection to this node")
	}
	ctx, cancel := context.WithTimeout(ctx, destinationTestTimeout)
	defer cancel()
	client, authCtx, err := d.agent(ctx, node.ID)
	if err != nil {
		return fail(DestinationTestUnreachable, err.Error())
	}
	resp, err := client.TestObjectStore(authCtx, &pb.TestObjectStoreRequest{ObjectStore: objectStoreProto(dest), Key: key})
	switch {
	case status.Code(err) == codes.Unimplemented:
		return fail(DestinationTestNeedsUpdate, "this node's agent is older than the panel and cannot be given a destination; update it under Nodes")
	case err != nil:
		return fail(DestinationTestUnreachable, err.Error())
	case !resp.GetSuccess():
		msg := resp.GetError().GetMessage()
		if msg == "" {
			msg = "the node reported a failure without a reason"
		}
		return fail(DestinationTestFailed, msg)
	}
	return res
}

// CheckNode asks one node to prove it can use the storage a backup is in,
// before a restore creates anything there: the saved destination, or, for a
// backup kept in a node's own storage, the store in that node's environment
// file. An agent too old to answer is refused either way: it would keep the
// download on its root filesystem and not check it.
func (d *BackupDestinations) CheckNode(ctx context.Context, node *models.Node, inDestination bool) error {
	var dest storage.Destination // empty: the node uses its own
	key := fmt.Sprintf(".destination-test-%d-%s", time.Now().UnixNano(), node.ID)
	what := "read backups kept in a node's own storage"
	if inDestination {
		saved, prefix, ok, err := d.resolve(ctx)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("no backup destination is set")
		}
		dest, key, what = saved, storage.JoinKey(prefix, key), "use the backup destination"
	}
	res := d.testFromNode(ctx, node, dest, key)
	if res.Status != DestinationTestOK {
		return fmt.Errorf("node %s cannot %s: %s", node.Name, what, res.Message)
	}
	return nil
}
