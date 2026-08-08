package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The rate limit is only as good as the flags it is built from: a typo in a
// hashlimit option makes iptables reject the rule at startup, and the agent
// logs a warning and carries on unprotected. Pin the shape of the rule.
func TestSynFloodRule(t *testing.T) {
	rule := strings.Join(synFloodRule(), " ")

	for _, want := range []string{
		"-p tcp --syn",
		"--hashlimit-above " + synRateAbove,
		"--hashlimit-mode srcip",
		"--hashlimit-htable-expire 60000",
		"-j DROP",
	} {
		if !strings.Contains(rule, want) {
			t.Errorf("rule is missing %q\ngot: %s", want, rule)
		}
	}

	// Per-source, never a single global budget: one busy guest must not be able
	// to consume the whole allowance and starve its neighbours.
	if strings.Contains(rule, "--hashlimit-mode dstip") {
		t.Errorf("rate limit must be keyed on source, got: %s", rule)
	}
}

func TestQuarantineRuleDropsBySourceMAC(t *testing.T) {
	rule := strings.Join(quarantineRule("52:54:00:12:6c:86"), " ")

	if !strings.Contains(rule, "--mac-source 52:54:00:12:6c:86") {
		t.Errorf("quarantine must match the guest's MAC, got: %s", rule)
	}
	if !strings.Contains(rule, "-j DROP") {
		t.Errorf("quarantine must drop, got: %s", rule)
	}
}

func TestReadQuarantine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quarantine")

	const content = `
# scanning guests found 2026-08-08
52:54:00:12:6c:86
  52:54:00:D1:C9:03   # uppercase and indented on purpose

not-a-mac
103.118.174.33
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	macs, err := readQuarantine(path)
	if err != nil {
		t.Fatalf("readQuarantine: %v", err)
	}

	// Comments, blanks and junk are skipped; MACs are normalised to lowercase so
	// the rule comment matching them stays stable.
	want := []string{"52:54:00:12:6c:86", "52:54:00:d1:c9:03"}
	if len(macs) != len(want) {
		t.Fatalf("got %v, want %v", macs, want)
	}
	for i := range want {
		if macs[i] != want[i] {
			t.Errorf("entry %d: got %q, want %q", i, macs[i], want[i])
		}
	}
}

// A node with nothing quarantined is the normal case; it must not look like a
// failure, or every agent start would log an error and operators would learn to
// ignore it.
func TestReadQuarantineMissingFileIsNotAnError(t *testing.T) {
	macs, err := readQuarantine(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if len(macs) != 0 {
		t.Errorf("expected no MACs, got %v", macs)
	}
}
