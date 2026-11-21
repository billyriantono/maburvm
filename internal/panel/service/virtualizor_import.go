package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
)

// Importing a whole Virtualizor installation: its customers, plans, IP pools
// and VMs, with each VM going to its real owner and keeping its billing link.
// VMs are adopted where they run; nothing on a node changes.

var (
	ErrImportSourceNotFound = errors.New("import source not found")
	ErrInvalidImportSource  = errors.New("invalid import source")
	ErrImportNoPreview      = errors.New("preview the import first: there is nothing to apply yet")
	ErrImportNoMail         = errors.New("set up email first (System settings, Email): the invitations are sent by email")
	ErrImportNoPanelURL     = errors.New("set the panel's address first (System settings, Panel): the invitation links point to it")
	ErrImportReading        = errors.New("the source is still being read; apply once the preview is up to date")
)

// importReadTimeout bounds one read of a master and the scans of its nodes.
const importReadTimeout = 20 * time.Minute

// importInviteTTL is how long an imported account's "choose a password" link
// works. Longer than a reset link: the customer did not ask for it.
const importInviteTTL = 7 * 24 * time.Hour

// VirtualizorSource is a master the operator imports from.
type VirtualizorSource struct {
	ID             string                   `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Kind           string                   `json:"kind" gorm:"column:kind"`
	Name           string                   `json:"name" gorm:"column:name"`
	BaseURL        string                   `json:"base_url" gorm:"column:base_url"`
	APIKey         string                   `json:"-" gorm:"column:api_key;serializer:secret"`
	APIPass        string                   `json:"-" gorm:"column:api_pass;serializer:secret"`
	TLSFingerprint string                   `json:"tls_fingerprint" gorm:"column:tls_fingerprint"`
	Options        VirtualizorImportOptions `json:"options" gorm:"column:options;type:jsonb;serializer:json"`
	Snapshot       *VirtualizorSnapshot     `json:"-" gorm:"column:snapshot;type:jsonb;serializer:json"`
	SnapshotAt     *time.Time               `json:"snapshot_at" gorm:"column:snapshot_at"`
	CreatedAt      time.Time                `json:"created_at" gorm:"column:created_at"`
	UpdatedAt      time.Time                `json:"updated_at" gorm:"column:updated_at"`

	HasCredentials bool           `json:"has_credentials" gorm:"-"`
	Imported       map[string]int `json:"imported" gorm:"-"`
}

func (VirtualizorSource) TableName() string { return "import_sources" }

// importLink records what a source record became here.
type importLink struct {
	SourceID  string     `gorm:"column:source_id;primaryKey"`
	Kind      string     `gorm:"column:kind;primaryKey"`
	SourceKey string     `gorm:"column:source_key;primaryKey"`
	TargetID  string     `gorm:"column:target_id"`
	Created   bool       `gorm:"column:created"`
	InvitedAt *time.Time `gorm:"column:invited_at"`
	CreatedAt time.Time  `gorm:"column:created_at"`
}

func (importLink) TableName() string { return "import_links" }

// VirtualizorSourceInput creates or changes a source. On a change, an empty
// key or pass keeps the stored one.
type VirtualizorSourceInput struct {
	Name           string `json:"name"`
	BaseURL        string `json:"base_url"`
	APIKey         string `json:"api_key"`
	APIPass        string `json:"api_pass"`
	TLSFingerprint string `json:"tls_fingerprint"`
}

// ImportActor is who runs an import, for the audit log.
type ImportActor struct {
	UserID, IP, UserAgent string
}

// ImportItemRef names one preview item to apply.
type ImportItemRef struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

// ImportApplyResult is what happened to one item.
type ImportApplyResult struct {
	Kind     string `json:"kind"`
	Key      string `json:"key"`
	Label    string `json:"label"`
	Status   string `json:"status"` // created, linked, updated, skipped, failed
	TargetID string `json:"target_id,omitempty"`
	Message  string `json:"message,omitempty"`
}

// SourceTestResult is the outcome of a connection test.
type SourceTestResult struct {
	OK      bool   `json:"ok"`
	Servers int    `json:"servers"`
	Message string `json:"message"`
	// Fingerprint is the certificate the master presented when it is not
	// trusted, for the operator to compare and pin.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// VirtualizorImportService runs imports from Virtualizor masters.
type VirtualizorImportService struct {
	db      *gorm.DB
	imports *ImportService
	logger  *slog.Logger
	// scan lists the libvirt domains on a node; the agent scan outside tests.
	scan func(ctx context.Context, nodeID string) ([]DiscoveredVM, error)
	// ponytail: one apply at a time on this panel; per-source locks if several
	// operators ever import from different masters at once.
	applyMu sync.Mutex

	// Reads of a master and its nodes run in the background: they can take
	// minutes, longer than a request may stay open. One per source; kept in
	// memory, so a restart forgets one in progress and it is simply started again.
	readMu sync.Mutex
	reads  map[string]*importRead
}

// importRead is the state of a source's latest background read.
type importRead struct {
	running    bool
	err        string
	startedAt  time.Time
	finishedAt time.Time
}

// NewVirtualizorImportService builds the service on the node import's scan
// and VM recording.
func NewVirtualizorImportService(db *gorm.DB, imports *ImportService) *VirtualizorImportService {
	s := &VirtualizorImportService{db: db, imports: imports, logger: slog.Default(), reads: map[string]*importRead{}}
	s.scan = func(ctx context.Context, nodeID string) ([]DiscoveredVM, error) {
		return imports.ListImportableVMs(ctx, nodeID, "")
	}
	return s
}

func (in *VirtualizorSourceInput) clean(creating bool) error {
	in.Name = strings.TrimSpace(in.Name)
	in.BaseURL = strings.TrimRight(strings.TrimSpace(in.BaseURL), "/")
	in.APIKey, in.APIPass = strings.TrimSpace(in.APIKey), strings.TrimSpace(in.APIPass)
	if in.Name == "" || len(in.Name) > 100 {
		return fmt.Errorf("%w: a name of 1 to 100 characters is required", ErrInvalidImportSource)
	}
	if len(in.BaseURL) > 500 {
		return fmt.Errorf("%w: the address is too long", ErrInvalidImportSource)
	}
	if _, err := parseSourceURL(in.BaseURL); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidImportSource, err)
	}
	if creating && (in.APIKey == "" || in.APIPass == "") {
		return fmt.Errorf("%w: the API key and API pass are required", ErrInvalidImportSource)
	}
	if len(in.APIKey) > 500 || len(in.APIPass) > 500 {
		return fmt.Errorf("%w: the API key or pass is too long", ErrInvalidImportSource)
	}
	if strings.TrimSpace(in.TLSFingerprint) != "" {
		fp := NormalizeFingerprint(in.TLSFingerprint)
		if fp == "" {
			return fmt.Errorf("%w: the certificate fingerprint must be a SHA-256 fingerprint (64 hex digits)", ErrInvalidImportSource)
		}
		in.TLSFingerprint = fp
	}
	return nil
}

// ListSources returns every source, without credentials or snapshot.
func (s *VirtualizorImportService) ListSources(ctx context.Context) ([]VirtualizorSource, error) {
	var out []VirtualizorSource
	if err := s.db.WithContext(ctx).Omit("snapshot").Order("created_at").Find(&out).Error; err != nil {
		return nil, err
	}
	var counts []struct {
		SourceID string
		Kind     string
		N        int
	}
	if err := s.db.WithContext(ctx).Table("import_links").Select("source_id, kind, COUNT(*) AS n").Group("source_id, kind").Scan(&counts).Error; err != nil {
		return nil, err
	}
	for i := range out {
		out[i].HasCredentials = out[i].APIKey != "" && out[i].APIPass != ""
		out[i].Imported = map[string]int{}
		for _, c := range counts {
			if c.SourceID == out[i].ID {
				out[i].Imported[c.Kind] = c.N
			}
		}
	}
	return out, nil
}

func (s *VirtualizorImportService) getSource(ctx context.Context, id string, withSnapshot bool) (*VirtualizorSource, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrImportSourceNotFound
	}
	var src VirtualizorSource
	q := s.db.WithContext(ctx)
	if !withSnapshot {
		q = q.Omit("snapshot")
	}
	if err := q.First(&src, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrImportSourceNotFound
		}
		return nil, err
	}
	src.HasCredentials = src.APIKey != "" && src.APIPass != ""
	return &src, nil
}

// CreateSource stores a new source; its credentials are encrypted.
func (s *VirtualizorImportService) CreateSource(ctx context.Context, in VirtualizorSourceInput) (*VirtualizorSource, error) {
	if err := in.clean(true); err != nil {
		return nil, err
	}
	src := &VirtualizorSource{ID: uuid.New().String(), Kind: "virtualizor", Name: in.Name, BaseURL: in.BaseURL,
		APIKey: in.APIKey, APIPass: in.APIPass, TLSFingerprint: in.TLSFingerprint, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := s.db.WithContext(ctx).Omit("snapshot", "snapshot_at").Create(src).Error; err != nil {
		return nil, err
	}
	src.HasCredentials, src.Imported = true, map[string]int{}
	return src, nil
}

// UpdateSource changes a source's name, address, credentials or pinned
// certificate.
func (s *VirtualizorImportService) UpdateSource(ctx context.Context, id string, in VirtualizorSourceInput) (*VirtualizorSource, error) {
	src, err := s.getSource(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if err := in.clean(false); err != nil {
		return nil, err
	}
	// The saved credentials go only to the host they were saved for: pointing
	// a source elsewhere would hand them to that host.
	if sourceOrigin(in.BaseURL) != sourceOrigin(src.BaseURL) && (in.APIKey == "" || in.APIPass == "") {
		return nil, fmt.Errorf("%w: enter the API key and pass again to change the address", ErrInvalidImportSource)
	}
	src.Name, src.BaseURL, src.TLSFingerprint = in.Name, in.BaseURL, in.TLSFingerprint
	if in.APIKey != "" {
		src.APIKey = in.APIKey
	}
	if in.APIPass != "" {
		src.APIPass = in.APIPass
	}
	src.UpdatedAt = time.Now().UTC()
	if err := s.db.WithContext(ctx).Model(src).Select("name", "base_url", "api_key", "api_pass", "tls_fingerprint", "updated_at").Updates(src).Error; err != nil {
		return nil, err
	}
	src.HasCredentials = src.APIKey != "" && src.APIPass != ""
	return src, nil
}

// sourceOrigin is the scheme and host credentials are sent to; "" when unreadable.
func sourceOrigin(raw string) string {
	u, err := parseSourceURL(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + strings.ToLower(u.Host)
}

// DeleteSource forgets a source and its links. What was imported stays.
func (s *VirtualizorImportService) DeleteSource(ctx context.Context, id string) error {
	if _, err := s.getSource(ctx, id, false); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Delete(&VirtualizorSource{}, "id = ?", id).Error
}

func (s *VirtualizorImportService) api(src *VirtualizorSource) (*VirtualizorAPI, error) {
	return NewVirtualizorAPI(src.BaseURL, src.APIKey, src.APIPass, src.TLSFingerprint)
}

// TestSource checks that the master answers and takes the credentials. An
// untrusted certificate comes back with the fingerprint it presented.
func (s *VirtualizorImportService) TestSource(ctx context.Context, id string) (*SourceTestResult, error) {
	src, err := s.getSource(ctx, id, false)
	if err != nil {
		return nil, err
	}
	a, err := s.api(src)
	if err != nil {
		return &SourceTestResult{Message: err.Error()}, nil
	}
	// Within what a request through the web front end may take.
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	n, err := a.TestConnection(ctx)
	if err != nil {
		res := &SourceTestResult{Message: err.Error()}
		if IsUntrustedCertificate(err) {
			if fp, ferr := PresentedFingerprint(ctx, src.BaseURL); ferr == nil {
				res.Fingerprint = fp
				res.Message = "The master's certificate is not trusted. If the fingerprint below is the one your master shows, save it as the certificate fingerprint to trust that certificate."
			}
		}
		return res, nil
	}
	return &SourceTestResult{OK: true, Servers: n, Message: fmt.Sprintf("Connected. The master lists %d server(s).", n)}, nil
}

func (o *VirtualizorImportOptions) clean() error {
	for k, v := range o.BillingRefs {
		if strings.TrimSpace(v) == "" {
			delete(o.BillingRefs, k)
			continue
		}
		if len(NormalizeBillingRef(v)) > 255 {
			return fmt.Errorf("%w: the billing reference of VM %s is longer than 255 characters", ErrInvalidImportSource, k)
		}
	}
	if len(o.BillingRefs) > 100000 || len(o.NodeMap) > 10000 || len(o.PlanMap) > 10000 {
		return fmt.Errorf("%w: too many entries", ErrInvalidImportSource)
	}
	return nil
}

// Preview returns what an import would do, planned from the last snapshot
// read from the master and its nodes. opts, when given, are kept as the
// source's mapping choices first. refresh starts a new read of the master in
// the background; a mapping that points at a node not read yet starts a read
// of that node. The plan says whether a read is running, so the caller polls
// until it is not. Nothing but the options and the snapshot is written.
func (s *VirtualizorImportService) Preview(ctx context.Context, id string, refresh bool, opts *VirtualizorImportOptions) (*ImportPlan, error) {
	src, err := s.getSource(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if opts != nil {
		if err := opts.clean(); err != nil {
			return nil, err
		}
		src.Options = *opts
		if err := s.db.WithContext(ctx).Model(src).Select("options", "updated_at").
			Updates(&VirtualizorSource{Options: src.Options, UpdatedAt: time.Now().UTC()}).Error; err != nil {
			return nil, err
		}
	}
	st, err := s.loadState(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	if refresh || (src.Snapshot == nil && s.readState(src.ID).startedAt.IsZero()) {
		if refresh && !src.HasCredentials {
			return nil, fmt.Errorf("%w: save the API key and pass first", ErrInvalidImportSource)
		}
		s.startRead(src.ID, true)
	} else if src.Snapshot != nil && len(unscannedNodes(src.Snapshot, src.Options, st)) > 0 {
		s.startRead(src.ID, false)
	}

	var plan *ImportPlan
	if src.Snapshot != nil {
		plan = planVirtualizorImport(src.Snapshot, src.Options, st)
		plan.Ready = true
	} else {
		plan = &ImportPlan{Options: src.Options, Items: []ImportItem{}}
	}
	plan.SourceID = src.ID
	r := s.readState(src.ID)
	plan.Reading, plan.ReadError = r.running, r.err
	if !r.startedAt.IsZero() {
		started := r.startedAt
		plan.ReadStartedAt = &started
	}
	if plan.PendingInvites, err = s.PendingInvites(ctx, src.ID); err != nil {
		return nil, err
	}
	return plan, nil
}

// unscannedNodes are the nodes the mapping sends KVM VMs to that the snapshot
// has not read.
func unscannedNodes(snap *VirtualizorSnapshot, opts VirtualizorImportOptions, st *importState) map[string]bool {
	_, nodeOf := resolveServers(snap, opts, st)
	need := map[string]bool{}
	for _, v := range snap.VMs {
		if n, ok := nodeOf[v.ServerID]; ok && isKVM(v.Virt) && snap.scan(n.ID) == nil {
			need[n.ID] = true
		}
	}
	return need
}

func (s *VirtualizorImportService) readState(id string) importRead {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if r := s.reads[id]; r != nil {
		return *r
	}
	return importRead{}
}

// startRead begins a background read of a source unless one is running.
// master re-reads the master's API; otherwise only nodes not read yet are.
func (s *VirtualizorImportService) startRead(id string, master bool) {
	s.readMu.Lock()
	if r := s.reads[id]; r != nil && r.running {
		s.readMu.Unlock()
		return
	}
	s.reads[id] = &importRead{running: true, startedAt: time.Now().UTC()}
	s.readMu.Unlock()
	go func() {
		var err error
		// A panic here would take the whole panel down and leave the wizard
		// polling a read marked running forever.
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("reading the source stopped on an internal error: %v", p)
				s.logger.Error("import: read panicked", "source_id", id, "panic", p, "stack", string(debug.Stack()))
			}
			s.finishRead(id, err)
		}()
		err = s.read(id, master)
	}()
}

// finishRead records the end of a background read.
func (s *VirtualizorImportService) finishRead(id string, err error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	r := s.reads[id]
	r.running, r.finishedAt = false, time.Now().UTC()
	if err != nil {
		r.err = err.Error()
		s.logger.Warn("import: reading the source failed", "source_id", id, "error", err)
	}
}

// read fetches the master (when master is set or nothing was read yet) and
// scans the nodes its VMs are mapped to, then keeps that snapshot.
func (s *VirtualizorImportService) read(id string, master bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), importReadTimeout)
	defer cancel()
	src, err := s.getSource(ctx, id, true)
	if err != nil {
		return err
	}
	snap := src.Snapshot
	if master || snap == nil {
		a, err := s.api(src)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidImportSource, err)
		}
		if snap, err = a.Fetch(ctx); err != nil {
			return err
		}
	}
	st, err := s.loadState(ctx, id)
	if err != nil {
		return err
	}
	// The options as they are now: the operator may have changed the mapping
	// while the master was read. A node mapped after this is read next time.
	if cur, err := s.getSource(ctx, id, false); err == nil {
		src.Options = cur.Options
	}
	snap.Scans = append(snap.Scans, s.scanNodes(ctx, unscannedNodes(snap, src.Options, st))...)
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Model(&VirtualizorSource{ID: id}).Select("snapshot", "snapshot_at").
		Updates(&VirtualizorSource{Snapshot: snap, SnapshotAt: &now}).Error
}

// scanNodes reads each node's libvirt domains at once. A node that fails is
// kept with its error so its VMs say why they cannot be imported.
func (s *VirtualizorImportService) scanNodes(ctx context.Context, nodes map[string]bool) []NodeScan {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]NodeScan, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					out[i] = NodeScan{NodeID: id, Error: fmt.Sprintf("reading this node stopped on an internal error: %v", p)}
				}
			}()
			sctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			out[i] = NodeScan{NodeID: id}
			found, err := s.scan(sctx, id)
			if err != nil {
				out[i].Error = err.Error()
				return
			}
			for _, d := range found {
				if d.Candidate == nil {
					continue
				}
				c := *d.Candidate
				if c.VNC != nil {
					// Kept only sealed: the VM row takes it, so the console opens
					// with the password the running domain has.
					vnc := *c.VNC
					if sealed, err := models.SealSecret(vnc.Password); err == nil && models.IsSealed(sealed) {
						vnc.Password = sealed
					} else {
						vnc.Password = ""
					}
					c.VNC = &vnc
				}
				out[i].Domains = append(out[i].Domains, c)
			}
		}(i, id)
	}
	wg.Wait()
	return out
}

// loadState reads what planning compares against.
func (s *VirtualizorImportService) loadState(ctx context.Context, sourceID string) (*importState, error) {
	db := s.db.WithContext(ctx)
	st := &importState{users: map[string]stateUser{}, userByID: map[string]stateUser{}, assigned: map[string]stateHolder{},
		vms: map[string]stateVM{}, hostnames: map[string]string{}, links: map[string]importLink{}}

	var nodes []struct{ ID, Name, IPAddress string }
	if err := db.Table("nodes").Select("id, name, ip_address").Where("deleted_at IS NULL").Order("name").Scan(&nodes).Error; err != nil {
		return nil, fmt.Errorf("could not read the nodes: %w", err)
	}
	for _, n := range nodes {
		st.nodes = append(st.nodes, stateNode{ID: n.ID, Name: n.Name, IP: strings.SplitN(n.IPAddress, "/", 2)[0]})
	}
	var templates []models.OSTemplate
	if err := db.Select("id", "name", "version").Order("name, version").Find(&templates).Error; err != nil {
		return nil, fmt.Errorf("could not read the templates: %w", err)
	}
	for _, t := range templates {
		st.templates = append(st.templates, stateTemplate{ID: t.ID, Name: t.Name, Version: t.Version})
	}
	var users []struct {
		ID, Email, Role string
		DeletedAt       *time.Time
	}
	if err := db.Table("users").Select("id, email, role, deleted_at").Scan(&users).Error; err != nil {
		return nil, fmt.Errorf("could not read the accounts: %w", err)
	}
	for _, u := range users {
		su := stateUser{ID: u.ID, Email: strings.ToLower(u.Email), Staff: u.Role == string(models.RoleAdmin), Deleted: u.DeletedAt != nil}
		st.userByID[u.ID] = su
		if prev, ok := st.users[su.Email]; !ok || prev.Deleted {
			st.users[su.Email] = su
		}
	}
	var plans []models.Plan
	if err := db.Select("id", "name", "cpu", "ram", "disk").Order("name").Find(&plans).Error; err != nil {
		return nil, fmt.Errorf("could not read the plans: %w", err)
	}
	for _, p := range plans {
		st.plans = append(st.plans, statePlan{ID: p.ID, Name: p.Name, CPU: p.CPU, RAM: p.RAM, Disk: p.Disk})
	}
	var pools []struct {
		ID, Name, Family string
		CIDR             string `gorm:"column:cidr"`
	}
	if err := db.Table("ip_pools").Select("id, name, family, COALESCE(CAST(cidr AS TEXT), '') AS cidr").Where("deleted_at IS NULL").Scan(&pools).Error; err != nil {
		return nil, fmt.Errorf("could not read the IP pools: %w", err)
	}
	for _, p := range pools {
		st.pools = append(st.pools, statePool{ID: p.ID, Name: p.Name, Family: p.Family, CIDR: p.CIDR})
	}
	var vms []struct {
		ID, UserID, Hostname, NodeID string
		PlanID, BillingRef, Source   *string
	}
	if err := db.Table("vms").Select("id, user_id, hostname, node_id, plan_id, billing_ref, source_migration AS source").
		Where("deleted_at IS NULL").Scan(&vms).Error; err != nil {
		return nil, fmt.Errorf("could not read the VMs: %w", err)
	}
	hostOf := map[string]string{}
	for _, v := range vms {
		sv := stateVM{ID: strings.ToLower(v.ID), UserID: v.UserID, Hostname: v.Hostname, NodeID: v.NodeID,
			PlanID: deref(v.PlanID), BillingRef: deref(v.BillingRef), Source: deref(v.Source)}
		st.vms[sv.ID] = sv
		st.hostnames[hostKey(v.UserID, v.Hostname)] = sv.ID
		hostOf[sv.ID] = v.Hostname
	}
	var held []struct{ Address, VMID string }
	if err := db.Raw(`SELECT CAST(address AS TEXT) AS address, CAST(vm_id AS TEXT) AS vm_id FROM ip_addresses
		WHERE vm_id IS NOT NULL AND status = 'assigned' AND deleted_at IS NULL
		UNION SELECT CAST(address AS TEXT), '' FROM ip_addresses
		WHERE status = 'reserved' AND delivery_mode = 'floating' AND deleted_at IS NULL
		UNION SELECT CAST(n.ip_address AS TEXT), CAST(n.vm_id AS TEXT) FROM networks n
		JOIN vms v ON v.id = n.vm_id AND v.deleted_at IS NULL WHERE n.deleted_at IS NULL`).Scan(&held).Error; err != nil {
		return nil, fmt.Errorf("could not read the assigned addresses: %w", err)
	}
	for _, h := range held {
		ip := net.ParseIP(strings.SplitN(h.Address, "/", 2)[0])
		if ip == nil {
			continue
		}
		vmID := strings.ToLower(h.VMID)
		if prev, ok := st.assigned[ip.String()]; ok && prev.VMID != "" && vmID == "" {
			continue // a VM's hold says more than a floating reservation
		}
		st.assigned[ip.String()] = stateHolder{VMID: vmID, Hostname: hostOf[vmID]}
	}
	var links []importLink
	if err := db.Where("source_id = ?", sourceID).Find(&links).Error; err != nil {
		return nil, fmt.Errorf("could not read what was imported before: %w", err)
	}
	for _, l := range links {
		l.TargetID = strings.ToLower(l.TargetID)
		st.links[linkKey(l.Kind, l.SourceKey)] = l
	}
	return st, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Apply imports the chosen preview items, in the order they depend on each
// other, and says what happened to each. Items already imported are skipped,
// so an interrupted import is finished by applying again.
func (s *VirtualizorImportService) Apply(ctx context.Context, id string, refs []ImportItemRef, actor ImportActor) ([]ImportApplyResult, error) {
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	src, err := s.getSource(ctx, id, true)
	if err != nil {
		return nil, err
	}
	if src.Snapshot == nil {
		return nil, ErrImportNoPreview
	}
	if s.readState(src.ID).running {
		return nil, ErrImportReading
	}
	st, err := s.loadState(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	plan := planVirtualizorImport(src.Snapshot, src.Options, st)
	index := map[string]*ImportItem{}
	for i := range plan.Items {
		index[linkKey(plan.Items[i].Kind, plan.Items[i].Key)] = &plan.Items[i]
	}
	// A VM cannot go in without its owner, so choosing a VM brings its owner
	// when that owner is not here yet.
	sorted := append([]ImportItemRef(nil), refs...)
	for _, ref := range refs {
		if it := index[linkKey(ref.Kind, ref.Key)]; it != nil && it.VM != nil {
			if owner := index[linkKey(importKindUser, it.VM.OwnerKey)]; owner != nil && owner.Action != ImportActionExists {
				sorted = append(sorted, ImportItemRef{Kind: importKindUser, Key: it.VM.OwnerKey})
			}
		}
	}
	sort.SliceStable(sorted, func(i, j int) bool { return importKindRank(sorted[i].Kind) < importKindRank(sorted[j].Kind) })

	run := &importRun{s: s, src: src, actor: actor, links: map[string]string{}}
	for k, l := range st.links {
		run.links[k] = l.TargetID
	}
	results := make([]ImportApplyResult, 0, len(sorted))
	done := map[string]bool{}
	for _, ref := range sorted {
		k := linkKey(ref.Kind, ref.Key)
		if done[k] {
			continue
		}
		done[k] = true
		it := index[k]
		if it == nil {
			results = append(results, ImportApplyResult{Kind: ref.Kind, Key: ref.Key, Status: "failed", Message: "It is not in the preview; refresh the preview."})
			continue
		}
		res := ImportApplyResult{Kind: it.Kind, Key: it.Key, Label: it.Label}
		switch it.Action {
		case ImportActionExists:
			res.Status, res.TargetID, res.Message = "skipped", it.TargetID, "Imported before."
		case ImportActionBlocked:
			res.Status, res.Message = "failed", strings.Join(it.Conflicts, " ")
		default:
			var err error
			switch it.Kind {
			case importKindPool:
				err = run.pool(ctx, it, &res)
			case importKindPlan:
				err = run.plan(ctx, it, &res)
			case importKindUser:
				err = run.user(ctx, it, &res)
			case importKindVM:
				err = run.vm(ctx, it, &res)
			}
			if err != nil {
				res.Status, res.Message = "failed", err.Error()
			}
		}
		results = append(results, res)
	}
	return results, nil
}

// importRun is one apply in progress.
type importRun struct {
	s     *VirtualizorImportService
	src   *VirtualizorSource
	actor ImportActor
	links map[string]string // kind|key -> target id, as of now
}

func (r *importRun) link(tx *gorm.DB, kind, key, target string, created bool) error {
	// A link already there points at something deleted since, which this run
	// imported again: it now points at the new one.
	err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_id"}, {Name: "kind"}, {Name: "source_key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{"target_id": target, "created": created, "invited_at": nil}),
	}).Create(&importLink{SourceID: r.src.ID, Kind: kind, SourceKey: key,
		TargetID: target, Created: created, CreatedAt: time.Now().UTC()}).Error
	if err == nil {
		r.links[linkKey(kind, key)] = target
	}
	return err
}

func (r *importRun) audit(ctx context.Context, action, resourceType, target string, it *ImportItem, extra map[string]any) {
	details := map[string]any{"source_id": r.src.ID, "source": r.src.Name, "source_key": it.Key, "label": it.Label}
	for k, v := range extra {
		details[k] = v
	}
	entry := &models.AuditLog{Action: action, ResourceType: resourceType, ResourceID: &target, IPAddress: r.actor.IP,
		UserAgent: r.actor.UserAgent, Details: details}
	if r.actor.UserID != "" {
		uid := r.actor.UserID
		entry.UserID = &uid
	}
	q := r.s.db.WithContext(ctx)
	if entry.IPAddress == "" {
		q = q.Omit("ip_address") // an inet column: "" is not an address
	}
	if err := q.Create(entry).Error; err != nil {
		r.s.logger.Warn("import: could not write an audit row", "action", action, "target", target, "error", err)
	}
}

func (r *importRun) sourcePool(key string) *SourcePool {
	for i := range r.src.Snapshot.Pools {
		if r.src.Snapshot.Pools[i].ID == key {
			return &r.src.Snapshot.Pools[i]
		}
	}
	return nil
}

func (r *importRun) sourceVM(key string) *SourceVM {
	for i := range r.src.Snapshot.VMs {
		if r.src.Snapshot.VMs[i].ID == key {
			return &r.src.Snapshot.VMs[i]
		}
	}
	return nil
}

func (r *importRun) sourceUser(key string) *SourceUser {
	for i := range r.src.Snapshot.Users {
		if r.src.Snapshot.Users[i].ID == key {
			return &r.src.Snapshot.Users[i]
		}
	}
	return nil
}

// heldNote marks an address a source VM still holds.
func (r *importRun) heldNote(vmID string) string {
	name := "#" + vmID
	if v := r.sourceVM(vmID); v != nil && v.Name != "" {
		name = v.Name
	}
	return fmt.Sprintf("held by %s on %s (import)", name, r.src.Name)
}

func (r *importRun) pool(ctx context.Context, it *ImportItem, res *ImportApplyResult) error {
	sp := r.sourcePool(it.Key)
	if sp == nil || it.Pool == nil {
		return errors.New("it is no longer in the snapshot; refresh the preview")
	}
	return r.s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		repo := repository.NewIPAMRepository(tx)
		poolID := it.TargetID
		if it.Action == ImportActionCreate {
			desc := fmt.Sprintf("Imported from %s (pool #%s).", r.src.Name, sp.ID)
			if len(sp.DNS) > 0 {
				desc += " DNS: " + strings.Join(sp.DNS, ", ") + "."
			}
			pool := &models.IPPool{Name: it.Label, Family: it.Pool.Family, CIDR: it.Pool.CIDR, Gateway: sp.Gateway,
				Description: desc, NodeIDs: it.Pool.NodeIDs}
			if net.ParseIP(pool.Gateway) == nil {
				pool.Gateway = ""
			}
			if lo, hi := addressSpan(poolAddressesIn(*sp, it.Pool.CIDR)); lo != "" {
				pool.RangeStart, pool.RangeEnd = lo, hi
			}
			if len(pool.Name) > 255 {
				pool.Name = pool.Name[:255]
			}
			if err := repo.CreatePool(ctx, pool); err != nil {
				return fmt.Errorf("could not create the pool: %w", err)
			}
			poolID = pool.ID
		}
		reserved, err := r.addPoolAddresses(ctx, tx, poolID, it.Pool.Family, it.Pool.CIDR, sp, it.Action == ImportActionCreate)
		if err != nil {
			return err
		}
		if err := r.link(tx, importKindPool, it.Key, poolID, it.Action == ImportActionCreate); err != nil {
			return err
		}
		res.TargetID = poolID
		if it.Action == ImportActionCreate {
			n := len(poolAddressesIn(*sp, it.Pool.CIDR))
			res.Status, res.Message = "created", fmt.Sprintf("Pool %s created with %d addresses.", it.Pool.CIDR, n)
			r.audit(ctx, "import.ip_pool.created", "IP_POOL", poolID, it, map[string]any{"cidr": it.Pool.CIDR, "addresses": n})
		} else {
			res.Status, res.Message = "linked", fmt.Sprintf("Uses the panel's pool %s; %d address(es) held by source VMs reserved.", it.TargetLabel, reserved)
			r.audit(ctx, "import.ip_pool.linked", "IP_POOL", poolID, it, map[string]any{"reserved": reserved})
		}
		return nil
	})
}

// addPoolAddresses records a source pool's addresses in a panel pool. Every
// address a source VM holds is reserved until that VM is imported, so the
// panel never hands it to a new VM meanwhile. all also adds the free ones.
func (r *importRun) addPoolAddresses(ctx context.Context, tx *gorm.DB, poolID, family, cidr string, sp *SourcePool, all bool) (int, error) {
	repo := repository.NewIPAMRepository(tx)
	reserved := 0
	gw := net.ParseIP(sp.Gateway)
	for _, a := range poolAddressesIn(*sp, cidr) {
		ip := net.ParseIP(a.IP)
		if ip == nil || (gw != nil && ip.Equal(gw)) || ipFamily(ip) != family {
			continue
		}
		if a.VMID == "" && !all {
			continue
		}
		existing, err := repo.FindAddressForUpdate(ctx, poolID, ip.String())
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			row := &models.IPAddress{PoolID: poolID, Address: ip.String(), Family: family, Status: models.IPAddressStatusAvailable}
			if a.VMID != "" {
				row.Status, row.Note = models.IPAddressStatusReserved, r.heldNote(a.VMID)
				reserved++
			}
			if err := repo.CreateAddress(ctx, row); err != nil {
				return reserved, fmt.Errorf("could not record %s: %w", ip, err)
			}
		case err != nil:
			return reserved, err
		case a.VMID != "" && existing.Status == models.IPAddressStatusAvailable:
			if err := tx.Model(&models.IPAddress{}).Where("id = ?", existing.ID).
				Updates(map[string]interface{}{"status": models.IPAddressStatusReserved, "note": r.heldNote(a.VMID)}).Error; err != nil {
				return reserved, err
			}
			reserved++
		}
	}
	return reserved, nil
}

// addressSpan is the lowest and highest address of a list.
func addressSpan(addrs []SourceAddress) (string, string) {
	var lo, hi net.IP
	for _, a := range addrs {
		ip := net.ParseIP(a.IP)
		if ip == nil {
			continue
		}
		if lo == nil || ipCompare(ip, lo) < 0 {
			lo = ip
		}
		if hi == nil || ipCompare(ip, hi) > 0 {
			hi = ip
		}
	}
	if lo == nil {
		return "", ""
	}
	return lo.String(), hi.String()
}

func (r *importRun) plan(ctx context.Context, it *ImportItem, res *ImportApplyResult) error {
	return r.s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		target := it.TargetID
		if it.Action == ImportActionCreate {
			d := it.Plan
			p, err := NewPlanService(repository.NewPlanRepository(tx)).CreatePlan(ctx, &PlanRequest{Name: d.Name, CPU: d.CPU, RAM: d.RAMMB,
				Disk: d.DiskGB, BandwidthMbps: d.BandwidthMbps, DataQuotaGB: d.DataQuotaGB,
				Description: fmt.Sprintf("Imported from %s (plan #%s).", r.src.Name, it.Key)})
			if err != nil {
				return fmt.Errorf("could not create the plan: %w", err)
			}
			target = p.ID
		}
		if err := r.link(tx, importKindPlan, it.Key, target, it.Action == ImportActionCreate); err != nil {
			return err
		}
		res.TargetID = target
		if it.Action == ImportActionCreate {
			res.Status, res.Message = "created", "Plan "+it.Plan.Name+" created."
			r.audit(ctx, "import.plan.created", "PLAN", target, it, nil)
		} else {
			res.Status, res.Message = "linked", "Uses the panel's plan "+it.TargetLabel+"."
			r.audit(ctx, "import.plan.linked", "PLAN", target, it, nil)
		}
		return nil
	})
}

func (r *importRun) user(ctx context.Context, it *ImportItem, res *ImportApplyResult) error {
	su := r.sourceUser(it.Key)
	if su == nil {
		return errors.New("it is no longer in the snapshot; refresh the preview")
	}
	created := false
	var target string
	err := r.s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var found models.User
		ferr := tx.Unscoped().Where("LOWER(email) = ?", su.Email).First(&found).Error
		switch {
		case ferr == nil:
			if found.DeletedAt.Valid {
				return errors.New("an account with this email was deleted from this panel; its email stays taken")
			}
			if found.Role != models.RoleClient {
				return errors.New("the email belongs to a staff account here")
			}
			target = found.ID.String()
		case errors.Is(ferr, gorm.ErrRecordNotFound):
			// Nobody knows this password: the owner chooses theirs from the invitation.
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return err
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(secret)), bcrypt.DefaultCost)
			if err != nil {
				return err
			}
			name := su.Name
			if len(name) > 255 {
				name = name[:255]
			}
			u := &models.User{Name: name, Email: su.Email, PasswordHash: string(hash), Role: models.RoleClient}
			if err := repository.NewUserRepository(tx).Create(ctx, u); err != nil {
				return fmt.Errorf("could not create the account: %w", err)
			}
			if err := ApplyNewClientQuota(ctx, tx, u.ID.String()); err != nil {
				return err
			}
			target, created = u.ID.String(), true
		default:
			return ferr
		}
		return r.link(tx, importKindUser, it.Key, target, created)
	})
	if err != nil {
		return err
	}
	res.TargetID = target
	if created {
		res.Status, res.Message = "created", "Account created with no usable password; not emailed."
		r.audit(ctx, "import.user.created", "USER", target, it, map[string]any{"email": su.Email})
	} else {
		res.Status, res.Message = "linked", "Uses the existing account."
		r.audit(ctx, "import.user.linked", "USER", target, it, map[string]any{"email": su.Email})
	}
	return nil
}

func (r *importRun) vm(ctx context.Context, it *ImportItem, res *ImportApplyResult) error {
	d := it.VM
	sv := r.sourceVM(it.Key)
	if sv == nil || d == nil || d.DomainUUID == "" {
		return errors.New("it is no longer in the snapshot; refresh the preview")
	}
	owner := r.links[linkKey(importKindUser, d.OwnerKey)]
	if owner == "" {
		return fmt.Errorf("its owner %s has not been imported; include them", d.OwnerLabel)
	}
	var planID *string
	if p := r.links[linkKey(importKindPlan, d.PlanKey)]; p != "" {
		planID = &p
	}
	var billingRef *string
	if d.BillingRef != "" {
		ref := d.BillingRef
		billingRef = &ref
	}
	db := r.s.db.WithContext(ctx)
	imports := r.s.imports
	speed, quota := importSpeedMbps(sv.SpeedKBs), max(sv.BandwidthGB, 0)

	var warnings []string
	vm, err := imports.vmRepo.GetByID(ctx, d.DomainUUID)
	switch {
	case err == nil:
		if vm.SourceMigration != "virtualizor" {
			return errors.New("a VM with this ID was created in this panel; it is left alone")
		}
		if vm.UserID != owner {
			taken, err := imports.vmRepo.HostnameExistsForUser(ctx, owner, vm.Hostname, vm.ID)
			if err != nil {
				return err
			}
			if taken {
				return fmt.Errorf("%s already has a VM named %s", d.OwnerLabel, vm.Hostname)
			}
			if err := db.Table("vms").Where("id = ?", vm.ID).Update("user_id", owner).Error; err != nil {
				return fmt.Errorf("could not change its owner: %w", err)
			}
			vm.UserID = owner
		}
		res.Status = "updated"
	case errors.Is(err, gorm.ErrRecordNotFound):
		taken, err := imports.vmRepo.HostnameExistsForUser(ctx, owner, d.Hostname, "")
		if err != nil {
			return err
		}
		if taken {
			return fmt.Errorf("%s already has a VM named %s", d.OwnerLabel, d.Hostname)
		}
		dom := findDomain(r.src.Snapshot.scan(d.NodeID), *sv)
		if dom == nil {
			return errors.New("its libvirt domain is no longer in the snapshot; refresh the preview")
		}
		spec := importedVMSpec{NodeID: d.NodeID, UserID: owner, TemplateID: d.TemplateID,
			Hostname: d.Hostname, IPs: append([]string{}, d.IPs...), BandwidthMbps: speed, QuotaGB: quota}
		if dom.VNC != nil && models.IsSealed(dom.VNC.Password) {
			spec.VNCPassword, _ = models.OpenSecret(dom.VNC.Password)
		}
		vm, warnings, err = imports.createImportedVM(ctx, spec, dom)
		if err != nil {
			return err
		}
		res.Status = "created"
	default:
		return err
	}
	res.TargetID = vm.ID

	if res.Status == "updated" {
		warnings = imports.recordImportedAddresses(ctx, vm, d.IPs, speed, quota)
	}
	newPlan, newRef := vm.PlanID, vm.BillingRef
	if planID != nil {
		newPlan = planID
	}
	if billingRef != nil {
		newRef = billingRef
	}
	if err := writeBillingLink(db, vm, newPlan, newRef); err != nil {
		return err
	}
	if sv.Suspended && !vm.Suspended {
		if err := db.Table("vms").Where("id = ?", vm.ID).Update("suspended", true).Error; err != nil {
			return fmt.Errorf("could not keep it suspended: %w", err)
		}
	}
	if err := r.link(db, importKindVM, it.Key, vm.ID, res.Status == "created"); err != nil {
		return err
	}
	msg := "Adopted on node " + d.NodeName + "."
	action := "import.vm.adopted"
	if res.Status == "updated" {
		msg, action = "Already in the panel; owner, plan and billing reference brought in line.", "import.vm.updated"
	}
	if len(warnings) > 0 {
		msg += " " + strings.Join(warnings, "; ") + "."
	}
	res.Message = msg
	r.audit(ctx, action, "VM", vm.ID, it, map[string]any{"owner_id": owner, "plan_id": deref(newPlan), "billing_ref": deref(newRef),
		"node_id": d.NodeID, "addresses": d.IPs, "suspended": sv.Suspended})
	return nil
}

// SendInvites emails every account this source's import created, and has not
// invited yet, a link to choose its password. It returns how many were sent.
func (s *VirtualizorImportService) SendInvites(ctx context.Context, id string, actor ImportActor) (int, error) {
	// Two sends at once would both pick the same uninvited accounts.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()
	src, err := s.getSource(ctx, id, false)
	if err != nil {
		return 0, err
	}
	cfg, ok, err := LoadSMTPSettings(s.db.WithContext(ctx))
	if err != nil {
		return 0, err
	}
	if !ok {
		return 0, ErrImportNoMail
	}
	base := PublicURL()
	if base == "" {
		return 0, ErrImportNoPanelURL
	}
	var rows []struct {
		SourceKey, UserID, Email, Name string
	}
	if err := s.db.WithContext(ctx).Raw(`SELECT l.source_key, CAST(u.id AS TEXT) AS user_id, u.email, u.name FROM import_links l
		JOIN users u ON u.id = l.target_id AND u.deleted_at IS NULL
		WHERE l.source_id = ? AND l.kind = ? AND l.created AND l.invited_at IS NULL ORDER BY u.email`, src.ID, importKindUser).Scan(&rows).Error; err != nil {
		return 0, err
	}
	sent := 0
	repo := repository.NewEnrollmentRepository(s.db)
	tmpl := cfg.Template(EmailAccountImported)
	logo := EmailLogoURL(cfg.Brand, base)
	// Two master accounts the import folded into one panel account (the same
	// address in another case) get one email, not one each.
	emailed := map[string]bool{}
	for _, row := range rows {
		if emailed[row.UserID] {
			if err := s.db.WithContext(ctx).Model(&importLink{}).Where("source_id = ? AND kind = ? AND source_key = ?", src.ID, importKindUser, row.SourceKey).
				Update("invited_at", time.Now().UTC()).Error; err != nil {
				return sent, err
			}
			continue
		}
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return sent, err
		}
		raw := hex.EncodeToString(buf)
		uid, err := uuid.Parse(row.UserID)
		if err != nil {
			continue
		}
		tok := &models.PasswordResetToken{ID: uuid.New(), UserID: uid, TokenHash: hashResetToken(raw), ExpiresAt: time.Now().Add(importInviteTTL)}
		if err := repo.CreatePasswordResetToken(ctx, tok); err != nil {
			return sent, fmt.Errorf("could not issue the link for %s: %w", row.Email, err)
		}
		msg := RenderEmail(tmpl, cfg.Brand, EmailData{Name: row.Name, Email: row.Email, Link: base + "/reset-password?token=" + raw, LogoURL: logo})
		if err := queueRendered(ctx, s.db, EmailAccountImported, row.Email, msg, emailRefs{userID: row.UserID}); err != nil {
			return sent, fmt.Errorf("could not queue the invitation to %s: %w", row.Email, err)
		}
		if err := s.db.WithContext(ctx).Model(&importLink{}).Where("source_id = ? AND kind = ? AND source_key = ?", src.ID, importKindUser, row.SourceKey).
			Update("invited_at", time.Now().UTC()).Error; err != nil {
			return sent, err
		}
		emailed[row.UserID] = true
		sent++
	}
	entry := &models.AuditLog{Action: "import.invites_sent", ResourceType: "IMPORT", ResourceID: &src.ID, IPAddress: actor.IP,
		UserAgent: actor.UserAgent, Details: map[string]any{"source": src.Name, "sent": sent}}
	if actor.UserID != "" {
		entry.UserID = &actor.UserID
	}
	q := s.db.WithContext(ctx)
	if entry.IPAddress == "" {
		q = q.Omit("ip_address")
	}
	if err := q.Create(entry).Error; err != nil {
		s.logger.Warn("import: could not write an audit row", "action", entry.Action, "error", err)
	}
	return sent, nil
}

// PendingInvites counts the accounts this source's import created that have
// not been sent their invitation.
func (s *VirtualizorImportService) PendingInvites(ctx context.Context, id string) (int64, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&importLink{}).
		Where("source_id = ? AND kind = ? AND created AND invited_at IS NULL", id, importKindUser).Count(&n).Error
	return n, err
}
