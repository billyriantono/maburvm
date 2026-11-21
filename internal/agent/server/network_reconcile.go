package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/maburvm/panel/internal/agent/guestos"
	"github.com/maburvm/panel/internal/agent/libvirt"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// Host rules are only rewritten when the panel pushes a change, so anything
// that removes them in between (an iptables flush, another tool reloading its
// rules, someone tidying up by hand) used to leave a VM unprotected with the
// panel still showing its firewall as on. Every few minutes the agent compares
// what is installed for each VM with the request it last applied and, where
// they differ, applies that request again.

const (
	// reconcileTick is how often pending guest changes are retried.
	reconcileTick = time.Minute
	// driftEvery is how many ticks pass between two drift checks.
	driftEvery = 5
)

var (
	networkRepairs    atomic.Int64
	networkRepairLast atomic.Value // string
	// unrepaired holds, per VM, the difference a re-apply did not clear (a
	// forward the host refuses, say), so it is not re-applied every check.
	unrepaired sync.Map // vm id -> string
)

func networkRepairLastText() string {
	s, _ := networkRepairLast.Load().(string)
	return s
}

// runNetworkReconciler retries pending guest changes every tick and checks
// every VM's host rules every driftEvery ticks, until ctx ends.
func (s *NodeAgentService) runNetworkReconciler(ctx context.Context) {
	t := time.NewTicker(reconcileTick)
	defer t.Stop()
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.retryPendingGuests(ctx)
		if n%driftEvery == 0 {
			// The first check after a start brings rules written by an older
			// agent in line; that is no sign of anything rewriting them.
			s.checkNetworkDrift(ctx, n > driftEvery)
		}
	}
}

// checkNetworkDrift compares every saved VM config with the host and repairs
// the VMs that differ; count says whether repairs are reported to the panel.
func (s *NodeAgentService) checkNetworkDrift(ctx context.Context, count bool) {
	if s.networkMgr == nil {
		return
	}
	if s.networkMgr.EnsureChains() {
		log.Printf("[NetworkReconcile] the agent's chains or their jumps were missing and were put back")
	}
	saved := loadNetworkConfigs(netConfigPath())
	if len(saved.Configs) == 0 {
		return
	}
	snap, publish := s.networkMgr.Snapshot()
	for vmID, raw := range saved.Configs {
		if ctx.Err() != nil {
			return
		}
		req := &pb.NetworkConfigRequest{}
		if err := protojson.Unmarshal(raw, req); err != nil || req.Config == nil {
			continue
		}
		if _, err := libvirt.GetVMInfo(vmID); err != nil {
			if libvirt.IsNoDomain(err) {
				updateNetworkConfig(netConfigPath(), currentBootID(), vmID, nil)
			}
			continue
		}
		d, _ := desiredFromRequest(req)
		if drift := s.networkMgr.Drift(d, snap, publish); len(drift) > 0 {
			s.repairVMNetwork(ctx, vmID, count)
		} else {
			unrepaired.Delete(vmID)
		}
	}
}

// repairVMNetwork re-applies a VM's saved config when it still differs from the
// host once looked at again under the VM's lock: a push that was halfway
// through when the snapshot was taken is not drift.
func (s *NodeAgentService) repairVMNetwork(ctx context.Context, vmID string, count bool) {
	unlock := lockVMNetwork(vmID)
	defer unlock()
	raw, ok := loadNetworkConfigs(netConfigPath()).Configs[vmID]
	if !ok {
		return
	}
	req := &pb.NetworkConfigRequest{}
	if err := protojson.Unmarshal(raw, req); err != nil || req.Config == nil {
		return
	}
	d, _ := desiredFromRequest(req)
	snap, publish := s.networkMgr.Snapshot()
	drift := s.networkMgr.Drift(d, snap, publish)
	if len(drift) == 0 {
		unrepaired.Delete(vmID)
		return
	}
	text := strings.Join(drift, "; ")
	if prev, ok := unrepaired.Load(vmID); ok && prev.(string) == text {
		return
	}
	req.ConfigureGuest = false
	rctx := context.WithValue(ctx, ContextKeyNodeID, "network-reconcile")
	if _, err := s.applyNetworkConfigLocked(rctx, req); err != nil {
		log.Printf("[NetworkReconcile] WARNING: VM %s differs from its config (%s) and could not be repaired: %v", vmID, text, err)
		return
	}
	snap, publish = s.networkMgr.Snapshot()
	if after := s.networkMgr.Drift(d, snap, publish); len(after) > 0 {
		unrepaired.Store(vmID, strings.Join(after, "; "))
		log.Printf("[NetworkReconcile] WARNING: VM %s re-applied but still differs from its config (%s); not retried until that changes", vmID, strings.Join(after, "; "))
		return
	}
	unrepaired.Delete(vmID)
	msg := fmt.Sprintf("VM %s: %s", vmID, text)
	if !count {
		log.Printf("[NetworkReconcile] brought host rules in line at start: %s", msg)
		return
	}
	networkRepairs.Add(1)
	networkRepairLast.Store(time.Now().UTC().Format(time.RFC3339) + " " + msg)
	log.Printf("[NetworkReconcile] repaired host rules of %s", msg)
}

// An address change a running guest could not take is kept here until the
// guest agent answers or the VM is started from the panel, so it is applied
// then rather than by restarting the VM.

var pendingGuestMu sync.Mutex

func pendingGuestPath() string { return filepath.Join(agentDataDir(), "guest-network-pending.json") }

func loadPendingGuests() map[string]guestNet {
	out := map[string]guestNet{}
	b, err := os.ReadFile(pendingGuestPath())
	if err != nil {
		return out
	}
	if err := json.Unmarshal(b, &out); err != nil {
		log.Printf("[NetworkReconcile] WARNING: ignoring unreadable %s: %v", pendingGuestPath(), err)
		return map[string]guestNet{}
	}
	return out
}

// refusedGuests are VMs whose pending change the guest ran and could not take
// (or whose network set-up the panel does not know). Running it again every
// tick would rewrite and bounce the guest's network each minute, so it is
// tried once more only after the VM's next start or an agent restart.
var refusedGuests sync.Map // vm id -> struct{}

func updatePendingGuest(vmID string, n *guestNet) {
	refusedGuests.Delete(vmID)
	pendingGuestMu.Lock()
	defer pendingGuestMu.Unlock()
	all := loadPendingGuests()
	if _, had := all[vmID]; !had && n == nil {
		return
	}
	if n == nil {
		delete(all, vmID)
	} else {
		all[vmID] = *n
	}
	if err := writeJSONFile(pendingGuestPath(), all); err != nil {
		log.Printf("[NetworkReconcile] WARNING: saving %s: %v", pendingGuestPath(), err)
	}
}

func savePendingGuest(vmID string, n guestNet) { updatePendingGuest(vmID, &n) }
func clearPendingGuest(vmID string)            { updatePendingGuest(vmID, nil) }

// pendingGuestIDs lists the VMs whose guest still waits for an address change.
func pendingGuestIDs() []string {
	pendingGuestMu.Lock()
	all := loadPendingGuests()
	pendingGuestMu.Unlock()
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// retryPendingGuests tries each pending change once on running guests.
func (s *NodeAgentService) retryPendingGuests(ctx context.Context) {
	pendingGuestMu.Lock()
	all := loadPendingGuests()
	pendingGuestMu.Unlock()
	for vmID, n := range all {
		info, err := libvirt.GetVMInfo(vmID)
		if libvirt.IsNoDomain(err) {
			clearPendingGuest(vmID)
			continue
		}
		if err != nil || info.Status != libvirt.VMStatusRunning {
			continue
		}
		if _, refused := refusedGuests.Load(vmID); refused {
			continue
		}
		err = s.applyGuestNetwork(ctx, vmID, n, 0)
		var na *guestNotApplied
		switch {
		case err == nil || (errors.As(err, &na) && na.applied):
			clearPendingGuest(vmID)
			log.Printf("[NetworkReconcile] VM %s took its pending address change", vmID)
		case errors.As(err, &na) && !na.pending:
			refusedGuests.Store(vmID, struct{}{})
			log.Printf("[NetworkReconcile] VM %s did not take its pending address change (%s); tried again after its next start", vmID, na.msg)
		}
	}
}

// applyPendingGuestBeforeStart writes a pending change onto a stopped guest's
// disk (Linux) or start-up script (RouterOS) just before it starts. Windows
// is only changed live, so it stays pending until its guest agent answers.
// A failure here never stops the start.
func (s *NodeAgentService) applyPendingGuestBeforeStart(ctx context.Context, vmID string) {
	pendingGuestMu.Lock()
	n, ok := loadPendingGuests()[vmID]
	pendingGuestMu.Unlock()
	if !ok {
		return
	}
	refusedGuests.Delete(vmID) // a start is worth one more try once it runs
	if libvirt.GuestOSType(vmID) == guestos.Windows {
		return
	}
	if info, err := libvirt.GetVMInfo(vmID); err != nil || info.Status != libvirt.VMStatusStopped {
		return
	}
	var na *guestNotApplied
	if err := s.applyGuestNetwork(ctx, vmID, n, 0); err != nil && !(errors.As(err, &na) && na.applied) {
		log.Printf("[NodeAgent] WARNING: VM %s: pending address change not applied before start: %v", vmID, err)
		return
	}
	clearPendingGuest(vmID)
	log.Printf("[NodeAgent] VM %s: pending address change applied before start", vmID)
}
