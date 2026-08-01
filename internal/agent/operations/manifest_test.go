package operations

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func newTempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return New(dir)
}

func fp(t *testing.T, s string) string {
	t.Helper()
	return Fingerprint([]byte(s))
}

func TestFingerprintStable(t *testing.T) {
	a := Fingerprint([]byte(`{"vm":"x","size":1}`))
	b := Fingerprint([]byte(`{"vm":"x","size":1}`))
	if a != b {
		t.Fatalf("fingerprint not stable: %s != %s", a, b)
	}
	c := Fingerprint([]byte(`{"vm":"x","size":2}`))
	if a == c {
		t.Fatalf("different input produced same fingerprint")
	}
}

func TestDefaultDirHonorsEnv(t *testing.T) {
	t.Setenv("MABURVM_DATA_DIR", "/tmp/custom-data")
	if got := DefaultDir(); got != "/tmp/custom-data/operations" {
		t.Fatalf("DefaultDir = %q; want /tmp/custom-data/operations", got)
	}
	t.Setenv("MABURVM_DATA_DIR", "")
	if got := DefaultDir(); got != "/var/lib/maburvm/operations" {
		t.Fatalf("DefaultDir = %q; want /var/lib/maburvm/operations", got)
	}
}

func TestBeginOrLoadCreatesInProgress(t *testing.T) {
	s := newTempStore(t)
	rec, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "req1"), DiskMeta{Device: "", Path: "/img/a.qcow2"})
	if err != nil {
		t.Fatalf("BeginOrLoad: %v", err)
	}
	if rec.State != StateInProgress {
		t.Fatalf("state = %s; want in_progress", rec.State)
	}
	if rec.Fingerprint != fp(t, "req1") {
		t.Fatalf("fingerprint not persisted")
	}
	if rec.Version != ManifestVersion {
		t.Fatalf("version = %d; want %d", rec.Version, ManifestVersion)
	}
	// File exists on disk.
	if _, err := os.Stat(s.pathFor("op-1")); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
}

func TestBeginOrLoadReplaysSameFingerprint(t *testing.T) {
	s := newTempStore(t)
	_, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "req1"), DiskMeta{Path: "/img/a.qcow2"})
	if err != nil {
		t.Fatalf("first BeginOrLoad: %v", err)
	}
	// Retry with identical fingerprint must return the same record, not create a
	// new one. Since there is no external side effect here, the contract is that
	// the load path returns the existing one.
	rec2, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "req1"), DiskMeta{Path: "/img/a.qcow2"})
	if err != nil {
		t.Fatalf("retry BeginOrLoad: %v", err)
	}
	if rec2.State != StateInProgress {
		t.Fatalf("replay state = %s; want in_progress", rec2.State)
	}
	if rec2.Fingerprint != fp(t, "req1") {
		t.Fatalf("replay fingerprint mismatch")
	}
}

func TestBeginOrLoadRejectsMismatch(t *testing.T) {
	s := newTempStore(t)
	if _, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "req1"), DiskMeta{}); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Different fingerprint.
	if _, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "req2"), DiskMeta{}); !errors.Is(err, ErrMismatch) {
		t.Fatalf("diff fingerprint err = %v; want ErrMismatch", err)
	}
	// Different VM.
	if _, err := s.BeginOrLoad("vm-2", KindAttachDisk, "op-1", fp(t, "req1"), DiskMeta{}); !errors.Is(err, ErrMismatch) {
		t.Fatalf("diff vm err = %v; want ErrMismatch", err)
	}
	// Different kind.
	if _, err := s.BeginOrLoad("vm-1", KindDetachDisk, "op-1", fp(t, "req1"), DiskMeta{}); !errors.Is(err, ErrMismatch) {
		t.Fatalf("diff kind err = %v; want ErrMismatch", err)
	}
}

func TestCompleteIdempotentReplay(t *testing.T) {
	s := newTempStore(t)
	if _, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "req1"), DiskMeta{Device: "vdb", Path: "/img/a.qcow2"}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	rec, err := s.Complete("vm-1", "op-1", fp(t, "req1"), true, DispositionAttached, "", "")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if !rec.Success || rec.Disposition != DispositionAttached || rec.State != StateCompleted {
		t.Fatalf("unexpected terminal record: %+v", rec)
	}
	if rec.CompletedAt.IsZero() {
		t.Fatalf("CompletedAt not set")
	}
	// Retry completion with identical fingerprint must replay, not create new.
	rec2, err := s.Complete("vm-1", "op-1", fp(t, "req1"), true, DispositionAttached, "", "")
	if err != nil {
		t.Fatalf("replay complete: %v", err)
	}
	if rec2.Success != rec.Success || rec2.Disposition != rec.Disposition {
		t.Fatalf("replay changed semantics: %+v vs %+v", rec2, rec)
	}
}

func TestCompleteRejectsFingerprintMismatch(t *testing.T) {
	s := newTempStore(t)
	if _, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "req1"), DiskMeta{}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := s.Complete("vm-1", "op-1", fp(t, "WRONG"), true, DispositionAttached, "", ""); !errors.Is(err, ErrMismatch) {
		t.Fatalf("err = %v; want ErrMismatch", err)
	}
}

func TestMarkUncertainPersistsAndReplays(t *testing.T) {
	s := newTempStore(t)
	if _, err := s.BeginOrLoad("vm-1", KindDestroyVM, "op-1", fp(t, "req1"), DiskMeta{Path: "/img/a.qcow2"}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	rec, err := s.MarkUncertain("vm-1", "op-1", fp(t, "req1"), "E_TIMEOUT", "timeout before verify")
	if err != nil {
		t.Fatalf("uncertain: %v", err)
	}
	if rec.State != StateUncertain || rec.Disposition != DispositionUnknown {
		t.Fatalf("unexpected uncertain record: %+v", rec)
	}
	// Replay must return the same uncertain record, not downgrade/upgrade.
	rec2, err := s.MarkUncertain("vm-1", "op-1", fp(t, "req1"), "E_TIMEOUT", "timeout before verify")
	if err != nil {
		t.Fatalf("replay uncertain: %v", err)
	}
	if rec2.State != StateUncertain {
		t.Fatalf("replay changed state to %s", rec2.State)
	}
}

func TestUncertainNotDowngradedByComplete(t *testing.T) {
	s := newTempStore(t)
	if _, err := s.BeginOrLoad("vm-1", KindDetachDisk, "op-1", fp(t, "req1"), DiskMeta{Device: "vdb"}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := s.MarkUncertain("vm-1", "op-1", fp(t, "req1"), "E_AMBIG", "ambiguous"); err != nil {
		t.Fatalf("uncertain: %v", err)
	}
	// A later retried Complete with matching fingerprint must replay the
	// uncertain record, not overwrite it as success.
	rec, err := s.Complete("vm-1", "op-1", fp(t, "req1"), true, DispositionAbsent, "", "")
	if err != nil {
		t.Fatalf("complete after uncertain: %v", err)
	}
	if rec.State != StateUncertain {
		t.Fatalf("completed overwrote uncertain: state=%s", rec.State)
	}
}

func TestSurvivesRestartReload(t *testing.T) {
	dir := t.TempDir()
	s1 := New(dir)
	if _, err := s1.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "req1"), DiskMeta{Device: "vdb", Path: "/img/a.qcow2"}); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := s1.Complete("vm-1", "op-1", fp(t, "req1"), true, DispositionAttached, "", ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// New Store instance (simulates agent restart) must reload from disk.
	s2 := New(dir)
	rec, err := s2.Load("op-1")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if rec.VMID != "vm-1" || rec.Kind != KindAttachDisk || rec.Disposition != DispositionAttached {
		t.Fatalf("reloaded record wrong: %+v", rec)
	}
}

func TestInvalidIDsRejected(t *testing.T) {
	s := newTempStore(t)
	cases := []string{"", "../escape", "a/b", "a\\b", "..", "has space", "new\nline", string([]byte{0})}
	for _, bad := range cases {
		if _, err := s.BeginOrLoad(bad, KindAttachDisk, "op-1", fp(t, "r"), DiskMeta{}); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("vm_id %q: err=%v; want ErrInvalidID", bad, err)
		}
		if _, err := s.BeginOrLoad("vm-1", KindAttachDisk, bad, fp(t, "r"), DiskMeta{}); !errors.Is(err, ErrInvalidID) {
			t.Fatalf("operation_id %q: err=%v; want ErrInvalidID", bad, err)
		}
	}
	// Oversized ID.
	big := strings.Repeat("a", maxIDLen+1)
	if _, err := s.BeginOrLoad("vm-1", KindAttachDisk, big, fp(t, "r"), DiskMeta{}); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("oversized id err=%v; want ErrInvalidID", err)
	}
}

func TestCorruptManifestIsIntegrityError(t *testing.T) {
	s := newTempStore(t)
	// Write a corrupt manifest directly using the hashed path.
	path := s.pathFor("op-1")
	if err := os.MkdirAll(s.root, manifestDirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not valid json"), manifestPerm); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load("op-1")
	var ie *IntegrityError
	if !errors.As(err, &ie) {
		t.Fatalf("err = %v; want *IntegrityError", err)
	}
	// A corrupt manifest must NOT be silently overwritten by BeginOrLoad.
	if _, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "r"), DiskMeta{}); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("BeginOrLoad err = %v; want ErrIntegrity", err)
	}
}

func TestEmptyManifestFileIsIntegrityError(t *testing.T) {
	s := newTempStore(t)
	path := s.pathFor("op-2")
	if err := os.MkdirAll(s.root, manifestDirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("   "), manifestPerm); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load("op-2"); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("empty manifest err = %v; want ErrIntegrity", err)
	}
}

func TestPerVMSerialization(t *testing.T) {
	s := newTempStore(t)
	const n = 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			op := "op-" + string(rune('a'+i%26)) + "-" + itoa(i)
			_, err := s.BeginOrLoad("vm-shared", KindAttachDisk, op, fp(t, op), DiskMeta{})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent begin failed: %v", err)
		}
	}
	// All manifests should be present and parseable.
	for i := 0; i < n; i++ {
		op := "op-" + string(rune('a'+i%26)) + "-" + itoa(i)
		if _, err := s.Load(op); err != nil {
			t.Fatalf("missing manifest for %s: %v", op, err)
		}
	}
}

func TestAtomicReloadRoundTrip(t *testing.T) {
	s := newTempStore(t)
	rec, err := s.BeginOrLoad("vm-1", KindDestroyVM, "op-d", fp(t, "r"), DiskMeta{Path: "/img/vm-1.qcow2"})
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	rec, err = s.Complete("vm-1", "op-d", fp(t, "r"), false, DispositionUnknown, "E_TRANSPORT", "lost connection")
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	raw, err := os.ReadFile(s.pathFor("op-d"))
	if err != nil {
		t.Fatal(err)
	}
	var round Record
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("re-read json: %v", err)
	}
	if round.Success != rec.Success || round.ErrorCode != "E_TRANSPORT" || round.State != StateCompleted {
		t.Fatalf("round-trip mismatch: %+v", round)
	}
}

func TestUpdateDiskMetaNoopOnTerminal(t *testing.T) {
	s := newTempStore(t)
	if _, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "r"), DiskMeta{Device: "vdb", Path: "/img/a.qcow2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete("vm-1", "op-1", fp(t, "r"), true, DispositionAttached, "", ""); err != nil {
		t.Fatal(err)
	}
	// Update on a terminal record must return the existing record unchanged.
	rec, err := s.UpdateDiskMeta("vm-1", "op-1", DiskMeta{Device: "vdc", Path: "/img/b.qcow2"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if rec.Device != "vdb" || rec.Path != "/img/a.qcow2" {
		t.Fatalf("terminal record mutated: %+v", rec)
	}
}

func TestUpdateDiskMetaAmendsInProgress(t *testing.T) {
	s := newTempStore(t)
	if _, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-1", fp(t, "r"), DiskMeta{}); err != nil {
		t.Fatal(err)
	}
	rec, err := s.UpdateDiskMeta("vm-1", "op-1", DiskMeta{Device: "vdb", Path: "/img/a.qcow2", PoolType: "dir"})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if rec.Device != "vdb" || rec.Path != "/img/a.qcow2" || rec.PoolType != "dir" {
		t.Fatalf("meta not amended: %+v", rec)
	}
}

// wantPathsEqual asserts exact order-preserving equality of two path lists.
func wantPathsEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("paths len = %d; want %d (%v vs %v)", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths[%d] = %q; want %q (%v vs %v)", i, got[i], want[i], got, want)
		}
	}
}

func TestMultiPathBeginPersistRestartReplay(t *testing.T) {
	dir := t.TempDir()
	s1 := New(dir)
	want := []string{"/img/disk-0.qcow2", "/img/disk-1.qcow2", "/vol/extra.img"}
	rec, err := s1.BeginOrLoad("vm-9", KindDestroyVM, "op-mp", fp(t, "req-mp"), DiskMeta{Paths: want})
	if err != nil {
		t.Fatalf("BeginOrLoad: %v", err)
	}
	wantPathsEqual(t, rec.Paths, want)

	// Raw JSON on disk must actually contain the paths, in order.
	raw, err := os.ReadFile(s1.pathFor("op-mp"))
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatalf("wire unmarshal: %v", err)
	}
	wantPathsEqual(t, wire.Paths, want)

	// Simulate agent restart: new Store loads from disk.
	s2 := New(dir)
	loaded, err := s2.Load("op-mp")
	if err != nil {
		t.Fatalf("restart load: %v", err)
	}
	wantPathsEqual(t, loaded.Paths, want)

	// Same-fingerprint replay must return persisted list unchanged.
	replayed, err := s2.BeginOrLoad("vm-9", KindDestroyVM, "op-mp", fp(t, "req-mp"), DiskMeta{Paths: []string{"/other/path"}})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	wantPathsEqual(t, replayed.Paths, want)
}

func TestMultiPathUpdatePersistsInProgress(t *testing.T) {
	s := newTempStore(t)
	if _, err := s.BeginOrLoad("vm-9", KindDestroyVM, "op-u", fp(t, "r"), DiskMeta{}); err != nil {
		t.Fatal(err)
	}
	want := []string{"/a/one.qcow2", "/b/two.qcow2"}
	rec, err := s.UpdateDiskMeta("vm-9", "op-u", DiskMeta{Paths: want})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	wantPathsEqual(t, rec.Paths, want)

	// Reload from disk proves durability.
	reloaded, err := s.Load("op-u")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	wantPathsEqual(t, reloaded.Paths, want)
}

func TestMultiPathUpdateNoopOnTerminal(t *testing.T) {
	s := newTempStore(t)
	orig := []string{"/orig/disk.qcow2"}
	if _, err := s.BeginOrLoad("vm-9", KindDestroyVM, "op-t", fp(t, "r"), DiskMeta{Paths: orig}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete("vm-9", "op-t", fp(t, "r"), true, DispositionAbsent, "", ""); err != nil {
		t.Fatal(err)
	}
	// A different list from the caller must NOT replace persisted paths.
	rec, err := s.UpdateDiskMeta("vm-9", "op-t", DiskMeta{Paths: []string{"/new/disk.qcow2"}})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	wantPathsEqual(t, rec.Paths, orig)
}

func TestMultiPathInvalidListsRejected(t *testing.T) {
	s := newTempStore(t)
	cases := []struct {
		name  string
		paths []string
	}{
		{"empty entry", []string{"/a", "", "/c"}},
		{"duplicate", []string{"/a", "/a"}},
		{"control char", []string{"/a", "/b\x07c"}},
	}
	// Build the too-many list deterministically.
	tooMany := make([]string, maxPaths+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("/disk-%d", i)
	}
	cases = append(cases, struct {
		name  string
		paths []string
	}{"too many", tooMany})
	for i, c := range cases {
		// Use an independently valid operation ID so ErrInvalidID proves that the
		// path-list validation failed rather than the operation-ID validation.
		_, err := s.BeginOrLoad("vm-x", KindDestroyVM, fmt.Sprintf("op-invalid-%d", i), fp(t, "r"), DiskMeta{Paths: c.paths})
		if !errors.Is(err, ErrInvalidID) {
			t.Fatalf("%s: err=%v; want ErrInvalidID", c.name, err)
		}
	}
}

func TestLegacySinglePathLoadsWithoutPaths(t *testing.T) {
	s := newTempStore(t)
	// Valid legacy record: only singular `path`, no `paths` key at all.
	legacy := `{
  "version": 1,
  "operation_id": "op-legacy",
  "vm_id": "vm-1",
  "kind": "attach_disk",
  "fingerprint": "abc",
  "device": "vdb",
  "path": "/img/legacy.qcow2",
  "pool_type": "dir",
  "state": "in_progress",
  "disposition": "UNSPECIFIED",
  "success": false,
  "created_at": "2026-01-01T00:00:00Z",
  "updated_at": "2026-01-01T00:00:00Z"
}`
	if err := os.WriteFile(s.pathFor("op-legacy"), []byte(legacy), manifestPerm); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Load("op-legacy")
	if err != nil {
		t.Fatalf("load legacy: %v", err)
	}
	if rec.Path != "/img/legacy.qcow2" {
		t.Fatalf("legacy path = %q", rec.Path)
	}
	// No inference: Paths stays nil/empty.
	if len(rec.Paths) != 0 {
		t.Fatalf("legacy Paths should be empty, got %v", rec.Paths)
	}
	// Backward-compatible replay: same fingerprint returns it unchanged.
	rep, err := s.BeginOrLoad("vm-1", KindAttachDisk, "op-legacy", "abc", DiskMeta{Path: "/img/legacy.qcow2"})
	if err != nil {
		t.Fatalf("replay legacy: %v", err)
	}
	if rep.Path != "/img/legacy.qcow2" || len(rep.Paths) != 0 {
		t.Fatalf("legacy replay mutated: %+v", rep)
	}
}

// --- small helpers ---

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

var _ = strings.Repeat
