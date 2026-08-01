// Package operations is a pure-Go, CGO-free foundation for durable on-agent
// destructive operation manifests. It is the Lane C1 building block that a later
// lane (C2) will wire into the agent's libvirt/storage handlers for the
// fail-closed, idempotent disk/storage lifecycle contract.
//
// Design constraints (Oracle Lane C1):
//   - No dependency on libvirt, gRPC, the panel DB, or any CGO package. Keeping
//     it libvirt/panel-free lets it be unit-tested deterministically and reused
//     by attach-disk, detach-disk and destroy-vm without import cycles.
//   - The manifest is the durable idempotency store. It must survive an agent
//     restart: the Store loads records from disk on demand. Domain XML metadata
//     is deliberately NOT used (it disappears on undefine).
//   - Persistence is atomic and durable: 0700 manifest dir, 0600 files, temp
//     write + file sync + close + rename + parent-dir sync. No request details
//     are ever logged (only opaque IDs / non-secret error codes).
//   - Per-VM in-process serialization prevents two operations on the same VM
//     from racing through the pre-action "begin" check and the terminal write.
//   - Operation IDs are opaque but bounded and validated; manifest filenames are
//     derived from a hash of the ID (never the raw ID) so a hostile/odd ID cannot
//     escape the manifest directory via traversal.
//
// This package only records and replays intent/results. It does NOT perform any
// libvirt or storage action — C2 owns that integration.
package operations

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Kind enumerates the destructive operation families that persist a manifest.
type Kind string

const (
	KindAttachDisk Kind = "attach_disk"
	KindDetachDisk Kind = "detach_disk"
	KindDestroyVM  Kind = "destroy_vm"
)

// State is the lifecycle state of a manifest record.
type State string

const (
	// StateInProgress means the external (libvirt/storage) action has not yet
	// reached a terminal, verified outcome.
	StateInProgress State = "in_progress"
	// StateCompleted means a terminal, verified outcome was reached (success or a
	// fail-closed non-success, e.g. ABSENT or PRESENT with success=false).
	StateCompleted State = "completed"
	// StateUncertain means the agent could not prove the outcome (timeout,
	// transport loss, ambiguous recovery) and must NOT be treated as success.
	StateUncertain State = "uncertain"
)

// Disposition is the fail-closed disk/storage verdict, mirroring the protobuf
// DiskStorageDisposition enum values but kept local (no protobuf import) so this
// package stays free of any serialization/runtime coupling.
type Disposition string

const (
	DispositionUnspecified Disposition = "UNSPECIFIED"
	DispositionUnknown     Disposition = "UNKNOWN"
	DispositionPresent     Disposition = "PRESENT"
	DispositionAttached    Disposition = "ATTACHED"
	DispositionAbsent      Disposition = "ABSENT"
)

// ManifestVersion is bumped only on incompatible record-shape changes.
const ManifestVersion = 1

// Bounds for opaque IDs and metadata to keep manifests bounded and prevent abuse.
const (
	maxIDLen        = 200
	maxFieldLen     = 4096
	manifestPerm    = 0o600
	manifestDirPerm = 0o700

	// maxPaths bounds the number of managed disk paths recorded for a single
	// operation (e.g. destroy-vm) so a hostile caller cannot make the manifest
	// grow without bound.
	maxPaths = 256
)

// controlChars is the set of ASCII control characters rejected in recorded disk
// path strings (defense-in-depth; the manifest filename itself is hashed, but a
// recorded path is still caller-influenced and must never embed control bytes).
const controlChars = "\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f\x10\x11\x12\x13\x14\x15\x16\x17\x18\x19\x1a\x1b\x1c\x1d\x1e\x1f"

// Safe ID characters: alphanumerics, dot, underscore, hyphen. UUIDs (with
// hyphens) and opaque operation tokens both fit. Slashes / backslashes and ".."
// are rejected to defeat path traversal.
var safeIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)

// Typed errors so callers can branch without string matching. None of these
// messages contain request details or secrets.
var (
	// ErrInvalidID is returned for empty/oversized/illegal IDs or metadata that
	// would be unsafe to persist (path traversal, control chars).
	ErrInvalidID = errors.New("operations: invalid or unsafe identifier")
	// ErrMismatch is returned when an operation ID is reused with a different
	// VM, kind, or request fingerprint.
	ErrMismatch = errors.New("operations: operation id reused with conflicting fingerprint")
	// ErrIntegrity is returned when an existing manifest file cannot be parsed
	// (corrupt JSON). We refuse to silently overwrite it.
	ErrIntegrity = errors.New("operations: corrupt manifest (integrity)")
	// ErrNotFound is returned when a manifest for an operation ID does not exist.
	ErrNotFound = errors.New("operations: manifest not found")
	// ErrPersistFailed is returned when durable persistence cannot be confirmed.
	ErrPersistFailed = errors.New("operations: failed to durably persist manifest")
)

// MismatchError carries the conflicting fields for diagnostics without exposing
// request content.
type MismatchError struct {
	OperationID string
	Reason      string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("%v (op=%s: %s)", ErrMismatch, e.OperationID, e.Reason)
}

func (e *MismatchError) Unwrap() error { return ErrMismatch }

// IntegrityError reports a corrupt manifest path for diagnostics.
type IntegrityError struct {
	OperationID string
	Path        string
	Err         error
}

func (e *IntegrityError) Error() string {
	return fmt.Sprintf("%v (op=%s path=%s: %v)", ErrIntegrity, e.OperationID, e.Path, e.Err)
}

func (e *IntegrityError) Unwrap() error { return ErrIntegrity }

// Record is the durable manifest entry for a single destructive operation.
type Record struct {
	Version     int    `json:"version"`
	OperationID string `json:"operation_id"`
	VMID        string `json:"vm_id"`
	Kind        Kind   `json:"kind"`
	Fingerprint string `json:"fingerprint"`

	// Disk metadata discovered / supplied for the operation.
	Device   string `json:"device,omitempty"`
	Path     string `json:"path,omitempty"`
	PoolType string `json:"pool_type,omitempty"`

	// Paths is an explicit, typed, ordered list of EVERY managed disk path the
	// operation touches (used by destroy-vm so C2 can preserve each managed
	// disk before libvirt undefines the domain XML). It is backward-compatible:
	// old manifests serialized with only the singular `path` load unchanged,
	// and the singular Path is still authoritative for attach/detach. Order is
	// preserved exactly as supplied (never sorted/reordered) so callers can
	// rely on deterministic replay.
	Paths []string `json:"paths,omitempty"`

	State       State       `json:"state"`
	Disposition Disposition `json:"disposition"`
	Success     bool        `json:"success"`

	// Non-secret failure classification. Never the raw libvirt/storage error text
	// (which can leak paths); callers map to a coarse code.
	ErrorCode    string `json:"error_code,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`

	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
}

// DiskMeta carries the disk metadata recorded in a manifest. Kept separate so
// callers (C2) can pass it without constructing a full Record.
type DiskMeta struct {
	Device   string
	Path     string
	PoolType string

	// Paths is an explicit, ordered, typed list of EVERY managed disk path the
	// operation touches (used by destroy-vm so C2 can preserve each managed
	// disk before libvirt undefines the domain XML). Order is preserved exactly
	// as supplied. An empty list is valid; when non-empty it is subject to the
	// same fail-closed validation as singular Path (no empty entries, no
	// duplicates, no control chars, bounded count/length). It is never inferred
	// from the singular Path.
	Paths []string
}

// Terminal reports whether the record has reached a terminal state (completed or
// uncertain). In-progress records are not terminal.
func (r *Record) Terminal() bool {
	return r.State == StateCompleted || r.State == StateUncertain
}

// DefaultDir resolves the root manifest directory from MABURVM_DATA_DIR (the
// same knob the secret store and agent cert paths use) or /var/lib/maburvm,
// then a private "operations" child. Tests must pass an explicit temp dir to New.
func DefaultDir() string {
	dir := os.Getenv("MABURVM_DATA_DIR")
	if dir == "" {
		dir = "/var/lib/maburvm"
	}
	return filepath.Join(dir, "operations")
}

// Store persists and loads operation manifests keyed by operation ID, with
// per-VM in-process serialization.
type Store struct {
	root string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// New creates a Store rooted at root (a temp dir in tests; DefaultDir() in
// production). The manifest directory is created 0700 on first write.
func New(root string) *Store {
	return &Store{root: root, locks: make(map[string]*sync.Mutex)}
}

// vmLock returns (creating if needed) the per-VM mutex, holding the Store mutex
// only for the map update so unrelated VMs don't serialize.
func (s *Store) vmLock(vmID string) *sync.Mutex {
	s.mu.Lock()
	l, ok := s.locks[vmID]
	if !ok {
		l = &sync.Mutex{}
		s.locks[vmID] = l
	}
	s.mu.Unlock()
	return l
}

// pathFor derives the manifest file path from a hash of the operation ID. The
// raw ID is never used as a path component, so traversal/odd characters in the
// ID cannot escape root.
func (s *Store) pathFor(operationID string) string {
	sum := sha256.Sum256([]byte(operationID))
	return filepath.Join(s.root, hex.EncodeToString(sum[:])+".json")
}

// Fingerprint returns a stable SHA-256 hex fingerprint over caller-supplied
// canonical bytes. Callers pass a deterministic encoding of the request (e.g.
// sorted JSON or a fixed concatenation); this package does not interpret it.
func Fingerprint(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// validateID checks an opaque/bounded ID is safe to persist.
func validateID(name, v string) error {
	if v == "" {
		return fmt.Errorf("%s: %w (empty)", name, ErrInvalidID)
	}
	if len(v) > maxIDLen {
		return fmt.Errorf("%s: %w (too long %d)", name, ErrInvalidID, len(v))
	}
	if !safeIDRe.MatchString(v) {
		return fmt.Errorf("%s: %w (illegal characters)", name, ErrInvalidID)
	}
	return nil
}

// validateMeta bounds disk metadata fields and rejects control characters /
// path-breaking content in the recorded values (defense-in-depth; the manifest
// filename itself is hashed, but recorded Path is still user-influenced). It
// also validates the Paths list fail-closed: bounded count, no empty entries, no
// duplicates, no control characters, bounded length. Order is preserved exactly.
func validateMeta(m DiskMeta) error {
	fields := []struct {
		name string
		v    string
	}{
		{"device", m.Device},
		{"path", m.Path},
		{"pool_type", m.PoolType},
	}
	for _, f := range fields {
		if f.v == "" {
			continue
		}
		if len(f.v) > maxFieldLen {
			return fmt.Errorf("%s: %w (too long)", f.name, ErrInvalidID)
		}
		if strings.ContainsAny(f.v, controlChars) {
			return fmt.Errorf("%s: %w (control char)", f.name, ErrInvalidID)
		}
	}
	if err := validatePaths(m.Paths); err != nil {
		return err
	}
	return nil
}

// validatePaths fail-closes on a managed path list: it rejects excessive count,
// empty entries, duplicates, control characters, and oversized entries. It
// preserves and checks caller order (it never sorts or deduplicates silently).
func validatePaths(paths []string) error {
	if len(paths) > maxPaths {
		return fmt.Errorf("paths: %w (too many %d)", ErrInvalidID, len(paths))
	}
	seen := make(map[string]struct{}, len(paths))
	for i, p := range paths {
		if p == "" {
			return fmt.Errorf("paths[%d]: %w (empty entry)", i, ErrInvalidID)
		}
		if len(p) > maxFieldLen {
			return fmt.Errorf("paths[%d]: %w (too long)", i, ErrInvalidID)
		}
		if strings.ContainsAny(p, controlChars) {
			return fmt.Errorf("paths[%d]: %w (control char)", i, ErrInvalidID)
		}
		if _, dup := seen[p]; dup {
			return fmt.Errorf("paths[%d]: %w (duplicate %q)", i, ErrInvalidID, p)
		}
		seen[p] = struct{}{}
	}
	return nil
}

// clonePaths defensively copies a caller-supplied path list so that subsequent
// caller mutation cannot alter a returned/persisted Record's slice. A nil input
// stays nil (distinct from an empty non-nil list) to preserve caller intent.
func clonePaths(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

// load reads and parses a manifest from disk. It returns ErrNotFound when the
// file is absent, and *IntegrityError when the file exists but is not valid JSON.
func (s *Store) load(operationID string) (*Record, error) {
	if err := validateID("operation_id", operationID); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.pathFor(operationID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("%w: read: %v", ErrPersistFailed, err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return nil, &IntegrityError{OperationID: operationID, Path: s.pathFor(operationID), Err: errors.New("empty file")}
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, &IntegrityError{OperationID: operationID, Path: s.pathFor(operationID), Err: err}
	}
	return &r, nil
}

// persist writes the record atomically and durably. Failure at any step is
// surfaced as ErrPersistFailed (fail-closed: we never claim durability we can't
// confirm).
func (s *Store) persist(r *Record) error {
	r.UpdatedAt = nowFunc()
	if err := os.MkdirAll(s.root, manifestDirPerm); err != nil {
		return fmt.Errorf("%w: mkdir %q: %v", ErrPersistFailed, s.root, err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("%w: marshal: %v", ErrPersistFailed, err)
	}
	tmp, err := os.CreateTemp(s.root, ".manifest-*.tmp")
	if err != nil {
		return fmt.Errorf("%w: temp: %v", ErrPersistFailed, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if err := tmp.Chmod(manifestPerm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: chmod: %v", ErrPersistFailed, err)
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: write: %v", ErrPersistFailed, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: fsync file: %v", ErrPersistFailed, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: close: %v", ErrPersistFailed, err)
	}
	if err := os.Rename(tmpName, s.pathFor(r.OperationID)); err != nil {
		return fmt.Errorf("%w: rename: %v", ErrPersistFailed, err)
	}
	if err := dirFsync(s.root); err != nil {
		return err
	}
	return nil
}

// dirFsync durably flushes the manifest directory so the rename is persisted
// where the platform supports it. It is a package var (injectable seam) so tests
// can simulate directory fsync failure without an unmountable filesystem.
var dirFsync = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("%w: open dir %q: %v", ErrPersistFailed, dir, err)
	}
	defer func() { _ = d.Close() }()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("%w: fsync dir %q: %v", ErrPersistFailed, dir, err)
	}
	return nil
}

// nowFunc is a seam so tests can assert timestamps deterministically if needed.
var nowFunc = time.Now

// BeginOrLoad atomically records an in-progress manifest BEFORE the external
// (libvirt/storage) action, OR returns the existing same-fingerprint record for
// idempotent retry after response loss.
//
//   - same ID + same VM/kind/fingerprint already in_progress (or even terminal):
//     returns the existing record (replay). This is how a retried attach never
//     creates a second volume.
//   - same ID but different VM/kind/fingerprint: returns *MismatchError (fail
//     closed — never clobber another operation's manifest).
//   - new ID: creates an in_progress record and persists it durably.
func (s *Store) BeginOrLoad(vmID string, kind Kind, operationID, fingerprint string, meta DiskMeta) (*Record, error) {
	if err := validateID("vm_id", vmID); err != nil {
		return nil, err
	}
	if err := validateID("operation_id", operationID); err != nil {
		return nil, err
	}
	if err := validateMeta(meta); err != nil {
		return nil, err
	}
	if fingerprint == "" {
		return nil, fmt.Errorf("fingerprint: %w (empty)", ErrInvalidID)
	}

	lock := s.vmLock(vmID)
	lock.Lock()
	defer lock.Unlock()

	existing, err := s.load(operationID)
	if err == nil {
		// Existing manifest for this ID. Reject conflicting reuse.
		if existing.VMID != vmID || existing.Kind != kind || existing.Fingerprint != fingerprint {
			return nil, &MismatchError{
				OperationID: operationID,
				Reason:      "vm/kind/fingerprint differs from existing manifest",
			}
		}
		return existing, nil // idempotent replay (may be in_progress or terminal)
	}
	if !errors.Is(err, ErrNotFound) {
		// Corrupt manifest or read failure: do not overwrite silently.
		return nil, err
	}

	now := nowFunc()
	r := &Record{
		Version:     ManifestVersion,
		OperationID: operationID,
		VMID:        vmID,
		Kind:        kind,
		Fingerprint: fingerprint,
		Device:      meta.Device,
		Path:        meta.Path,
		PoolType:    meta.PoolType,
		Paths:       clonePaths(meta.Paths),
		State:       StateInProgress,
		Disposition: DispositionUnspecified,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if perr := s.persist(r); perr != nil {
		return nil, perr
	}
	return r, nil
}

// UpdateDiskMeta amends the disk metadata of an in-progress record (e.g. once
// the volume path/device is known after provisioning). It refuses to mutate a
// terminal record.
func (s *Store) UpdateDiskMeta(vmID, operationID string, meta DiskMeta) (*Record, error) {
	if err := validateID("vm_id", vmID); err != nil {
		return nil, err
	}
	if err := validateID("operation_id", operationID); err != nil {
		return nil, err
	}
	if err := validateMeta(meta); err != nil {
		return nil, err
	}

	lock := s.vmLock(vmID)
	lock.Lock()
	defer lock.Unlock()

	existing, err := s.load(operationID)
	if err != nil {
		return nil, err
	}
	if existing.VMID != vmID {
		return nil, &MismatchError{OperationID: operationID, Reason: "vm mismatch"}
	}
	if existing.Terminal() {
		// Terminal records are immutable; replay them rather than mutate.
		return existing, nil
	}
	existing.Device = meta.Device
	existing.Path = meta.Path
	existing.PoolType = meta.PoolType
	existing.Paths = clonePaths(meta.Paths)
	if perr := s.persist(existing); perr != nil {
		return nil, perr
	}
	return existing, nil
}

// Complete finalizes an in-progress (or uncertain) record into a terminal
// completed result. It must NEVER silently overwrite an already-completed record
// with different semantics: a retry of a completed operation replays the recorded
// result unchanged. A corrupt/loaded record with a fingerprint mismatch is
// rejected. Returns the resulting (persisted) record.
func (s *Store) Complete(vmID, operationID, fingerprint string, success bool, disp Disposition, errorCode, errorMessage string) (*Record, error) {
	if err := validateID("vm_id", vmID); err != nil {
		return nil, err
	}
	if err := validateID("operation_id", operationID); err != nil {
		return nil, err
	}

	lock := s.vmLock(vmID)
	lock.Lock()
	defer lock.Unlock()

	existing, err := s.load(operationID)
	if err != nil {
		return nil, err
	}
	if existing.VMID != vmID {
		return nil, &MismatchError{OperationID: operationID, Reason: "vm mismatch"}
	}
	if existing.Fingerprint != fingerprint {
		return nil, &MismatchError{OperationID: operationID, Reason: "fingerprint mismatch"}
	}
	// Idempotent replay: an already-terminal manifest is returned as-is. This is
	// what guarantees a retried attach never performs a second destructive action
	// and that a recorded ABSENT success — or an UNCERTAIN (could-not-prove)
	// outcome — is returned verbatim. Uncertainty must persist and replay on
	// retries (the panel retains quota accounting); a caller that wants to
	// re-attempt after an uncertain result must use a fresh operation ID.
	if existing.Terminal() {
		return existing, nil
	}

	existing.State = StateCompleted
	existing.Success = success
	existing.Disposition = disp
	existing.ErrorCode = errorCode
	existing.ErrorMessage = errorMessage
	existing.CompletedAt = nowFunc()
	if perr := s.persist(existing); perr != nil {
		return nil, perr
	}
	return existing, nil
}

// MarkUncertain marks an in-progress record as uncertain (could not prove the
// outcome). Like Complete, it refuses to overwrite an already-completed record
// with different semantics and rejects fingerprint mismatches. Uncertainty
// persists and replays on retries (the panel retains quota accounting).
func (s *Store) MarkUncertain(vmID, operationID, fingerprint, errorCode, errorMessage string) (*Record, error) {
	if err := validateID("vm_id", vmID); err != nil {
		return nil, err
	}
	if err := validateID("operation_id", operationID); err != nil {
		return nil, err
	}

	lock := s.vmLock(vmID)
	lock.Lock()
	defer lock.Unlock()

	existing, err := s.load(operationID)
	if err != nil {
		return nil, err
	}
	if existing.VMID != vmID {
		return nil, &MismatchError{OperationID: operationID, Reason: "vm mismatch"}
	}
	if existing.Fingerprint != fingerprint {
		return nil, &MismatchError{OperationID: operationID, Reason: "fingerprint mismatch"}
	}
	// Already completed: replay, never downgrade a verified result to uncertain.
	if existing.State == StateCompleted {
		return existing, nil
	}
	existing.State = StateUncertain
	existing.Disposition = DispositionUnknown
	if errorCode != "" {
		existing.ErrorCode = errorCode
	}
	if errorMessage != "" {
		existing.ErrorMessage = errorMessage
	}
	if perr := s.persist(existing); perr != nil {
		return nil, perr
	}
	return existing, nil
}

// Load returns the current manifest for an operation ID (useful for diagnostics
// and for C2 to re-derive state after recovery). It returns ErrNotFound for an
// absent manifest and *IntegrityError for a corrupt one.
func (s *Store) Load(operationID string) (*Record, error) {
	return s.load(operationID)
}

// mustRandom is unused by the store but kept as a tiny entropy helper seam so the
// package can mint operation IDs in the future without importing crypto elsewhere.
var _ = func() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
