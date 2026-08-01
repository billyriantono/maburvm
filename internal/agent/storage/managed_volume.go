// Package storage (managed_volume) implements the strict, policy-bound
// diskops.ManagedStorage contract for dir/LVM/ZFS backends. It is the C2-A
// foundation that later C2-B/C coordinators call for verified attach/detach/
// destroy. It is pure Go (no libvirt/CGO) and never shells out for dir volumes.
//
// Hard safety rules (Oracle fail-closed):
//   - Construction FAILS CLOSED if no trusted roots/pools are configured. There
//     is NO unrestricted fallback: a permissive/empty policy must never silently
//     grant the agent the ability to delete arbitrary files/devices.
//   - The RPC/operation pool input is NOT authority. The pool must EXACTLY match
//     a configured trusted root (dir), volume group (lvm), or dataset (zfs).
//   - Deterministic volume identity is versioned and unambiguously VM-bound:
//     format "mv2-<slug>-<vmTag>-<opTag>" where <vmTag> is a bounded cryptographic
//     (SHA-256) tag of the EXACT vmID, <slug> is a safe name form, and <opTag> is
//     a bounded cryptographic tag of (vmID|operationID|backend|pool|sizeGB). The
//     <vmTag> lets validateRef PROVE the exact VM binding: VM A can never forge a
//     name carrying VM B's vmTag. The only legacy backward-compatible dir
//     filename is EXACTLY "<vmID>.qcow2" | "<vmID>.img" | "<vmID>.raw"; the old
//     ambiguous "maburvm-<slug>-<hash>" family is rejected.
//   - A configured dir root is RE-VALIDATED at operation time (not only at
//     construction): it must remain a canonical, existing, non-symlink directory.
//     Inability to confirm it makes Inspect Unknown / the destructive path fail
//     closed.
//   - Ref fields are never authority: every Inspect/VerifyReuse/Destroy/delete
//     revalidates the exact canonical pool/name/resolved path and exact VM
//     ownership, and the allowed backend grammar.
//   - Presence is tri-state. A successful structured LIST that does not contain
//     the exact item means Absent. A command FAILURE means Unknown — we never
//     infer absence from an error.
//   - DestroyVerified only reports Absent after a post-delete inspect PROVES
//     absence (already-absent is idempotent -> Absent,nil; delete-success and
//     clean post-inspect proven-absent -> Absent,nil). A delete error NEVER
//     yields Absent: post-proven-present -> Present,error; post-proven-absent OR
//     any inconclusive/Unknown post-state -> Unknown,error. An inconclusive
//     initial inspect yields Unknown,error with NO delete command. It NEVER
//     returns (Absent, non-nil error).
//   - An existing deterministic volume may only be reused after its logical/
//     virtual size is positively verified EQUAL to the request (strict positive
//     integer bytes); a mismatch is a typed non-success error, never silent reuse.
//   - This strict path never delegates to the old permissive
//     VolumeManager.DeleteVolume.
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/maburvm/panel/internal/agent/diskops"
)

// ManagedVolumeBackend is the strict diskops.ManagedStorage implementation.
type ManagedVolumeBackend struct {
	// dirRoots are the allowlisted directory pool roots (absolute).
	dirRoots map[string]struct{}
	// lvmVGs are the allowlisted LVM volume groups.
	lvmVGs map[string]struct{}
	// zfsDatasets are the allowlisted ZFS datasets (parent dataset; leaf is the
	// volume name).
	zfsDatasets map[string]struct{}

	// runner executes external commands (lvs/zfs). Injectable for
	// tests so no real LVM/ZFS is required. The CommandResult seam is defined
	// in managed_command.go and returns separated stdout/stderr.
	runner commandRunner
}

// NewManagedVolumeBackendFromEnv builds a strict backend from configuration.
// defaultDir seeds the dir-root allowlist when non-empty AND valid; callers
// should pass the agent's default image dir (e.g. /var/lib/libvirt/images).
// Trusted roots/pools come from MABURVM_MANAGED_DIR_ROOTS (comma/space
// separated absolute dirs), MABURVM_MANAGED_LVM_VGS (VGs), and
// MABURVM_MANAGED_ZFS_DATASETS (datasets). Construction fails closed if NO
// trusted pool of any kind is configured (no unrestricted fallback).
func NewManagedVolumeBackendFromEnv(defaultDir string) (*ManagedVolumeBackend, error) {
	b := &ManagedVolumeBackend{
		dirRoots:    map[string]struct{}{},
		lvmVGs:      map[string]struct{}{},
		zfsDatasets: map[string]struct{}{},
		runner:      osRunner{},
	}
	if defaultDir != "" {
		if err := validateAbsDir(defaultDir); err != nil {
			return nil, fmt.Errorf("managed volume: default dir invalid: %w", err)
		}
		b.dirRoots[filepath.Clean(defaultDir)] = struct{}{}
	}
	for _, d := range splitList(envOr("MABURVM_MANAGED_DIR_ROOTS", "")) {
		if err := validateAbsDir(d); err != nil {
			return nil, fmt.Errorf("managed volume: dir root %q invalid: %w", d, err)
		}
		b.dirRoots[filepath.Clean(d)] = struct{}{}
	}
	for _, vg := range splitList(envOr("MABURVM_MANAGED_LVM_VGS", "")) {
		if err := validateLVMName(vg, "vg"); err != nil {
			return nil, fmt.Errorf("managed volume: lvm vg %q invalid: %w", vg, err)
		}
		b.lvmVGs[vg] = struct{}{}
	}
	for _, ds := range splitList(envOr("MABURVM_MANAGED_ZFS_DATASETS", "")) {
		if err := validateZFSDataset(ds); err != nil {
			return nil, fmt.Errorf("managed volume: zfs dataset %q invalid: %w", ds, err)
		}
		b.zfsDatasets[ds] = struct{}{}
	}
	if len(b.dirRoots) == 0 && len(b.lvmVGs) == 0 && len(b.zfsDatasets) == 0 {
		return nil, errors.New("managed volume: no trusted dir roots, LVM VGs, or ZFS datasets configured (refusing unrestricted fallback)")
	}
	return b, nil
}

// ErrSizeMismatch is returned by VerifyReuse when an existing volume's verified
// size differs from the requested size (must NOT be reused).
var ErrSizeMismatch = errors.New("managed volume: existing volume size differs from requested")

// ErrNotManaged is returned when a path/pool fails policy classification.
var ErrNotManaged = errors.New("managed volume: path/pool not classified as managed under policy")

// ErrPoolNotAllowed is returned when an operation's pool input does not exactly
// match a configured trusted pool.
var ErrPoolNotAllowed = errors.New("managed volume: pool not in allowlist")

// ErrRefForged is returned when a VolumeRef's fields are internally inconsistent
// or no longer match the configured allowlist (RPC/ref fields are never
// authority; every action revalidates the ref against policy).
var ErrRefForged = errors.New("managed volume: ref failed allowlist/path consistency revalidation")

// ErrInspectInconclusive is returned when an initial/post-delete inspection
// cannot determine presence; callers must NOT proceed to a destructive action.
var ErrInspectInconclusive = errors.New("managed volume: initial inspection inconclusive; refusing destructive action")

// maxSizeGB is the inclusive safe upper bound (in GiB) for a requested size,
// derived from math.MaxInt64 so that sizeGBToBytes can reject out-of-range
// inputs BEFORE any multiplication (no reliance on int overflow wrapping).
// It is the largest int such that int64(gib)*gibibyte does not exceed
// math.MaxInt64. Computed with integer division to stay within int64.
const maxSizeGB = int(math.MaxInt64 / gibibyte)

// --- deterministic identity ---

// volumeName derives a strict, VM-owned, versioned, bounded volume name from
// (vmID, operationID, backend, pool, sizeGB). Format "mv2-<slug>-<vmTag>-<opTag>"
// where the cryptographic <vmTag> pins the EXACT vmID so VM A cannot forge a
// name carrying VM B's tag. The opTag makes the name deterministic for the same
// operation and distinct across operations. Format avoids path separators and
// control characters; total length is bounded.
func volumeName(vmID, operationID string, backend diskops.Backend, pool string, sizeGB int) string {
	slug := vmSlug(vmID)
	vmTag := cryptoVMTag(vmID)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%d", vmID, operationID, backend, pool, sizeGB)))
	opTag := hex.EncodeToString(sum[:])[:20]
	return fmt.Sprintf("mv2-%s-%s-%s", slug, vmTag, opTag)
}

// cryptoVMTag returns a bounded (16 hex char) cryptographic tag of the exact
// vmID. It is the unambiguous ownership anchor: only the real owner's vmID
// reproduces it, so a deterministic name cannot be attributed to another VM.
func cryptoVMTag(vmID string) string {
	sum := sha256.Sum256([]byte("vm:" + vmID))
	return hex.EncodeToString(sum[:])[:16]
}

// vmSlug maps a VM ID to a strict, filesystem/name-safe slug (bounded). It is a
// human-readable hint only; ownership is proven by cryptoVMTag, never by slug.
func vmSlug(vmID string) string {
	clean := regexp.MustCompile(`[^A-Za-z0-9._-]`).ReplaceAllString(vmID, "-")
	if len(clean) > 64 {
		clean = clean[:64]
	}
	if clean == "" {
		clean = "vm"
	}
	return clean
}

// deterministicNameOwnedByVM reports whether a basename is a well-formed versioned
// deterministic name (mv2-<slug>-<vmTag>-<opTag>[.qcow2]) owned by EXACTLY the
// given vmID. It recomputes the expected <slug> and <vmTag> for the vmID and
// requires the name to carry them, so VM A cannot forge a deterministic name
// targeting VM B. The <opTag> remainder is checked structurally only (the full
// operationID is not stored on the ref).
func deterministicNameOwnedByVM(name, vmID string) bool {
	if !deterministicNameRe.MatchString(name) {
		return false
	}
	expectedPrefix := "mv2-" + vmSlug(vmID) + "-" + cryptoVMTag(vmID) + "-"
	if !strings.HasPrefix(name, expectedPrefix) {
		return false
	}
	rest := strings.TrimPrefix(name, expectedPrefix)
	if rest == name {
		return false
	}
	if strings.HasSuffix(rest, ".qcow2") {
		rest = strings.TrimSuffix(rest, ".qcow2")
	}
	return opTagRe.MatchString(rest)
}

// legacyDirNameOwnedByVM reports whether a dir basename is EXACTLY the legacy
// "<vmID>.qcow2" | "<vmID>.img" | "<vmID>.raw" convention owned by the given
// vmID. Only this exact form is backward compatible; ambiguous old deterministic
// shapes and sibling/dotted variants are rejected.
func legacyDirNameOwnedByVM(name, vmID string) bool {
	return name == vmID+".qcow2" || name == vmID+".img" || name == vmID+".raw"
}

var (
	safeNameRe = regexp.MustCompile(`^[A-Za-z0-9._-]{1,200}$`)
	// deterministicNameRe matches the versioned deterministic name produced by
	// volumeName: "mv2-<slug>-<vmTag>-<opTag>". The <vmTag> is a cryptographic
	// binding to the exact vmID; this proves VM ownership unambiguously.
	deterministicNameRe = regexp.MustCompile(`^mv2-[A-Za-z0-9._-]{1,64}-[a-f0-9]{16}-[a-f0-9]{20}(\.qcow2)?$`)
	// opTagRe checks the trailing op tag (20 hex), optionally with a .qcow2 ext
	// already stripped by the caller.
	opTagRe     = regexp.MustCompile(`^[a-f0-9]{20}$`)
	legacyExtRe = regexp.MustCompile(`\.(qcow2|img|raw)$`)
)

// sizeGBToBytes converts a GiB size to bytes with overflow protection. Returns
// an error for non-positive or implausibly large inputs.
func sizeGBToBytes(sizeGB int) (int64, error) {
	if sizeGB <= 0 {
		return 0, fmt.Errorf("managed volume: size_gb must be positive: %d", sizeGB)
	}
	if sizeGB > maxSizeGB {
		return 0, fmt.Errorf("managed volume: size_gb implausibly large: %d", sizeGB)
	}
	return int64(sizeGB) * gibibyte, nil
}

// validateDirRootRuntime re-confirms a dir root is still an allowlisted,
// canonical, existing, non-symlink directory at operation time (not only at
// construction). Failure fails closed.
func (b *ManagedVolumeBackend) validateDirRootRuntime(root string) error {
	clean := filepath.Clean(root)
	if _, ok := b.dirRoots[clean]; !ok {
		return fmt.Errorf("%w: dir root %q not allowlisted", ErrRefForged, root)
	}
	if err := validateAbsDir(clean); err != nil {
		return fmt.Errorf("%w: dir root %q failed runtime revalidation: %v", ErrRefForged, clean, err)
	}
	return nil
}

// validateRef revalidates a resolved VolumeRef against the configured policy and
// the exact canonical path derivation. RPC/ref fields are never authority: a ref
// whose Pool/Name/ResolvedPath do not match a configured trusted pool, or whose
// ResolvedPath is not exactly the canonical path for its backend/pool/name, is
// rejected. Dir roots are re-validated at operation time. VM ownership of
// deterministic names is proven via the cryptographic vmTag; the only accepted
// legacy dir form is the exact "<vmID>.<ext>".
func (b *ManagedVolumeBackend) validateRef(ref diskops.VolumeRef) error {
	switch ref.Backend {
	case diskops.BackendDir:
		if err := b.validateDirRootRuntime(ref.Pool); err != nil {
			return err
		}
		if ref.Name == "" || !safeNameRe.MatchString(ref.Name) {
			return fmt.Errorf("%w: invalid name %q", ErrRefForged, ref.Name)
		}
		want := filepath.Join(filepath.Clean(ref.Pool), ref.Name)
		if ref.ResolvedPath != want {
			return fmt.Errorf("%w: path %q != canonical %q", ErrRefForged, ref.ResolvedPath, want)
		}
		if !deterministicNameOwnedByVM(ref.Name, ref.VMID) && !legacyDirNameOwnedByVM(ref.Name, ref.VMID) {
			return fmt.Errorf("%w: dir name %q not owned by vm %q", ErrRefForged, ref.Name, ref.VMID)
		}
		return nil
	case diskops.BackendLVM:
		if _, ok := b.lvmVGs[ref.Pool]; !ok {
			return fmt.Errorf("%w: lvm vg %q not allowlisted", ErrRefForged, ref.Pool)
		}
		if ref.Name == "" || !safeNameRe.MatchString(ref.Name) {
			return fmt.Errorf("%w: invalid lv name %q", ErrRefForged, ref.Name)
		}
		want := "/dev/" + ref.Pool + "/" + ref.Name
		if ref.ResolvedPath != want {
			return fmt.Errorf("%w: path %q != canonical %q", ErrRefForged, ref.ResolvedPath, want)
		}
		if !deterministicNameOwnedByVM(ref.Name, ref.VMID) {
			return fmt.Errorf("%w: lvm name %q not VM-owned", ErrRefForged, ref.Name)
		}
		return nil
	case diskops.BackendZFS:
		if _, ok := b.zfsDatasets[ref.Pool]; !ok {
			return fmt.Errorf("%w: zfs dataset %q not allowlisted", ErrRefForged, ref.Pool)
		}
		if ref.Name == "" || !safeNameRe.MatchString(ref.Name) {
			return fmt.Errorf("%w: invalid leaf %q", ErrRefForged, ref.Name)
		}
		want := "/dev/zvol/" + ref.Pool + "/" + ref.Name
		if ref.ResolvedPath != want {
			return fmt.Errorf("%w: path %q != canonical %q", ErrRefForged, ref.ResolvedPath, want)
		}
		if !deterministicNameOwnedByVM(ref.Name, ref.VMID) {
			return fmt.Errorf("%w: zfs name %q not VM-owned", ErrRefForged, ref.Name)
		}
		return nil
	default:
		return fmt.Errorf("%w: unsupported backend %q", ErrRefForged, ref.Backend)
	}
}

// --- ManagedStorage implementation ---

// ResolveCreate returns the deterministic VolumeRef for a proposed volume. It
// performs NO side effect. The pool must exactly match a configured trusted
// pool for the requested backend.
func (b *ManagedVolumeBackend) ResolveCreate(ctx context.Context, vmID, operationID string, backend diskops.Backend, pool string, sizeGB int) (diskops.VolumeRef, error) {
	if err := validateSafeID("vm_id", vmID); err != nil {
		return diskops.VolumeRef{}, err
	}
	if err := validateSafeID("operation_id", operationID); err != nil {
		return diskops.VolumeRef{}, err
	}
	if sizeGB <= 0 {
		return diskops.VolumeRef{}, fmt.Errorf("managed volume: size_gb must be positive: %d", sizeGB)
	}
	switch backend {
	case diskops.BackendDir:
		if err := b.validateDirRootRuntime(pool); err != nil {
			return diskops.VolumeRef{}, fmt.Errorf("%w: %v", ErrPoolNotAllowed, err)
		}
		name := volumeName(vmID, operationID, backend, pool, sizeGB) + ".qcow2"
		return diskops.VolumeRef{
			VMID:         vmID,
			Backend:      backend,
			Pool:         filepath.Clean(pool),
			Name:         name,
			ResolvedPath: filepath.Join(filepath.Clean(pool), name),
			SizeGB:       sizeGB,
		}, nil
	case diskops.BackendLVM:
		if _, ok := b.lvmVGs[pool]; !ok {
			return diskops.VolumeRef{}, fmt.Errorf("%w: lvm vg %q", ErrPoolNotAllowed, pool)
		}
		name := volumeName(vmID, operationID, backend, pool, sizeGB)
		return diskops.VolumeRef{
			VMID:         vmID,
			Backend:      backend,
			Pool:         pool,
			Name:         name,
			ResolvedPath: "/dev/" + pool + "/" + name,
			SizeGB:       sizeGB,
		}, nil
	case diskops.BackendZFS:
		if _, ok := b.zfsDatasets[pool]; !ok {
			return diskops.VolumeRef{}, fmt.Errorf("%w: zfs dataset %q", ErrPoolNotAllowed, pool)
		}
		name := volumeName(vmID, operationID, backend, pool, sizeGB)
		return diskops.VolumeRef{
			VMID:         vmID,
			Backend:      backend,
			Pool:         pool,
			Name:         name,
			ResolvedPath: "/dev/zvol/" + pool + "/" + name,
			SizeGB:       sizeGB,
		}, nil
	default:
		return diskops.VolumeRef{}, fmt.Errorf("managed volume: unsupported backend %q", backend)
	}
}

// Inspect reports current presence/size of a resolved ref.
func (b *ManagedVolumeBackend) Inspect(ctx context.Context, ref diskops.VolumeRef) (diskops.VolumeObservation, error) {
	if err := b.validateRef(ref); err != nil {
		// Ref failed revalidation (policy/root/path/ownership). Fail closed.
		return diskops.VolumeObservation{Presence: diskops.Unknown}, err
	}
	pres, path, err := b.observe(ctx, ref)
	if err != nil {
		// observation failure -> Unknown (fail-closed)
		return diskops.VolumeObservation{Presence: diskops.Unknown}, err
	}
	if pres == diskops.Absent {
		return diskops.VolumeObservation{Presence: diskops.Absent, ObservedPath: path}, nil
	}
	// Present: attempt to read verified size (backend-specific).
	vsize, serr := b.verifiedSize(ctx, ref)
	if serr != nil {
		// can't verify size but it exists -> Present without size.
		return diskops.VolumeObservation{Presence: diskops.Present, ObservedPath: path}, nil
	}
	return diskops.VolumeObservation{Presence: diskops.Present, ObservedPath: path, VirtualSizeBytes: vsize}, nil
}

// VerifyReuse verifies an existing volume's logical/virtual size is EQUAL to the
// requested size. Mismatch is a typed non-success error.
func (b *ManagedVolumeBackend) VerifyReuse(ctx context.Context, ref diskops.VolumeRef) (diskops.VolumeObservation, error) {
	if err := b.validateRef(ref); err != nil {
		return diskops.VolumeObservation{Presence: diskops.Unknown}, err
	}
	obs, err := b.Inspect(ctx, ref)
	if err != nil || obs.Presence != diskops.Present {
		return obs, fmt.Errorf("managed volume: cannot verify reuse (present=%s): %w", obs.Presence, err)
	}
	want, werr := sizeGBToBytes(ref.SizeGB)
	if werr != nil {
		return obs, werr
	}
	if obs.VirtualSizeBytes == 0 {
		return obs, errors.New("managed volume: size unverified (zero); refusing reuse")
	}
	if obs.VirtualSizeBytes != want {
		return obs, fmt.Errorf("%w: have %d want %d", ErrSizeMismatch, obs.VirtualSizeBytes, want)
	}
	return obs, nil
}

// DestroyVerified deletes the volume and proves absence by re-inspection.
func (b *ManagedVolumeBackend) DestroyVerified(ctx context.Context, ref diskops.VolumeRef) (diskops.Presence, error) {
	if err := b.validateRef(ref); err != nil {
		return diskops.Unknown, err
	}
	// Initial inspect: an inconclusive/error result MUST NOT lead to a delete.
	initObs, initErr := b.Inspect(ctx, ref)
	if initErr != nil || initObs.Presence == diskops.Unknown {
		return diskops.Unknown, fmt.Errorf("%w: %v", ErrInspectInconclusive, initErr)
	}
	if initObs.Presence == diskops.Absent {
		return diskops.Absent, nil // idempotent already-absent
	}
	delErr := b.delete(ctx, ref)
	// Re-inspect to PROVE outcome. Destructive error dominance: a delete error
	// NEVER yields Absent. Even a post-proven-absent after a delete error maps
	// to Unknown (the physical outcome is ambiguous from the agent's view).
	after, afterErr := b.Inspect(ctx, ref)
	if afterErr != nil || after.Presence == diskops.Unknown {
		return diskops.Unknown, fmt.Errorf("managed volume: post-delete inspection inconclusive: %w", afterErr)
	}
	switch after.Presence {
	case diskops.Absent:
		if delErr != nil {
			// Delete failed AND (proven absent OR unknowable). Never Absent with
			// a non-nil error: report Unknown so quota is retained fail-closed.
			return diskops.Unknown, fmt.Errorf("managed volume: delete reported error and volume state uncertain (proven absent): %w", delErr)
		}
		return diskops.Absent, nil
	case diskops.Present:
		if delErr != nil {
			return diskops.Present, delErr // delete failed, still present
		}
		return diskops.Present, errors.New("managed volume: delete reported success but volume still present")
	default:
		return diskops.Unknown, errors.New("managed volume: unexpected presence")
	}
}

// ClassifyLegacy validates an already-recorded/observed detached path as managed
// by vmID under the configured policy. It rejects arbitrary paths, traversal,
// sibling-prefix tricks, nested paths, symlinks, device paths, and unsupported
// extensions/types. Only an EXACT legacy filename ("<vmID>.qcow2|img|raw") that
// is a direct child of a currently trusted (re-validated) dir root is accepted;
// the ambiguous old deterministic family is rejected.
func (b *ManagedVolumeBackend) ClassifyLegacy(ctx context.Context, vmID, path string) (diskops.VolumeRef, error) {
	if path == "" {
		return diskops.VolumeRef{}, fmt.Errorf("%w: empty path", ErrNotManaged)
	}
	clean := filepath.Clean(path)
	if clean != path {
		// Normalization differs -> caller supplied a non-canonical/traversal form.
		return diskops.VolumeRef{}, fmt.Errorf("%w: non-canonical path %q", ErrNotManaged, path)
	}
	switch {
	case strings.HasPrefix(clean, "/dev/"):
		return b.classifyBlockDevice(vmID, clean)
	default:
		return b.classifyFilePath(vmID, clean)
	}
}

func (b *ManagedVolumeBackend) classifyFilePath(vmID, clean string) (diskops.VolumeRef, error) {
	if err := validateSafeID("vm_id", vmID); err != nil {
		return diskops.VolumeRef{}, err
	}
	// Must be a direct child of an allowlisted dir root (no nesting), and that
	// root must still be a valid real non-symlink dir at operation time.
	parent := filepath.Dir(clean)
	if err := b.validateDirRootRuntime(parent); err != nil {
		return diskops.VolumeRef{}, fmt.Errorf("%w: %q not a direct child of a trusted dir root: %v", ErrNotManaged, clean, err)
	}
	base := filepath.Base(clean)
	// EXACT legacy ownership only; ambiguous forms rejected.
	if !legacyDirNameOwnedByVM(base, vmID) {
		return diskops.VolumeRef{}, fmt.Errorf("%w: %q not an exact legacy file owned by vm %q", ErrNotManaged, base, vmID)
	}
	// Reject symlink and any non-regular existing object (dir/fifo/device/...).
	if fi, lerr := os.Lstat(clean); lerr == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return diskops.VolumeRef{}, fmt.Errorf("%w: symlink rejected %q", ErrNotManaged, clean)
		}
		if !fi.Mode().IsRegular() {
			return diskops.VolumeRef{}, fmt.Errorf("%w: non-regular object rejected %q", ErrNotManaged, clean)
		}
	} else if !os.IsNotExist(lerr) {
		return diskops.VolumeRef{}, fmt.Errorf("%w: stat error %q: %v", ErrNotManaged, clean, lerr)
	}
	// Absent candidates are classifiable (idempotent verified destroy); if
	// present they have already been validated as regular files above.
	return diskops.VolumeRef{
		VMID:         vmID,
		Backend:      diskops.BackendDir,
		Pool:         parent,
		Name:         base,
		ResolvedPath: clean,
	}, nil
}

func (b *ManagedVolumeBackend) classifyBlockDevice(vmID, clean string) (diskops.VolumeRef, error) {
	if err := validateSafeID("vm_id", vmID); err != nil {
		return diskops.VolumeRef{}, err
	}
	// LVM: /dev/<vg>/<lv> — exact configured VG and VM-owned LV leaf only.
	if strings.HasPrefix(clean, "/dev/") {
		rest := strings.TrimPrefix(clean, "/dev/")
		parts := strings.Split(rest, "/")
		if len(parts) == 2 {
			vg, lv := parts[0], parts[1]
			if _, ok := b.lvmVGs[vg]; ok && safeNameRe.MatchString(lv) && deterministicNameOwnedByVM(lv, vmID) {
				return diskops.VolumeRef{VMID: vmID, Backend: diskops.BackendLVM, Pool: vg, Name: lv, ResolvedPath: clean}, nil
			}
			return diskops.VolumeRef{}, fmt.Errorf("%w: lvm device %q not allowlisted/owned", ErrNotManaged, clean)
		}
		// ZFS zvol: /dev/zvol/<dataset>/<leaf> — exact configured dataset.
		if strings.HasPrefix(clean, "/dev/zvol/") {
			rest := strings.TrimPrefix(clean, "/dev/zvol/")
			idx := strings.LastIndex(rest, "/")
			if idx > 0 {
				ds := rest[:idx]
				leaf := rest[idx+1:]
				if _, ok := b.zfsDatasets[ds]; ok && safeNameRe.MatchString(leaf) && deterministicNameOwnedByVM(leaf, vmID) {
					return diskops.VolumeRef{VMID: vmID, Backend: diskops.BackendZFS, Pool: ds, Name: leaf, ResolvedPath: clean}, nil
				}
			}
		}
	}
	return diskops.VolumeRef{}, fmt.Errorf("%w: block device %q not managed", ErrNotManaged, clean)
}

// --- backend observation / delete helpers ---

// observe returns the presence and canonical path for a ref, and any error from
// the observation mechanism (used to decide Unknown).
func (b *ManagedVolumeBackend) observe(ctx context.Context, ref diskops.VolumeRef) (diskops.Presence, string, error) {
	switch ref.Backend {
	case diskops.BackendDir:
		fi, err := os.Lstat(ref.ResolvedPath)
		if err != nil {
			if os.IsNotExist(err) {
				return diskops.Absent, ref.ResolvedPath, nil
			}
			return diskops.Unknown, ref.ResolvedPath, err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			// A symlink at the managed path is unsafe; never trust/delete it.
			return diskops.Unknown, ref.ResolvedPath, errors.New("managed volume: symlink at managed path")
		}
		if !fi.Mode().IsRegular() {
			// A directory/device/FIFO/... at the managed path is unsafe to treat
			// as a present volume; fail closed to Unknown.
			return diskops.Unknown, ref.ResolvedPath, errors.New("managed volume: non-regular object at managed path")
		}
		return diskops.Present, ref.ResolvedPath, nil
	case diskops.BackendLVM:
		present, err := b.lvmLVExists(ctx, ref.Pool, ref.Name)
		if err != nil {
			return diskops.Unknown, ref.ResolvedPath, err
		}
		if present {
			return diskops.Present, ref.ResolvedPath, nil
		}
		return diskops.Absent, ref.ResolvedPath, nil
	case diskops.BackendZFS:
		present, err := b.zfsVolExists(ctx, ref.Pool, ref.Name)
		if err != nil {
			// A command/list failure is Unknown, never inferred absent.
			return diskops.Unknown, ref.ResolvedPath, err
		}
		if present {
			return diskops.Present, ref.ResolvedPath, nil
		}
		return diskops.Absent, ref.ResolvedPath, nil
	default:
		return diskops.Unknown, ref.ResolvedPath, fmt.Errorf("managed volume: unsupported backend %q", ref.Backend)
	}
}

// verifiedSize returns the logical/virtual size in bytes for a present volume.
// For dir it is delegated to the coordinator (would require qemu-img); presence-
// only here keeps this strict path dependency-free. For LVM/ZFS we parse the
// strict positive integer byte size from the backend tooling.
func (b *ManagedVolumeBackend) verifiedSize(ctx context.Context, ref diskops.VolumeRef) (int64, error) {
	switch ref.Backend {
	case diskops.BackendLVM:
		return b.lvmLVSize(ctx, ref.Pool, ref.Name)
	case diskops.BackendZFS:
		return b.zfsVolSize(ctx, ref.Pool, ref.Name)
	default:
		// dir: not inferred here (would require qemu-img); report unknown size.
		return 0, errors.New("managed volume: dir size verification delegated to coordinator")
	}
}

// delete performs the backend-specific removal. It never infers success from a
// command error and never calls the old permissive VolumeManager.DeleteVolume.
// It revalidates the ref and refuses symlink/non-regular dir objects.
//
// For LVM/ZFS, error dominance is absolute: ANY process error, ANY non-empty
// stderr (even a single space), or ANY unexpected non-empty stdout makes the
// delete fail. DestroyVerified still post-inspects for evidence, but a delete
// error can NEVER yield an (Absent, nil) outcome — the caller maps that to
// (Unknown|Present, error) fail-closed.
func (b *ManagedVolumeBackend) delete(ctx context.Context, ref diskops.VolumeRef) error {
	if err := b.validateRef(ref); err != nil {
		return err
	}
	switch ref.Backend {
	case diskops.BackendDir:
		// Refuse symlink/non-regular targets; only remove a regular file we own
		// at the canonical path. (Fail-closed; we do not claim openat2 race
		// elimination — C2-A2 rooted-directory enforcement is explicitly out of
		// scope here.)
		if fi, lerr := os.Lstat(ref.ResolvedPath); lerr == nil {
			if fi.Mode()&os.ModeSymlink != 0 {
				return errors.New("managed volume: refusing to delete symlink at managed path")
			}
			if !fi.Mode().IsRegular() {
				return errors.New("managed volume: refusing to delete non-regular object at managed path")
			}
		} else if !os.IsNotExist(lerr) {
			return lerr
		}
		err := os.Remove(ref.ResolvedPath)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	case diskops.BackendLVM:
		// `lvremove -f -- <VG>/<LV>`. The `--` guards the positional target from
		// being interpreted as an option even if it ever began with a dash. No
		// shell is used. Any process error, ANY non-empty stderr (raw bytes,
		// including whitespace), or ANY unexpected non-empty stdout is a delete
		// error. Never returns Absent (caller maps delete error fail-closed).
		res := b.runner.Run(ctx, "lvremove", "-f", "--", ref.Pool+"/"+ref.Name)
		if res.Err != nil {
			return fmt.Errorf("managed volume: lvremove process error: %w", res.Err)
		}
		if len(res.Stderr) > 0 {
			return fmt.Errorf("managed volume: lvremove stderr: %s", res.StderrString())
		}
		if len(res.Stdout) > 0 {
			return fmt.Errorf("managed volume: lvremove unexpected stdout: %s", res.StdoutString())
		}
		return nil
	case diskops.BackendZFS:
		// Exact dataset/leaf target, no shell, no positional `--`. Strict name
		// grammar already validated by validateRef. Same stderr/stdout dominance.
		res := b.runner.Run(ctx, "zfs", "destroy", ref.Pool+"/"+ref.Name)
		if res.Err != nil {
			return fmt.Errorf("managed volume: zfs destroy process error: %w", res.Err)
		}
		if len(res.Stderr) > 0 {
			return fmt.Errorf("managed volume: zfs destroy stderr: %s", res.StderrString())
		}
		if len(res.Stdout) > 0 {
			return fmt.Errorf("managed volume: zfs destroy unexpected stdout: %s", res.StdoutString())
		}
		return nil
	default:
		return fmt.Errorf("managed volume: unsupported backend %q", ref.Backend)
	}
}

// --- LVM/ZFS inspection via injectable runner ---

// lvmLVExists reports whether the exact LV /dev/<vg>/<lv> exists, using
// `lvs --noheadings -o lv_path -- <vg>`. The command must be clean: any process
// error or ANY non-empty stderr (raw bytes, including whitespace) => Unknown
// (error), never treated as absent; parsing does not proceed. Only a fully
// valid listing containing the exact /dev/<vg>/<lv> row proves presence; absence
// of that exact row (with all other rows valid siblings) proves Absent. Any
// malformed/wrong-VG/nested/prefix/duplicate row => Unknown (error). Malformed
// records are NEVER silently ignored.
func (b *ManagedVolumeBackend) lvmLVExists(ctx context.Context, vg, lv string) (bool, error) {
	res := b.runner.Run(ctx, "lvs", "--noheadings", "-o", "lv_path", "--", vg)
	if res.Err != nil {
		return false, res.Err
	}
	if len(res.Stderr) > 0 {
		return false, fmt.Errorf("managed volume: lvs stderr: %s", res.StderrString())
	}
	return parseLVMList(res.Stdout, vg, lv)
}

// lvmLVSize returns the strict positive integer byte size of the LV via
// `lvs --noheadings -o lv_size --nosuffix --units b -- <vg>/<lv>`. The command
// must be clean (no stderr); output must be exactly ONE positive ASCII decimal
// integer <= math.MaxInt64. Signs, zero, float, units, extra token/line, or any
// whitespace-record anomaly are rejected.
func (b *ManagedVolumeBackend) lvmLVSize(ctx context.Context, vg, lv string) (int64, error) {
	res := b.runner.Run(ctx, "lvs", "--noheadings", "-o", "lv_size", "--nosuffix", "--units", "b", "--", vg+"/"+lv)
	if res.Err != nil {
		return 0, res.Err
	}
	if len(res.Stderr) > 0 {
		return 0, fmt.Errorf("managed volume: lvs stderr: %s", res.StderrString())
	}
	return strictPositiveBytes(res.Stdout)
}

// zfsVolExists reports whether the exact ZFS volume <dataset>/<leaf> exists,
// using `zfs list -H -o name -t volume -d 1 <dataset>`. The command must be
// clean; only a valid listing containing the exact direct-child
// <dataset>/<leaf> row proves presence; the -d 1 scope and exact-match guard
// reject prefix/deeper-child collisions. Any error/stderr/parent/deeper/wrong/
// duplicate/malformed row => Unknown (error).
func (b *ManagedVolumeBackend) zfsVolExists(ctx context.Context, ds, leaf string) (bool, error) {
	res := b.runner.Run(ctx, "zfs", "list", "-H", "-o", "name", "-t", "volume", "-d", "1", ds)
	if res.Err != nil {
		return false, res.Err
	}
	if len(res.Stderr) > 0 {
		return false, fmt.Errorf("managed volume: zfs list stderr: %s", res.StderrString())
	}
	return parseZFSList(res.Stdout, ds, leaf)
}

// zfsVolSize returns the strict positive integer byte volsize of the ZFS volume
// via `zfs get -Hp -o value volsize <dataset>/<leaf>`. Clean command required;
// one positive decimal integer <= MaxInt64 only.
func (b *ManagedVolumeBackend) zfsVolSize(ctx context.Context, ds, leaf string) (int64, error) {
	res := b.runner.Run(ctx, "zfs", "get", "-Hp", "-o", "value", "volsize", ds+"/"+leaf)
	if res.Err != nil {
		return 0, res.Err
	}
	if len(res.Stderr) > 0 {
		return 0, fmt.Errorf("managed volume: zfs get stderr: %s", res.StderrString())
	}
	return strictPositiveBytes(res.Stdout)
}

// --- strict LVM/ZFS protocol parsers ---

// parseLVMList parses the stdout of `lvs --noheadings -o lv_path -- <vg>` and
// returns whether the exact LV /dev/<vg>/<lv> is present. Every nonblank stdout
// record must be a valid direct /dev/<vg>/<safe-leaf> path. Siblings are valid.
// The exact target establishes presence; a clean empty list establishes
// absence. Any duplicate, malformed, wrong-VG, prefix, nested path, or
// duplicate target/sibling record yields an error (Unknown). Malformed records
// are never silently ignored.
func parseLVMList(stdout []byte, vg, lv string) (bool, error) {
	target := "/dev/" + vg + "/" + lv
	seen := map[string]struct{}{}
	present := false
	records := strings.Split(string(stdout), "\n")
	for _, raw := range records {
		rec := strings.TrimSpace(raw)
		if rec == "" {
			continue
		}
		// Must be exactly a direct /dev/<vg>/<safe-leaf> path.
		if !strings.HasPrefix(rec, "/dev/") {
			return false, fmt.Errorf("managed volume: lvm list malformed record: %q", rec)
		}
		rest := strings.TrimPrefix(rec, "/dev/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 {
			return false, fmt.Errorf("managed volume: lvm list malformed/nested record: %q", rec)
		}
		if parts[0] != vg {
			return false, fmt.Errorf("managed volume: lvm list wrong vg record: %q", rec)
		}
		leaf := parts[1]
		if !safeNameRe.MatchString(leaf) {
			return false, fmt.Errorf("managed volume: lvm list malformed leaf: %q", rec)
		}
		if _, dup := seen[rec]; dup {
			return false, fmt.Errorf("managed volume: lvm list duplicate record: %q", rec)
		}
		seen[rec] = struct{}{}
		if rec == target {
			present = true
		}
	}
	return present, nil
}

// parseZFSList parses the stdout of `zfs list -H -o name -t volume -d 1 <ds>`
// and returns whether the exact direct-child volume <ds>/<leaf> is present.
// Every nonblank stdout record must be a valid direct <ds>/<safe-leaf> child.
// Siblings are valid and the exact target present establishes presence; a clean
// empty list establishes absence. A parent, deeper child, prefix, malformed,
// wrong-dataset, or duplicate record yields an error (Unknown).
func parseZFSList(stdout []byte, ds, leaf string) (bool, error) {
	target := ds + "/" + leaf
	seen := map[string]struct{}{}
	present := false
	records := strings.Split(string(stdout), "\n")
	for _, raw := range records {
		rec := strings.TrimSpace(raw)
		if rec == "" {
			continue
		}
		// Must be a direct child of the dataset: exactly one more path segment
		// beneath ds, and ds must be the exact prefix (reject prefix collisions
		// like ds+"x").
		if !strings.HasPrefix(rec, ds+"/") {
			return false, fmt.Errorf("managed volume: zfs list wrong dataset record: %q", rec)
		}
		rest := strings.TrimPrefix(rec, ds+"/")
		if rest == "" || strings.Contains(rest, "/") {
			// Parent (ds itself) or deeper child rejected.
			return false, fmt.Errorf("managed volume: zfs list parent/deeper record: %q", rec)
		}
		if !safeNameRe.MatchString(rest) {
			return false, fmt.Errorf("managed volume: zfs list malformed leaf: %q", rec)
		}
		if _, dup := seen[rec]; dup {
			return false, fmt.Errorf("managed volume: zfs list duplicate record: %q", rec)
		}
		seen[rec] = struct{}{}
		if rec == target {
			present = true
		}
	}
	return present, nil
}

// strictPositiveBytes parses stdout that must contain EXACTLY ONE positive
// ASCII decimal integer token (<= math.MaxInt64), with no sign, no float, no
// unit suffix, no extra token/line, and no whitespace-record anomaly. Any
// deviation yields an error. It is used for both LVM and ZFS size output.
func strictPositiveBytes(stdout []byte) (int64, error) {
	// Split into nonblank records; there must be exactly one.
	var token string
	records := strings.Split(string(stdout), "\n")
	found := false
	for _, raw := range records {
		rec := strings.TrimSpace(raw)
		if rec == "" {
			continue
		}
		if found {
			return 0, fmt.Errorf("managed volume: expected single size token, got extra record: %q", rec)
		}
		// A record must be a single integer token (no internal spaces).
		if strings.ContainsAny(rec, " \t") {
			return 0, fmt.Errorf("managed volume: size record not a single token: %q", rec)
		}
		token = rec
		found = true
	}
	if !found {
		return 0, fmt.Errorf("managed volume: empty size output")
	}
	// Reject any sign prefix; only a bare positive decimal integer is allowed.
	if token[0] == '+' || token[0] == '-' {
		return 0, fmt.Errorf("managed volume: size must not carry a sign: %q", token)
	}
	n, err := strconv.ParseInt(token, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("managed volume: size not a positive integer: %q: %w", token, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("managed volume: size must be positive: %d", n)
	}
	return n, nil
}

// --- validation helpers ---

func validateAbsDir(d string) error {
	if !filepath.IsAbs(d) {
		return fmt.Errorf("not absolute: %q", d)
	}
	clean := filepath.Clean(d)
	if clean != d {
		return fmt.Errorf("not canonical: %q", d)
	}
	if strings.Contains(d, "..") {
		return fmt.Errorf("contains parent reference: %q", d)
	}
	// Require the configured root to EXIST as a real directory and to not be a
	// symlink (so a symlinked root cannot silently redirect managed writes).
	fi, err := os.Lstat(clean)
	if err != nil {
		return fmt.Errorf("dir root not accessible: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("dir root must not be a symlink: %q", clean)
	}
	if !fi.IsDir() {
		return fmt.Errorf("dir root not a directory: %q", clean)
	}
	return nil
}

func validateLVMName(v, kind string) error {
	if v == "" || !safeNameRe.MatchString(v) {
		return fmt.Errorf("invalid %s name %q", kind, v)
	}
	return nil
}

func validateZFSDataset(ds string) error {
	if ds == "" || strings.Contains(ds, "..") || strings.HasPrefix(ds, "/") {
		return fmt.Errorf("invalid zfs dataset %q", ds)
	}
	for _, part := range strings.Split(ds, "/") {
		if part == "" || !safeNameRe.MatchString(part) {
			return fmt.Errorf("invalid zfs dataset part %q in %q", part, ds)
		}
	}
	return nil
}

func validateSafeID(name, v string) error {
	if v == "" || len(v) > 200 || !safeNameRe.MatchString(v) {
		return fmt.Errorf("%s: %w (unsafe/empty/oversized)", name, ErrNotManaged)
	}
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range regexp.MustCompile(`[\s,]+`).Split(strings.TrimSpace(s), -1) {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
