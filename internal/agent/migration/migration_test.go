package migration

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	nodeA = "11111111-1111-1111-1111-111111111111"
	nodeB = "22222222-2222-2222-2222-222222222222"
	vmID  = "33333333-3333-3333-3333-333333333333"
)

func testLayout(t *testing.T) Layout {
	dir := t.TempDir()
	return Layout{Dir: filepath.Join(dir, "migration"), AuthorizedKeys: filepath.Join(dir, "root", ".ssh", "authorized_keys"), HostKeyGlob: filepath.Join(dir, "etc", "ssh_host_*_key.pub")}
}

func TestEnsureKeyIsStable(t *testing.T) {
	l := testLayout(t)
	first, err := EnsureKey(l, "maburvm-migration@a")
	if err != nil {
		t.Fatal(err)
	}
	again, err := EnsureKey(l, "maburvm-migration@a")
	if err != nil || again != first {
		t.Fatalf("second call gave %q, %v; want %q", again, err, first)
	}
	if !strings.HasPrefix(first, "ssh-ed25519 ") || Fingerprint(first) == "" {
		t.Fatalf("not an ed25519 key: %q", first)
	}
	if fi, err := os.Stat(l.KeyPath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode: %v %v", fi.Mode(), err)
	}
}

func testKey(t *testing.T, l Layout) string {
	k, err := EnsureKey(l, "x")
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestRenderAuthorizedKeysKeepsOtherLinesAndRestricts(t *testing.T) {
	key := testKey(t, testLayout(t))
	existing := "ssh-ed25519 AAAAoperator admin@laptop\n"
	out, err := RenderAuthorizedKeys(existing, []Peer{{NodeID: nodeB, Address: "203.0.113.7", PublicKey: key}}, "/usr/local/bin/maburvm-agent migration-shell")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, existing) {
		t.Fatalf("operator key lost:\n%s", out)
	}
	want := `from="203.0.113.7",restrict,command="/usr/local/bin/maburvm-agent migration-shell" ` + strings.TrimSuffix(key, " x") + " maburvm-migration:" + nodeB
	if !strings.Contains(out, want+"\n") {
		t.Fatalf("peer line missing; got:\n%s\nwant line:\n%s", out, want)
	}
	// Removing the peer removes exactly its block.
	again, err := RenderAuthorizedKeys(out, nil, "/usr/local/bin/maburvm-agent migration-shell")
	if err != nil || again != existing {
		t.Fatalf("revocation left %q (%v), want %q", again, err, existing)
	}
}

func TestRenderAuthorizedKeysRefusesInjection(t *testing.T) {
	key := testKey(t, testLayout(t))
	if _, err := RenderAuthorizedKeys("", []Peer{{NodeID: nodeB, Address: `1.2.3.4",command="sh`, PublicKey: key}}, "x"); err == nil {
		t.Fatal("an address that is not an IP was accepted")
	}
	if _, err := RenderAuthorizedKeys("", []Peer{{NodeID: nodeB, Address: "1.2.3.4", PublicKey: "not a key"}}, "x"); err == nil {
		t.Fatal("garbage key accepted")
	}
	if _, err := RenderAuthorizedKeys("", nil, `a" b`); err == nil {
		t.Fatal("a quote in the forced command was accepted")
	}
	// Options smuggled in with the key are dropped.
	out, err := RenderAuthorizedKeys("", []Peer{{NodeID: nodeB, Address: "1.2.3.4", PublicKey: `command="sh" ` + key}}, "x")
	if err != nil || strings.Contains(out, `command="sh"`) {
		t.Fatalf("options kept: %q %v", out, err)
	}
}

func TestApplyPeersPinsHostKeysAndRevokes(t *testing.T) {
	l := testLayout(t)
	key := testKey(t, l)
	hostKey := strings.TrimSuffix(testKey(t, testLayout(t)), " x")
	peers := []Peer{{NodeID: nodeB, Address: "198.51.100.2", PublicKey: key, HostKeys: []string{hostKey}}}
	if err := ApplyPeers(l, peers, "/opt/agent migration-shell"); err != nil {
		t.Fatal(err)
	}
	kh, err := os.ReadFile(filepath.Join(l.KnownHostsDir(), nodeB))
	if err != nil || string(kh) != "198.51.100.2 "+hostKey+"\n" {
		t.Fatalf("known_hosts = %q, %v", kh, err)
	}
	cfg, _ := os.ReadFile(l.ConfigPath())
	for _, want := range []string{"Host 198.51.100.2\n    UserKnownHostsFile " + filepath.Join(l.KnownHostsDir(), nodeB), "StrictHostKeyChecking yes", "UserKnownHostsFile /dev/null", "BatchMode yes"} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("ssh_config lacks %q:\n%s", want, cfg)
		}
	}
	if strings.Contains(string(cfg), "StrictHostKeyChecking no") {
		t.Fatal("host key checking turned off")
	}
	ak, _ := os.ReadFile(l.AuthorizedKeys)
	if !strings.Contains(string(ak), "maburvm-migration:"+nodeB) {
		t.Fatalf("peer key not accepted:\n%s", ak)
	}
	// The node is removed: its key and pinned host keys go.
	if err := ApplyPeers(l, nil, "/opt/agent migration-shell"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(l.KnownHostsDir(), nodeB)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("known_hosts of a removed peer kept")
	}
	if ak, _ := os.ReadFile(l.AuthorizedKeys); strings.Contains(string(ak), nodeB) {
		t.Fatalf("removed peer still accepted:\n%s", ak)
	}
}

func TestHostKeysAndURIs(t *testing.T) {
	l := testLayout(t)
	_ = os.MkdirAll(filepath.Dir(l.HostKeyGlob), 0o755)
	k := testKey(t, testLayout(t))
	_ = os.WriteFile(filepath.Join(filepath.Dir(l.HostKeyGlob), "ssh_host_ed25519_key.pub"), []byte(k+"\n"), 0o644)
	_ = os.WriteFile(filepath.Join(filepath.Dir(l.HostKeyGlob), "ssh_host_rsa_key.pub"), []byte("garbage\n"), 0o644)
	if got := HostKeys(l.HostKeyGlob); len(got) != 1 || got[0] != strings.TrimSuffix(k, " x") {
		t.Fatalf("HostKeys = %v", got)
	}
	l.Dir = "/var/lib/maburvm/migration"
	uri, err := DestURI(l, "2001:db8::5")
	if err != nil || uri != "qemu+ssh://root@[2001:db8::5]/system?command=/var/lib/maburvm/migration/ssh&no_tty=1" {
		t.Fatalf("DestURI = %q, %v", uri, err)
	}
	if _, err := DestURI(l, "evil.example;rm"); err == nil {
		t.Fatal("non-IP destination accepted")
	}
	if uri, _ := DestURI(DefaultLayout("/var/lib/maburvm"), "192.0.2.1"); uri != "qemu+ssh://root@192.0.2.1/system?command=/usr/bin/maburvm-migration-ssh&no_tty=1" {
		t.Fatalf("default DestURI = %q", uri)
	}
	if DataURI("192.0.2.1") != "tcp://192.0.2.1" || DataURI("2001:db8::1") != "tcp://[2001:db8::1]" {
		t.Fatal("DataURI")
	}
}

func TestSSHArgsSendOnlyTheCommand(t *testing.T) {
	l := Layout{Dir: "/m"}
	got := strings.Join(SSHArgs(l, "192.0.2.4", ReadDiskRemote(vmID, "vda")), " ")
	if got != "-F /m/ssh_config -- root@192.0.2.4 maburvm-read-disk "+vmID+" vda" {
		t.Fatalf("ssh args = %q", got)
	}
	// What the remote end receives as SSH_ORIGINAL_COMMAND parses as a disk read.
	if req, err := ParseShellCommand(ReadDiskRemote(vmID, "vda")); err != nil || !req.ReadDisk {
		t.Fatalf("%+v %v", req, err)
	}
}

func TestParseShellCommand(t *testing.T) {
	for _, c := range []struct {
		in       string
		readDisk bool
		bad      bool
	}{
		{"", false, false},
		{"sh -c 'nc -U /var/run/libvirt/libvirt-sock'", false, false},
		{"virt-ssh-helper qemu:///system", false, false},
		{ReadDiskRemote(vmID, "vda"), true, false},
		{ReadDiskCommand + " " + vmID + " ../../etc/shadow", false, true},
		{ReadDiskCommand + " not-a-uuid vda", false, true},
		{ReadDiskCommand + " " + vmID + " vda; rm -rf /", false, true},
	} {
		got, err := ParseShellCommand(c.in)
		if (err != nil) != c.bad || got.ReadDisk != c.readDisk {
			t.Errorf("%q: got %+v, %v", c.in, got, err)
		}
	}
}

func TestProxyCommandPrefersHelper(t *testing.T) {
	have := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, x := range names {
				if x == n {
					return "/usr/bin/" + n, nil
				}
			}
			return "", errors.New("no")
		}
	}
	none := func(string) bool { return false }
	if cmd, name := ProxyCommand(have("virt-ssh-helper", "nc"), none, func(string) bool { return true }); name != "virt-ssh-helper" || cmd[1] != "qemu:///system" {
		t.Fatalf("got %v %s", cmd, name)
	}
	modular := func(s string) bool { return s == virtqemudSock }
	if cmd, name := ProxyCommand(have("nc"), modular, func(string) bool { return true }); name != "nc" || strings.Join(cmd, " ") != "/usr/bin/nc -q0 -U "+virtqemudSock {
		t.Fatalf("got %v %s", cmd, name)
	}
	if cmd, _ := ProxyCommand(have(), none, nil); cmd != nil {
		t.Fatal("a proxy with nothing installed")
	}
}

const sampleDomain = `<domain type='kvm'>
  <name>vm</name>
  <cpu mode='custom' match='exact'><model fallback='allow'>Haswell</model></cpu>
  <devices>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='/vm/a.qcow2'/><target dev='vda' bus='virtio'/></disk>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='/vm/a-vdb.qcow2'/><target dev='vdb' bus='virtio'/></disk>
    <disk type='file' device='cdrom'><driver name='qemu' type='raw'/><source file='/vm/a-seed.iso'/><target dev='sda' bus='sata'/><readonly/></disk>
    <disk type='block' device='disk'><source dev='/dev/vg/x'/><target dev='vdc' bus='virtio'/></disk>
    <interface type='bridge'><source bridge='br1'/></interface>
    <interface type='bridge'><source bridge='br0'/></interface>
    <graphics type='vnc' port='5901' autoport='no' listen='127.0.0.1'/>
  </devices>
</domain>`

func TestDrivesAndRewrite(t *testing.T) {
	drives, err := Drives(sampleDomain)
	if err != nil || len(drives) != 4 {
		t.Fatalf("Drives = %v, %v", drives, err)
	}
	if !drives[0].Copied() || drives[2].Copied() || !drives[2].Seed || drives[3].SourceType != "block" {
		t.Fatalf("drive classification wrong: %+v", drives)
	}
	if b, _ := Bridges(sampleDomain); strings.Join(b, ",") != "br0,br1" {
		t.Fatalf("Bridges = %v", b)
	}
	if mode, cpu, _ := CPUOf(sampleDomain); mode != "custom" || !strings.Contains(cpu, "Haswell") {
		t.Fatalf("CPUOf = %s %s", mode, cpu)
	}
	out, err := RewriteForTarget(sampleDomain, map[string]string{"vda": "/data/a.qcow2", "vdb": "/data/a-vdb.qcow2", "sda": "/data/a-seed.iso"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := Drives(out)
	if got[0].Path != "/data/a.qcow2" || got[1].Path != "/data/a-vdb.qcow2" || got[2].Path != "/data/a-seed.iso" || got[3].Path != "/dev/vg/x" {
		t.Fatalf("rewritten drives: %+v", got)
	}
	if strings.Contains(out, "5901") || !strings.Contains(out, `autoport="yes"`) {
		t.Fatalf("console kept a fixed port:\n%s", out)
	}
	if _, err := RewriteForTarget(sampleDomain, map[string]string{"vdz": "/x"}); err == nil {
		t.Fatal("unknown drive accepted")
	}
	if _, err := RewriteForTarget(sampleDomain, map[string]string{"vdc": "/x"}); err == nil {
		t.Fatal("a block device was rewritten as a file")
	}
}

func TestRewriteDropsChainAndNodeAddress(t *testing.T) {
	in := `<domain type='kvm'><name>vm</name><devices>
    <disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='/vm/a.qcow2'/>
      <backingStore type='file'><format type='qcow2'/><source file='/var/lib/templates/debian.qcow2'/></backingStore>
      <target dev='vda' bus='virtio'/></disk>
    <graphics type='vnc' port='5901' listen='192.0.2.10'><listen type='address' address='192.0.2.10'/></graphics>
  </devices></domain>`
	out, err := RewriteForTarget(in, map[string]string{"vda": "/data/a.qcow2"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "backingStore") || strings.Contains(out, "debian.qcow2") {
		t.Fatalf("the source's backing chain went to the destination:\n%s", out)
	}
	if strings.Contains(out, "192.0.2.10") || !strings.Contains(out, `listen="127.0.0.1"`) {
		t.Fatalf("the console kept this node's address:\n%s", out)
	}
	wild, err := RewriteForTarget(strings.ReplaceAll(in, "192.0.2.10", "0.0.0.0"), map[string]string{"vda": "/data/a.qcow2"})
	if err != nil || !strings.Contains(wild, `listen="0.0.0.0"`) {
		t.Fatalf("an address every node has was changed: %v\n%s", err, wild)
	}
}

func TestCopySparseKeepsContentAndLength(t *testing.T) {
	src := make([]byte, 5<<10)
	copy(src[1024:], "data")
	src[len(src)-3] = 7 // data near the end, then two zero bytes
	dst, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	var last int64
	n, err := CopySparse(dst, bytes.NewReader(src), 1024, func(v int64) { last = v })
	if err != nil || n != int64(len(src)) || last != n {
		t.Fatalf("copied %d (progress %d), %v", n, last, err)
	}
	dst.Close()
	got, _ := os.ReadFile(dst.Name())
	if !bytes.Equal(got, src) {
		t.Fatal("content differs")
	}
	// A source ending in zeros still has its full length.
	dst2, _ := os.Create(filepath.Join(t.TempDir(), "zeros"))
	if n, err := CopySparse(dst2, bytes.NewReader(make([]byte, 3000)), 1024, nil); err != nil || n != 3000 {
		t.Fatal(n, err)
	}
	fi, _ := dst2.Stat()
	if fi.Size() != 3000 {
		t.Fatalf("size %d", fi.Size())
	}
}

func TestPreparedRemovesOnlyItsOwnFiles(t *testing.T) {
	l := testLayout(t)
	dir := t.TempDir()
	mine := filepath.Join(dir, vmID+".qcow2")
	_ = os.WriteFile(mine, []byte("x"), 0o600)
	used := filepath.Join(dir, "used.qcow2")
	_ = os.WriteFile(used, []byte("x"), 0o600)
	link := filepath.Join(dir, "link.qcow2")
	_ = os.Symlink("/etc/passwd", link)
	if err := SavePrepared(l, Prepared{VMID: vmID, MigrationID: "m1", Files: []string{mine}}); err != nil {
		t.Fatal(err)
	}
	p, ok := LoadPrepared(l, vmID)
	if !ok {
		t.Fatal("record lost")
	}
	if got, err := Removable(p, "m1", nil); err != nil || len(got) != 1 || got[0] != mine {
		t.Fatalf("Removable = %v, %v", got, err)
	}
	if _, err := Removable(p, "m2", nil); err == nil {
		t.Fatal("another migration's files offered for removal")
	}
	if _, err := Removable(Prepared{VMID: vmID, MigrationID: "m1", Files: []string{used}}, "m1", map[string]bool{used: true}); err == nil {
		t.Fatal("a file in use offered for removal")
	}
	if _, err := Removable(Prepared{VMID: vmID, MigrationID: "m1", Files: []string{link}}, "m1", nil); err == nil {
		t.Fatal("a symlink offered for removal")
	}
	ClearPrepared(l, vmID)
	if _, ok := LoadPrepared(l, vmID); ok {
		t.Fatal("record kept")
	}
}

func TestCreateImageRefusesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.qcow2")
	_ = os.WriteFile(path, []byte("customer data"), 0o600)
	if err := CreateImage(context.Background(), path, "qcow2", 1<<30); err == nil {
		t.Fatal("an existing file was overwritten")
	}
	if b, _ := os.ReadFile(path); string(b) != "customer data" {
		t.Fatal("existing file changed")
	}
	if err := CreateImage(context.Background(), path+"2", "vmdk", 1<<30); err == nil {
		t.Fatal("unknown format accepted")
	}
}

func TestTargetPathStaysInPool(t *testing.T) {
	if p, err := TargetPath("/data/vms", vmID+".qcow2"); err != nil || p != "/data/vms/"+vmID+".qcow2" {
		t.Fatalf("got %q %v", p, err)
	}
	for _, c := range [][2]string{{"/data", "../etc/passwd"}, {"/data", "a/b"}, {"/data", ".hidden"}, {"/data", ""}, {"relative", "x"}, {"/data/../etc", "x"}, {"/", "x"}} {
		if _, err := TargetPath(c[0], c[1]); err == nil {
			t.Errorf("%q in %q accepted", c[1], c[0])
		}
	}
}

func TestRegistry(t *testing.T) {
	var r Registry
	cancelled := false
	if !r.Start(vmID, "m1", "copying", func() { cancelled = true }) {
		t.Fatal("start refused")
	}
	if r.Start(vmID, "m1", "copying", nil) || r.Start(vmID, "m2", "copying", nil) {
		t.Fatal("a second run started for the same VM")
	}
	r.Update(vmID, "m1", "", 100, 40)
	if p := r.Get(vmID, "m1"); p.State != StateRunning || p.Processed != 40 || p.Phase != "copying" {
		t.Fatalf("progress %+v", p)
	}
	if !r.Abort(vmID, "m1") || !cancelled {
		t.Fatal("abort did not reach the run")
	}
	r.Finish(vmID, "m1", errors.New("operation aborted"))
	if p := r.Get(vmID, "m1"); p.State != StateCancelled {
		t.Fatalf("state %s", p.State)
	}
	if p := r.Get(vmID, "other"); p.State != StateUnknown {
		t.Fatalf("unknown run reported %s", p.State)
	}
	if !r.Start(vmID, "m2", "copying", nil) {
		t.Fatal("a finished run blocked the next one")
	}
	r.Finish(vmID, "m2", nil)
	if p := r.Get(vmID, "m2"); p.State != StateCompleted {
		t.Fatalf("state %s", p.State)
	}
}
