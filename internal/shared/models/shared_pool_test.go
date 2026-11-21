package models

import (
	"strings"
	"testing"
)

func TestValidateSharedPool(t *testing.T) {
	const id = "0b6f1c3e-8a52-4a0f-9d0e-1f2a3b4c5d6e"
	if p, err := ValidateSharedPool(id, SharedPoolNFS, "", "nas.example.net", "/export/vms", "vers=4.2,noatime"); err != nil || p != "/var/lib/maburvm/pools/"+id {
		t.Fatalf("a plain NFS pool: %q %v", p, err)
	}
	if p, err := ValidateSharedPool(id, SharedPoolMounted, "/mnt/vmstore", "", "", ""); err != nil || p != "/mnt/vmstore" {
		t.Fatalf("a mounted pool: %q %v", p, err)
	}
	// What would break out of the mount unit, or make the mount unsafe.
	bad := []struct{ kind, dir, server, export, options, why string }{
		{SharedPoolNFS, "", "nas\nExecStart=/bin/sh", "/export", "", "host name"},
		{SharedPoolNFS, "", "nas", "/export\nOptions=x", "", "export"},
		{SharedPoolNFS, "", "nas", "/export /other", "", "export"},
		{SharedPoolNFS, "", "nas", "/export/../etc", "", "export"},
		{SharedPoolNFS, "", "nas", "/export", "rw nosuid", "mount options"},
		{SharedPoolNFS, "", "nas", "/export", "vers=4,nolock", "nolock"},
		{SharedPoolNFS, "", "nas", "/export", "soft", "soft"},
		{SharedPoolNFS, "", "nas", "/export", "vers=4.2,local_lock=posix", "local_lock=posix"},
		{SharedPoolNFS, "", "nas", "/export", "local_lock=all", "local_lock=all"},
		{SharedPoolNFS, "", "nas", "/export", "softerr", "softerr"},
		{SharedPoolMounted, "mnt/x", "", "", "", "absolute"},
		{SharedPoolMounted, "/", "", "", "", "absolute"},
		{SharedPoolMounted, "/mnt/a b", "", "", "", "absolute"},
		{"ceph", "/mnt/x", "", "", "", "kind"},
	}
	for _, b := range bad {
		if _, err := ValidateSharedPool(id, b.kind, b.dir, b.server, b.export, b.options); err == nil || !strings.Contains(err.Error(), b.why) {
			t.Errorf("%+v: got %v, want a refusal naming %q", b, err, b.why)
		}
	}
	if _, err := ValidateSharedPool("../../etc", SharedPoolNFS, "", "nas", "/export", ""); err == nil {
		t.Error("an id that is not a uuid was accepted")
	}
}
