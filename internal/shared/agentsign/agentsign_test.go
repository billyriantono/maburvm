package agentsign

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	const digest = "9F86D081884C7D659A2FEAA0C55AD015A3BF4F1B2B0B822CD15D6C15B0F00A08"
	sig := Sign(priv, digest)

	parsed, err := ParsePublicKey(base64.StdEncoding.EncodeToString(pub) + "\n")
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeSignature(base64.StdEncoding.EncodeToString(sig) + "\n")
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(parsed, "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", decoded) {
		t.Fatal("a signature must verify whatever the digest's case")
	}
	if Verify(parsed, "0000000000000000000000000000000000000000000000000000000000000000", decoded) {
		t.Fatal("a signature for another binary must not verify")
	}
	other, _, _ := ed25519.GenerateKey(rand.Reader)
	if Verify(other, digest, decoded) || Verify(parsed, digest, nil) {
		t.Fatal("wrong key or no signature must not verify")
	}
	if _, err := ParsePublicKey("short"); err == nil {
		t.Fatal("a malformed key must be refused")
	}
}
