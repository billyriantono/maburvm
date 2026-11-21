package server

import (
	"context"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/agent/libvirt"
)

// A graceful stop is done when the domain is shut off, not when the power
// button was pressed; a guest that stays on is reported, within the caller's
// deadline.
func TestWaitShutOffWaitsForTheDomain(t *testing.T) {
	polls := 0
	shutsDown := func(string) (libvirt.VMStatus, error) {
		polls++
		if polls < 3 {
			return libvirt.VMStatusRunning, nil
		}
		return libvirt.VMStatusStopped, nil
	}
	if err := waitShutOff(context.Background(), "vm", shutsDown); err != nil || polls != 3 {
		t.Fatalf("err %v after %d polls, want nil after 3", err, polls)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	staysOn := func(string) (libvirt.VMStatus, error) { return libvirt.VMStatusRunning, nil }
	start := time.Now()
	if err := waitShutOff(ctx, "vm", staysOn); err == nil {
		t.Fatal("a guest that never powered off was reported stopped")
	}
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("answered after %s, past what the caller could wait for", took)
	}
	if got := stopWait(context.Background()); got != gracefulStopWait {
		t.Errorf("without a deadline the stop waits %s", got)
	}
}
