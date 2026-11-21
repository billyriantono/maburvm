package vmhealth

import (
	"sync"
	"time"
)

// Restart-on-crash policy: a crashed VM is started again at once the first
// time, then after a growing pause. Past MaxCrashRestarts crashes within
// CrashWindow the node stops restarting it, leaves it off and says so, until
// someone starts it again.
const (
	CrashWindow      = time.Hour
	MaxCrashRestarts = 3
)

var crashDelays = []time.Duration{0, time.Minute, 5 * time.Minute}

// retryAfterFailedStart is how long a crashed VM whose start failed waits
// before the next try.
const retryAfterFailedStart = time.Minute

// Crashes tracks the crashes of the VMs whose policy is to restart on crash.
type Crashes struct {
	mu  sync.Mutex
	vms map[string]*crashState
	now func() time.Time
}

type crashState struct {
	crashes []time.Time
	// pending: a crash was recorded and the VM has not been seen running since.
	pending bool
	next    time.Time
	paused  bool
	// held: someone is working on the VM (a reinstall, a restore), so it is
	// not started under them until it is seen running again.
	held bool
}

// NewCrashes returns an empty tracker.
func NewCrashes() *Crashes {
	return &Crashes{vms: map[string]*crashState{}, now: time.Now}
}

func (c *Crashes) state(vmID string) *crashState {
	st := c.vms[vmID]
	if st == nil {
		st = &crashState{}
		c.vms[vmID] = st
	}
	return st
}

// Crashed is called each time the VM is seen crashed (it may be seen many
// times before it runs again). It reports whether to start it now.
func (c *Crashes) Crashed(vmID string) (startNow bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	st := c.state(vmID)
	if !st.pending {
		st.pending = true
		st.crashes = append(recentCrashes(st.crashes, now), now)
		n := len(st.crashes)
		if n > MaxCrashRestarts {
			st.paused = true
		} else {
			st.next = now.Add(crashDelays[min(n-1, len(crashDelays)-1)])
		}
	}
	return !st.paused && !st.held && !now.Before(st.next)
}

// Hold stops restarting the VM until it is seen running again: an operation
// on a stopped VM (a reinstall, a disk restore, a password written onto its
// disk) must not have the VM started under it.
func (c *Crashes) Hold(vmID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state(vmID).held = true
}

// StartFailed pushes the next try back after a start that did not work.
func (c *Crashes) StartFailed(vmID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state(vmID).next = c.now().Add(retryAfterFailedStart)
}

// Running is called when the VM is seen running. Someone starting a VM the
// node gave up on lifts the pause; its history stays, so it is paused again
// quickly if it keeps crashing.
func (c *Crashes) Running(vmID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.vms[vmID]
	if st == nil {
		return
	}
	st.pending, st.paused, st.held = false, false, false
	st.crashes = recentCrashes(st.crashes, c.now())
	if len(st.crashes) == 0 {
		delete(c.vms, vmID)
	}
}

// Status is what the node reports for the VM: crashes within the window, and
// whether it stopped restarting it.
func (c *Crashes) Status(vmID string) (crashes int, paused bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.vms[vmID]
	if st == nil {
		return 0, false
	}
	return len(recentCrashes(st.crashes, c.now())), st.paused
}

// Forget drops a VM: deleted, moved away, or its policy turned off.
func (c *Crashes) Forget(vmID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.vms, vmID)
}

func recentCrashes(all []time.Time, now time.Time) []time.Time {
	out := all[:0]
	for _, t := range all {
		if now.Sub(t) < CrashWindow {
			out = append(out, t)
		}
	}
	return out
}
