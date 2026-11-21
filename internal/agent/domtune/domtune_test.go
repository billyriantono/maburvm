package domtune

import (
	"encoding/xml"
	"strings"
	"testing"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"libvirt.org/go/libvirtxml"
)

func TestSharesAndQuota(t *testing.T) {
	cases := []struct {
		weight int
		v2     bool
		want   uint64
	}{
		{0, false, 0}, {0, true, 0},
		{100, false, 1024}, {100, true, 100},
		{50, false, 512}, {50, true, 50},
		{200, false, 2048}, {200, true, 200},
		{1, false, 10}, {1, true, 2},
	}
	for _, c := range cases {
		if got := Shares(c.weight, c.v2); got != c.want {
			t.Errorf("Shares(%d, v2=%v) = %d, want %d", c.weight, c.v2, got, c.want)
		}
	}
	if DefaultShares(true) != Shares(100, true) || DefaultShares(false) != Shares(100, false) {
		t.Error("a weight of 100 must be the host default")
	}
	if Quota(0) != 0 || Quota(100) != 0 || Quota(150) != 0 {
		t.Error("no cap, or a cap of a whole core, must not set a quota")
	}
	if Quota(50) != 50000 || Quota(1) != 1000 {
		t.Errorf("Quota(50) = %d, Quota(1) = %d", Quota(50), Quota(1))
	}
}

func testDomain() *libvirtxml.Domain {
	return &libvirtxml.Domain{
		CPUTune: &libvirtxml.DomainCPUTune{
			Shares:  &libvirtxml.DomainCPUTuneShares{Value: 2},
			VCPUPin: []libvirtxml.DomainCPUTuneVCPUPin{{VCPU: 0, CPUSet: "1"}},
		},
		Devices: &libvirtxml.DomainDeviceList{Disks: []libvirtxml.DomainDisk{
			{Device: "disk", Driver: &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "qcow2"}, Target: &libvirtxml.DomainDiskTarget{Dev: "vda"}},
			{Driver: &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "qcow2"}, Target: &libvirtxml.DomainDiskTarget{Dev: "vdb"}},
			{Device: "cdrom", Target: &libvirtxml.DomainDiskTarget{Dev: "sda"}},
		}},
	}
}

func TestApply(t *testing.T) {
	d := testDomain()
	Apply(d, Limits{CPUWeight: 50, CPUCapPercent: 25, TotalIOPS: 500, ReadIOPS: 99, ReadBytes: 10 << 20, WriteBytes: 5 << 20}, true)

	if d.CPUTune.Shares.Value != 50 || d.CPUTune.Period.Value != Period || d.CPUTune.Quota.Value != 25000 {
		t.Errorf("cputune: %+v %+v %+v", d.CPUTune.Shares, d.CPUTune.Period, d.CPUTune.Quota)
	}
	if len(d.CPUTune.VCPUPin) != 1 {
		t.Error("pinning set by someone else was lost")
	}
	for _, i := range []int{0, 1} {
		tune := d.Devices.Disks[i].IOTune
		if tune == nil || tune.TotalIopsSec != 500 || tune.ReadIopsSec != 0 || tune.ReadBytesSec != 10<<20 || tune.WriteBytesSec != 5<<20 {
			t.Errorf("disk %d iotune: %+v (a total must drop the split beside it)", i, tune)
		}
	}
	if d.Devices.Disks[0].IOTune == d.Devices.Disks[1].IOTune {
		t.Error("disks share one iotune value")
	}
	if d.Devices.Disks[2].IOTune != nil {
		t.Error("a cdrom got disk limits")
	}

	// No limits: back to the node's defaults, keeping what is not ours.
	Apply(d, Limits{}, true)
	if d.CPUTune == nil || d.CPUTune.Shares != nil || d.CPUTune.Quota != nil || d.CPUTune.Period != nil || len(d.CPUTune.VCPUPin) != 1 {
		t.Errorf("clearing: %+v", d.CPUTune)
	}
	if d.Devices.Disks[0].IOTune != nil || d.Devices.Disks[1].IOTune != nil {
		t.Error("clearing left disk limits")
	}

	bare := &libvirtxml.Domain{}
	Apply(bare, Limits{}, false)
	if bare.CPUTune != nil {
		t.Error("no limits on a domain without cputune added an empty one")
	}
	only := &libvirtxml.Domain{CPUTune: &libvirtxml.DomainCPUTune{Shares: &libvirtxml.DomainCPUTuneShares{Value: 1024}}}
	Apply(only, Limits{}, false)
	if only.CPUTune != nil {
		t.Error("an emptied cputune was kept")
	}
}

func TestApplyMarshals(t *testing.T) {
	d := testDomain()
	d.Type, d.Name = "kvm", "vm"
	Apply(d, Limits{CPUCapPercent: 50, ReadIOPS: 100, WriteIOPS: 50}, false)
	out, err := xml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"<period>100000</period>", "<quota>50000</quota>", "<read_iops_sec>100</read_iops_sec>", "<write_iops_sec>50</write_iops_sec>"} {
		if !strings.Contains(s, want) {
			t.Errorf("definition lacks %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, "total_iops_sec") || strings.Contains(s, "<shares>") {
		t.Errorf("unset caps written:\n%s", s)
	}
}

func TestDiscard(t *testing.T) {
	drv := &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "qcow2"}
	Discard(drv)
	if drv.Discard != "unmap" || drv.DetectZeros != "unmap" {
		t.Errorf("qcow2: %+v", drv)
	}
	raw := &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"}
	Discard(raw)
	if raw.Discard != "" {
		t.Error("a raw disk (cdrom, seed) was given discard")
	}
	chosen := &libvirtxml.DomainDiskDriver{Type: "qcow2", Discard: "ignore"}
	Discard(chosen)
	if chosen.Discard != "ignore" || chosen.DetectZeros != "" {
		t.Errorf("an operator's choice was overwritten: %+v", chosen)
	}
	Discard(nil)
}

func TestDiscardXML(t *testing.T) {
	in := `<domain><devices>
<disk type="file" device="disk"><driver name="qemu" type="qcow2" cache="none"/><source file="/vm/a.qcow2"/><target dev="vda" bus="virtio"/></disk>
<disk type='file' device='disk'><driver name='qemu' type='qcow2'/><target dev='vdb' bus='virtio'/></disk>
<disk type="file" device="disk"><driver name="qemu" type="qcow2" discard="ignore"/><target dev="vdc"/></disk>
<disk type="file" device="disk"><driver name="qemu" type="qcow2" detect_zeroes="on"/><target dev="vdd"/></disk>
<disk type="file" device="cdrom"><driver name="qemu" type="raw"/><target dev="sda"/></disk>
<disk type="file" device="cdrom"><driver name="qemu" type="qcow2" cache="none"/><target dev="sdb"/></disk>
<disk type="file"><driver name="qemu" type="qcow2" io="native"/><target dev="vde"/></disk>
<interface type="bridge"><driver name="vhost" queues="2"/></interface>
</devices></domain>`
	out := DiscardXML(in)
	for _, want := range []string{
		`<driver discard="unmap" detect_zeroes="unmap" name="qemu" type="qcow2" cache="none"/>`,
		`<driver discard="unmap" detect_zeroes="unmap" name='qemu' type='qcow2'/>`,
		`<driver name="qemu" type="qcow2" discard="ignore"/>`,
		`<driver name="qemu" type="qcow2" detect_zeroes="on"/>`,
		`<driver name="qemu" type="raw"/>`,
		`<driver name="vhost" queues="2"/>`,
		`<driver name="qemu" type="qcow2" cache="none"/><target dev="sdb"/>`,
		`<driver discard="unmap" detect_zeroes="unmap" name="qemu" type="qcow2" io="native"/>`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	if again := DiscardXML(out); again != out {
		t.Error("not idempotent")
	}
	var d libvirtxml.Domain
	if err := xml.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("the result does not parse: %v", err)
	}
}

func TestFromProto(t *testing.T) {
	if FromProto(nil) != (Limits{}) {
		t.Error("no limits sent must be no limits")
	}
	got := FromProto(&pb.VMLimits{CpuWeight: 50, CpuCapPercent: 30, TotalIopsSec: 500, ReadBytesSec: 1e6, WriteBytesSec: -5})
	want := Limits{CPUWeight: 50, CPUCapPercent: 30, TotalIOPS: 500, ReadBytes: 1e6}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
