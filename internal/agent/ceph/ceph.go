// Package ceph gives the agent access to existing Ceph clusters the panel
// names: a conf file and a keyring per cluster under the agent's own data
// directory, and the ceph and rbd command-line tools run against them. There
// is no librados here, so the agent builds on nodes without Ceph libraries.
//
// The key is only ever in the keyring file (mode 0600): never on a command
// line, never in an error, never in a log.
package ceph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// Dir holds one directory per cluster, named by the cluster's id. The agent
// sets it from its data directory at start; /etc/ceph is never touched.
var Dir = "/var/lib/maburvm/ceph"

// ErrNotEnabled is a cluster this node has no access files for.
var ErrNotEnabled = errors.New("the Ceph cluster is not enabled on this node")

// cmdTimeout bounds every ceph and rbd command: a cluster that does not
// answer must not hold the caller.
const cmdTimeout = 12 * time.Second

// Cluster is one cluster's access on this node.
type Cluster struct {
	ID       string   `json:"id"`
	Monitors []string `json:"monitors"`
	ClientID string   `json:"client_id"`
	dir      string
}

// Conf and Keyring are the files the ceph tools, and qemu-img, are pointed at.
func (c Cluster) Conf() string    { return filepath.Join(c.dir, "ceph.conf") }
func (c Cluster) Keyring() string { return filepath.Join(c.dir, "keyring") }

func clusterDir(id string) string { return filepath.Join(Dir, id) }

// clusterFiles are a cluster's files, in the order they are put in place:
// cluster.json last, as it is what says the cluster is enabled here.
var clusterFiles = []string{"keyring", "ceph.conf", "cluster.json"}

// Stage writes a cluster's files beside its directory, so new settings can be
// proved before they replace ones that work. Commit puts them in place;
// Discard drops them.
func Stage(id string, monitors []string, clientID, key string) (Cluster, error) {
	if err := models.ValidateCephAccess(id, monitors, clientID, key); err != nil {
		return Cluster{}, err
	}
	c := Cluster{ID: id, Monitors: monitors, ClientID: clientID, dir: clusterDir(id) + ".staged"}
	if err := os.RemoveAll(c.dir); err != nil {
		return Cluster{}, err
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return Cluster{}, err
	}
	meta, _ := json.Marshal(c)
	conf := "# Written by the MaburVM agent for Ceph cluster " + id + ". Rewritten from the panel; do not edit.\n" +
		"[global]\nmon_host = " + strings.Join(monitors, ",") + "\n"
	keyring := "[client." + clientID + "]\n\tkey = " + key + "\n"
	for i, data := range []string{keyring, conf, string(meta)} {
		if err := os.WriteFile(filepath.Join(c.dir, clusterFiles[i]), []byte(data), 0o600); err != nil {
			os.RemoveAll(c.dir)
			return Cluster{}, err
		}
	}
	return c, nil
}

// Commit makes staged files the cluster's files. Each file is renamed into
// place, so a command that runs meanwhile finds the old file or the new one,
// never none.
func Commit(staged Cluster) (Cluster, error) {
	dir := clusterDir(staged.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Cluster{}, err
	}
	for _, name := range clusterFiles {
		if err := os.Rename(filepath.Join(staged.dir, name), filepath.Join(dir, name)); err != nil {
			return Cluster{}, err
		}
	}
	_ = os.RemoveAll(staged.dir)
	staged.dir = dir
	return staged, nil
}

// Discard drops staged files.
func Discard(staged Cluster) { _ = os.RemoveAll(staged.dir) }

// Load returns a cluster enabled on this node.
func Load(id string) (Cluster, error) {
	if !models.ValidCephFSID(id) { // a cluster id is a UUID too
		return Cluster{}, ErrNotEnabled
	}
	b, err := os.ReadFile(filepath.Join(clusterDir(id), "cluster.json"))
	if err != nil {
		return Cluster{}, ErrNotEnabled
	}
	c := Cluster{dir: clusterDir(id)}
	if err := json.Unmarshal(b, &c); err != nil || c.ID != id {
		return Cluster{}, fmt.Errorf("this node's record of Ceph cluster %s cannot be read; enable the cluster on the node again", id)
	}
	return c, nil
}

// Remove deletes a cluster's conf and keyring, staged ones too.
func Remove(id string) error {
	if !models.ValidCephFSID(id) {
		return nil
	}
	_ = os.RemoveAll(clusterDir(id) + ".staged")
	return os.RemoveAll(clusterDir(id))
}

// Run executes a command and returns its stdout and stderr; tests replace it.
var Run = func(ctx context.Context, name string, args ...string) ([]byte, []byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

// run calls ceph or rbd as this cluster's client, with the conf, keyring and
// id given explicitly so nothing in /etc/ceph or the environment is used.
func (c Cluster) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return c.runFor(ctx, cmdTimeout, name, args...)
}

// runFor is run with its own time limit, for work on an image that takes as
// long as the image is large.
func (c Cluster) runFor(ctx context.Context, limit time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	full := append([]string{"--conf", c.Conf(), "--keyring", c.Keyring(), "--id", c.ClientID}, args...)
	stdout, stderr, err := Run(ctx, name, full...)
	switch {
	case err == nil:
		return stdout, nil
	case errors.Is(err, exec.ErrNotFound):
		return nil, fmt.Errorf("%s is not installed on this node; install the ceph-common package", name)
	case ctx.Err() != nil:
		return nil, fmt.Errorf("the cluster did not answer %s within %s; check that this node reaches the monitors (%s) and the OSDs",
			name, limit, strings.Join(c.Monitors, ", "))
	}
	if msg := lastLine(string(stderr)); msg != "" {
		return nil, fmt.Errorf("%s: %s", name, msg)
	}
	return nil, fmt.Errorf("%s: %w", name, err)
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Status is a cluster's id and health.
type Status struct {
	FSID   string
	Health string
	// Summary lists the health checks that are not OK, one per line.
	Summary string
}

// Status reads the cluster's fsid and health: the proof that this node, with
// this id and key, reaches the cluster.
func (c Cluster) Status(ctx context.Context) (Status, error) {
	out, err := c.run(ctx, "ceph", "--connect-timeout", "10", "status", "--format", "json")
	if err != nil {
		return Status{}, err
	}
	return parseStatus(out)
}

func parseStatus(out []byte) (Status, error) {
	var doc struct {
		FSID   string `json:"fsid"`
		Health struct {
			Status        string `json:"status"`
			OverallStatus string `json:"overall_status"` // Luminous
			Checks        map[string]struct {
				Summary struct {
					Message string `json:"message"`
				} `json:"summary"`
			} `json:"checks"`
		} `json:"health"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return Status{}, fmt.Errorf("ceph status gave an answer that could not be read: %w", err)
	}
	st := Status{FSID: strings.ToLower(doc.FSID), Health: doc.Health.Status}
	if st.Health == "" {
		st.Health = doc.Health.OverallStatus
	}
	if !models.ValidCephFSID(st.FSID) {
		return Status{}, fmt.Errorf("ceph status did not report the cluster's fsid")
	}
	switch st.Health {
	case models.CephHealthOK, models.CephHealthWarn, models.CephHealthErr:
	default:
		return Status{}, fmt.Errorf("ceph status did not report the cluster's health")
	}
	var lines []string
	for code, chk := range doc.Health.Checks {
		lines = append(lines, code+": "+chk.Summary.Message)
	}
	sort.Strings(lines)
	st.Summary = strings.Join(lines, "\n")
	return st, nil
}

// PoolUsage is what is stored in a pool and what can still be stored there.
type PoolUsage struct{ Used, Available int64 }

// Usage is the cluster's raw capacity and each pool's use.
type Usage struct {
	Total, Used, Available int64
	Pools                  map[string]PoolUsage
}

// Usage reads `ceph df`. A client whose caps do not allow it gets an error;
// callers treat that as "not measured", not as a fault.
func (c Cluster) Usage(ctx context.Context) (Usage, error) {
	out, err := c.run(ctx, "ceph", "--connect-timeout", "10", "df", "--format", "json")
	if err != nil {
		return Usage{}, err
	}
	return parseUsage(out)
}

func parseUsage(out []byte) (Usage, error) {
	var doc struct {
		Stats struct {
			Total int64 `json:"total_bytes"`
			Avail int64 `json:"total_avail_bytes"`
		} `json:"stats"`
		Pools []struct {
			Name  string `json:"name"`
			Stats struct {
				Stored    *int64 `json:"stored"` // Nautilus and later
				BytesUsed int64  `json:"bytes_used"`
				MaxAvail  int64  `json:"max_avail"`
			} `json:"stats"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		return Usage{}, fmt.Errorf("ceph df gave an answer that could not be read: %w", err)
	}
	u := Usage{Total: doc.Stats.Total, Available: doc.Stats.Avail, Used: doc.Stats.Total - doc.Stats.Avail, Pools: map[string]PoolUsage{}}
	for _, p := range doc.Pools {
		used := p.Stats.BytesUsed
		if p.Stats.Stored != nil {
			used = *p.Stats.Stored
		}
		u.Pools[p.Name] = PoolUsage{Used: used, Available: p.Stats.MaxAvail}
	}
	return u, nil
}

// CheckPool lists the images of an RBD pool (and namespace): it passes only
// when the pool exists, the client's caps allow it and the OSDs answer.
func (c Cluster) CheckPool(ctx context.Context, pool, namespace string) error {
	if !models.ValidRBDName(pool) || (namespace != "" && !models.ValidRBDName(namespace)) {
		return fmt.Errorf("%q is not a usable RBD pool name", models.RBDPoolPath(pool, namespace))
	}
	args := []string{"ls", "--pool", pool}
	if namespace != "" {
		args = append(args, "--namespace", namespace)
	}
	_, err := c.run(ctx, "rbd", args...)
	return err
}

// PoolCheck is one RBD pool to check, and what CheckPool said of it.
type PoolCheck struct {
	Pool, Namespace string
	Err             error
}

// Survey reads usage and checks every pool at once. Run one after another, a
// cluster whose OSDs hang would cost a timeout per pool and the answer would
// reach the panel after it stopped waiting, so nothing would be recorded.
func (c Cluster) Survey(ctx context.Context, pools []PoolCheck) (Usage, error) {
	var wg sync.WaitGroup
	var u Usage
	var uerr error
	wg.Add(1 + len(pools))
	go func() { defer wg.Done(); u, uerr = c.Usage(ctx) }()
	for i := range pools {
		go func(p *PoolCheck) { defer wg.Done(); p.Err = c.CheckPool(ctx, p.Pool, p.Namespace) }(&pools[i])
	}
	wg.Wait()
	return u, uerr
}
