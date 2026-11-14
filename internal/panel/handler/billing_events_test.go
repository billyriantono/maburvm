package handler

import "testing"

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
