package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/storage"
)

var (
	// ErrBackupDestinationInvalid wraps every reason a destination is refused.
	ErrBackupDestinationInvalid = errors.New("invalid backup destination")
	// ErrBackupDestinationUnreadable is returned when a destination's secret
	// cannot be decrypted with the panel's current key.
	ErrBackupDestinationUnreadable = errors.New("the saved backup destination can no longer be read; save it again with its secret")
	// ErrBackupDestinationNotFound is a destination ID that is not saved.
	ErrBackupDestinationNotFound = errors.New("backup destination not found")
	// ErrBackupDestinationInUse refuses removing a destination backups are in.
	ErrBackupDestinationInUse = errors.New("backups are kept in this destination")
	// ErrDestinationOnNodes: a mounted directory is reached through a node.
	ErrDestinationOnNodes = errors.New("this destination is a directory on the nodes; the panel reaches it through them")
)

// BackupDestinationSection is the system_settings row the one S3 destination
// was kept in before destinations had their own table (migration 082 copies
// it). It holds a credential, so the generic settings endpoints still neither
// return nor accept it.
const BackupDestinationSection = "backup_destination"

// Node agent features a destination or a backup needs.
const (
	featureBackupTargets     = "backup_targets"
	featureIncrementalBackup = "incremental_backup"
)

// backupDestinationRow is one row of backup_destinations.
type backupDestinationRow struct {
	ID        string `gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name      string
	Kind      string
	Config    string `gorm:"type:jsonb"`
	SecretEnc string
	IsDefault bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (backupDestinationRow) TableName() string { return "backup_destinations" }

// destConfig is the part of a destination that is not secret.
type destConfig struct {
	// S3
	Endpoint   string `json:"endpoint,omitempty"`
	Region     string `json:"region,omitempty"`
	Bucket     string `json:"bucket,omitempty"`
	PathPrefix string `json:"path_prefix,omitempty"`
	AccessKey  string `json:"access_key,omitempty"`
	PlainHTTP  bool   `json:"plain_http,omitempty"`
	// SFTP
	Host    string `json:"host,omitempty"`
	Port    int    `json:"port,omitempty"`
	User    string `json:"user,omitempty"`
	HostKey string `json:"host_key,omitempty"`
	Auth    string `json:"auth,omitempty"` // "password" or "key"
	// SFTP: the directory on the server. Path: the mounted directory.
	Path string `json:"path,omitempty"`
}

// BackupDestinationInput is what an administrator types.
type BackupDestinationInput struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // s3 (default), sftp, path
	// S3
	Endpoint   string `json:"endpoint"`
	Region     string `json:"region"`
	Bucket     string `json:"bucket"`
	PathPrefix string `json:"path_prefix"`
	AccessKey  string `json:"access_key"`
	// SecretKey left empty means "the one already saved".
	SecretKey string `json:"secret_key"`
	UseTLS    *bool  `json:"use_tls"`
	// SFTP. Password or PrivateKey left empty keeps the saved one.
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	Password   string `json:"password"`
	PrivateKey string `json:"private_key"`
	HostKey    string `json:"host_key"`
	Auth       string `json:"auth"`
	// Path: the SFTP server's directory, or the directory mounted on nodes.
	Path string `json:"path"`
}

// BackupDestinationView is what an administrator may see of a destination.
// Secrets are never part of it, only whether one is stored.
type BackupDestinationView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	IsDefault bool   `json:"is_default"`
	// S3
	Endpoint   string `json:"endpoint,omitempty"`
	Region     string `json:"region,omitempty"`
	Bucket     string `json:"bucket,omitempty"`
	PathPrefix string `json:"path_prefix,omitempty"`
	AccessKey  string `json:"access_key,omitempty"`
	UseTLS     bool   `json:"use_tls"`
	// SFTP
	Host               string `json:"host,omitempty"`
	Port               int    `json:"port,omitempty"`
	User               string `json:"user,omitempty"`
	HostKey            string `json:"host_key,omitempty"`
	HostKeyFingerprint string `json:"host_key_fingerprint,omitempty"`
	Auth               string `json:"auth,omitempty"`
	Path               string `json:"path,omitempty"`
	HasSecret          bool   `json:"has_secret"`
	// What is kept there now.
	Backups     int64     `json:"backups"`
	BackupBytes int64     `json:"backup_bytes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// AgentDialer returns a client for a node's agent and the context that
// authenticates to it.
type AgentDialer func(ctx context.Context, nodeID string) (pb.NodeAgentClient, context.Context, error)

// BackupDestinations are the places backups are kept, as set by an
// administrator: S3 buckets, SFTP servers, directories mounted on the nodes.
// One is the default, where new backups go; every backup records its own, so
// older ones stay restorable after the default changes. Nodes are sent the
// destination with each backup and restore, so they need no keys of their own.
//
// Secrets are stored encrypted with the panel's AES key, which lives outside
// the database: a dump alone does not yield credentials for every backup.
type BackupDestinations struct {
	db       *gorm.DB
	key      []byte
	prevKeys [][]byte
	agent    AgentDialer

	mu       sync.Mutex
	cache    map[string]*ResolvedDestination // "" is the default
	cachedAt time.Time
}

// ResolvedDestination is a destination ready to be used.
type ResolvedDestination struct {
	ID     string
	Name   string
	Kind   string
	Target storage.Target
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

func kindOf(k string) string {
	if k == "" {
		return storage.KindS3
	}
	return k
}

// row reads one destination: by ID, or the default for "". ok is false when
// there is none. A failed read is an error, never "none": that would send
// backups to whatever each node has in its own environment file.
func (d *BackupDestinations) row(ctx context.Context, id string) (backupDestinationRow, bool, error) {
	var rows []backupDestinationRow
	q := d.db.WithContext(ctx).Model(&backupDestinationRow{})
	if id == "" {
		q = q.Where("is_default = ?", true)
	} else {
		q = q.Where("id = ?", id)
	}
	if err := q.Limit(1).Find(&rows).Error; err != nil {
		return backupDestinationRow{}, false, fmt.Errorf("could not read the backup destination: %w", err)
	}
	if len(rows) == 0 {
		return backupDestinationRow{}, false, nil
	}
	return rows[0], true, nil
}

func (r backupDestinationRow) config() destConfig {
	var c destConfig
	_ = json.Unmarshal([]byte(r.Config), &c)
	return c
}

// resolveRow turns a row into a usable destination. A secret that cannot be
// read is an error on it, never an empty credential.
func (d *BackupDestinations) resolveRow(r backupDestinationRow) *ResolvedDestination {
	c := r.config()
	out := &ResolvedDestination{ID: r.ID, Name: r.Name, Kind: kindOf(r.Kind), prefix: c.PathPrefix}
	secret := ""
	if r.SecretEnc != "" {
		s, err := d.decrypt(r.SecretEnc)
		if err != nil || s == "" {
			out.err = ErrBackupDestinationUnreadable
		}
		secret = s
	} else if out.Kind != storage.KindPath {
		out.err = ErrBackupDestinationUnreadable
	}
	out.Target = targetOf(out.Kind, c, secret)
	return out
}

func targetOf(kind string, c destConfig, secret string) storage.Target {
	t := storage.Target{Kind: kind}
	switch kind {
	case storage.KindSFTP:
		t.SFTP = storage.SFTPTarget{Host: c.Host, Port: c.Port, User: c.User, HostKey: c.HostKey, Path: c.Path}
		if c.Auth == "key" {
			t.SFTP.PrivateKey = secret
		} else {
			t.SFTP.Password = secret
		}
	case storage.KindPath:
		t.Path = c.Path
	default:
		t.S3 = storage.Destination{Endpoint: c.Endpoint, Region: c.Region, Bucket: c.Bucket, AccessKey: c.AccessKey, SecretKey: secret, PlainHTTP: c.PlainHTTP}
	}
	return t
}

// Resolve returns a destination by ID, or the default for ""; nil when none
// is saved. Cached briefly so a burst of backups does not read and decrypt it
// each time; every change drops the cache.
func (d *BackupDestinations) Resolve(ctx context.Context, id string) (*ResolvedDestination, error) {
	if d == nil {
		return nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cache == nil || time.Since(d.cachedAt) > 30*time.Second {
		d.cache, d.cachedAt = map[string]*ResolvedDestination{}, time.Now()
	}
	r, cached := d.cache[id]
	if !cached {
		row, ok, err := d.row(ctx, id)
		if err != nil {
			return nil, err // not cached: the next call reads again
		}
		if ok {
			r = d.resolveRow(row)
		}
		d.cache[id] = r
	}
	switch {
	case r != nil:
		return r, r.err
	case id != "":
		return nil, ErrBackupDestinationNotFound
	}
	return nil, nil
}

func (d *BackupDestinations) invalidate() {
	d.mu.Lock()
	d.cache = nil
	d.mu.Unlock()
}

// Proto is the destination in the form a node is sent.
func (r *ResolvedDestination) Proto() *pb.ObjectStore {
	t := r.Target
	switch r.Kind {
	case storage.KindSFTP:
		return &pb.ObjectStore{Kind: storage.KindSFTP, Sftp: &pb.SFTPStore{Host: t.SFTP.Host, Port: int32(t.SFTP.Port), User: t.SFTP.User,
			Password: t.SFTP.Password, PrivateKey: t.SFTP.PrivateKey, HostKey: t.SFTP.HostKey, Path: t.SFTP.Path}}
	case storage.KindPath:
		return &pb.ObjectStore{Kind: storage.KindPath, Path: t.Path}
	}
	s := t.S3
	return &pb.ObjectStore{Endpoint: s.Endpoint, Region: s.Region, Bucket: s.Bucket, AccessKey: s.AccessKey, SecretKey: s.SecretKey, PlainHttp: s.PlainHTTP}
}

// Key puts an object key under the destination's path prefix. The result is
// stored on the backup, so a later change of prefix does not lose older ones.
func (r *ResolvedDestination) Key(key string) string {
	if r == nil {
		return key
	}
	return storage.JoinKey(r.prefix, key)
}

// NeedsFeature is the agent feature a node must have to be sent it; "" for S3.
func (r *ResolvedDestination) NeedsFeature() string {
	if r == nil || r.Kind == storage.KindS3 {
		return ""
	}
	return featureBackupTargets
}

// Open connects the panel to the destination. A directory on the nodes is
// not reachable from here (ErrDestinationOnNodes).
func (r *ResolvedDestination) Open(ctx context.Context) (storage.Store, error) {
	if r.Kind == storage.KindPath {
		return nil, ErrDestinationOnNodes
	}
	return r.Target.Open(ctx)
}

// ObjectStore returns a destination (by ID, or the default for "") in the form
// a node is sent and its ID, or nil when none is saved. It is the queue's hook.
func (d *BackupDestinations) ObjectStore(ctx context.Context, id string) (*pb.ObjectStore, string, error) {
	r, err := d.Resolve(ctx, id)
	if err != nil || r == nil {
		return nil, "", err
	}
	return r.Proto(), r.ID, nil
}

// List returns every destination, the default first, with what each holds.
func (d *BackupDestinations) List(ctx context.Context) ([]BackupDestinationView, error) {
	var rows []backupDestinationRow
	if err := d.db.WithContext(ctx).Order("is_default DESC, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	type total struct {
		DestinationID string
		N             int64
		Bytes         int64
	}
	var totals []total
	if err := d.db.WithContext(ctx).Table("backups").
		Select("destination_id, COUNT(*) AS n, COALESCE(SUM(size), 0) AS bytes").
		Where("deleted_at IS NULL AND status = ? AND destination_id IS NOT NULL", models.BackupStatusCompleted).
		Group("destination_id").Scan(&totals).Error; err != nil {
		return nil, err
	}
	byID := map[string]total{}
	for _, t := range totals {
		byID[t.DestinationID] = t
	}
	out := make([]BackupDestinationView, 0, len(rows))
	for _, r := range rows {
		v := viewOf(r)
		v.Backups, v.BackupBytes = byID[r.ID].N, byID[r.ID].Bytes
		out = append(out, v)
	}
	return out, nil
}

func viewOf(r backupDestinationRow) BackupDestinationView {
	c := r.config()
	v := BackupDestinationView{ID: r.ID, Name: r.Name, Kind: kindOf(r.Kind), IsDefault: r.IsDefault, HasSecret: r.SecretEnc != "",
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	switch v.Kind {
	case storage.KindSFTP:
		v.Host, v.Port, v.User, v.HostKey, v.Auth, v.Path = c.Host, c.Port, c.User, c.HostKey, c.Auth, c.Path
		if k, err := storage.ParseHostKey(c.HostKey); err == nil {
			v.HostKeyFingerprint = ssh.FingerprintSHA256(k)
		}
	case storage.KindPath:
		v.Path = c.Path
	default:
		v.Endpoint, v.Region, v.Bucket, v.PathPrefix, v.AccessKey, v.UseTLS = c.Endpoint, c.Region, c.Bucket, c.PathPrefix, c.AccessKey, !c.PlainHTTP
	}
	return v
}

func invalidDestination(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrBackupDestinationInvalid, fmt.Sprintf(format, a...))
}

// normalize validates what was typed against the saved destination (nil for a
// new one) and resolves which secret it means. A blank secret keeps the saved
// one only while where it is sent is unchanged: otherwise anyone able to edit
// a destination could have the panel present the saved secret to a server of
// their choosing.
func (d *BackupDestinations) normalize(in BackupDestinationInput, saved *backupDestinationRow) (destConfig, string, string, error) {
	kind := kindOf(strings.TrimSpace(in.Kind))
	var c destConfig
	var secret string
	switch kind {
	case storage.KindS3:
		c = destConfig{
			Endpoint:   strings.TrimSuffix(strings.TrimSpace(in.Endpoint), "/"),
			Region:     strings.TrimSpace(in.Region),
			Bucket:     strings.TrimSpace(in.Bucket),
			PathPrefix: strings.Trim(strings.TrimSpace(in.PathPrefix), "/"),
			AccessKey:  strings.TrimSpace(in.AccessKey),
			PlainHTTP:  in.UseTLS != nil && !*in.UseTLS,
		}
		// A scheme typed with the endpoint is the clearer statement of intent,
		// so it decides TLS and is then dropped: one fact, kept in one place.
		lower := strings.ToLower(c.Endpoint)
		switch {
		case strings.HasPrefix(lower, "https://"):
			c.Endpoint, c.PlainHTTP = c.Endpoint[len("https://"):], false
		case strings.HasPrefix(lower, "http://"):
			c.Endpoint, c.PlainHTTP = c.Endpoint[len("http://"):], true
		case strings.Contains(lower, "://"):
			return c, "", "", invalidDestination("the endpoint must be a host name, with a port if it needs one")
		}
		if strings.ContainsAny(c.Endpoint, "/ @") {
			return c, "", "", invalidDestination("the endpoint must be a host name, with a port if it needs one")
		}
		if c.Bucket == "" {
			return c, "", "", invalidDestination("the bucket is required")
		}
		if strings.ContainsAny(c.Bucket, "/ ") {
			return c, "", "", invalidDestination("the bucket is a name, not a path; put folders in the path prefix")
		}
		if c.AccessKey == "" {
			return c, "", "", invalidDestination("the access key is required")
		}
		if len(c.PathPrefix) > 200 || strings.Contains(c.PathPrefix, "..") {
			return c, "", "", invalidDestination("the path prefix is too long or contains '..'")
		}
		secret = strings.TrimSpace(in.SecretKey)
	case storage.KindSFTP:
		c = destConfig{Host: strings.TrimSpace(in.Host), Port: in.Port, User: strings.TrimSpace(in.User),
			Path: strings.TrimRight(strings.TrimSpace(in.Path), "/"), Auth: in.Auth}
		if c.Port == 0 {
			c.Port = 22
		}
		if k, err := storage.ParseHostKey(in.HostKey); err == nil {
			c.HostKey = storage.HostKeyLine(k)
		} else {
			return c, "", "", invalidDestination("%v", err)
		}
		switch {
		case in.PrivateKey != "":
			c.Auth, secret = "key", strings.TrimSpace(in.PrivateKey)+"\n"
		case in.Password != "":
			c.Auth, secret = "password", in.Password
		case c.Auth != "key":
			c.Auth = "password"
		}
		if len(c.Path) > 200 {
			return c, "", "", invalidDestination("the path is too long")
		}
	case storage.KindPath:
		c = destConfig{Path: strings.TrimRight(strings.TrimSpace(in.Path), "/")}
		if !strings.HasPrefix(c.Path, "/") || c.Path == "" || strings.Contains(c.Path, "..") || len(c.Path) > 200 {
			return c, "", "", invalidDestination("the path must be an absolute directory such as /mnt/backups, not /")
		}
	default:
		return c, "", "", invalidDestination("the kind must be s3, sftp or path")
	}

	if kind != storage.KindPath && secret == "" {
		if saved == nil || saved.SecretEnc == "" || kindOf(saved.Kind) != kind {
			return c, "", "", invalidDestination("%s is required", secretName(kind, c))
		}
		old := saved.config()
		same := old.Endpoint == c.Endpoint && old.Bucket == c.Bucket && old.PlainHTTP == c.PlainHTTP
		if kind == storage.KindSFTP {
			same = old.Host == c.Host && old.Port == c.Port && old.User == c.User && old.HostKey == c.HostKey && old.Auth == c.Auth
		}
		if !same {
			return c, "", "", invalidDestination("%s is required when where it is sent changes", secretName(kind, c))
		}
		var err error
		if secret, err = d.decrypt(saved.SecretEnc); err != nil || secret == "" {
			return c, "", "", ErrBackupDestinationUnreadable
		}
	}
	t := targetOf(kind, c, secret)
	switch kind {
	case storage.KindS3:
		if _, err := t.S3.Client(); err != nil {
			return c, "", "", invalidDestination("%v", err)
		}
	case storage.KindSFTP:
		if err := t.SFTP.Validate(); err != nil {
			return c, "", "", invalidDestination("%v", err)
		}
	}
	return c, kind, secret, nil
}

func secretName(kind string, c destConfig) string {
	switch {
	case kind == storage.KindSFTP && c.Auth == "key":
		return "the private key"
	case kind == storage.KindSFTP:
		return "the password"
	}
	return "the secret key"
}

func destName(in BackupDestinationInput, kind string, c destConfig) (string, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		switch kind {
		case storage.KindSFTP:
			name = c.User + "@" + c.Host
		case storage.KindPath:
			name = c.Path
		default:
			name = c.Bucket
		}
	}
	if len(name) > 100 {
		return "", invalidDestination("the name is too long")
	}
	return name, nil
}

// Create saves a new destination. The first one becomes the default.
func (d *BackupDestinations) Create(ctx context.Context, in BackupDestinationInput) (*BackupDestinationView, error) {
	c, kind, secret, err := d.normalize(in, nil)
	if err != nil {
		return nil, err
	}
	row := backupDestinationRow{ID: uuid.NewString(), Kind: kind}
	if row.Name, err = destName(in, kind, c); err != nil {
		return nil, err
	}
	if err := d.seal(&row, c, secret); err != nil {
		return nil, err
	}
	err = d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var n int64
		if err := tx.Model(&backupDestinationRow{}).Where("is_default = ?", true).Count(&n).Error; err != nil {
			return err
		}
		row.IsDefault = n == 0
		return tx.Create(&row).Error
	})
	d.invalidate()
	if err != nil {
		return nil, err
	}
	v := viewOf(row)
	return &v, nil
}

func (d *BackupDestinations) seal(row *backupDestinationRow, c destConfig, secret string) error {
	b, _ := json.Marshal(c)
	row.Config, row.SecretEnc = string(b), ""
	if secret != "" {
		enc, err := encryptWithKey(d.key, secret)
		if err != nil {
			return fmt.Errorf("could not encrypt the secret: %w", err)
		}
		row.SecretEnc = enc
	}
	return nil
}

// Update changes a destination. Backups already made keep the keys they were
// stored under, so a new prefix applies to new backups only. The bucket or
// directory is not part of a key: while backups are kept there it cannot
// change, or every one of them would be looked for where it is not.
func (d *BackupDestinations) Update(ctx context.Context, id string, in BackupDestinationInput) (*BackupDestinationView, error) {
	saved, ok, err := d.row(ctx, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrBackupDestinationNotFound
	}
	if in.Kind != "" && kindOf(in.Kind) != kindOf(saved.Kind) {
		return nil, invalidDestination("a destination's kind cannot change; add another destination instead")
	}
	in.Kind = saved.Kind
	c, kind, secret, err := d.normalize(in, &saved)
	if err != nil {
		return nil, err
	}
	if old := saved.config(); old.Bucket != c.Bucket || old.Path != c.Path {
		held, err := d.held(ctx, id)
		if err != nil {
			return nil, err
		}
		if held > 0 {
			return nil, invalidDestination("%d backups are kept in %s, and their files stay where they are; add a new destination and make it the default instead of moving this one", held, saved.Name)
		}
	}
	if saved.Name, err = destName(in, kind, c); err != nil {
		return nil, err
	}
	if err := d.seal(&saved, c, secret); err != nil {
		return nil, err
	}
	saved.UpdatedAt = time.Now()
	err = d.db.WithContext(ctx).Model(&backupDestinationRow{}).Where("id = ?", id).
		Updates(map[string]interface{}{"name": saved.Name, "config": saved.Config, "secret_enc": saved.SecretEnc, "updated_at": saved.UpdatedAt}).Error
	d.invalidate()
	if err != nil {
		return nil, err
	}
	v := viewOf(saved)
	return &v, nil
}

// SetDefault makes new backups go to the destination.
func (d *BackupDestinations) SetDefault(ctx context.Context, id string) error {
	if _, ok, err := d.row(ctx, id); err != nil || !ok {
		if err == nil {
			err = ErrBackupDestinationNotFound
		}
		return err
	}
	err := d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&backupDestinationRow{}).Where("is_default = ?", true).Update("is_default", false).Error; err != nil {
			return err
		}
		return tx.Model(&backupDestinationRow{}).Where("id = ?", id).Update("is_default", true).Error
	})
	d.invalidate()
	return err
}

// Delete removes a destination no backup is kept in. The default can go only
// when it is the last one: new backups then use each node's own storage.
func (d *BackupDestinations) Delete(ctx context.Context, id string) error {
	row, ok, err := d.row(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrBackupDestinationNotFound
	}
	held, err := d.held(ctx, id)
	if err != nil {
		return err
	}
	if held > 0 {
		return fmt.Errorf("%w: %d backups are kept in %s; delete them first, or keep the destination so they can be restored", ErrBackupDestinationInUse, held, row.Name)
	}
	if row.IsDefault {
		var others int64
		if err := d.db.WithContext(ctx).Model(&backupDestinationRow{}).Where("id <> ?", id).Count(&others).Error; err != nil {
			return err
		}
		if others > 0 {
			return invalidDestination("make another destination the default before removing this one")
		}
	}
	err = d.db.WithContext(ctx).Where("id = ?", id).Delete(&backupDestinationRow{}).Error
	d.invalidate()
	return err
}

// held counts the backups kept, or being made, in a destination.
func (d *BackupDestinations) held(ctx context.Context, id string) (int64, error) {
	var n int64
	err := d.db.WithContext(ctx).Table("backups").
		Where("destination_id = ? AND deleted_at IS NULL AND status <> ?", id, models.BackupStatusFailed).Count(&n).Error
	return n, err
}

// Outcomes of testing a destination from one place.
const (
	DestinationTestOK          = "ok"
	DestinationTestFailed      = "failed"
	DestinationTestNeedsUpdate = "needs_agent_update"
	DestinationTestUnreachable = "unreachable"
	DestinationTestSkipped     = "not_applicable"
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
// given node: connect, write a small file, read it back, delete it. id names
// the saved destination being edited (its secret is used when none is typed),
// or is empty. The panel must reach an S3 or SFTP store to delete backups;
// each node must reach it to write and restore them, and the two are often on
// different networks.
func (d *BackupDestinations) Test(ctx context.Context, id string, in BackupDestinationInput, nodes []models.Node) ([]BackupDestinationTestResult, error) {
	var saved *backupDestinationRow
	if id != "" {
		row, ok, err := d.row(ctx, id)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrBackupDestinationNotFound
		}
		saved = &row
		in.Kind = row.Kind
	}
	c, kind, secret, err := d.normalize(in, saved)
	if err != nil {
		return nil, err
	}
	r := &ResolvedDestination{Kind: kind, Target: targetOf(kind, c, secret), prefix: c.PathPrefix}
	key := r.Key(fmt.Sprintf(".destination-test-%d", time.Now().UnixNano()))

	results := make([]BackupDestinationTestResult, len(nodes)+1)
	var wg sync.WaitGroup
	wg.Add(len(nodes) + 1)
	go func() {
		defer wg.Done()
		results[0] = BackupDestinationTestResult{Name: "Panel", Status: DestinationTestOK}
		if kind == storage.KindPath {
			results[0].Status, results[0].Message = DestinationTestSkipped, "The panel does not mount it; nodes delete backups kept there."
			return
		}
		tctx, cancel := context.WithTimeout(ctx, destinationTestTimeout)
		defer cancel()
		s, err := r.Open(tctx)
		if err == nil {
			err = storage.ProbeStore(tctx, s, key+"-panel")
			s.Close()
		}
		if err != nil {
			results[0].Status, results[0].Message = DestinationTestFailed, err.Error()
		}
	}()
	for i := range nodes {
		go func(i int) {
			defer wg.Done()
			results[i+1] = d.testFromNode(ctx, &nodes[i], r, key+"-"+nodes[i].ID, nil)
		}(i)
	}
	wg.Wait()
	return results, nil
}

// ScanHostKey reads the key an SSH server presents, for the operator to
// check and pin.
func (d *BackupDestinations) ScanHostKey(ctx context.Context, host string, port int) (line, fingerprint string, err error) {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " /@") {
		return "", "", invalidDestination("the host must be a host name or address")
	}
	if port == 0 {
		port = 22
	}
	ctx, cancel := context.WithTimeout(ctx, destinationTestTimeout)
	defer cancel()
	k, err := storage.ScanHostKey(ctx, host, port)
	if err != nil {
		return "", "", err
	}
	return storage.HostKeyLine(k), ssh.FingerprintSHA256(k), nil
}

// nodeFeatures asks a node's agent what it can do. An agent too old to say
// has none of the features.
func nodeFeatures(ctx context.Context, client pb.NodeAgentClient) ([]string, error) {
	info, err := client.GetNodeInfo(ctx, &pb.GetNodeInfoRequest{})
	if err != nil && status.Code(err) != codes.Unimplemented {
		return nil, err
	}
	return info.GetFeatures(), nil
}

func hasFeature(features []string, f string) bool {
	for _, x := range features {
		if x == f {
			return true
		}
	}
	return false
}

// testFromNode asks one node's agent to run the test. An agent built before
// the test existed, or before the destination's kind, is not reported as a
// failure of the destination.
func (d *BackupDestinations) testFromNode(ctx context.Context, node *models.Node, r *ResolvedDestination, key string, expect []string) BackupDestinationTestResult {
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
	if f := r.NeedsFeature(); f != "" {
		features, err := nodeFeatures(authCtx, client)
		if err != nil {
			return fail(DestinationTestUnreachable, err.Error())
		}
		if !hasFeature(features, f) {
			return fail(DestinationTestNeedsUpdate, "this node's agent is older than the panel and cannot use an SFTP server or a mounted directory; update it under Nodes")
		}
	}
	resp, err := client.TestObjectStore(authCtx, &pb.TestObjectStoreRequest{ObjectStore: r.Proto(), Key: key, ExpectKeys: expect})
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

// CheckNode asks one node to prove it can use the destination a backup is in
// (by ID; "" is the store in that node's own environment file) and sees the
// backup's files there, before a restore creates anything on it. An agent too
// old to answer is refused either way: it would keep the download on its root
// filesystem and not check it.
func (d *BackupDestinations) CheckNode(ctx context.Context, node *models.Node, destinationID string, keys ...string) error {
	r := &ResolvedDestination{Kind: storage.KindS3} // empty: the node uses its own
	key := fmt.Sprintf(".destination-test-%d-%s", time.Now().UnixNano(), node.ID)
	what := "read backups kept in a node's own storage"
	if destinationID != "" {
		saved, err := d.Resolve(ctx, destinationID)
		if err != nil {
			return err
		}
		if saved == nil {
			return errors.New("no backup destination is set")
		}
		r, key, what = saved, saved.Key(key), "use the backup destination "+saved.Name
	}
	res := d.testFromNode(ctx, node, r, key, keys)
	if res.Status != DestinationTestOK {
		return fmt.Errorf("node %s cannot %s: %s", node.Name, what, res.Message)
	}
	return nil
}

// DeleteOnNodes removes files from a directory mounted on the nodes, through
// writer (the node that wrote them; "" when it is gone) or, when it does not
// answer, the first of others that does. missing are the keys not found. Only
// the writer's "not found" is believed: a disk mounted on one node is not seen
// from another, and believing it would forget files that are still there.
func (d *BackupDestinations) DeleteOnNodes(ctx context.Context, r *ResolvedDestination, writer string, others []string, keys []string) (missing []string, err error) {
	if d.agent == nil {
		return nil, errors.New("the panel has no connection to the nodes")
	}
	nodeIDs := others
	if writer != "" {
		nodeIDs = append([]string{writer}, others...)
	}
	var errs []error
	for _, id := range nodeIDs {
		client, authCtx, derr := d.agent(ctx, id)
		if derr != nil {
			errs = append(errs, derr)
			continue
		}
		features, ferr := nodeFeatures(authCtx, client)
		if ferr != nil {
			errs = append(errs, ferr)
			continue
		}
		if !hasFeature(features, featureBackupTargets) {
			errs = append(errs, errors.New("the node's agent is older than the panel; update it under Nodes"))
			continue
		}
		resp, rerr := client.DeleteBackupFiles(authCtx, &pb.DeleteBackupFilesRequest{ObjectStore: r.Proto(), Keys: keys})
		if rerr == nil && resp.GetError() != nil {
			rerr = errors.New(resp.GetError().GetMessage())
		}
		if rerr != nil {
			errs = append(errs, rerr)
			continue
		}
		if writer != "" && id != writer && len(resp.GetMissing()) > 0 {
			return nil, errors.Join(append(errs, fmt.Errorf("the node that wrote this backup did not answer, and another one does not see %d of its files; try again when it is back",
				len(resp.GetMissing())))...)
		}
		return resp.GetMissing(), nil
	}
	if len(errs) == 0 {
		return nil, errors.New("no node holds this backup any more")
	}
	return nil, errors.Join(errs...)
}
