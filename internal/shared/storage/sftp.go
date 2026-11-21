package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// SFTPStore keeps backups on an SSH server.
type SFTPStore struct {
	conn *ssh.Client
	c    *sftp.Client
	base string
}

const sftpDialTimeout = 20 * time.Second

// ParseHostKey reads a server key given as "ssh-ed25519 AAAA..." or as a line
// of ssh-keyscan / known_hosts output ("host ssh-ed25519 AAAA...").
func ParseHostKey(s string) (ssh.PublicKey, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("the server's host key is required")
	}
	if k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(s)); err == nil {
		return k, nil
	}
	if _, _, k, _, _, err := ssh.ParseKnownHosts([]byte(s)); err == nil {
		return k, nil
	}
	return nil, errors.New("the host key is not a public key; paste a line such as \"ssh-ed25519 AAAA...\"")
}

// HostKeyLine is a key in the form ParseHostKey reads and an operator pastes.
func HostKeyLine(k ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

// Validate checks everything needed to connect is there and readable.
func (t SFTPTarget) Validate() error {
	if strings.TrimSpace(t.Host) == "" || strings.ContainsAny(t.Host, " /@") {
		return errors.New("the host must be a host name or address")
	}
	if t.Port < 0 || t.Port > 65535 {
		return errors.New("the port must be between 1 and 65535")
	}
	if strings.TrimSpace(t.User) == "" {
		return errors.New("the user is required")
	}
	if t.Password == "" && t.PrivateKey == "" {
		return errors.New("a password or a private key is required")
	}
	if t.PrivateKey != "" {
		if _, err := ssh.ParsePrivateKey([]byte(t.PrivateKey)); err != nil {
			return fmt.Errorf("the private key cannot be read (it must be PEM, without a passphrase): %v", err)
		}
	}
	if _, err := ParseHostKey(t.HostKey); err != nil {
		return err
	}
	if strings.Contains(t.Path, "..") {
		return errors.New("the path must not contain '..'")
	}
	return nil
}

func (t SFTPTarget) addr() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(strings.TrimSpace(t.Host), strconv.Itoa(port))
}

// DialSFTP connects and logs in. A server presenting any key but the pinned
// one is refused before the password or key is offered.
func DialSFTP(ctx context.Context, t SFTPTarget) (*SFTPStore, error) {
	if err := t.Validate(); err != nil {
		return nil, refused("%v", err)
	}
	hostKey, _ := ParseHostKey(t.HostKey)
	var auth []ssh.AuthMethod
	if t.PrivateKey != "" {
		signer, _ := ssh.ParsePrivateKey([]byte(t.PrivateKey))
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if t.Password != "" {
		pw := t.Password
		auth = append(auth, ssh.Password(pw), ssh.KeyboardInteractive(func(_, _ string, qs []string, _ []bool) ([]string, error) {
			answers := make([]string, len(qs))
			for i := range answers {
				answers[i] = pw
			}
			return answers, nil
		}))
	}
	cfg := &ssh.ClientConfig{
		User:            strings.TrimSpace(t.User),
		Auth:            auth,
		HostKeyCallback: ssh.FixedHostKey(hostKey),
		Timeout:         sftpDialTimeout,
	}
	addr := t.addr()
	d := net.Dialer{Timeout: sftpDialTimeout}
	raw, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("could not connect to %s: %w", addr, err)
	}
	_ = raw.SetDeadline(time.Now().Add(sftpDialTimeout))
	cc, chans, reqs, err := ssh.NewClientConn(raw, addr, cfg)
	if err != nil {
		raw.Close()
		if strings.Contains(err.Error(), "unable to authenticate") || strings.Contains(err.Error(), "host key mismatch") {
			return nil, refused("%s refused the login: %v", addr, err)
		}
		return nil, fmt.Errorf("could not log in to %s: %w", addr, err)
	}
	_ = raw.SetDeadline(time.Time{})
	conn := ssh.NewClient(cc, chans, reqs)
	c, err := sftp.NewClient(conn, sftp.UseConcurrentWrites(true), sftp.MaxConcurrentRequestsPerFile(64))
	if err != nil {
		conn.Close()
		return nil, refused("%s does not offer SFTP: %v", addr, err)
	}
	return &SFTPStore{conn: conn, c: c, base: strings.TrimRight(strings.TrimSpace(t.Path), "/")}, nil
}

// ScanHostKey connects only far enough to read the key the server presents.
func ScanHostKey(ctx context.Context, host string, port int) (ssh.PublicKey, error) {
	t := SFTPTarget{Host: host, Port: port}
	var got ssh.PublicKey
	errSeen := errors.New("host key read")
	cfg := &ssh.ClientConfig{
		User: "maburvm-keyscan",
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			got = k
			return errSeen
		},
		Timeout: sftpDialTimeout,
	}
	d := net.Dialer{Timeout: sftpDialTimeout}
	raw, err := d.DialContext(ctx, "tcp", t.addr())
	if err != nil {
		return nil, fmt.Errorf("could not connect to %s: %w", t.addr(), err)
	}
	defer raw.Close()
	_ = raw.SetDeadline(time.Now().Add(sftpDialTimeout))
	_, _, _, err = ssh.NewClientConn(raw, t.addr(), cfg)
	if got == nil {
		return nil, fmt.Errorf("%s did not present a host key: %v", t.addr(), err)
	}
	return got, nil
}

func (s *SFTPStore) remote(key string) (string, error) {
	k, err := cleanKey(key)
	if err != nil {
		return "", err
	}
	if s.base == "" {
		return k, nil
	}
	return s.base + "/" + k, nil
}

func sftpErr(op, p string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return refused("%s %s: permission denied on the server", op, p)
	}
	return fmt.Errorf("%s %s: %w", op, p, err)
}

// Upload writes beside the final name and renames it there, so a transfer cut
// short never looks like a finished backup.
func (s *SFTPStore) Upload(ctx context.Context, key string, body io.Reader, size int64, _ string) error {
	p, err := s.remote(key)
	if err != nil {
		return err
	}
	if dir := path.Dir(p); dir != "." && dir != "/" {
		if err := s.c.MkdirAll(dir); err != nil {
			return sftpErr("create directory", dir, err)
		}
	}
	tmp := p + ".part"
	f, err := s.c.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		return sftpErr("create", tmp, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = s.conn.Close() })
	n, werr := f.ReadFrom(body)
	cerr := f.Close()
	stop()
	if werr == nil {
		werr = cerr
	}
	if werr == nil && size > 0 && n != size {
		werr = fmt.Errorf("wrote %d bytes of %d", n, size)
	}
	if werr != nil {
		_ = s.c.Remove(tmp)
		return sftpErr("upload", p, werr)
	}
	if err := s.c.PosixRename(tmp, p); err != nil {
		// Servers without the OpenSSH extension refuse a rename over a file.
		_ = s.c.Remove(p)
		if err := s.c.Rename(tmp, p); err != nil {
			_ = s.c.Remove(tmp)
			return sftpErr("rename", tmp, err)
		}
	}
	return nil
}

// Download opens the file; io.Copy from it reads with parallel requests. ctx
// ending before it is closed cuts the connection, as SFTP reads cannot be
// cancelled one by one.
func (s *SFTPStore) Download(ctx context.Context, key string) (io.ReadCloser, error) {
	p, err := s.remote(key)
	if err != nil {
		return nil, err
	}
	f, err := s.c.Open(p)
	if err != nil {
		return nil, sftpErr("open", p, err)
	}
	return &sftpReader{File: f, stop: context.AfterFunc(ctx, func() { _ = s.conn.Close() })}, nil
}

type sftpReader struct {
	*sftp.File
	stop func() bool
}

func (r *sftpReader) Close() error {
	r.stop()
	return r.File.Close()
}

func (s *SFTPStore) GetSize(_ context.Context, key string) (int64, error) {
	p, err := s.remote(key)
	if err != nil {
		return 0, err
	}
	fi, err := s.c.Stat(p)
	if err != nil {
		return 0, sftpErr("stat", p, err)
	}
	return fi.Size(), nil
}

func (s *SFTPStore) Exists(_ context.Context, key string) (bool, error) {
	p, err := s.remote(key)
	if err != nil {
		return false, err
	}
	if _, err := s.c.Stat(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, sftpErr("stat", p, err)
	}
	return true, nil
}

func (s *SFTPStore) Delete(_ context.Context, key string) error {
	p, err := s.remote(key)
	if err != nil {
		return err
	}
	if err := s.c.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return sftpErr("delete", p, err)
	}
	return nil
}

func (s *SFTPStore) Close() error {
	_ = s.c.Close()
	return s.conn.Close()
}
