// Command agent-sign makes the offline key agent updates are signed with, and
// signs an agent build. Keep the private key off the panel host: a node with
// AGENT_UPDATE_PUBKEY set then installs only builds you signed, so a
// compromised panel cannot push its own agent.
//
//	agent-sign keygen agent-signing.key          prints the public key for AGENT_UPDATE_PUBKEY
//	agent-sign sign agent-signing.key agent-amd64  writes agent-amd64.sig
//	agent-sign verify <public key> agent-amd64     checks agent-amd64.sig
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/maburvm/panel/internal/shared/agentsign"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "agent-sign:", err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	if len(args) < 2 {
		return errors.New("usage: agent-sign keygen <key file> | sign <key file> <binary> | verify <public key> <binary>")
	}
	switch args[0] {
	case "keygen":
		pub, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(args[1], os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(f, base64.StdEncoding.EncodeToString(priv.Seed())); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		fmt.Fprintf(out, "AGENT_UPDATE_PUBKEY=%s\n", base64.StdEncoding.EncodeToString(pub))
		return nil
	case "sign":
		if len(args) != 3 {
			return errors.New("usage: agent-sign sign <key file> <binary>")
		}
		raw, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(seed) != ed25519.SeedSize {
			return errors.New("the key file is not one agent-sign keygen wrote")
		}
		digest, err := fileSHA256(args[2])
		if err != nil {
			return err
		}
		sig := agentsign.Sign(ed25519.NewKeyFromSeed(seed), digest)
		if err := os.WriteFile(args[2]+".sig", []byte(base64.StdEncoding.EncodeToString(sig)+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "signed %s (sha256 %s) -> %s.sig\n", args[2], digest, args[2])
		return nil
	case "verify":
		if len(args) != 3 {
			return errors.New("usage: agent-sign verify <public key> <binary>")
		}
		pub, err := agentsign.ParsePublicKey(strings.TrimPrefix(args[1], "AGENT_UPDATE_PUBKEY="))
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(args[2] + ".sig")
		if err != nil {
			return err
		}
		sig, err := agentsign.DecodeSignature(string(raw))
		if err != nil {
			return err
		}
		digest, err := fileSHA256(args[2])
		if err != nil {
			return err
		}
		if !agentsign.Verify(pub, digest, sig) {
			return errors.New("signature does not match this key and binary")
		}
		fmt.Fprintf(out, "ok: %s is signed by that key\n", args[2])
		return nil
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
