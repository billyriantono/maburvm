package secret

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMain points the global secret store at a throwaway temp dir so tests that
// touch the package-level JWTSecret/AESKey don't create a stray ./data dir.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "secret-test-*")
	if err != nil {
		panic(err)
	}
	os.Setenv(dataDirEnv, dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestResolve_GeneratesPersistsAndReuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s := newStore(path)

	calls := 0
	gen := func() string { calls++; return "generated-value" }

	// First call generates and persists.
	v1 := s.resolve("SOME_ENV_THAT_IS_UNSET", "k", gen)
	if v1 != "generated-value" || calls != 1 {
		t.Fatalf("expected generation once, got value=%q calls=%d", v1, calls)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected secrets file to be written: %v", err)
	}

	// Second call on the same store reuses the cached value (no regeneration).
	if v2 := s.resolve("SOME_ENV_THAT_IS_UNSET", "k", gen); v2 != v1 || calls != 1 {
		t.Fatalf("expected reuse without regeneration, got value=%q calls=%d", v2, calls)
	}

	// A fresh store loading the same file returns the persisted value.
	s2 := newStore(path)
	if v3 := s2.resolve("SOME_ENV_THAT_IS_UNSET", "k", func() string { t.Fatal("should not regenerate"); return "" }); v3 != v1 {
		t.Fatalf("expected persisted value %q, got %q", v1, v3)
	}
}

func TestResolve_EnvOverrideWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets.json")
	s := newStore(path)

	t.Setenv("MY_SECRET_ENV", "from-env")
	v := s.resolve("MY_SECRET_ENV", "k", func() string { t.Fatal("should not generate when env set"); return "" })
	if v != "from-env" {
		t.Fatalf("expected env override, got %q", v)
	}
	// Env override must not be persisted to the file.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("env override should not create a secrets file")
	}
}

func TestAESKey_Is32Bytes(t *testing.T) {
	// Uses the global store; ensure no env override for a clean generation path.
	os.Unsetenv("AES_KEY")
	if got := len(AESKey()); got != 32 {
		t.Fatalf("AES key must be exactly 32 bytes, got %d", got)
	}
}

func TestJWTSecret_NonEmptyAndStable(t *testing.T) {
	os.Unsetenv("JWT_SECRET_KEY")
	a := JWTSecret()
	if a == "" {
		t.Fatal("JWT secret must not be empty")
	}
	if b := JWTSecret(); b != a {
		t.Fatalf("JWT secret must be stable across calls: %q vs %q", a, b)
	}
}
