// Package agentsign is the signature on an agent binary the panel pushes to a
// node. The private key stays offline (cmd/agent-sign); a node configured with
// the public key (AGENT_UPDATE_PUBKEY) installs only binaries signed with it.
package agentsign

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
)

// message is what is signed: a fixed label and the binary's SHA-256 in hex.
func message(sha256hex string) []byte {
	return []byte("maburvm-agent-update:v1:" + strings.ToLower(strings.TrimSpace(sha256hex)))
}

// Sign signs the binary whose SHA-256 is sha256hex.
func Sign(priv ed25519.PrivateKey, sha256hex string) []byte {
	return ed25519.Sign(priv, message(sha256hex))
}

// Verify checks sig over the binary whose SHA-256 is sha256hex.
func Verify(pub ed25519.PublicKey, sha256hex string, sig []byte) bool {
	return len(sig) == ed25519.SignatureSize && ed25519.Verify(pub, message(sha256hex), sig)
}

// ParsePublicKey reads a base64 ed25519 public key, as `agent-sign keygen` prints it.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("not a base64 ed25519 public key (32 bytes)")
	}
	return ed25519.PublicKey(b), nil
}

// DecodeSignature reads a signature file: base64, surrounding whitespace allowed.
func DecodeSignature(s string) ([]byte, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.SignatureSize {
		return nil, errors.New("not a base64 ed25519 signature (64 bytes)")
	}
	return b, nil
}
