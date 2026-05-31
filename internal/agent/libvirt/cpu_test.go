package libvirt

import "testing"

func TestBuildDomainCPU(t *testing.T) {
	// Empty model → portable, live-migratable default (kvm64 custom model).
	def := buildDomainCPU("")
	if def.Mode != "custom" || def.Model == nil || def.Model.Value != "kvm64" {
		t.Fatalf("empty model: got mode=%q model=%+v, want custom/kvm64", def.Mode, def.Model)
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
