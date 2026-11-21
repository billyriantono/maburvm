package storage

import (
	"strings"
	"testing"

	"github.com/maburvm/panel/internal/agent/ceph"
)

const (
	rbdCluster = "11111111-2222-3333-4444-555555555555"
	rbdDisk    = "rbd://" + rbdCluster + "/vms/maburvm-vm1-vda"
)

// enableCluster gives the test node the cluster's files, as SetCephCluster does.
func enableCluster(t *testing.T) {
	t.Helper()
	old := ceph.Dir
	ceph.Dir = t.TempDir()
	t.Cleanup(func() { ceph.Dir = old })
	c, err := ceph.Stage(rbdCluster, []string{"10.0.0.1", "10.0.0.2:6789"}, "maburvm", "bWFidXJ2bS10ZXN0LWtleS1ub3QtcmVhbA==")
	if err == nil {
		_, err = ceph.Commit(c)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// A disk on RBD is defined as a network disk that names every monitor and the
// cluster's libvirt secret, bypasses the host cache and gives freed blocks
// back; read back from a definition it is the same disk path.
func TestDiskDeviceOnRBD(t *testing.T) {
	enableCluster(t)
	if err := CheckDiskSource(rbdDisk); err != nil {
		t.Fatal(err)
	}
	d := DiskDevice(rbdDisk, "vda")
	if d.Driver.Type != "raw" || d.Driver.Cache != "none" || d.Driver.Discard != "unmap" || d.Driver.IO != "" {
		t.Errorf("driver = %+v", d.Driver)
	}
	doc, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`type="network"`, `protocol="rbd"`, `name="vms/maburvm-vm1-vda"`, `<host name="10.0.0.1"`, `<host name="10.0.0.2" port="6789"`,
		`username="maburvm"`, `uuid="` + rbdCluster + `"`} {
		if !strings.Contains(doc, want) {
			t.Errorf("disk lacks %s:\n%s", want, doc)
		}
	}
	disks, err := DomainDisks("<domain><devices>" + doc + "</devices></domain>")
	if err != nil || len(disks) != 1 {
		t.Fatalf("DomainDisks = %+v, %v", disks, err)
	}
	if got := disks[0]; got.Path != rbdDisk || !got.RBD || got.Block || got.Format != "raw" || got.Target != "vda" {
		t.Errorf("read back as %+v", got)
	}
}

// Without the cluster enabled here there is no monitor to name: refused
// before a definition is written.
func TestCheckDiskSourceNeedsTheClusterHere(t *testing.T) {
	old := ceph.Dir
	ceph.Dir = t.TempDir()
	t.Cleanup(func() { ceph.Dir = old })
	if err := CheckDiskSource(rbdDisk); err == nil {
		t.Fatal("a disk on a cluster this node does not have passed")
	}
	if err := CheckDiskSource("/var/lib/libvirt/images/a.qcow2"); err != nil {
		t.Fatal(err)
	}
}

func TestRBDDisksTakeTheirOwnRoutes(t *testing.T) {
	rbd := DomainDisk{Target: "vda", Path: rbdDisk, Format: "raw", RBD: true}
	file := DomainDisk{Target: "vdb", Path: "/vm/x-data.qcow2", Format: "qcow2"}

	if all, any := OnRBD([]DomainDisk{rbd}); !all || !any {
		t.Error("a VM on RBD")
	}
	if all, any := OnRBD([]DomainDisk{rbd, file}); all || !any {
		t.Error("a VM with both kinds")
	}
	if all, any := OnRBD([]DomainDisk{file}); all || any {
		t.Error("a VM on files")
	}
	if all, _ := OnRBD(nil); all {
		t.Error("no disks are not disks on RBD")
	}
	if RestoreMethod(rbd) != RestoreReplaceRBD {
		t.Errorf("restore method = %s", RestoreMethod(rbd))
	}
	// A raw image keeps no record of what changed: backups of it are full.
	if ok, why := ChainableDisks([]DomainDisk{rbd}); ok || !strings.Contains(why, "Ceph RBD") {
		t.Errorf("ChainableDisks = %v, %q", ok, why)
	}
	// A VM with both kinds takes no snapshot inside its images.
	if why := SnapshotBlocker([]DomainDisk{file, rbd}); !strings.Contains(why, "Ceph RBD") {
		t.Errorf("SnapshotBlocker = %q", why)
	}
}

// A backup overlay is never put over an image on RBD.
func TestBackupSnapshotRefusesRBD(t *testing.T) {
	enableCluster(t)
	doc, _ := func() (string, error) { d := DiskDevice(rbdDisk, "vda"); return d.Marshal() }()
	if _, _, err := BackupSnapshot("<domain><devices>"+doc+"</devices></domain>", "/vm", "vm1", 1); err == nil || !strings.Contains(err.Error(), "Ceph RBD") {
		t.Fatalf("err = %v", err)
	}
}
