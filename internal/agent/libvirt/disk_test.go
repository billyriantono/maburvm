package libvirt

import (
	"testing"

	"libvirt.org/go/libvirtxml"
)

func TestNextFreeVirtioTarget(t *testing.T) {
	mk := func(devs ...string) *libvirtxml.Domain {
		d := &libvirtxml.Domain{Devices: &libvirtxml.DomainDeviceList{}}
		for _, dev := range devs {
			d.Devices.Disks = append(d.Devices.Disks, libvirtxml.DomainDisk{
				Target: &libvirtxml.DomainDiskTarget{Dev: dev},
			})
		}
		return d
	}

	cases := []struct {
		name string
		in   *libvirtxml.Domain
		want string
	}{
		{"only primary vda", mk("vda"), "vdb"},
		{"vda+vdb taken", mk("vda", "vdb"), "vdc"},
		{"fills gap before vdc", mk("vda", "vdc"), "vdb"},
		{"no disks", mk(), "vdb"},
		{"ignores non-virtio targets", mk("vda", "sda", "hdc"), "vdb"},
	}
	for _, c := range cases {
		if got := nextFreeVirtioTarget(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
