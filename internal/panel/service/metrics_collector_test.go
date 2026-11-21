package service

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// Nodes are worked on side by side, never more than the bound at once; a slow
// or panicking node costs only its own pass.
func TestEachNodeBoundedAndIsolated(t *testing.T) {
	c := &MetricsCollector{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	ids := make([]string, 3*collectorNodeParallelism)
	for i := range ids {
		ids[i] = string(rune('a' + i))
	}
	var running, peak, done atomic.Int32
	var mu sync.Mutex
	seen := map[string]bool{}
	start := time.Now()
	c.eachNode(context.Background(), ids, func(_ context.Context, id string) {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer running.Add(-1)
		if id == "a" {
			panic("boom")
		}
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		seen[id] = true
		mu.Unlock()
		done.Add(1)
	})
	assert.LessOrEqual(t, int(peak.Load()), collectorNodeParallelism)
	assert.Greater(t, int(peak.Load()), 1, "nodes ran side by side")
	assert.Equal(t, int32(len(ids)-1), done.Load(), "every node but the one that panicked finished")
	assert.Less(t, time.Since(start), time.Duration(len(ids))*20*time.Millisecond, "not one after another")
}

// VMs are grouped by online node, and each node's list starts one further on
// every tick.
func TestVMsByNode(t *testing.T) {
	vms := []models.VM{{ID: "1", NodeID: "n1"}, {ID: "2", NodeID: "n2"}, {ID: "3", NodeID: "n1"}, {ID: "4", NodeID: "off"}, {ID: "5", NodeID: "n1"}}
	online := map[string]bool{"n1": true, "n2": true}
	ids := func(list []models.VM) []string {
		out := []string{}
		for _, v := range list {
			out = append(out, v.ID)
		}
		return out
	}
	order, by := vmsByNode(vms, online, 0)
	require.Equal(t, []string{"n1", "n2"}, order)
	assert.Equal(t, []string{"1", "3", "5"}, ids(by["n1"]))
	_, by = vmsByNode(vms, online, 1)
	assert.Equal(t, []string{"3", "5", "1"}, ids(by["n1"]))
	assert.Equal(t, []string{"2"}, ids(by["n2"]))
	assert.Equal(t, []string{"1", "2", "3", "4", "5"}, ids(vms), "the input is left as it was")
}
