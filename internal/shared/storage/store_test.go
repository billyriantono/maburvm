package storage

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestCleanKeyStaysInside(t *testing.T) {
	for _, ok := range []string{"backups/vm/1.qcow2", "/backups/x", "a"} {
		if _, err := cleanKey(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "../x", "a/../../x", "a/./b", "a//b", "."} {
		if k, err := cleanKey(bad); err == nil {
			t.Errorf("%q accepted as %q", bad, k)
		}
	}
}

// withRoot makes the test's temp dir count as the root file system, or not.
func withRoot(t *testing.T, dir string, isRoot bool) {
	t.Helper()
	old := rootDevice
	t.Cleanup(func() { rootDevice = old })
	dev, err := deviceOf(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !isRoot {
		dev++
	}
	rootDevice = func() (uint64, error) { return dev, nil }
}

func TestDirStoreRefusesTheRootFileSystem(t *testing.T) {
	dir := t.TempDir()
	withRoot(t, dir, true)
	_, err := OpenDir(dir)
	if err == nil || !strings.Contains(err.Error(), "root file system") || !Rejected(err) {
		t.Fatalf("want a refusal naming the root file system, got %v", err)
	}
	for _, bad := range []string{"relative/dir", "/", filepath.Join(dir, "missing")} {
		if _, err := OpenDir(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDirStoreRoundTripAndUnmount(t *testing.T) {
	dir := t.TempDir()
	withRoot(t, dir, false)
	s, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := ProbeStore(ctx, s, "backups/.test"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	body := "disk image"
	if err := s.Upload(ctx, "backups/vm/a.qcow2", strings.NewReader(body), int64(len(body)), ""); err != nil {
		t.Fatal(err)
	}
	if n, err := s.GetSize(ctx, "backups/vm/a.qcow2"); err != nil || n != int64(len(body)) {
		t.Fatalf("size %d, %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "backups/vm/a.qcow2.part")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file left behind: %v", err)
	}
	if err := s.Upload(ctx, "../escape", strings.NewReader("x"), 1, ""); err == nil {
		t.Fatal("a key above the directory was written")
	}
	// The mount going away must stop every call, not send writes to "/".
	withRoot(t, dir, true)
	if _, err := s.Exists(ctx, "backups/vm/a.qcow2"); err == nil {
		t.Fatal("an unmounted store answered")
	}
	withRoot(t, dir, false)
	if err := s.Delete(ctx, "backups/vm/a.qcow2"); err != nil {
		t.Fatal(err)
	}
	if found, err := s.Exists(ctx, "backups/vm/a.qcow2"); err != nil || found {
		t.Fatalf("still there: %v %v", found, err)
	}
	if err := s.Delete(ctx, "backups/vm/a.qcow2"); err != nil {
		t.Fatalf("deleting a missing file: %v", err)
	}
}

func TestDirStoreDoesNotFollowLinksOut(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	withRoot(t, dir, false)
	victim := filepath.Join(outside, "vm", "a.qcow2")
	if err := os.MkdirAll(filepath.Dir(victim), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(victim, []byte("a customer's disk"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Whoever controls the mounted file system plants a link to the node's disks.
	if err := os.Symlink(outside, filepath.Join(dir, "backups")); err != nil {
		t.Fatal(err)
	}
	s, err := OpenDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Delete(ctx, "backups/vm/a.qcow2"); err == nil || !Rejected(err) {
		t.Fatalf("delete through a link out of the store: %v", err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("the file outside the store was removed: %v", err)
	}
	if err := s.Upload(ctx, "backups/new/b.qcow2", strings.NewReader("x"), 1, ""); err == nil {
		t.Fatal("an upload went through a link out of the store")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a directory was made outside the store: %v", err)
	}
	// A link that stays inside is fine.
	if err := os.MkdirAll(filepath.Join(dir, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "inner")); err != nil {
		t.Fatal(err)
	}
	if err := s.Upload(ctx, "inner/backups/c.qcow2", strings.NewReader("x"), 1, ""); err != nil {
		t.Fatalf("a link inside the store was refused: %v", err)
	}
}

func TestParseHostKeyForms(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k, _ := ssh.NewPublicKey(pub)
	line := HostKeyLine(k)
	for _, in := range []string{line, "backup.example.com " + line, "[backup.example.com]:2222 " + line + "\n"} {
		got, err := ParseHostKey(in)
		if err != nil || string(got.Marshal()) != string(k.Marshal()) {
			t.Errorf("%q: %v", in, err)
		}
	}
	if _, err := ParseHostKey("not a key"); err == nil {
		t.Error("garbage accepted")
	}
}

// sftpServer runs an SSH server with an SFTP subsystem rooted at dir.
func sftpServer(t *testing.T, dir, password string) (addr string, hostKey ssh.PublicKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
		if string(p) == password {
			return nil, nil
		}
		return nil, errors.New("wrong password")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					c.Close()
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, err := nc.Accept()
					if err != nil {
						continue
					}
					go func() {
						for r := range creqs {
							ok := r.Type == "subsystem" && string(r.Payload[4:]) == "sftp"
							_ = r.Reply(ok, nil)
							if ok {
								srv, _ := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(dir))
								_ = srv.Serve()
								ch.Close()
							}
						}
					}()
				}
			}(c)
		}
	}()
	return ln.Addr().String(), signer.PublicKey()
}

func TestSFTPStoreRoundTripAndPinnedKey(t *testing.T) {
	dir := t.TempDir()
	addr, hostKey := sftpServer(t, dir, "s3cret")
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	target := SFTPTarget{Host: host, Port: port, User: "backup", Password: "s3cret", HostKey: HostKeyLine(hostKey), Path: "store"}
	ctx := context.Background()

	scanned, err := ScanHostKey(ctx, host, port)
	if err != nil || HostKeyLine(scanned) != HostKeyLine(hostKey) {
		t.Fatalf("scan: %v", err)
	}

	s, err := DialSFTP(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := ProbeStore(ctx, s, "backups/.test"); err != nil {
		t.Fatalf("probe: %v", err)
	}
	body := strings.Repeat("block ", 50000)
	if err := s.Upload(ctx, "backups/vm/a.qcow2", strings.NewReader(body), int64(len(body)), ""); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "store/backups/vm/a.qcow2")); err != nil || string(b) != body {
		t.Fatalf("stored file wrong: %v", err)
	}
	rc, err := s.Download(ctx, "backups/vm/a.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != body {
		t.Fatal("downloaded file differs")
	}
	if found, _ := s.Exists(ctx, "backups/vm/missing"); found {
		t.Fatal("a missing file exists")
	}
	if err := s.Delete(ctx, "backups/vm/a.qcow2"); err != nil {
		t.Fatal(err)
	}

	other, _, _ := ed25519.GenerateKey(rand.Reader)
	wrongKey, _ := ssh.NewPublicKey(other)
	bad := target
	bad.HostKey = HostKeyLine(wrongKey)
	if _, err := DialSFTP(ctx, bad); err == nil || !Rejected(err) {
		t.Fatalf("a server with another key was accepted: %v", err)
	}
	bad = target
	bad.Password = "wrong"
	if _, err := DialSFTP(ctx, bad); err == nil || !Rejected(err) {
		t.Fatalf("a wrong password was not a refusal: %v", err)
	}
}
