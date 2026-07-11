package client

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"sync"
	"testing"
	"time"
)

// selfSignedCert generates a fresh self-signed TLS cert (like an agent's).
func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "maburvm-agent"},
		NotBefore:    time.Unix(1_000_000_000, 0),
		NotAfter:     time.Unix(4_000_000_000, 0),
		DNSNames:     []string{"agent"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// handshake starts a one-shot TLS server with serverCert and dials it with the
// pinning client config, returning any handshake error seen by the client.
func handshake(t *testing.T, serverCert tls.Certificate, clientCfg *tls.Config) error {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{serverCert}})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		// Force the handshake, then close.
		_ = conn.(*tls.Conn).Handshake()
		conn.Close()
	}()

	conn, err := tls.Dial("tcp", ln.Addr().String(), clientCfg)
	if err == nil {
		err = conn.Handshake()
		conn.Close()
	}
	wg.Wait()
	return err
}

type mapPinStore struct {
	mu sync.Mutex
	m  map[string]string
}

func newMapPinStore() *mapPinStore { return &mapPinStore{m: map[string]string{}} }
func (s *mapPinStore) GetFingerprint(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}
func (s *mapPinStore) SetFingerprint(id, fp string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = fp
}

func TestPinning_TOFURecordsThenVerifies(t *testing.T) {
	cert := selfSignedCert(t)
	store := newMapPinStore()

	// First connection: no pin yet → trust on first use, records fingerprint.
	if err := handshake(t, cert, pinnedTLSConfig("node-1", "agent", store)); err != nil {
		t.Fatalf("first (TOFU) handshake should succeed: %v", err)
	}
	fp := store.GetFingerprint("node-1")
	if fp == "" {
		t.Fatal("expected fingerprint to be recorded on first use")
	}
	if fp != CertFingerprint(cert.Certificate[0]) {
		t.Fatal("recorded fingerprint doesn't match the server cert")
	}

	// Second connection: same cert, pin now set → must still succeed.
	if err := handshake(t, cert, pinnedTLSConfig("node-1", "agent", store)); err != nil {
		t.Fatalf("second handshake with matching pin should succeed: %v", err)
	}
}

func TestPinning_RejectsMITM(t *testing.T) {
	realCert := selfSignedCert(t)
	store := newMapPinStore()
	// Pin the real cert via a first connection.
	if err := handshake(t, realCert, pinnedTLSConfig("node-1", "agent", store)); err != nil {
		t.Fatalf("pinning handshake failed: %v", err)
	}

	// Now an attacker presents a DIFFERENT self-signed cert for the same node.
	attackerCert := selfSignedCert(t)
	err := handshake(t, attackerCert, pinnedTLSConfig("node-1", "agent", store))
	if err == nil {
		t.Fatal("handshake with a mismatched (MITM) certificate must be rejected")
	}
}

func TestPinning_NilStoreStillConnects(t *testing.T) {
	cert := selfSignedCert(t)
	// With no store, we still encrypt and accept (no worse than the old behaviour).
	if err := handshake(t, cert, pinnedTLSConfig("node-1", "agent", nil)); err != nil {
		t.Fatalf("handshake with nil store should succeed: %v", err)
	}
}
