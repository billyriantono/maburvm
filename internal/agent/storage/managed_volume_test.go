package storage

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/agent/diskops"
)

// fakeRunner is an injectable commandRunner that records exact calls and returns
// a CommandResult (separated stdout/stderr/error) for a given (name, args). It
// never invokes real binaries.
type fakeRunner struct {
	// calls records each (name, args) invocation in order.
	calls []runnerCall
	// handler returns a CommandResult for a given (name, args).
	handler func(name string, args []string) CommandResult
}

type runnerCall struct {
	name string
	args []string
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) CommandResult {
	f.calls = append(f.calls, runnerCall{name: name, args: append([]string{}, args...)})
	if f.handler != nil {
		return f.handler(name, args)
	}
	return CommandResult{}
}

// hasArg reports whether args contains exactly the given token.
func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// joinArgs is a small helper for assertion messages.
func joinArgs(args []string) string { return strings.Join(args, " ") }

// mkfifo creates a FIFO special file (non-regular target) for reject tests.
func mkfifo(path string) error {
	return exec.Command("mkfifo", path).Run()
}

// mkBackend builds a backend rooted at a temp dir (as the agent default dir),
// with an injectable fakeRunner unless overridden by extra.
func mkBackend(t *testing.T, dir string, extra func(*ManagedVolumeBackend)) *ManagedVolumeBackend {
	t.Helper()
	b := &ManagedVolumeBackend{
		dirRoots:    map[string]struct{}{},
		lvmVGs:      map[string]struct{}{},
		zfsDatasets: map[string]struct{}{},
		runner:      &fakeRunner{},
	}
	if dir != "" {
		b.dirRoots[filepath.Clean(dir)] = struct{}{}
	}
	if extra != nil {
		extra(b)
	}
	return b
}

func TestConstructionNoTrustedRootsRejected(t *testing.T) {
	if _, err := NewManagedVolumeBackendFromEnv(""); err == nil {
		t.Fatalf("expected error when no trusted roots configured")
	}
}

func TestConstructionExistingDirRootRequired(t *testing.T) {
	// A non-existent absolute path must be rejected (roots must exist as dirs).
	t.Setenv("MABURVM_MANAGED_DIR_ROOTS", "/nonexistent/path/xyz")
	if _, err := NewManagedVolumeBackendFromEnv(""); err == nil {
		t.Fatalf("expected error for non-existent dir root")
	}
	// Symlink root rejection.
	tmp := t.TempDir()
	link := tmp + "/linkroot"
	if err := os.Symlink(tmp, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	t.Setenv("MABURVM_MANAGED_DIR_ROOTS", link)
	if _, err := NewManagedVolumeBackendFromEnv(""); err == nil {
		t.Fatalf("expected error for symlink dir root")
	}
}

func TestResolveCreateDeterministicVMOwned(t *testing.T) {
	dir := t.TempDir()
	b := mkBackend(t, dir, nil)
	ref, err := b.ResolveCreate(context.Background(), "vm-A", "op-1", diskops.BackendDir, dir, 10)
	if err != nil {
		t.Fatalf("ResolveCreate: %v", err)
	}
	if ref.VMID != "vm-A" {
		t.Fatalf("VMID not populated: %q", ref.VMID)
	}
	if ref.ResolvedPath != filepath.Join(dir, ref.Name) {
		t.Fatalf("canonical path mismatch: %q", ref.ResolvedPath)
	}
	// New format must be the versioned mv2- form with an exact vmTag.
	if !strings.HasPrefix(ref.Name, "mv2-") {
		t.Fatalf("expected versioned name, got %q", ref.Name)
	}
	if !deterministicNameOwnedByVM(ref.Name, "vm-A") {
		t.Fatalf("generated name not owned by vm-A: %q", ref.Name)
	}
	if deterministicNameOwnedByVM(ref.Name, "vm-B") {
		t.Fatalf("vm-B must NOT own vm-A's generated name: %q", ref.Name)
	}
	// Different operation -> different name.
	ref2, err := b.ResolveCreate(context.Background(), "vm-A", "op-2", diskops.BackendDir, dir, 10)
	if err != nil {
		t.Fatalf("ResolveCreate2: %v", err)
	}
	if ref2.Name == ref.Name {
		t.Fatalf("different operation yielded same name: %q", ref.Name)
	}
	// Same operation -> same name (deterministic).
	ref3, err := b.ResolveCreate(context.Background(), "vm-A", "op-1", diskops.BackendDir, dir, 10)
	if err != nil {
		t.Fatalf("ResolveCreate3: %v", err)
	}
	if ref3.Name != ref.Name {
		t.Fatalf("same operation yielded different name: %q vs %q", ref.Name, ref3.Name)
	}
}

func TestResolveCreateRejectsUntrustedPool(t *testing.T) {
	dir := t.TempDir()
	b := mkBackend(t, dir, func(b *ManagedVolumeBackend) {
		b.lvmVGs["vg0"] = struct{}{}
	})
	if _, err := b.ResolveCreate(context.Background(), "vm-A", "op-1", diskops.BackendLVM, "vg-evil", 10); !errors.Is(err, ErrPoolNotAllowed) {
		t.Fatalf("lvm untrusted pool err = %v", err)
	}
	if _, err := b.ResolveCreate(context.Background(), "vm-A", "op-1", diskops.BackendDir, "/evil/root", 10); !errors.Is(err, ErrPoolNotAllowed) {
		t.Fatalf("dir untrusted pool err = %v", err)
	}
	// Forged ref with traversal name must fail validateRef.
	forged := diskops.VolumeRef{VMID: "vm-A", Backend: diskops.BackendDir, Pool: dir, Name: "../../etc/passwd", ResolvedPath: "/etc/passwd"}
	if err := b.validateRef(forged); err == nil {
		t.Fatalf("forged ref with traversal name passed validateRef")
	}
}

func TestExactVMOwnershipNoCrossVM(t *testing.T) {
	dir := t.TempDir()
	b := mkBackend(t, dir, nil)
	// VM A's deterministic ref must NOT classify/operate under VM B.
	refA, err := b.ResolveCreate(context.Background(), "vm-A", "op-1", diskops.BackendDir, dir, 10)
	if err != nil {
		t.Fatalf("refA: %v", err)
	}
	forged := refA
	forged.VMID = "vm-B"
	if err := b.validateRef(forged); err == nil {
		t.Fatalf("validateRef accepted cross-VM forged deterministic name")
	}
	// Ambiguous OLD deterministic form must be rejected as not VM-owned.
	oldName := "maburvm-vmA-abc123def456.qcow2"
	if deterministicNameOwnedByVM(oldName, "vm-A") {
		t.Fatalf("old ambiguous deterministic name must be rejected: %q", oldName)
	}
	if b.validateRefAllowlist(oldName, "vm-A", dir) == nil {
		t.Fatalf("old ambiguous deterministic name must fail validateRef")
	}
	// Legacy exact filename: vm-A's "<vm-A>.qcow2" must be classifiable
	// by vm-A but rejected for vm-B.
	legacy := filepath.Join(dir, "vm-A.qcow2")
	if err := os.WriteFile(legacy, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ClassifyLegacy(context.Background(), "vm-A", legacy); err != nil {
		t.Fatalf("vm-A should own its legacy file: %v", err)
	}
	if _, err := b.ClassifyLegacy(context.Background(), "vm-B", legacy); err == nil {
		t.Fatalf("vm-B must NOT classify vm-A's legacy file")
	}
}

// validateRefAllowlist is a tiny test helper that wraps a name in a dir ref and
// runs validateRef without needing a pre-resolved ref.
func (b *ManagedVolumeBackend) validateRefAllowlist(name, vmID, dir string) error {
	return b.validateRef(diskops.VolumeRef{
		VMID:         vmID,
		Backend:      diskops.BackendDir,
		Pool:         dir,
		Name:         name,
		ResolvedPath: filepath.Join(dir, name),
	})
}

func TestLegacyOnlyExactNameBackwardCompat(t *testing.T) {
	// Exact legacy names accepted.
	for _, ext := range []string{"qcow2", "img", "raw"} {
		name := "vm-A." + ext
		if !legacyDirNameOwnedByVM(name, "vm-A") {
			t.Fatalf("exact legacy %q should be owned by vm-A", name)
		}
		if !deterministicNameOwnedByVM("mv2-"+vmSlug("vm-A")+"-"+cryptoVMTag("vm-A")+"-"+strings.Repeat("a", 20)+".qcow2", "vm-A") {
			t.Fatalf("versioned name should be owned by vm-A")
		}
	}
	// Ambiguous / dotted variants rejected.
	for _, bad := range []string{"vm-A-real.qcow2", "vm-A.extra.qcow2", "vm-A-x.qcow2", "vm-A.bak", "vm-A.qcow2x"} {
		if legacyDirNameOwnedByVM(bad, "vm-A") {
			t.Fatalf("ambiguous name %q must NOT be legacy-owned", bad)
		}
	}
}

func TestClassifyLegacyRejections(t *testing.T) {
	dir := t.TempDir()
	b := mkBackend(t, dir, nil)
	// nested path under root -> rejected (must be direct child)
	nested := filepath.Join(dir, "sub", "vm-A.qcow2")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ClassifyLegacy(context.Background(), "vm-A", nested); err == nil {
		t.Fatalf("nested path must be rejected")
	}
	// traversal/non-canonical
	if _, err := b.ClassifyLegacy(context.Background(), "vm-A", dir+"/../"+filepath.Base(dir)+"/vm-A.qcow2"); err == nil {
		t.Fatalf("traversal/noncanonical path must be rejected")
	}
	// symlink candidate -> rejected
	real := filepath.Join(dir, "vm-A.qcow2")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "vm-A-link.qcow2")
	if err := os.Symlink(real, link); err == nil {
		if _, err := b.ClassifyLegacy(context.Background(), "vm-A", link); err == nil {
			t.Fatalf("symlink candidate must be rejected")
		}
	}
	// directory candidate -> rejected (non-regular)
	dircand := filepath.Join(dir, "vm-A-dir.qcow2")
	if err := os.Mkdir(dircand, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ClassifyLegacy(context.Background(), "vm-A", dircand); err == nil {
		t.Fatalf("directory candidate must be rejected")
	}
	// FIFO/non-regular device candidate -> rejected
	fifo := filepath.Join(dir, "vm-A-fifo.qcow2")
	if err := mkfifo(fifo); err == nil {
		if _, err := b.ClassifyLegacy(context.Background(), "vm-A", fifo); err == nil {
			t.Fatalf("fifo candidate must be rejected")
		}
	}
	// unsupported extension -> rejected
	bad := filepath.Join(dir, "vm-A.vmdk")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ClassifyLegacy(context.Background(), "vm-A", bad); err == nil {
		t.Fatalf("unsupported extension must be rejected")
	}
	// ambiguous legacy-ish name rejected (vm-A-real.qcow2 not exact)
	amb := filepath.Join(dir, "vm-A-real.qcow2")
	if err := os.WriteFile(amb, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ClassifyLegacy(context.Background(), "vm-A", amb); err == nil {
		t.Fatalf("ambiguous vm-A-real.qcow2 must be rejected")
	}
	// valid absent legacy candidate (exact name, no file) is classifiable for
	// idempotent verified destroy.
	absent := filepath.Join(dir, "vm-A.qcow2")
	if _, err := b.ClassifyLegacy(context.Background(), "vm-A", absent); err != nil {
		t.Fatalf("valid absent legacy candidate must be classifiable: %v", err)
	}
}

func TestDirectoryObserveSymlinkNonregularUnknown(t *testing.T) {
	dir := t.TempDir()
	b := mkBackend(t, dir, nil)
	// symlink at managed path -> Unknown.
	src := filepath.Join(dir, "src.qcow2")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sym := filepath.Join(dir, "v.qcow2")
	_ = os.Symlink(src, sym)
	ref := diskops.VolumeRef{VMID: "vm-A", Backend: diskops.BackendDir, Pool: dir, Name: "v.qcow2", ResolvedPath: sym, SizeGB: 1}
	pres, _, err := b.observe(context.Background(), ref)
	if pres != diskops.Unknown || err == nil {
		t.Fatalf("symlink must be Unknown, got pres=%s err=%v", pres, err)
	}
	// directory at managed path -> Unknown.
	d := filepath.Join(dir, "v2.qcow2")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	ref2 := diskops.VolumeRef{VMID: "vm-A", Backend: diskops.BackendDir, Pool: dir, Name: "v2.qcow2", ResolvedPath: d, SizeGB: 1}
	pres, _, err = b.observe(context.Background(), ref2)
	if pres != diskops.Unknown || err == nil {
		t.Fatalf("directory must be Unknown, got pres=%s err=%v", pres, err)
	}
	// regular file -> Present.
	reg := filepath.Join(dir, "v3.qcow2")
	if err := os.WriteFile(reg, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref3 := diskops.VolumeRef{VMID: "vm-A", Backend: diskops.BackendDir, Pool: dir, Name: "v3.qcow2", ResolvedPath: reg, SizeGB: 1}
	pres, _, err = b.observe(context.Background(), ref3)
	if pres != diskops.Present || err != nil {
		t.Fatalf("regular file must be Present, got pres=%s err=%v", pres, err)
	}
}

func TestDestroyVerifiedFailClosed(t *testing.T) {
	dir := t.TempDir()
	b := mkBackend(t, dir, nil)

	// (a) initial inspect Unknown (symlink at managed path) -> no delete, Unknown.
	symSrc := filepath.Join(dir, "src.qcow2")
	if err := os.WriteFile(symSrc, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	sym := filepath.Join(dir, "vm-A.qcow2")
	_ = os.Symlink(symSrc, sym)
	ref := diskops.VolumeRef{VMID: "vm-A", Backend: diskops.BackendDir, Pool: dir, Name: "vm-A.qcow2", ResolvedPath: sym, SizeGB: 1}
	pres, derr := b.DestroyVerified(context.Background(), ref)
	if derr == nil {
		t.Fatalf("expected error for unknown initial inspect")
	}
	if pres != diskops.Unknown {
		t.Fatalf("initial unknown must yield Unknown, got %s", pres)
	}

	// (b) valid present exact legacy file -> normal destroy -> Absent.
	dirB := t.TempDir()
	b2 := mkBackend(t, dirB, nil)
	real := filepath.Join(dirB, "vm-A.qcow2")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref2, err := b2.ClassifyLegacy(context.Background(), "vm-A", real)
	if err != nil {
		t.Fatalf("classify real: %v", err)
	}
	pres, derr = b2.DestroyVerified(context.Background(), ref2)
	if derr != nil || pres != diskops.Absent {
		t.Fatalf("normal destroy: pres=%s err=%v", pres, derr)
	}
	if _, statErr := os.Stat(real); !os.IsNotExist(statErr) {
		t.Fatalf("file should be gone")
	}

	// (c) already absent -> idempotent Absent.
	pres, derr = b2.DestroyVerified(context.Background(), ref2)
	if derr != nil || pres != diskops.Absent {
		t.Fatalf("already absent: pres=%s err=%v", pres, derr)
	}
}

func TestDestroyVerifiedPresentAfterDelete(t *testing.T) {
	ref, err := func() (diskops.VolumeRef, error) {
		b := mkBackend(t, "", func(b *ManagedVolumeBackend) { b.lvmVGs["vg0"] = struct{}{} })
		return b.ResolveCreate(context.Background(), "vm-A", "op-1", diskops.BackendLVM, "vg0", 10)
	}()
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	target := "/dev/vg0/" + ref.Name

	// Scenario 1: initial present, lvremove errors, post-list STILL present =>
	// Presence=Present + error. DestroyVerified still post-inspects for evidence.
	b1 := mkBackend(t, "", func(b *ManagedVolumeBackend) {
		b.lvmVGs["vg0"] = struct{}{}
		b.runner = &fakeRunner{
			handler: func(name string, args []string) CommandResult {
				if name == "lvs" {
					return CommandResult{Stdout: []byte(target)} // present both times
				}
				if name == "lvremove" {
					return CommandResult{Err: errors.New("device busy")} // delete fails
				}
				return CommandResult{}
			},
		}
	})
	pres, derr := b1.DestroyVerified(context.Background(), ref)
	if pres != diskops.Present {
		t.Fatalf("present-after-failed-delete should be Present, got %s", pres)
	}
	if derr == nil {
		t.Fatalf("expected error when delete failed but object present")
	}

	// Scenario 2 (Oracle C2-A1): initial present, lvremove errors, post-list
	// proves Absent => Presence=Unknown + wrapped error (NEVER Absent on a delete
	// error).
	state2 := struct{ step int }{}
	b2 := mkBackend(t, "", func(b *ManagedVolumeBackend) {
		b.lvmVGs["vg0"] = struct{}{}
		b.runner = &fakeRunner{
			handler: func(name string, args []string) CommandResult {
				if name == "lvs" {
					if state2.step >= 1 {
						return CommandResult{} // post-delete list empty => proven absent
					}
					return CommandResult{Stdout: []byte(target)} // present on initial inspect
				}
				if name == "lvremove" {
					state2.step = 1 // a delete was attempted (and failed)
					return CommandResult{Err: errors.New("device busy")}
				}
				return CommandResult{}
			},
		}
	})
	pres, derr = b2.DestroyVerified(context.Background(), ref)
	if pres != diskops.Unknown {
		t.Fatalf("delete-error but proven absent must be Unknown, got %s", pres)
	}
	if derr == nil {
		t.Fatalf("expected wrapped error reporting delete failure but proven absent")
	}
}

func TestLVMContractExactAndUnknown(t *testing.T) {
	b := mkBackend(t, "", func(b *ManagedVolumeBackend) {
		b.lvmVGs["vg0"] = struct{}{}
		b.runner = &fakeRunner{
			handler: func(name string, args []string) CommandResult {
				// Every command fails -> observation must be Unknown, never Absent.
				return CommandResult{Err: errors.New("simulated command failure")}
			},
		}
	})
	ctx := context.Background()
	ref, err := b.ResolveCreate(ctx, "vm-A", "op-1", diskops.BackendLVM, "vg0", 10)
	if err != nil {
		t.Fatalf("resolve lvm: %v", err)
	}
	obs, oerr := b.Inspect(ctx, ref)
	if oerr == nil || obs.Presence != diskops.Unknown {
		t.Fatalf("lvm command failure must be Unknown, got pres=%s err=%v", obs.Presence, oerr)
	}
	pres, derr := b.DestroyVerified(ctx, ref)
	if derr == nil || pres != diskops.Unknown {
		t.Fatalf("lvm destroy with command failure must be Unknown, got %s", pres)
	}

	// Successful empty LIST proves absent (not a command error).
	fr := &fakeRunner{handler: func(name string, args []string) CommandResult { return CommandResult{} }}
	b.runner = fr
	obs, _ = b.Inspect(ctx, ref)
	if obs.Presence != diskops.Absent {
		t.Fatalf("lvm empty successful list must be Absent, got %s", obs.Presence)
	}

	// Exact-match: a list containing a DIFFERENT lv (prefix collision) is Absent.
	fr.handler = func(name string, args []string) CommandResult {
		if name == "lvs" {
			return CommandResult{Stdout: []byte("/dev/vg0/otherlv")}
		}
		return CommandResult{}
	}
	obs, _ = b.Inspect(ctx, ref)
	if obs.Presence != diskops.Absent {
		t.Fatalf("lvm exact-match must reject prefix collision, got %s", obs.Presence)
	}

	// Exact present match.
	lvPath := "/dev/vg0/" + ref.Name
	fr.handler = func(name string, args []string) CommandResult {
		if name == "lvs" {
			return CommandResult{Stdout: []byte(lvPath)}
		}
		return CommandResult{}
	}
	obs, _ = b.Inspect(ctx, ref)
	if obs.Presence != diskops.Present {
		t.Fatalf("lvm exact present match should be Present, got %s", obs.Presence)
	}

	// Valid sibling allowed, target absent.
	fr.handler = func(name string, args []string) CommandResult {
		if name == "lvs" {
			return CommandResult{Stdout: []byte("/dev/vg0/siblinglv")}
		}
		return CommandResult{}
	}
	obs, _ = b.Inspect(ctx, ref)
	if obs.Presence != diskops.Absent {
		t.Fatalf("lvm valid sibling with target absent must be Absent, got %s", obs.Presence)
	}

	// Malformed record (non /dev path) => Unknown (error), never silently absent.
	fr.handler = func(name string, args []string) CommandResult {
		if name == "lvs" {
			return CommandResult{Stdout: []byte("garbage")}
		}
		return CommandResult{}
	}
	obs, oerr = b.Inspect(ctx, ref)
	if obs.Presence != diskops.Unknown || oerr == nil {
		t.Fatalf("lvm malformed record must be Unknown, got pres=%s err=%v", obs.Presence, oerr)
	}

	// Wrong VG record => Unknown.
	fr.handler = func(name string, args []string) CommandResult {
		if name == "lvs" {
			return CommandResult{Stdout: []byte("/dev/vg-other/" + ref.Name)}
		}
		return CommandResult{}
	}
	obs, oerr = b.Inspect(ctx, ref)
	if obs.Presence != diskops.Unknown || oerr == nil {
		t.Fatalf("lvm wrong vg record must be Unknown, got pres=%s err=%v", obs.Presence, oerr)
	}

	// Nested path record => Unknown.
	fr.handler = func(name string, args []string) CommandResult {
		if name == "lvs" {
			return CommandResult{Stdout: []byte("/dev/vg0/sub/" + ref.Name)}
		}
		return CommandResult{}
	}
	obs, oerr = b.Inspect(ctx, ref)
	if obs.Presence != diskops.Unknown || oerr == nil {
		t.Fatalf("lvm nested record must be Unknown, got pres=%s err=%v", obs.Presence, oerr)
	}

	// Whitespace-only stderr => Unknown (error).
	fr.handler = func(name string, args []string) CommandResult {
		if name == "lvs" {
			return CommandResult{Stdout: []byte(lvPath), Stderr: []byte(" ")}
		}
		return CommandResult{}
	}
	obs, oerr = b.Inspect(ctx, ref)
	if obs.Presence != diskops.Unknown || oerr == nil {
		t.Fatalf("lvm whitespace stderr must be Unknown, got pres=%s err=%v", obs.Presence, oerr)
	}

	// (A) ALREADY-ABSENT: a fresh fake that lists empty -> no delete command.
	frAbsent := &fakeRunner{handler: func(name string, args []string) CommandResult { return CommandResult{} }}
	b.runner = frAbsent
	if pres, derr := b.DestroyVerified(ctx, ref); derr != nil || pres != diskops.Absent {
		t.Fatalf("already-absent destroy: pres=%s err=%v", pres, derr)
	}
	for _, c := range frAbsent.calls {
		if c.name == "lvremove" {
			t.Fatalf("lvremove must NOT be called when already absent: %v", c.args)
		}
	}

	// (B) PRESENT -> delete -> post-list absent: lvremove invoked once with the
	// exact `lvremove -f -- vg0/<name>` contract, outcome Absent.
	state := struct{ deleted bool }{}
	frPresent := &fakeRunner{
		handler: func(name string, args []string) CommandResult {
			if name == "lvs" {
				if state.deleted {
					return CommandResult{} // post-list proves absent
				}
				return CommandResult{Stdout: []byte(lvPath)} // present before delete
			}
			if name == "lvremove" {
				state.deleted = true
				return CommandResult{} // delete succeeded
			}
			return CommandResult{}
		},
	}
	b.runner = frPresent
	pres, derr = b.DestroyVerified(ctx, ref)
	if derr != nil || pres != diskops.Absent {
		t.Fatalf("present destroy should be Absent: pres=%s err=%v", pres, derr)
	}
	foundLV := false
	for _, c := range frPresent.calls {
		if c.name == "lvremove" {
			foundLV = true
			if !hasArg(c.args, "--") {
				t.Fatalf("lvremove missing '--' guard: %v", c.args)
			}
			// Expect exactly: lvremove -f -- vg0/<name>
			if joinArgs(c.args) != "-f -- vg0/"+ref.Name {
				t.Fatalf("lvremove args wrong: %v", c.args)
			}
		}
	}
	if !foundLV {
		t.Fatalf("expected exactly one lvremove invocation")
	}
}

func TestLVMSizeStrict(t *testing.T) {
	b := mkBackend(t, "", func(b *ManagedVolumeBackend) {
		b.lvmVGs["vg0"] = struct{}{}
	})
	ctx := context.Background()
	fr := &fakeRunner{
		handler: func(name string, args []string) CommandResult {
			if name == "lvs" && hasArg(args, "lv_size") {
				return CommandResult{Stdout: []byte("10737418240")} // 10 GiB
			}
			return CommandResult{}
		},
	}
	b.runner = fr
	n, err := b.lvmLVSize(ctx, "vg0", "lv0")
	if err != nil || n != 10*gibibyte {
		t.Fatalf("strict lvm size parse failed: n=%d err=%v", n, err)
	}
	// Floats / suffixes rejected.
	fr.handler = func(name string, args []string) CommandResult {
		return CommandResult{Stdout: []byte("10.00 GiB")}
	}
	if _, err := b.lvmLVSize(ctx, "vg0", "lv0"); err == nil {
		t.Fatalf("lvm human/float size must be rejected")
	}
	// Zero rejected.
	fr.handler = func(name string, args []string) CommandResult {
		return CommandResult{Stdout: []byte("0")}
	}
	if _, err := b.lvmLVSize(ctx, "vg0", "lv0"); err == nil {
		t.Fatalf("lvm zero size must be rejected")
	}
	// Extra token rejected.
	fr.handler = func(name string, args []string) CommandResult {
		return CommandResult{Stdout: []byte("10737418240 extra")}
	}
	if _, err := b.lvmLVSize(ctx, "vg0", "lv0"); err == nil {
		t.Fatalf("lvm multi-token size must be rejected")
	}
	// Sign rejected.
	fr.handler = func(name string, args []string) CommandResult {
		return CommandResult{Stdout: []byte("+10737418240")}
	}
	if _, err := b.lvmLVSize(ctx, "vg0", "lv0"); err == nil {
		t.Fatalf("lvm signed size must be rejected")
	}
	// Whitespace-only record rejected.
	fr.handler = func(name string, args []string) CommandResult {
		return CommandResult{Stdout: []byte("   ")}
	}
	if _, err := b.lvmLVSize(ctx, "vg0", "lv0"); err == nil {
		t.Fatalf("lvm whitespace-only size must be rejected")
	}
	// Non-empty stderr => rejection.
	fr.handler = func(name string, args []string) CommandResult {
		return CommandResult{Stdout: []byte("10737418240"), Stderr: []byte("warn")}
	}
	if _, err := b.lvmLVSize(ctx, "vg0", "lv0"); err == nil {
		t.Fatalf("lvm stderr size must be rejected")
	}
}

func TestZFSContractExactAndUnknown(t *testing.T) {
	b := mkBackend(t, "", func(b *ManagedVolumeBackend) {
		b.zfsDatasets["tank/vm"] = struct{}{}
		b.runner = &fakeRunner{
			handler: func(name string, args []string) CommandResult {
				return CommandResult{Err: errors.New("simulated command failure")}
			},
		}
	})
	ctx := context.Background()
	zref, err := b.ResolveCreate(ctx, "vm-A", "op-1", diskops.BackendZFS, "tank/vm", 10)
	if err != nil {
		t.Fatalf("resolve zfs: %v", err)
	}
	zobs, zerr := b.Inspect(ctx, zref)
	if zerr == nil || zobs.Presence != diskops.Unknown {
		t.Fatalf("zfs command failure must propagate Unknown, got pres=%s err=%v", zobs.Presence, zerr)
	}

	// Successful empty list => absent.
	fr := &fakeRunner{handler: func(name string, args []string) CommandResult { return CommandResult{} }}
	b.runner = fr
	zobs, _ = b.Inspect(ctx, zref)
	if zobs.Presence != diskops.Absent {
		t.Fatalf("zfs empty list must be Absent, got %s", zobs.Presence)
	}

	// Prefix/deeper child rejected: list contains tank/vm/otherleaf and
	// tank/vm/leaf/sub, but NOT tank/vm/<leaf>.
	leaf := zref.Name
	fr.handler = func(name string, args []string) CommandResult {
		if name == "zfs" {
			return CommandResult{Stdout: []byte("tank/vm/otherleaf\ntank/vm/" + leaf + "sub")}
		}
		return CommandResult{}
	}
	zobs, _ = b.Inspect(ctx, zref)
	if zobs.Presence != diskops.Absent {
		t.Fatalf("zfs prefix/deeper child must be rejected (absent), got %s", zobs.Presence)
	}

	// Exact direct child present.
	fr.handler = func(name string, args []string) CommandResult {
		if name == "zfs" {
			return CommandResult{Stdout: []byte("tank/vm/" + leaf)}
		}
		return CommandResult{}
	}
	zobs, _ = b.Inspect(ctx, zref)
	if zobs.Presence != diskops.Present {
		t.Fatalf("zfs exact child should be Present, got %s", zobs.Presence)
	}

	// Valid sibling allowed, target absent.
	fr.handler = func(name string, args []string) CommandResult {
		if name == "zfs" {
			return CommandResult{Stdout: []byte("tank/vm/siblingleaf")}
		}
		return CommandResult{}
	}
	zobs, _ = b.Inspect(ctx, zref)
	if zobs.Presence != diskops.Absent {
		t.Fatalf("zfs valid sibling with target absent must be Absent, got %s", zobs.Presence)
	}

	// Parent (dataset itself) rejected => Unknown.
	fr.handler = func(name string, args []string) CommandResult {
		if name == "zfs" {
			return CommandResult{Stdout: []byte("tank/vm")}
		}
		return CommandResult{}
	}
	zobs, zerr = b.Inspect(ctx, zref)
	if zobs.Presence != diskops.Unknown || zerr == nil {
		t.Fatalf("zfs parent record must be Unknown, got pres=%s err=%v", zobs.Presence, zerr)
	}

	// Malformed/different dataset prefix rejected => Unknown.
	fr.handler = func(name string, args []string) CommandResult {
		if name == "zfs" {
			return CommandResult{Stdout: []byte("tank/vmx/" + leaf)}
		}
		return CommandResult{}
	}
	zobs, zerr = b.Inspect(ctx, zref)
	if zobs.Presence != diskops.Unknown || zerr == nil {
		t.Fatalf("zfs wrong-dataset record must be Unknown, got pres=%s err=%v", zobs.Presence, zerr)
	}

	// Duplicate exact target rejected => Unknown.
	fr.handler = func(name string, args []string) CommandResult {
		if name == "zfs" {
			return CommandResult{Stdout: []byte("tank/vm/" + leaf + "\ntank/vm/" + leaf)}
		}
		return CommandResult{}
	}
	zobs, zerr = b.Inspect(ctx, zref)
	if zobs.Presence != diskops.Unknown || zerr == nil {
		t.Fatalf("zfs duplicate record must be Unknown, got pres=%s err=%v", zobs.Presence, zerr)
	}

	// (A) ALREADY-ABSENT: empty list -> no zfs destroy command.
	frAbsent := &fakeRunner{handler: func(name string, args []string) CommandResult { return CommandResult{} }}
	b.runner = frAbsent
	if pres, derr := b.DestroyVerified(ctx, zref); derr != nil || pres != diskops.Absent {
		t.Fatalf("zfs already-absent destroy: pres=%s err=%v", pres, derr)
	}
	for _, c := range frAbsent.calls {
		if c.name == "zfs" && hasArg(c.args, "destroy") {
			t.Fatalf("zfs destroy must NOT be called when already absent: %v", c.args)
		}
	}

	// (B) PRESENT -> delete -> post-list absent: zfs destroy invoked once with
	// exact `zfs destroy tank/vm/<leaf>` (no --), outcome Absent.
	state := struct{ deleted bool }{}
	frPresent := &fakeRunner{
		handler: func(name string, args []string) CommandResult {
			if name == "zfs" && hasArg(args, "list") {
				if state.deleted {
					return CommandResult{} // post-list proves absent
				}
				return CommandResult{Stdout: []byte("tank/vm/" + leaf)}
			}
			if name == "zfs" && hasArg(args, "destroy") {
				state.deleted = true
				return CommandResult{}
			}
			return CommandResult{}
		},
	}
	b.runner = frPresent
	if pres, derr := b.DestroyVerified(ctx, zref); derr != nil || pres != diskops.Absent {
		t.Fatalf("zfs present destroy should be Absent: pres=%s err=%v", pres, derr)
	}
	foundZFS := false
	for _, c := range frPresent.calls {
		if c.name == "zfs" && hasArg(c.args, "destroy") {
			foundZFS = true
			if joinArgs(c.args) != "destroy tank/vm/"+leaf {
				t.Fatalf("zfs destroy args wrong: %v", c.args)
			}
		}
	}
	if !foundZFS {
		t.Fatalf("expected exactly one zfs destroy invocation")
	}
}

func TestZFSSizeStrict(t *testing.T) {
	b := mkBackend(t, "", func(b *ManagedVolumeBackend) {
		b.zfsDatasets["tank/vm"] = struct{}{}
	})
	ctx := context.Background()
	fr := &fakeRunner{
		handler: func(name string, args []string) CommandResult {
			if name == "zfs" && hasArg(args, "volsize") {
				return CommandResult{Stdout: []byte("10737418240")}
			}
			return CommandResult{}
		},
	}
	b.runner = fr
	n, err := b.zfsVolSize(ctx, "tank/vm", "lv0")
	if err != nil || n != 10*gibibyte {
		t.Fatalf("strict zfs size parse failed: n=%d err=%v", n, err)
	}
	// Reject human/float (single token "10G" fails ParseInt).
	fr.handler = func(name string, args []string) CommandResult {
		return CommandResult{Stdout: []byte("10G")}
	}
	if _, err := b.zfsVolSize(ctx, "tank/vm", "lv0"); err == nil {
		t.Fatalf("zfs human size must be rejected")
	}
	// Reject zero.
	fr.handler = func(name string, args []string) CommandResult {
		return CommandResult{Stdout: []byte("0")}
	}
	if _, err := b.zfsVolSize(ctx, "tank/vm", "lv0"); err == nil {
		t.Fatalf("zfs zero size must be rejected")
	}
	// Reject extra token.
	fr.handler = func(name string, args []string) CommandResult {
		return CommandResult{Stdout: []byte("10737418240 extra")}
	}
	if _, err := b.zfsVolSize(ctx, "tank/vm", "lv0"); err == nil {
		t.Fatalf("zfs multi-token size must be rejected")
	}
	// Whitespace-only record rejected.
	fr.handler = func(name string, args []string) CommandResult {
		return CommandResult{Stdout: []byte("\n")}
	}
	if _, err := b.zfsVolSize(ctx, "tank/vm", "lv0"); err == nil {
		t.Fatalf("zfs whitespace-only size must be rejected")
	}
}

func TestVerifyReuseFailClosed(t *testing.T) {
	dir := t.TempDir()
	// Dir backend: size verification is delegated; VerifyReuse must reject.
	b := mkBackend(t, dir, nil)
	real := filepath.Join(dir, "vm-A.qcow2")
	if err := os.WriteFile(real, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := b.ClassifyLegacy(context.Background(), "vm-A", real)
	if err != nil {
		t.Fatalf("classify: %v", err)
	}
	ref.SizeGB = 10
	if _, err := b.VerifyReuse(context.Background(), ref); err == nil {
		t.Fatalf("dir VerifyReuse must reject (size unverified)")
	}

	// LVM reuse mismatch: runner reports a different size. The lvs list call
	// ends with "vg0/<name>", so we return the exact target by closure.
	b2 := mkBackend(t, "", func(b *ManagedVolumeBackend) { b.lvmVGs["vg0"] = struct{}{} })
	lref, err := b2.ResolveCreate(context.Background(), "vm-A", "op-1", diskops.BackendLVM, "vg0", 10)
	if err != nil {
		t.Fatalf("resolve lvm: %v", err)
	}
	lref.SizeGB = 10
	lvTarget := "/dev/vg0/" + lref.Name
	b2.runner = &fakeRunner{
		handler: func(name string, args []string) CommandResult {
			if name == "lvs" && hasArg(args, "lv_path") {
				return CommandResult{Stdout: []byte(lvTarget)} // present
			}
			if name == "lvs" && hasArg(args, "lv_size") {
				return CommandResult{Stdout: []byte("21474836480")} // 20G, mismatches 10G request
			}
			return CommandResult{}
		},
	}
	_, rerr := b2.VerifyReuse(context.Background(), lref)
	if rerr == nil || !errors.Is(rerr, ErrSizeMismatch) {
		t.Fatalf("lvm reuse mismatch must yield ErrSizeMismatch, got %v", rerr)
	}
	// LVM reuse match.
	b2.runner = &fakeRunner{
		handler: func(name string, args []string) CommandResult {
			if name == "lvs" && hasArg(args, "lv_path") {
				return CommandResult{Stdout: []byte(lvTarget)}
			}
			if name == "lvs" && hasArg(args, "lv_size") {
				return CommandResult{Stdout: []byte("10737418240")} // 10G
			}
			return CommandResult{}
		},
	}
	lref.SizeGB = 10
	if _, err := b2.VerifyReuse(context.Background(), lref); err != nil {
		t.Fatalf("lvm reuse match should succeed: %v", err)
	}

	// Overflow / non-positive / too-large sizeGB rejected by sizeGBToBytes.
	if _, err := sizeGBToBytes(0); err == nil {
		t.Fatalf("zero size must error")
	}
	if _, err := sizeGBToBytes(-5); err == nil {
		t.Fatalf("negative size must error")
	}
	if _, err := sizeGBToBytes(maxSizeGB + 1); err == nil {
		t.Fatalf("implausibly large size must error")
	}
	if _, err := sizeGBToBytes(10); err != nil {
		t.Fatalf("valid size rejected: %v", err)
	}
}

// TestSizeGBSignedSafeBounds checks the signed-safe GiB sizing bound.
func TestSizeGBSignedSafeBounds(t *testing.T) {
	// Exact safe max when representable.
	safe := int(math.MaxInt64 / gibibyte)
	n, err := sizeGBToBytes(safe)
	if err != nil {
		t.Fatalf("safe max should convert: %v", err)
	}
	if n != math.MaxInt64/gibibyte*gibibyte {
		t.Fatalf("safe max bytes mismatch: %d != %d", n, math.MaxInt64/gibibyte*gibibyte)
	}
	// One above safe max rejected.
	if _, err := sizeGBToBytes(safe + 1); err == nil {
		t.Fatalf("safe max + 1 must be rejected")
	}
	// Wrapping-scale value that would overflow signed GiB conversion rejected
	// without relying on int overflow behavior.
	if _, err := sizeGBToBytes((1 << 34) + 1); err == nil {
		t.Fatalf("wrapping-scale size must be rejected")
	}
}

func TestPresenceZeroUnknownAndInvalidNormalize(t *testing.T) {
	// Zero value must be Unknown.
	var p diskops.Presence
	if p != diskops.Unknown {
		t.Fatalf("Presence zero must be Unknown, got %s", p)
	}
	// Invalid value normalizes to Unknown.
	var bogus diskops.Presence = 99
	if bogus.Normalize() != diskops.Unknown {
		t.Fatalf("invalid Presence must normalize to Unknown, got %s", bogus.Normalize())
	}
	// Valid values round-trip.
	for _, v := range []diskops.Presence{diskops.Unknown, diskops.Absent, diskops.Present} {
		if v.Normalize() != v {
			t.Fatalf("valid Presence %s should normalize to itself", v)
		}
	}
}

func TestLVMDeleteErrorWhitespaceStdoutUnknown(t *testing.T) {
	// A delete that returns whitespace-only stderr must be treated as an error,
	// and with a post-proven-absent list it yields Unknown,error (never Absent).
	ref := func() diskops.VolumeRef {
		b := mkBackend(t, "", func(b *ManagedVolumeBackend) { b.lvmVGs["vg0"] = struct{}{} })
		r, err := b.ResolveCreate(context.Background(), "vm-A", "op-1", diskops.BackendLVM, "vg0", 10)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		return r
	}()
	target := "/dev/vg0/" + ref.Name
	state := struct{ step int }{}
	b := mkBackend(t, "", func(b *ManagedVolumeBackend) {
		b.lvmVGs["vg0"] = struct{}{}
		b.runner = &fakeRunner{
			handler: func(name string, args []string) CommandResult {
				if name == "lvs" {
					if state.step >= 1 {
						return CommandResult{} // post delete proven absent
					}
					return CommandResult{Stdout: []byte(target)}
				}
				if name == "lvremove" {
					state.step = 1
					return CommandResult{Stderr: []byte(" ")} // whitespace stderr => error
				}
				return CommandResult{}
			},
		}
	})
	pres, derr := b.DestroyVerified(context.Background(), ref)
	if pres != diskops.Unknown {
		t.Fatalf("delete whitespace-stderr + proven-absent must be Unknown, got %s", pres)
	}
	if derr == nil {
		t.Fatalf("expected error on delete whitespace-stderr")
	}
}

func TestValidateRefRejectsInconsistentPath(t *testing.T) {
	dir := t.TempDir()
	b := mkBackend(t, dir, nil)
	// Forge a ref whose ResolvedPath diverges from canonical.
	ref := diskops.VolumeRef{VMID: "vm-A", Backend: diskops.BackendDir, Pool: dir, Name: "mv2-vmA-abc.qcow2", ResolvedPath: "/tmp/evil.qcow2", SizeGB: 1}
	if err := b.validateRef(ref); err == nil {
		t.Fatalf("inconsistent ResolvedPath must be rejected")
	}
}

func TestValidateRefRootRevalidation(t *testing.T) {
	dir := t.TempDir()
	b := mkBackend(t, dir, nil)
	// A ref pointing at an allowlisted root that has since become a symlink must
	// fail validation at operation time.
	link := dir + "-link"
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("cannot create symlink: %v", err)
	}
	// Point the backend's allowlist entry at the symlink (simulating a root that
	// was valid at construction but is now unsafe).
	bad := b
	bad.dirRoots = map[string]struct{}{link: {}}
	ref := diskops.VolumeRef{VMID: "vm-A", Backend: diskops.BackendDir, Pool: link, Name: "vm-A.qcow2", ResolvedPath: filepath.Join(link, "vm-A.qcow2")}
	if err := bad.validateRef(ref); err == nil {
		t.Fatalf("symlinked root must fail runtime revalidation")
	}
}

func TestDirDeleteRefusesNonregular(t *testing.T) {
	dir := t.TempDir()
	b := mkBackend(t, dir, nil)
	// A directory at the managed path must refuse deletion (non-regular).
	d := filepath.Join(dir, "v.qcow2")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	ref := diskops.VolumeRef{VMID: "vm-A", Backend: diskops.BackendDir, Pool: dir, Name: "v.qcow2", ResolvedPath: d, SizeGB: 1}
	if err := b.delete(context.Background(), ref); err == nil {
		t.Fatalf("delete must refuse non-regular target")
	}
}
