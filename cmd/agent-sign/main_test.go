package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeygenSignVerify(t *testing.T) {
	dir := t.TempDir()
	key, bin := filepath.Join(dir, "k"), filepath.Join(dir, "agent-amd64")
	if err := os.WriteFile(bin, []byte("agent build"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run([]string{"keygen", key}, &out); err != nil {
		t.Fatal(err)
	}
	pub := strings.TrimSpace(out.String())
	if err := run([]string{"keygen", key}, &out); err == nil {
		t.Fatal("keygen must never overwrite a key")
	}
	if err := run([]string{"sign", key, bin}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", pub, bin}, &out); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if err := os.WriteFile(bin, []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", pub, bin}, &out); err == nil {
		t.Fatal("a changed binary must not verify")
	}
}
