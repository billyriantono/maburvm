package libvirt

import "testing"

func TestBuildDomainCPU(t *testing.T) {
	// Empty model: the host's own CPU. The old kvm64 baseline lacks the
	// instructions current software is built for.
	def := buildDomainCPU("")
	if def.Mode != "host-passthrough" || def.Model != nil {
		t.Fatalf("empty model: got mode=%q model=%+v, want host-passthrough", def.Mode, def.Model)
	}

	// Performance modes pass through as libvirt CPU modes (no custom model).
	if hp := buildDomainCPU("host-passthrough"); hp.Mode != "host-passthrough" || hp.Model != nil {
		t.Errorf("host-passthrough: got mode=%q model=%+v", hp.Mode, hp.Model)
	}
	if hm := buildDomainCPU("host-model"); hm.Mode != "host-model" || hm.Model != nil {
		t.Errorf("host-model: got mode=%q model=%+v", hm.Mode, hm.Model)
	}

	// A named model is used as a custom CPU model.
	if c := buildDomainCPU("EPYC"); c.Mode != "custom" || c.Model == nil || c.Model.Value != "EPYC" {
		t.Errorf("EPYC: got mode=%q model=%+v", c.Mode, c.Model)
	}
}
