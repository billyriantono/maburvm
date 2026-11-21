package ceph

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testID  = "11111111-2222-3333-4444-555555555555"
	testKey = "bWFidXJ2bS10ZXN0LWtleS1ub3QtcmVhbA=="
)

func stage(t *testing.T) Cluster {
	t.Helper()
	Dir = t.TempDir()
	c, err := Stage(testID, []string{"10.0.0.1", "10.0.0.2:6789"}, "maburvm", testKey)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStageKeepsTheKeyInA0600KeyringOnly(t *testing.T) {
	c := stage(t)
	c, err := Commit(c)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(c.Conf()) != filepath.Join(Dir, testID) {
		t.Fatalf("files are at %s, not under the agent's own directory", c.Conf())
	}
	entries, _ := os.ReadDir(filepath.Dir(c.Conf()))
	for _, e := range entries {
		fi, _ := e.Info()
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %o, want 0600", e.Name(), fi.Mode().Perm())
		}
		b, _ := os.ReadFile(filepath.Join(filepath.Dir(c.Conf()), e.Name()))
		if e.Name() != "keyring" && strings.Contains(string(b), testKey) {
			t.Errorf("%s holds the key", e.Name())
		}
	}
	kr, _ := os.ReadFile(c.Keyring())
	if string(kr) != "[client.maburvm]\n\tkey = "+testKey+"\n" {
		t.Errorf("keyring = %q", kr)
	}
	conf, _ := os.ReadFile(c.Conf())
	if !strings.Contains(string(conf), "mon_host = 10.0.0.1,10.0.0.2:6789\n") {
		t.Errorf("conf = %q", conf)
	}
	got, err := Load(testID)
	if err != nil || got.ClientID != "maburvm" || len(got.Monitors) != 2 || got.Conf() != c.Conf() {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	if err := Remove(testID); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(testID); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("after Remove, Load = %v", err)
	}
}

func TestStageRefusesWhatCouldEscapeTheFiles(t *testing.T) {
	Dir = t.TempDir()
	for name, in := range map[string][4]string{
		"id walks out of the directory": {"../../etc", "10.0.0.1", "maburvm", testKey},
		"monitor with a newline":        {testID, "10.0.0.1\nkeyring = /etc/shadow", "maburvm", testKey},
		"client id with a bracket":      {testID, "10.0.0.1", "x]\n[client.admin", testKey},
		"key with a second line":        {testID, "10.0.0.1", "maburvm", testKey + "\n\tcaps mon = allow *"},
	} {
		if _, err := Stage(in[0], []string{in[1]}, in[2], in[3]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if entries, _ := os.ReadDir(Dir); len(entries) != 0 {
		t.Errorf("a refused cluster left files: %v", entries)
	}
}

func TestAStagedClusterDoesNotReplaceAWorkingOne(t *testing.T) {
	c, _ := Commit(stage(t))
	staged, err := Stage(testID, []string{"10.9.9.9"}, "other", testKey)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(testID); got.ClientID != "maburvm" {
		t.Fatalf("staging replaced the cluster in use: %+v", got)
	}
	Discard(staged)
	if _, err := os.Stat(c.Keyring()); err != nil {
		t.Fatalf("discarding the staged files removed the working ones: %v", err)
	}
}

// fakeRun records every command and answers from a table keyed by the tool
// and its first own argument.
func fakeRun(t *testing.T, answers map[string]string, calls *[][]string) {
	t.Helper()
	old := Run
	t.Cleanup(func() { Run = old })
	Run = func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		*calls = append(*calls, append([]string{name}, args...))
		for _, a := range args[6:] { // after --conf, --keyring and --id
			if out, ok := answers[name+" "+a]; ok {
				if strings.HasPrefix(out, "ERR ") {
					return nil, []byte(out[4:]), errors.New("exit status 1")
				}
				return []byte(out), nil, nil
			}
		}
		return nil, []byte("unexpected command"), errors.New("exit status 2")
	}
}

func TestCommandsNameTheConfKeyringAndIDAndNeverTheKey(t *testing.T) {
	c := stage(t)
	var calls [][]string
	fakeRun(t, map[string]string{
		"ceph status": `{"fsid":"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE","health":{"status":"HEALTH_WARN","checks":{
			"OSD_DOWN":{"severity":"HEALTH_WARN","summary":{"message":"1 osds down"}},
			"MON_DOWN":{"severity":"HEALTH_WARN","summary":{"message":"1/3 mons down"}}}}}`,
		"ceph df": `{"stats":{"total_bytes":3000,"total_avail_bytes":2000,"total_used_raw_bytes":1000},
			"pools":[{"name":"vms","stats":{"stored":100,"bytes_used":300,"max_avail":600}},
			         {"name":"old","stats":{"bytes_used":50,"max_avail":10}}]}`,
		"rbd ls": "",
	}, &calls)
	ctx := context.Background()

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.FSID != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" || st.Health != "HEALTH_WARN" ||
		st.Summary != "MON_DOWN: 1/3 mons down\nOSD_DOWN: 1 osds down" {
		t.Errorf("status = %+v", st)
	}
	u, err := c.Usage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if u.Total != 3000 || u.Used != 1000 || u.Available != 2000 || u.Pools["vms"] != (PoolUsage{100, 600}) || u.Pools["old"] != (PoolUsage{50, 10}) {
		t.Errorf("usage = %+v", u)
	}
	if err := c.CheckPool(ctx, "vms", "tenant-a"); err != nil {
		t.Fatal(err)
	}
	if err := c.CheckPool(ctx, "--pool=x", ""); err == nil {
		t.Error("a pool name that reads as an option was accepted")
	}

	if len(calls) != 3 {
		t.Fatalf("ran %d commands, want 3: %v", len(calls), calls)
	}
	for _, call := range calls {
		line := strings.Join(call, " ")
		if !strings.HasPrefix(line, call[0]+" --conf "+c.Conf()+" --keyring "+c.Keyring()+" --id maburvm ") {
			t.Errorf("command does not name its conf, keyring and id: %s", line)
		}
		if strings.Contains(line, testKey) {
			t.Errorf("the key is on a command line: %s", line)
		}
	}
	if got := strings.Join(calls[2], " "); !strings.HasSuffix(got, "ls --pool vms --namespace tenant-a") {
		t.Errorf("pool check ran %q", got)
	}
}

// Each rbd ls here finishes only once every pool's check has started: run one
// after another, the first would wait for the deadline and fail.
func TestSurveyChecksPoolsAtOnce(t *testing.T) {
	c := stage(t)
	old := Run
	t.Cleanup(func() { Run = old })
	const pools = 4
	var mu sync.Mutex
	started := 0
	all := make(chan struct{})
	Run = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
		if name == "ceph" {
			return []byte(`{"stats":{"total_bytes":10,"total_avail_bytes":4},"pools":[]}`), nil, nil
		}
		mu.Lock()
		if started++; started == pools {
			close(all)
		}
		mu.Unlock()
		select {
		case <-all:
			return nil, nil, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	checks := []PoolCheck{{Pool: "a"}, {Pool: "b"}, {Pool: "c", Namespace: "n"}, {Pool: "d"}}
	u, err := c.Survey(ctx, checks)
	if err != nil || u.Total != 10 || u.Used != 6 {
		t.Fatalf("usage = %+v, %v", u, err)
	}
	for _, chk := range checks {
		if chk.Err != nil {
			t.Errorf("pool %s: %v (checked one after another?)", chk.Pool, chk.Err)
		}
	}
}

func TestFailuresSayWhatIsWrongWithoutTheKey(t *testing.T) {
	c := stage(t)
	var calls [][]string
	fakeRun(t, map[string]string{
		"rbd ls":      "ERR noise\nrbd: error opening pool 'nope': (2) No such file or directory",
		"ceph status": `{"fsid":"","health":{}}`,
	}, &calls)
	err := c.CheckPool(context.Background(), "nope", "")
	if err == nil || err.Error() != "rbd: rbd: error opening pool 'nope': (2) No such file or directory" {
		t.Errorf("pool error = %v", err)
	}
	if _, err := c.Status(context.Background()); err == nil {
		t.Error("an answer without an fsid passed as proof")
	}

	Run = func(context.Context, string, ...string) ([]byte, []byte, error) { return nil, nil, exec.ErrNotFound }
	if _, err := c.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "ceph-common") {
		t.Errorf("missing tool error = %v", err)
	}
	Run = func(ctx context.Context, _ string, _ ...string) ([]byte, []byte, error) {
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stands for the deadline passing
	if _, err := c.Status(ctx); err == nil || !strings.Contains(err.Error(), "did not answer") || strings.Contains(err.Error(), testKey) {
		t.Errorf("timeout error = %v", err)
	}
}
