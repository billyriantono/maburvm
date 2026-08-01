// Package diskops defines the narrow, pure-Go contracts that later C2
// coordinator layers (C2-B attach, C2-C detach/destroy) will use to drive a
// fail-closed, idempotent, destructive disk lifecycle. It is intentionally
// free of: CGO/libvirt, the protobuf package, and the operations manifest
// package — so it can be unit-tested without a live hypervisor and reused by
// the agent handlers without import cycles.
//
// The contracts here describe capabilities, not implementations:
//   - ManagedStorage: safe, policy-bound resolution + verified destroy of a
//     managed volume (dir/LVM/ZFS) with strict allowlisted pools/roots.
//   - Hypervisor: libvirt-domain snapshot / attach / detach / domain-absence
//     observation (implemented later in C2-B/C behind this seam).
//   - ManifestStore: durable idempotency record (the operations.Store satisfies
//     this; kept here only as a minimal interface so coordinators don't import
//     the concrete package).
//
// Presence is tri-state on purpose: only an explicit, proven absence may be
// reported Absent. Anything ambiguous (command/transport failure, unverifiable
// post-delete state) is Unknown, which the coordinator must map to a
// non-success PRESENT/UNKNOWN disposition so the panel retains quota.
package diskops

import "context"

// Presence is the tri-state observation of a resource's existence.
//
// Unknown is the ZERO value on purpose: any code that reads an uninitialized or
// externally-forged Presence fails closed to Unknown rather than accidentally
// treating "not proven present" as "proven absent". Only an explicit, proven
// absence may be reported Absent.
type Presence int

const (
	// Unknown means existence could not be proven either way (fail-closed). It
	// is the zero value so an uninitialized/forged Presence is never treated as
	// proven absent.
	Unknown Presence = iota
	// Absent means the resource is proven gone (or idempotently already gone).
	Absent
	// Present means the resource is confirmed to exist.
	Present
)

// Normalize maps any value outside the set {Unknown, Absent, Present} back to
// Unknown. It is used at observation boundaries so a forged/invalid Presence
// can never masquerade as a proven presence or absence.
func (p Presence) Normalize() Presence {
	switch p {
	case Unknown, Absent, Present:
		return p
	default:
		return Unknown
	}
}

func (p Presence) String() string {
	switch p.Normalize() {
	case Absent:
		return "absent"
	case Present:
		return "present"
	default:
		return "unknown"
	}
}

// Backend enumerates the supported managed-storage backends.
type Backend string

const (
	BackendDir Backend = "dir"
	BackendLVM Backend = "lvm"
	BackendZFS Backend = "zfs"
)

// VolumeRef is a resolved, validated reference to a managed volume. It is
// produced deterministically from (vmID, operationID, backend, pool, sizeGB),
// so the SAME operation always resolves to the SAME ref and a DIFFERENT
// operation resolves to a DIFFERENT ref. It is NOT derived from an arbitrary
// caller-supplied path.
type VolumeRef struct {
	// VMID is the VM that owns this volume. It is populated by both ResolveCreate
	// (deterministic) and ClassifyLegacy (verified ownership) and is used to
	// enforce that a ref cannot be forged to target another VM's storage.
	VMID string
	// Backend is the storage backend.
	Backend Backend
	// Pool is the allowlisted pool identity: the directory root (dir), the LVM
	// volume group (lvm), or the ZFS dataset (zfs).
	Pool string
	// Name is the strictly validated volume name within the pool (basename for
	// dir; LV name for lvm; leaf dataset for zfs).
	Name string
	// ResolvedPath is the canonical, absolute path/device the backend manages
	// (a file path for dir, /dev/<vg>/<lv> for lvm, /dev/zvol/<ds>/<leaf> for
	// zfs). Populated only after successful ResolveCreate.
	ResolvedPath string
	// SizeGB is the requested virtual size in GiB.
	SizeGB int
}

// VolumeObservation is the result of inspecting a managed volume's existence
// and, when present, its identity/size.
type VolumeObservation struct {
	Presence Presence
	// VirtualSizeBytes is the verified virtual/logical size when Present.
	VirtualSizeBytes int64
	// ObservedPath is the canonical path/device seen during inspection.
	ObservedPath string
}

// Attachment describes a libvirt disk attachment observation.
type Attachment struct {
	// Present reports whether the device is attached to the domain.
	Present bool
	// Device is the target dev (e.g. "vdb").
	Device string
	// Path is the backing source path/device seen in the domain XML.
	Path string
}

// DomainSnapshot is a durable capture of a domain's managed disk paths and
// attachment set, taken BEFORE any destructive action (so it survives undefine).
type DomainSnapshot struct {
	// VMID is the domain UUID/name.
	VMID string
	// ManagedPaths lists every managed (allowlisted-pool) disk path discovered
	// in the domain XML, in deterministic order.
	ManagedPaths []string
	// Attachments lists every disk attachment observed (device + path).
	Attachments []Attachment
}

// ManagedStorage resolves and verified-destroys managed volumes under a strict,
// allowlisted policy. Implementations must never accept an arbitrary path as
// authority: the RPC/operation pool input must exactly match a configured
// trusted root/pool, and legacy recorded paths may only be classified when they
// are a direct child of an allowlisted pool and match safe VM-owned naming.
type ManagedStorage interface {
	// ResolveCreate returns the deterministic VolumeRef for a proposed new
	// volume without performing any side effect. Same (vmID, operationID,
	// backend, pool, sizeGB) always yields the same ref; different inputs yield
	// a different ref.
	ResolveCreate(ctx context.Context, vmID, operationID string, backend Backend, pool string, sizeGB int) (VolumeRef, error)

	// Inspect reports the current presence/size of a previously resolved ref.
	Inspect(ctx context.Context, ref VolumeRef) (VolumeObservation, error)

	// VerifyReuse returns the observation for an existing ref whose logical
	// size is positively verified EQUAL to ref.SizeGB. A size mismatch yields a
	// typed error (non-success): the volume must NOT be reused.
	VerifyReuse(ctx context.Context, ref VolumeRef) (VolumeObservation, error)

	// DestroyVerified deletes the volume behind ref (if present) and then
	// re-inspects to PROVE absence. Fail-closed contract (Oracle C2-A1):
	//   - already absent (initial inspect cleanly proves absent) -> Absent, nil
	//   - deleted and clean post-delete inspect proves absent -> Absent, nil
	//   - delete failed AND post-delete inspect proves present -> Present, error
	//   - delete failed AND post-delete inspect proves absent -> Unknown, error
	//     (NEVER Absent with a non-nil error; the agent's view is inconclusive)
	//   - delete failed AND post-delete inspect unknown/error -> Unknown, error
	//   - initial inspect error/unknown/malformed -> Unknown, error (NO delete)
	//   - post-delete inspect error/unknown/malformed -> Unknown, error
	// Invariants:
	//   - A delete error MUST NEVER yield (Absent): any delete error maps to
	//     (Present,error) when still present, or (Unknown,error) otherwise.
	//   - (Absent, non-nil error) is NEVER produced. Only (Absent, nil) and
	//     (Present,error)/(Unknown,error) are legal outcomes.
	DestroyVerified(ctx context.Context, ref VolumeRef) (Presence, error)

	// ClassifyLegacy validates an already-recorded/observed detached path as a
	// managed volume owned by vmID under the configured policy. Returns the
	// corresponding VolumeRef, or an error if the path is arbitrary, traverses,
	// is a sibling-prefix trick, nested, a symlink, a device path, or uses an
	// unsupported extension/type. Only direct children of an allowlisted pool
	// matching safe VM-owned name conventions are accepted.
	ClassifyLegacy(ctx context.Context, vmID, path string) (VolumeRef, error)
}

// Hypervisor abstracts the libvirt domain observation/attach/detach seam so the
// coordinator can be tested with a fake. It must NOT perform undefine or storage
// deletion itself — those are driven by the coordinator through ManagedStorage
// and a separate domain-undefine call.
type Hypervisor interface {
	// SnapshotDomain captures the domain's managed disk paths and attachments
	// BEFORE any destructive action. Returns an error if the domain is absent
	// (coordinator decides idempotency) or the XML cannot be read.
	SnapshotDomain(ctx context.Context, vmID string) (DomainSnapshot, error)

	// InspectAttachment reports whether the given device is attached.
	InspectAttachment(ctx context.Context, vmID, device string) (Attachment, error)

	// Detach detaches the given device from the domain (config + live).
	Detach(ctx context.Context, vmID, device string) error

	// DomainAbsent reports whether the domain itself is gone (post-undefine).
	DomainAbsent(ctx context.Context, vmID string) (bool, error)

	// Undefine removes the persistent domain definition (called by coordinator
	// only after disk paths are snapshotted and, for verified destroy, storage).
	Undefine(ctx context.Context, vmID string) error
}

// ManifestState mirrors the durable manifest lifecycle states the coordinator
// observes (declared locally to avoid coupling diskops to the operations
// package; the concrete operations.Store maps onto these).
type ManifestState string

const (
	ManifestStateInProgress ManifestState = "in_progress"
	ManifestStateCompleted  ManifestState = "completed"
	ManifestStateUncertain  ManifestState = "uncertain"
)

// ManifestRecord is the minimal idempotency record the coordinator reads from a
// ManifestStore, free of the concrete operations package type.
type ManifestRecord struct {
	Version     int
	OperationID string
	VMID        string
	Kind        string
	Fingerprint string
	State       ManifestState
	// Terminal reports whether the record is in a terminal state.
	Terminal bool
}

// ManifestStore is the minimal idempotency surface the coordinator needs. The
// concrete operations.Store satisfies it; declared here so coordinators depend
// on the contract, not the package.
type ManifestStore interface {
	// BeginOrLoad atomically records an in-progress manifest before external
	// side effects, or returns the same-fingerprint existing record. kind is the
	// operation family (e.g. "attach_disk"); meta is an opaque, backend-specific
	// descriptor encoded by the caller (the store validates/persists it). A
	// returned (nil, err) with a mismatch sentinel signals conflicting reuse.
	BeginOrLoad(vmID string, kind string, operationID, fingerprint string, meta interface{}) (*ManifestRecord, error)
}
