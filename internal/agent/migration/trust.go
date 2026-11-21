// Package migration holds the parts of moving a VM between nodes that need no
// libvirt: the node-to-node link (keys, pinned host keys, the restricted login),
// rewriting a domain for its new paths, and copying a disk file.
package migration

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Layout is where a node keeps its side of the migration link.
type Layout struct {
	Dir            string // the agent's migration directory
	AuthorizedKeys string // root's authorized_keys, where peers' keys are accepted
	HostKeyGlob    string // this node's SSH host public keys
	// Wrapper is the ssh command libvirt runs for a migration connection;
	// empty puts it in Dir.
	Wrapper string
}

// DefaultLayout is the layout under the agent's data directory. The wrapper
// sits in /usr/bin because libvirtd's AppArmor profile runs commands from
// there and refuses them from the agent's directory.
func DefaultLayout(dataDir string) Layout {
	return Layout{
		Dir:            filepath.Join(dataDir, "migration"),
		AuthorizedKeys: "/root/.ssh/authorized_keys",
		HostKeyGlob:    "/etc/ssh/ssh_host_*_key.pub",
		Wrapper:        "/usr/bin/maburvm-migration-ssh",
	}
}

func (l Layout) KeyPath() string    { return filepath.Join(l.Dir, "id_ed25519") }
func (l Layout) ConfigPath() string { return filepath.Join(l.Dir, "ssh_config") }

// SSHWrapper is the ssh command libvirt runs: ssh with only the migration settings.
func (l Layout) SSHWrapper() string {
	if l.Wrapper != "" {
		return l.Wrapper
	}
	return filepath.Join(l.Dir, "ssh")
}

func (l Layout) KnownHostsDir() string { return filepath.Join(l.Dir, "known_hosts.d") }
func (l Layout) PreparedDir() string   { return filepath.Join(l.Dir, "prepared") }

var (
	uuidRe     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	safePathRe = regexp.MustCompile(`^/[A-Za-z0-9/._-]+$`)
	keyMu      sync.Mutex
)

// ValidUUID reports whether s is a VM or node id.
func ValidUUID(s string) bool { return uuidRe.MatchString(s) }

// EnsureKey returns this node's migration public key in authorized_keys form,
// creating the key pair the first time.
func EnsureKey(l Layout, comment string) (string, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	if b, err := os.ReadFile(l.KeyPath()); err == nil {
		signer, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return "", fmt.Errorf("the migration key %s cannot be read: %w", l.KeyPath(), err)
		}
		return keyText(signer.PublicKey()) + " " + comment, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(l.Dir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create %s: %w", l.Dir, err)
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return "", err
	}
	line := keyText(sshPub) + " " + comment
	if err := writeFileAtomic(l.KeyPath()+".pub", []byte(line+"\n"), 0o644); err != nil {
		return "", err
	}
	if err := writeFileAtomic(l.KeyPath(), pem.EncodeToMemory(block), 0o600); err != nil {
		return "", err
	}
	return line, nil
}

// keyText is a public key as "type base64", without options or comment.
func keyText(k ssh.PublicKey) string { return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))) }

// NormalizeKey parses one public key and returns it as "type base64". Options
// and comments are dropped, so nothing but the key itself reaches a file.
func NormalizeKey(s string) (string, error) {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s))
	if err != nil {
		return "", fmt.Errorf("not a public key: %w", err)
	}
	return keyText(k), nil
}

// Fingerprint is a key's SHA256 fingerprint, "" when it is not a key.
func Fingerprint(key string) string {
	k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		return ""
	}
	return ssh.FingerprintSHA256(k)
}

// HostKeys reads this node's SSH host public keys, sorted.
func HostKeys(glob string) []string {
	files, _ := filepath.Glob(glob)
	var out []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		if k, err := NormalizeKey(string(b)); err == nil {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// Peer is another node this one trusts for migration.
type Peer struct {
	NodeID    string
	Address   string
	PublicKey string   // accepted here, from Address only
	HostKeys  []string // pinned for connections to Address
}

func (p Peer) check() error {
	if !ValidUUID(p.NodeID) {
		return fmt.Errorf("peer id %q is not a node id", p.NodeID)
	}
	if net.ParseIP(p.Address) == nil {
		return fmt.Errorf("peer %s: %q is not an IP address", p.NodeID, p.Address)
	}
	return nil
}

const (
	beginMarker = "# BEGIN maburvm migration peers (written by maburvm-agent; edits are replaced)"
	endMarker   = "# END maburvm migration peers"
	peerTag     = "maburvm-migration:"
)

// RenderAuthorizedKeys returns existing with the managed block replaced by one
// line per peer: its key, accepted only from its address, with no shell, pty
// or forwarding, and only ever running command. Lines not ours are kept as
// they are.
func RenderAuthorizedKeys(existing string, peers []Peer, command string) (string, error) {
	if command == "" || strings.ContainsAny(command, "\"\n\\") {
		return "", fmt.Errorf("unusable forced command %q", command)
	}
	var kept []string
	for _, line := range strings.Split(existing, "\n") {
		f := strings.Fields(line)
		if line == beginMarker || line == endMarker || (len(f) > 0 && strings.HasPrefix(f[len(f)-1], peerTag)) {
			continue
		}
		kept = append(kept, line)
	}
	for len(kept) > 0 && kept[len(kept)-1] == "" {
		kept = kept[:len(kept)-1]
	}
	var block []string
	for _, p := range peers {
		if p.PublicKey == "" {
			continue
		}
		if err := p.check(); err != nil {
			return "", err
		}
		key, err := NormalizeKey(p.PublicKey)
		if err != nil {
			return "", fmt.Errorf("peer %s: %w", p.NodeID, err)
		}
		block = append(block, fmt.Sprintf(`from="%s",restrict,command="%s" %s %s%s`, p.Address, command, key, peerTag, p.NodeID))
	}
	if len(block) > 0 {
		kept = append(kept, beginMarker)
		kept = append(kept, block...)
		kept = append(kept, endMarker)
	}
	if len(kept) == 0 {
		return "", nil
	}
	return strings.Join(kept, "\n") + "\n", nil
}

// RenderSSHConfig is the client configuration this node's migration
// connections use, and nothing else: its own key, and each peer's host keys
// checked strictly. An address with no pinned keys is refused.
func RenderSSHConfig(l Layout, peers []Peer) string {
	var b strings.Builder
	b.WriteString("# Written by maburvm-agent for VM migration between nodes; edits are replaced.\n")
	for _, p := range peers {
		if len(p.HostKeys) == 0 {
			continue
		}
		fmt.Fprintf(&b, "Host %s\n    UserKnownHostsFile %s\n", p.Address, filepath.Join(l.KnownHostsDir(), p.NodeID))
	}
	fmt.Fprintf(&b, `Host *
    User root
    IdentityFile %s
    IdentitiesOnly yes
    BatchMode yes
    StrictHostKeyChecking yes
    UserKnownHostsFile /dev/null
    GlobalKnownHostsFile /dev/null
    CheckHostIP no
    HashKnownHosts no
    UpdateHostKeys no
    PasswordAuthentication no
    ForwardAgent no
    ForwardX11 no
    ClearAllForwardings yes
    ConnectTimeout 15
    ServerAliveInterval 15
    ServerAliveCountMax 4
`, l.KeyPath())
	return b.String()
}

// RenderKnownHosts pins a peer's host keys to its address.
func RenderKnownHosts(p Peer) (string, error) {
	var b strings.Builder
	for _, k := range p.HostKeys {
		key, err := NormalizeKey(k)
		if err != nil {
			return "", fmt.Errorf("peer %s host key: %w", p.NodeID, err)
		}
		fmt.Fprintf(&b, "%s %s\n", p.Address, key)
	}
	return b.String(), nil
}

// ApplyPeers makes the files on this node say exactly what peers says: their
// keys accepted (restricted to command), their host keys pinned, and nothing
// left of a node that is not listed.
func ApplyPeers(l Layout, peers []Peer, command string) error {
	// Two pushes at once would each rewrite authorized_keys from what they read.
	keyMu.Lock()
	defer keyMu.Unlock()
	for _, p := range peers {
		if err := p.check(); err != nil {
			return err
		}
	}
	if !safePathRe.MatchString(l.Dir) {
		return fmt.Errorf("unusable migration directory %q", l.Dir)
	}
	if err := os.MkdirAll(l.KnownHostsDir(), 0o700); err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, p := range peers {
		if len(p.HostKeys) == 0 {
			continue
		}
		text, err := RenderKnownHosts(p)
		if err != nil {
			return err
		}
		if err := writeFileAtomic(filepath.Join(l.KnownHostsDir(), p.NodeID), []byte(text), 0o600); err != nil {
			return err
		}
		keep[p.NodeID] = true
	}
	if entries, err := os.ReadDir(l.KnownHostsDir()); err == nil {
		for _, e := range entries {
			if !keep[e.Name()] {
				_ = os.Remove(filepath.Join(l.KnownHostsDir(), e.Name()))
			}
		}
	}
	if err := writeFileAtomic(l.ConfigPath(), []byte(RenderSSHConfig(l, peers)), 0o600); err != nil {
		return err
	}
	sshBin := "ssh"
	if p, err := exec.LookPath("ssh"); err == nil {
		sshBin = p
	}
	wrapper := fmt.Sprintf("#!/bin/sh\n# libvirt runs this for migration connections: ssh with only the migration settings.\nexec %s -F %s \"$@\"\n", sshBin, l.ConfigPath())
	if err := writeFileAtomic(l.SSHWrapper(), []byte(wrapper), 0o755); err != nil {
		return err
	}

	existing, err := os.ReadFile(l.AuthorizedKeys)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	rendered, err := RenderAuthorizedKeys(string(existing), peers, command)
	if err != nil {
		return err
	}
	if rendered == string(existing) {
		return nil
	}
	target := l.AuthorizedKeys
	if real, err := filepath.EvalSymlinks(target); err == nil {
		target = real // a linked authorized_keys stays linked
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	return writeFileAtomic(target, []byte(rendered), 0o600)
}

// hostPart is an address as it goes into a URI.
func hostPart(address string) string {
	if strings.Contains(address, ":") {
		return "[" + address + "]"
	}
	return address
}

// DestURI is the libvirt URI for a peer, over the migration link.
func DestURI(l Layout, address string) (string, error) {
	if net.ParseIP(address) == nil {
		return "", fmt.Errorf("%q is not an IP address", address)
	}
	if !safePathRe.MatchString(l.SSHWrapper()) {
		return "", fmt.Errorf("unusable migration directory %q", l.Dir)
	}
	return fmt.Sprintf("qemu+ssh://root@%s/system?command=%s&no_tty=1", hostPart(address), l.SSHWrapper()), nil
}

// DataURI is where the memory and disk streams go: the peer's address, never
// whatever its hostname resolves to (often a loopback address).
func DataURI(address string) string { return "tcp://" + hostPart(address) }

// SSHArgs runs remote on a peer over the migration link. The "--" ends ssh's
// options before the destination; after it would become part of the command.
func SSHArgs(l Layout, address string, remote ...string) []string {
	return append([]string{"-F", l.ConfigPath(), "--", "root@" + address}, remote...)
}

// writeFileAtomic replaces path with data, never leaving it half written.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
