package handler

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/maburvm/panel/internal/panel/service"
)

func TestBillingValidatePayload(t *testing.T) {
	h := &BillingHandler{}
	cases := []struct {
		name string
		p    WebhookPayload
		ok   bool
	}{
		{"usage needs vm_id", WebhookPayload{Event: EventVMUsage}, false},
		{"usage", WebhookPayload{Event: EventVMUsage, Data: WebhookData{VMID: "v"}}, true},
		{"short password", WebhookPayload{Event: EventVMResetPassword, Data: WebhookData{VMID: "v", Password: "short"}}, false},
		{"password", WebhookPayload{Event: EventVMResetPassword, Data: WebhookData{VMID: "v", Password: "long-enough"}}, true},
		{"resize without specs", WebhookPayload{Event: EventVMResize, Data: WebhookData{VMID: "v"}}, false},
		{"resize", WebhookPayload{Event: EventVMResize, Data: WebhookData{VMID: "v", VMSpecs: VMSpecs{CPU: 2, Memory: 2048, Disk: 40}}}, true},
		{"restart", WebhookPayload{Event: EventVMRestart, Data: WebhookData{VMID: "v"}}, true},
	}
	for _, c := range cases {
		if !c.p.Event.IsValid() {
			t.Errorf("%s: event %q not accepted", c.name, c.p.Event)
		}
		if err := h.validatePayload(&c.p); (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
}

// A refusal the billing system can act on must not read as a panel fault: a
// 5xx makes it retry, or show the order as broken, for a node that is simply full.
func TestWebhookErrorStatus(t *testing.T) {
	wrap := func(err error) error { return fmt.Errorf("failed to create VM: %w", err) }
	for _, c := range []struct {
		name string
		err  error
		want int
	}{
		{"NAT ports used up", wrap(fmt.Errorf("%w: it offers none until its NAT ports range is set on its page", service.ErrNATPortsExhausted)), http.StatusConflict},
		{"no NAT pool", wrap(service.ErrNoNATPool), http.StatusConflict},
		{"no usable pool", wrap(service.ErrNoUsablePool), http.StatusConflict},
		{"pool full", wrap(service.ErrNoAvailableIPAddress), http.StatusConflict},
		{"region full", wrap(service.ErrRegionNoCapacity), http.StatusConflict},
		{"VM being moved", fmt.Errorf("failed to suspend VM: %w", service.ErrVMMigrating), http.StatusConflict},
		{"unknown VM", service.ErrVMNotFound, http.StatusNotFound},
		{"inactive plan", wrap(service.ErrPlanInactive), http.StatusBadRequest},
		{"SSO refused", errBillingSSORefused, http.StatusForbidden},
		{"database down", wrap(errors.New("connection refused")), http.StatusInternalServerError},
	} {
		if got := webhookErrorStatus(c.err); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
}
