package queue

import (
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
)

func TestAttachISORef(t *testing.T) {
	const iso = "https://example.com/debian.iso"
	if got := attachISORef("/var/lib/libvirt/templates/rescue.iso", ISOSourceOperator); got != "/var/lib/libvirt/templates/rescue.iso" {
		t.Errorf("the operator's ISO was sent as %q", got)
	}
	// Anything but the operator's marker is a customer's, a missing one included.
	for _, source := range []string{"", "customer", "Operator", "operator "} {
		got := attachISORef(iso, source)
		if url, customer := models.SplitCustomerISORef(got); !customer || url != iso {
			t.Errorf("source %q: sent %q, want it marked as a customer's", source, got)
		}
		// An agent that predates the marker must take it for a missing file, never a URL.
		if got[:4] == "http" {
			t.Errorf("source %q: %q still reads as a URL to an old agent", source, got)
		}
	}
	if _, customer := models.SplitCustomerISORef(iso); customer {
		t.Error("an unmarked URL was taken for a customer's")
	}
}
