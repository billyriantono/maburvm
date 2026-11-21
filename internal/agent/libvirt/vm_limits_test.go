//go:build linux

package libvirt

import (
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/agent/domtune"
)

// A new VM is defined with its caps and with discard on its disk, so it never
// runs a moment without them.
func TestDomainXMLCarriesLimitsAndDiscard(t *testing.T) {
	x, err := generateDomainXML(VMConfig{Name: "vm", UUID: "1b4e28ba-2fa1-11d2-883f-0016d3cca427", CPU: 2, Memory: 2048,
		DiskPath: "/tmp/vm.qcow2", Bridge: "br0", CloudInitISOPath: "/tmp/seed.iso",
		Limits: domtune.Limits{CPUCapPercent: 50, TotalIOPS: 1000, ReadBytes: 50000000}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`discard="unmap"`, `detect_zeroes="unmap"`, `<quota>50000</quota>`, `<total_iops_sec>1000</total_iops_sec>`, `<read_bytes_sec>50000000</read_bytes_sec>`} {
		if !strings.Contains(x, want) {
			t.Errorf("the domain lacks %s:\n%s", want, x)
		}
	}
	if strings.Count(x, "<iotune>") != 1 || strings.Count(x, `discard="unmap"`) != 1 {
		t.Errorf("the seed cdrom got disk settings:\n%s", x)
	}

	x, err = generateDomainXML(VMConfig{Name: "vm", UUID: "1b4e28ba-2fa1-11d2-883f-0016d3cca427", CPU: 1, Memory: 512, DiskPath: "/tmp/vm.qcow2", Bridge: "br0"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(x, "<cputune>") || strings.Contains(x, "<iotune>") {
		t.Errorf("a VM with no limits got caps:\n%s", x)
	}
}
