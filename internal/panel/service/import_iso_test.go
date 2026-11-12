package service

import (
	"testing"

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
