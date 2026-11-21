package vmhealth

import (
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestGuestAgentTimeoutBacksOffAndNeverStacksCalls(t *testing.T) {
	g := NewGuestAgents()
	clock := time.Unix(1000, 0)
	g.now = func() time.Time { return clock }

	release := make(chan struct{})
	var calls atomic.Int32
	hung := func() error { calls.Add(1); <-release; return nil }

	start := time.Now()
	if err := g.Do("vm1", 20*time.Millisecond, hung); !errors.Is(err, ErrGuestAgentSilent) {
		t.Fatalf("hung call: %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("a hung guest agent held the caller")
	}
	// Backed off, and the first call is still running: nothing new starts.
	clock = clock.Add(2 * time.Minute)
	if err := g.Do("vm1", time.Second, hung); !errors.Is(err, ErrGuestAgentSilent) || calls.Load() != 1 {
		t.Fatalf("second call while the first runs: %v, %d calls", err, calls.Load())
	}
	close(release)
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		g.mu.Lock()
		inFlight := g.vms["vm1"].inFlight
		g.mu.Unlock()
		if !inFlight {
			break
		}
	}
	// Past the back-off a call runs again, and success clears it.
	clock = clock.Add(time.Hour)
	if err := g.Do("vm1", time.Second, func() error { return nil }); err != nil {
		t.Fatalf("after back-off: %v", err)
	}
	if g.Silent("vm1") {
		t.Fatal("a guest agent that answered is still backed off")
	}
	// Other VMs are never held up by one that hangs.
	if err := g.Do("vm2", time.Second, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestGuestAgentBackoffDoubles(t *testing.T) {
	g := NewGuestAgents()
	clock := time.Unix(0, 0)
	g.now = func() time.Time { return clock }
	fail := func() error { return errors.New("no agent") }
	var waits []time.Duration
	for i := 0; i < 6; i++ {
		_ = g.Do("vm", time.Second, fail)
		waits = append(waits, g.vms["vm"].until.Sub(clock))
		clock = g.vms["vm"].until
	}
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 15 * time.Minute, 15 * time.Minute}
	if !slices.Equal(waits, want) {
		t.Fatalf("back-off %v, want %v", waits, want)
	}
}

func TestInterfaceIPs(t *testing.T) {
	reply := `{"return":[{"name":"lo","ip-addresses":[{"ip-address":"127.0.0.1"}]},
		{"name":"eth0","ip-addresses":[{"ip-address":"192.0.2.10"},{"ip-address":"fe80::1"},{"ip-address":"2001:db8::10"},{"ip-address":"192.0.2.10"}]}]}`
	got, err := InterfaceIPs(reply)
	if err != nil || !slices.Equal(got, []string{"192.0.2.10", "2001:db8::10"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if HostName(`{"return":{"host-name":" web-01 "}}`) != "web-01" {
		t.Fatal("host name not read")
	}
}

func TestCrashRestartsBackOffThenStop(t *testing.T) {
	c := NewCrashes()
	clock := time.Unix(0, 0)
	c.now = func() time.Time { return clock }

	// First crash: started at once.
	if !c.Crashed("vm") {
		t.Fatal("first crash not restarted at once")
	}
	c.Running("vm")
	// Second: a minute's pause, and being seen crashed again counts once.
	if c.Crashed("vm") || c.Crashed("vm") {
		t.Fatal("second crash restarted without a pause")
	}
	clock = clock.Add(time.Minute)
	if !c.Crashed("vm") {
		t.Fatal("not restarted after its pause")
	}
	c.StartFailed("vm")
	if c.Crashed("vm") {
		t.Fatal("retried straight after a failed start")
	}
	clock = clock.Add(time.Minute)
	if !c.Crashed("vm") {
		t.Fatal("not retried after a failed start")
	}
	c.Running("vm")
	clock = clock.Add(time.Minute)
	c.Crashed("vm") // third
	c.Running("vm")
	clock = clock.Add(time.Minute)
	if c.Crashed("vm") {
		t.Fatal("a fourth crash within the window was restarted")
	}
	if n, paused := c.Status("vm"); n != 4 || !paused {
		t.Fatalf("status %d %v", n, paused)
	}
	clock = clock.Add(2 * time.Hour)
	if c.Crashed("vm") {
		t.Fatal("a paused VM was restarted without anyone starting it")
	}
	// Someone starts it: the pause lifts and old crashes age out.
	c.Running("vm")
	if n, paused := c.Status("vm"); n != 0 || paused {
		t.Fatalf("after a manual start: %d %v", n, paused)
	}
}

// A crashed VM someone is working on (a reinstall) is not started under them,
// and is not reported as given up on; it is looked after again once it runs.
func TestHeldVMIsNotRestarted(t *testing.T) {
	c := NewCrashes()
	c.Hold("vm")
	if c.Crashed("vm") {
		t.Fatal("a held VM was started")
	}
	if n, paused := c.Status("vm"); n != 1 || paused {
		t.Fatalf("a held VM reads as given up on: %d %v", n, paused)
	}
	c.Running("vm")
	clock := time.Now().Add(10 * time.Minute)
	c.now = func() time.Time { return clock }
	c.Crashed("vm") // its second crash: a minute's pause
	clock = clock.Add(time.Minute)
	if !c.Crashed("vm") {
		t.Fatal("not restarted after being seen running again")
	}
}
