package service

import (
	"testing"

	vmimport "github.com/maburvm/panel/internal/agent/import"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

func TestISOFromScan(t *testing.T) {
	disk := func(dev, src string) *pb.ScannedDisk { return &pb.ScannedDisk{Device: dev, SourceFile: src} }
	cases := map[string]struct {
		disks []*pb.ScannedDisk
		want  string
	}{
		"no cd drive":            {[]*pb.ScannedDisk{disk("disk", "/vm/a.qcow2")}, ""},
		"only the cloud-init cd": {[]*pb.ScannedDisk{disk("disk", "/vm/a.qcow2"), disk("cdrom", "/vm/a-seed.iso")}, ""},
		"an iso is mounted":      {[]*pb.ScannedDisk{disk("cdrom", "/vm/a-seed.iso"), disk("cdrom", "/iso/debian-13.iso")}, "/iso/debian-13.iso"},
		"empty drive":            {[]*pb.ScannedDisk{disk("cdrom", "")}, ""},
		"an iso used as a disk":  {[]*pb.ScannedDisk{disk("disk", "/iso/odd.iso")}, ""},
	}
	for name, c := range cases {
		if got := isoFromScan(&pb.ScannedVM{Disks: c.disks}); got != c.want {
			t.Errorf("%s: got %q, want %q", name, got, c.want)
		}
	}
}

func TestImportedDisksRecordBlockDataDisks(t *testing.T) {
	const gib = int64(1) << 30
	c := &vmimport.ImportCandidate{Disks: []vmimport.DiskInfo{
		{SourceFile: "/dev/vg0/legacy101-0", Device: "disk", TargetDev: "vda", Size: 40 * gib},
		{SourceFile: "/var/iso/x.iso", Device: "cdrom", TargetDev: "hda"},
		{SourceFile: "/dev/vg0/legacy101-1", Device: "disk", TargetDev: "vdb", Size: 100*gib + 1},
		{SourceFile: "/vm/extra.qcow2", Device: "disk", TargetDev: "vdc", Size: 10 * gib},
	}}
	gb, data := importedDisks(c)
	if gb != 50 {
		t.Fatalf("the row counts the system disk and file disks: got %d", gb)
	}
	if len(data) != 1 || data[0].Device != "vdb" || data[0].SizeGB != 101 || data[0].Path != "/dev/vg0/legacy101-1" {
		t.Fatalf("data disks = %+v", data)
	}
}
