package ceph

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"libvirt.org/go/libvirtxml"
)

// fakeRBD answers rbd, ceph and qemu-img: reply maps the first words that
// follow the access arguments to stdout, or to an error whose text is stderr.
type fakeRBD struct {
	mu    sync.Mutex
	calls []string
	reply func(cmd string) (string, error)
}

func (f *fakeRBD) install(t *testing.T) {
	t.Helper()
	old := Run
	t.Cleanup(func() { Run = old })
	Run = func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name != "qemu-img" {
			if len(args) < 6 || args[0] != "--conf" || args[2] != "--keyring" || args[4] != "--id" {
				t.Errorf("%s ran without explicit --conf/--keyring/--id: %v", name, args)
			}
			args = args[6:]
		}
		cmd := name + " " + strings.Join(args, " ")
		if strings.Contains(cmd, testKey) {
			t.Errorf("the key is on a command line: %s", cmd)
		}
		f.mu.Lock()
		f.calls = append(f.calls, cmd)
		f.mu.Unlock()
		out, err := f.reply(cmd)
		if err != nil {
			return nil, []byte(err.Error()), errors.New("exit status 1")
		}
		return []byte(out), nil, nil
	}
}

func (f *fakeRBD) ran(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func testImage(t *testing.T) Image {
	t.Helper()
	if _, err := Commit(stage(t)); err != nil {
		t.Fatal(err)
	}
	img, err := OpenImage("rbd://" + testID + "/vms/maburvm-vm1-vda")
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestOpenImageNeedsTheClusterEnabledHere(t *testing.T) {
	Dir = t.TempDir()
	if _, err := OpenImage("rbd://" + testID + "/vms/x"); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("err = %v, want ErrNotEnabled", err)
	}
	if _, err := OpenImage("/var/lib/libvirt/images/x.qcow2"); err == nil {
		t.Fatal("a file path opened as an image")
	}
}

func TestRemoveLeavesAnImageAnotherClientHasOpen(t *testing.T) {
	img := testImage(t)
	f := &fakeRBD{reply: func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "rbd status") {
			return `{"watchers":[{"address":"10.0.0.9:0/1234","client":4567,"cookie":1}]}`, nil
		}
		return "", nil
	}}
	f.install(t)
	// No waiting in the test: the watcher is there on the first look and after.
	err := img.CheckUnused(context.Background(), 0)
	var inUse *InUseError
	if !errors.As(err, &inUse) || inUse.Watchers[0] != "10.0.0.9:0/1234" {
		t.Fatalf("err = %v, want an InUseError naming the watcher", err)
	}
	if f.ran("rbd rm") || f.ran("rbd snap purge") {
		t.Fatal("an image with a watcher was touched")
	}
}

func TestRemovePurgesSnapshotsThenTheImage(t *testing.T) {
	img := testImage(t)
	f := &fakeRBD{reply: func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "rbd status") {
			return `{"watchers":[]}`, nil
		}
		return "", nil
	}}
	f.install(t)
	if err := img.Remove(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	got := strings.Join(f.calls, "\n")
	f.mu.Unlock()
	purge, rm := strings.Index(got, "rbd snap purge --no-progress vms/maburvm-vm1-vda"), strings.Index(got, "rbd rm --no-progress vms/maburvm-vm1-vda")
	if purge < 0 || rm < purge {
		t.Fatalf("calls:\n%s", got)
	}
}

func TestRemoveOfAMissingImageIsDone(t *testing.T) {
	img := testImage(t)
	f := &fakeRBD{reply: func(string) (string, error) {
		return "", errors.New("rbd: error opening image maburvm-vm1-vda: (2) No such file or directory")
	}}
	f.install(t)
	if err := img.Remove(context.Background()); err != nil {
		t.Fatalf("Remove = %v", err)
	}
	if _, err := img.Size(context.Background()); !errors.Is(err, ErrNoImage) {
		t.Fatalf("Size = %v, want ErrNoImage", err)
	}
}

func TestGrowNeverShrinks(t *testing.T) {
	img := testImage(t)
	f := &fakeRBD{reply: func(cmd string) (string, error) {
		if strings.HasPrefix(cmd, "rbd info") {
			return `{"name":"maburvm-vm1-vda","size":21474836480,"features":["layering","exclusive-lock"]}`, nil
		}
		return "", nil
	}}
	f.install(t)
	if err := img.Grow(context.Background(), 10<<30); err != nil || f.ran("rbd resize") {
		t.Fatalf("a smaller size resized the image (err %v)", err)
	}
	if err := img.Grow(context.Background(), 30<<30); err != nil || !f.ran("rbd resize --no-progress --size 30720 vms/maburvm-vm1-vda") {
		t.Fatalf("grow to 30 GiB: err %v, calls %v", err, f.calls)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "allow-shrink") {
			t.Fatalf("shrink allowed: %s", c)
		}
	}
}

func TestFenceBreaksLocksAndBlocklistsOtherWatchers(t *testing.T) {
	img := testImage(t)
	f := &fakeRBD{reply: func(cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "rbd lock ls"):
			return `[{"id":"auto 94321","locker":"client.4567","address":"10.0.0.9:0/1234"}]`, nil
		case strings.HasPrefix(cmd, "rbd status"):
			return `{"watchers":[{"address":"10.0.0.9:0/1234"},{"address":"10.0.0.8:0/77"}]}`, nil
		case strings.HasPrefix(cmd, "ceph --connect-timeout 10 osd blocklist"):
			return "", errors.New("no valid command found") // an older cluster
		}
		return "", nil
	}}
	f.install(t)
	got, err := img.Fence(context.Background())
	if err != nil || strings.Join(got, ",") != "10.0.0.8:0/77,10.0.0.9:0/1234" {
		t.Fatalf("Fence = %v, %v", got, err)
	}
	if !f.ran("rbd lock rm vms/maburvm-vm1-vda auto 94321 client.4567") {
		t.Fatalf("the lock was not broken: %v", f.calls)
	}
	if !f.ran("ceph --connect-timeout 10 osd blacklist add 10.0.0.8:0/77") {
		t.Fatalf("the watcher without a lock was not blocklisted: %v", f.calls)
	}
}

func TestFenceFailsWhenTheClusterRefuses(t *testing.T) {
	img := testImage(t)
	f := &fakeRBD{reply: func(cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "rbd lock ls"):
			return `{"auto 1":{"locker":"client.9","address":"10.0.0.9:0/1"}}`, nil // the form before Nautilus
		case strings.HasPrefix(cmd, "rbd lock rm"):
			return "", errors.New("rbd: releasing lock failed: (1) Operation not permitted")
		}
		return "", nil
	}}
	f.install(t)
	if _, err := img.Fence(context.Background()); err == nil || !strings.Contains(err.Error(), "profile rbd") {
		t.Fatalf("Fence = %v, want a refusal that names the caps", err)
	}
}

func TestReplaceWithKeepsTheImageWhenTheImportFails(t *testing.T) {
	img := testImage(t)
	f := &fakeRBD{reply: func(cmd string) (string, error) {
		switch {
		case strings.HasPrefix(cmd, "rbd info"):
			return `{"size":10737418240}`, nil
		case strings.HasPrefix(cmd, "rbd status"):
			return `{"watchers":[]}`, nil
		case strings.HasPrefix(cmd, "qemu-img convert"):
			return "", errors.New("qemu-img: error while writing")
		}
		return "", nil
	}}
	f.install(t)
	if err := img.ReplaceWith(context.Background(), "/tmp/t.qcow2", "qcow2", 0); err == nil {
		t.Fatal("a failed import passed")
	}
	if f.ran("rbd rename") || f.ran("rbd rm --no-progress vms/maburvm-vm1-vda ") || f.ran("rbd snap purge --no-progress vms/maburvm-vm1-vda ") {
		t.Fatalf("the image was touched after a failed import: %v", f.calls)
	}
	for _, c := range f.calls {
		if c == "rbd rm --no-progress vms/maburvm-vm1-vda" {
			t.Fatalf("the image was removed after a failed import")
		}
	}
}

func TestQemuURLNamesTheKeyringNotTheKey(t *testing.T) {
	img := testImage(t)
	u := img.QemuURL("maburvm-backup")
	want := "rbd:vms/maburvm-vm1-vda@maburvm-backup:id=maburvm:conf=" + img.Conf() + ":keyring=" + img.Keyring()
	if u != want || strings.Contains(u, testKey) {
		t.Fatalf("QemuURL = %s", u)
	}
}

func TestDiskSourceRoundTrip(t *testing.T) {
	img := testImage(t)
	d := libvirtxml.DomainDisk{Device: "disk", Source: img.DiskSource()}
	doc, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`protocol="rbd"`, `name="vms/maburvm-vm1-vda"`, `<host name="10.0.0.1"></host>`, `<host name="10.0.0.2" port="6789"></host>`,
		`<auth username="maburvm">`, `<secret type="ceph" uuid="` + testID + `"></secret>`} {
		if !strings.Contains(doc, want) {
			t.Errorf("disk XML lacks %s:\n%s", want, doc)
		}
	}
	if strings.Contains(doc, testKey) || strings.Contains(doc, "keyring") {
		t.Errorf("disk XML carries the key or the agent's files:\n%s", doc)
	}
	var back libvirtxml.DomainDisk
	if err := back.Unmarshal(doc); err != nil {
		t.Fatal(err)
	}
	if p, ok := DiskPath(&back); !ok || p != img.Path() {
		t.Fatalf("DiskPath = %q, %v", p, ok)
	}
	if h := monitorHost("[2001:db8::1]:3300"); h.Name != "2001:db8::1" || h.Port != "3300" {
		t.Errorf("monitorHost = %+v", h)
	}
	// A disk on RBD that MaburVM did not define names no cluster of ours.
	if _, ok := DiskPath(&libvirtxml.DomainDisk{Source: &libvirtxml.DomainDiskSource{Network: &libvirtxml.DomainDiskSourceNetwork{Protocol: "rbd", Name: "a/b"}}}); ok {
		t.Error("a disk without a secret read as ours")
	}
}
