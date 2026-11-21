// Package vmhealth keeps the agent's bookkeeping about guests that misbehave:
// a guest agent that stops answering, and a VM that keeps crashing. It uses no
// libvirt bindings, so it builds and tests anywhere.
package vmhealth

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// ErrGuestAgentSilent is returned for a VM whose guest agent did not answer
// recently: it is not asked again until its back-off has passed.
var ErrGuestAgentSilent = errors.New("the guest agent is not answering")

// Back-off for a guest agent that did not answer: doubling from the first to
// the last, so a hung agent costs one bounded attempt every few minutes
// instead of a stalled status call every minute.
const (
	guestAgentFirstBackoff = time.Minute
	guestAgentMaxBackoff   = 15 * time.Minute
)

// GuestAgents bounds calls into VMs' guest agents. A call that does not finish
// in time is abandoned (it goes on in the background, and no second call to
// the same VM starts until it returns), and the VM is left alone for a while.
type GuestAgents struct {
	mu  sync.Mutex
	vms map[string]*agentState
	now func() time.Time
}

type agentState struct {
	failures int
	until    time.Time
	inFlight bool
}

// NewGuestAgents returns an empty tracker.
func NewGuestAgents() *GuestAgents {
	return &GuestAgents{vms: map[string]*agentState{}, now: time.Now}
}

// Do runs fn against the VM's guest agent, waiting at most timeout. It returns
// ErrGuestAgentSilent without calling fn while the VM is backed off or an
// earlier call is still running, and when fn does not finish in time.
func (g *GuestAgents) Do(vmID string, timeout time.Duration, fn func() error) error {
	g.mu.Lock()
	st := g.vms[vmID]
	if st == nil {
		st = &agentState{}
		g.vms[vmID] = st
	}
	if st.inFlight || g.now().Before(st.until) {
		g.mu.Unlock()
		return ErrGuestAgentSilent
	}
	st.inFlight = true
	g.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		err := fn()
		g.mu.Lock()
		st.inFlight = false
		g.mu.Unlock()
		done <- err
	}()
	select {
	case err := <-done:
		g.record(st, err == nil)
		return err
	case <-time.After(timeout):
		g.record(st, false)
		return ErrGuestAgentSilent
	}
}

func (g *GuestAgents) record(st *agentState, ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ok {
		st.failures, st.until = 0, time.Time{}
		return
	}
	st.failures++
	backoff := guestAgentFirstBackoff << min(st.failures-1, 4)
	st.until = g.now().Add(min(backoff, guestAgentMaxBackoff))
}

// Silent reports a VM whose guest agent is backed off right now.
func (g *GuestAgents) Silent(vmID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.vms[vmID]
	return st != nil && (st.inFlight || g.now().Before(st.until))
}

// Forget drops a VM's record, for a VM that was deleted or moved away.
func (g *GuestAgents) Forget(vmID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.vms, vmID)
}

// InterfaceIPs reads the addresses out of a guest-network-get-interfaces
// reply, without loopback and link-local ones.
func InterfaceIPs(reply string) ([]string, error) {
	var r struct {
		Return []struct {
			Name        string `json:"name"`
			IPAddresses []struct {
				Address string `json:"ip-address"`
			} `json:"ip-addresses"`
		} `json:"return"`
	}
	if err := json.Unmarshal([]byte(reply), &r); err != nil {
		return nil, err
	}
	var out []string
	seen := map[string]bool{}
	for _, iface := range r.Return {
		if iface.Name == "lo" {
			continue
		}
		for _, a := range iface.IPAddresses {
			ip := strings.TrimSpace(a.Address)
			if ip == "" || seen[ip] || strings.HasPrefix(ip, "127.") || ip == "::1" || strings.HasPrefix(strings.ToLower(ip), "fe80:") {
				continue
			}
			seen[ip] = true
			out = append(out, ip)
		}
	}
	return out, nil
}

// HostName reads the name out of a guest-get-host-name reply.
func HostName(reply string) string {
	var r struct {
		Return struct {
			HostName string `json:"host-name"`
		} `json:"return"`
	}
	if json.Unmarshal([]byte(reply), &r) != nil {
		return ""
	}
	return strings.TrimSpace(r.Return.HostName)
}
