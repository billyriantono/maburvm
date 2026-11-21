package models

import (
	"strings"
	"testing"
)

func TestCephAccessIsCheckedBeforeItReachesAFile(t *testing.T) {
	const id, key = "11111111-2222-3333-4444-555555555555", "bWFidXJ2bS10ZXN0LWtleS1ub3QtcmVhbA=="
	if err := ValidateCephAccess(id, []string{"10.0.0.1", "mon2.example.net:6789", "[2001:db8::1]:3300"}, "maburvm", key); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"no monitors":            ValidateCephAccess(id, nil, "maburvm", key),
		"monitor with a space":   ValidateCephAccess(id, []string{"10.0.0.1 keyring=/x"}, "maburvm", key),
		"monitor that is a path": ValidateCephAccess(id, []string{"/etc/ceph"}, "maburvm", key),
		"bare IPv6 monitor":      ValidateCephAccess(id, []string{"2001:db8::1"}, "maburvm", key),
		"client. prefix":         ValidateCephAccess(id, []string{"10.0.0.1"}, "client.maburvm", key),
		"client id as an option": ValidateCephAccess(id, []string{"10.0.0.1"}, "-admin", key),
		"key with a newline":     ValidateCephAccess(id, []string{"10.0.0.1"}, "maburvm", key+"\n"),
		"key that is too short":  ValidateCephAccess(id, []string{"10.0.0.1"}, "maburvm", "QUJD"),
		"id that is a path":      ValidateCephAccess("../x", []string{"10.0.0.1"}, "maburvm", key),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), key) {
			t.Errorf("%s: the error carries the key", name)
		}
	}
	if mons, err := ParseCephMonitors(" 10.0.0.1, 10.0.0.2\n10.0.0.3 "); err != nil || len(mons) != 3 {
		t.Errorf("monitors = %v, %v", mons, err)
	}
	if _, err := ParseCephMonitors("10.0.0.1,10.0.0.1"); err == nil {
		t.Error("a monitor listed twice was accepted")
	}
	if got, err := NormalizeCephClientID(" client.maburvm "); err != nil || got != "maburvm" {
		t.Errorf("client id = %q, %v", got, err)
	}
}

func TestRBDNamesAreSafeInACommandAndAPath(t *testing.T) {
	for _, ok := range []string{"vms", "rbd", "tenant-a", "pool_1.ssd"} {
		if !ValidRBDName(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"", "-p", "--pool=x", "a/b", "a@b", "a b", "a:b", strings.Repeat("x", 129)} {
		if ValidRBDName(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if RBDPoolPath("vms", "") != "vms" || RBDPoolPath("vms", "a") != "vms/a" {
		t.Error("RBDPoolPath")
	}
	img, err := ParseRBDPath("rbd://11111111-2222-3333-4444-555555555555/vms/ns1/maburvm-x-vda")
	if err != nil || img.Pool != "vms" || img.Namespace != "ns1" || img.Name != "maburvm-x-vda" || img.Spec() != "vms/ns1/maburvm-x-vda" {
		t.Errorf("ParseRBDPath: %+v %v", img, err)
	}
	if img.Path() != "rbd://11111111-2222-3333-4444-555555555555/vms/ns1/maburvm-x-vda" {
		t.Errorf("Path: %s", img.Path())
	}
	if c, p, ns, err := ParseRBDPoolRef(RBDPoolRef(img.ClusterID, "vms", "")); err != nil || c != img.ClusterID || p != "vms" || ns != "" {
		t.Errorf("ParseRBDPoolRef: %s %s %s %v", c, p, ns, err)
	}
	for _, bad := range []string{"", "/var/lib/x.qcow2", "rbd://vms/img", "rbd://11111111-2222-3333-4444-555555555555/vms", "rbd://11111111-2222-3333-4444-555555555555/vms/a/b/c",
		"rbd://11111111-2222-3333-4444-555555555555/vms/im g", "rbd://11111111-2222-3333-4444-555555555555/vms/img@snap"} {
		if _, err := ParseRBDPath(bad); err == nil {
			t.Errorf("ParseRBDPath(%q) passed", bad)
		}
	}
	if !RBDPoolHolds("vms/ns1", img.Path()) || RBDPoolHolds("vms", img.Path()) || RBDPoolHolds("vms", "/dev/vms/x") {
		t.Error("RBDPoolHolds")
	}
}
