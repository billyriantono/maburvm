//go:build linux

package libvirt

import (
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/agent/guestos"
)

func TestDomainXMLRecordsTheKindOfSystem(t *testing.T) {
	base := VMConfig{Name: "vm", UUID: "1b4e28ba-2fa1-11d2-883f-0016d3cca427", CPU: 1, Memory: 384, DiskPath: "/tmp/vm.qcow2", Bridge: "br0"}

	router := base
	router.OSType = guestos.RouterOS
	x, err := generateDomainXML(router)
	if err != nil {
		t.Fatal(err)
	}
	// RouterOS 6 cannot find its disk on q35, so every router gets the older machine.
	if !strings.Contains(x, `machine="pc"`) {
		t.Errorf("a router's domain is not on the pc machine:\n%s", x)
	}
	if got := guestos.FromDomainXML(x); got != guestos.RouterOS {
		t.Errorf("a router's domain reads back as %q", got)
	}

	x, err = generateDomainXML(base)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(x, `machine="q35"`) || guestos.FromDomainXML(x) != guestos.Linux {
		t.Errorf("a VM that names no kind is not a Linux q35 domain:\n%s", x)
	}
}

func TestDomainXMLForWindows(t *testing.T) {
	x, err := generateDomainXML(VMConfig{Name: "vm", UUID: "1b4e28ba-2fa1-11d2-883f-0016d3cca427", CPU: 2, Memory: 4096,
		DiskPath: "/tmp/vm.qcow2", Bridge: "br0", OSType: guestos.Windows})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`offset="utc"`, `name="hypervclock"`, `<hyperv>`, `<relaxed state="on">`, `type="tablet"`, `machine="q35"`} {
		if !strings.Contains(x, want) {
			t.Errorf("a Windows domain lacks %s:\n%s", want, x)
		}
	}
	if guestos.FromDomainXML(x) != guestos.Windows {
		t.Error("a Windows domain does not read back as Windows")
	}
}
