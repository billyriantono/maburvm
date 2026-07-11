package client

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"

	"google.golang.org/grpc/credentials"
)

// hostOnly returns the host part of a "host:port" address (or the input if it
// has no port). Used for TLS SNI when dialing an agent.
func hostOnly(address string) string {
	if host, _, err := net.SplitHostPort(address); err == nil {
		return host
	}
	return address
}

// defaultPinStore is the process-wide pin store used by NodeTLSCredentials. The
// panel wires it to a NodeRepository-backed store at startup via
// SetDefaultPinStore. When nil (e.g. tests), connections still encrypt but are
// not pinned.
var defaultPinStore PinStore

// SetDefaultPinStore installs the process-wide pin store.
func SetDefaultPinStore(s PinStore) { defaultPinStore = s }

// NodeTLSCredentials returns gRPC transport credentials that encrypt the
// connection to a node's agent and pin its self-signed certificate (trust on
// first use, then verify). Use this everywhere the panel dials an agent instead
// of a bare InsecureSkipVerify config. host is used for SNI only.
func NodeTLSCredentials(nodeID, host string) credentials.TransportCredentials {
	return credentials.NewTLS(pinnedTLSConfig(nodeID, host, defaultPinStore))
}

// PinStore reads and records the pinned TLS certificate fingerprint for a node.
// It backs certificate pinning so the panel can verify it is talking to the same
// agent it saw before, defeating a man-in-the-middle on the panel↔node network
// even though agents use self-signed certificates (no CA/PKI required).
type PinStore interface {
	// GetFingerprint returns the stored SHA-256 fingerprint for a node, or "" if
	// none has been recorded yet.
	GetFingerprint(nodeID string) string
	// SetFingerprint records a newly observed fingerprint for a node (trust on
	// first use). Implementations should persist this.
	SetFingerprint(nodeID, fingerprint string)
}

// CertFingerprint returns the SHA-256 fingerprint (lowercase hex) of a DER cert.
func CertFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// pinnedTLSConfig builds a tls.Config that encrypts the connection and pins the
// agent's leaf certificate:
//
//   - Go's default CA-chain verification is disabled (InsecureSkipVerify) because
//     agents self-sign; identity is instead established by the pin.
//   - If a fingerprint is already stored for the node, the presented leaf cert
//     MUST match it or the handshake fails (MITM rejected).
//   - If none is stored yet, the observed fingerprint is recorded via the store
//     (trust on first use) and the connection is accepted. This keeps existing
//     nodes working — they simply get pinned on the next connection.
//
// serverName is used only for SNI (agents self-sign, so it isn't verified against
// the cert). nodeID keys the pin store.
func pinnedTLSConfig(nodeID, serverName string, store PinStore) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         serverName,
		InsecureSkipVerify: true, // identity verified by the pin below, not the CA chain
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("agent presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			fp := CertFingerprint(leaf.Raw)

			if store == nil {
				// No store wired: still encrypt, but we can't pin. Accept (this is
				// no worse than the previous InsecureSkipVerify behaviour).
				return nil
			}
			pinned := store.GetFingerprint(nodeID)
			if pinned == "" {
				// Trust on first use: record and accept.
				store.SetFingerprint(nodeID, fp)
				return nil
			}
			if fp != pinned {
				return fmt.Errorf("agent certificate fingerprint mismatch for node %s: expected %s, got %s (possible MITM or the node was re-provisioned — clear its pinned fingerprint to re-trust)", nodeID, pinned, fp)
			}
			return nil
		},
	}
}
