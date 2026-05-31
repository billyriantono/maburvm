package service

import (
	"strings"
	"testing"
)

// VNC "VNC Auth" uses at most 8 bytes and the value is pushed through the QEMU
// monitor, so the generated password must be exactly 8 chars and contain no
// shell/JSON-special characters that could be mangled in transit (the bug that
// caused intermittent "Authentication failed").
func TestGenerateVNCPasswordIsVNCSafe(t *testing.T) {
	const allowed = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	for i := 0; i < 200; i++ {
		pw := generateVNCPassword()
		if len(pw) != 8 {
			t.Fatalf("VNC password must be 8 chars, got %d (%q)", len(pw), pw)
		}
		for _, c := range pw {
			if !strings.ContainsRune(allowed, c) {
				t.Fatalf("VNC password contains unsafe char %q in %q", c, pw)
			}
		}
	}
}
