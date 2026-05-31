package cloudinit

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestNetworkConfigStatic(t *testing.T) {
	got := networkConfig(Config{
		InstanceID: "vm-1",
		MACAddress: "52:54:00:AB:CD:EF",
		IPAddress:  "203.0.113.10",
		Prefix:     24,
		Gateway:    "203.0.113.1",
	})
	for _, want := range []string{
		"version: 2",
		`macaddress: "52:54:00:ab:cd:ef"`, // lower-cased
		"set-name: eth0",
		"dhcp4: false",
		"- 203.0.113.10/24",
		"via: 203.0.113.1",
		"addresses: [1.1.1.1, 8.8.8.8]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("static network-config missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "dhcp4: true") {
		t.Errorf("static config should not enable dhcp4:\n%s", got)
	}
}

func TestNetworkConfigDHCPWhenNoIP(t *testing.T) {
	got := networkConfig(Config{InstanceID: "vm-2", MACAddress: "52:54:00:11:22:33"})
	if !strings.Contains(got, "dhcp4: true") {
		t.Errorf("expected dhcp4: true when no IP set:\n%s", got)
	}
	if strings.Contains(got, "addresses:") {
		t.Errorf("DHCP config should not contain static addresses:\n%s", got)
	}
}

func TestMetaDataAndUserData(t *testing.T) {
	cfg := Config{InstanceID: "vm-3", Hostname: "web01", SSHPublicKey: "ssh-ed25519 AAAA... user@host"}

	md := metaData(cfg)
	if !strings.Contains(md, "instance-id: vm-3") || !strings.Contains(md, "local-hostname: web01") {
		t.Errorf("meta-data incorrect:\n%s", md)
	}

	ud := userData(cfg)
	if !strings.HasPrefix(ud, "#cloud-config") {
		t.Errorf("user-data must start with #cloud-config:\n%s", ud)
	}
	for _, want := range []string{"hostname: web01", "ssh_authorized_keys:", "ssh-ed25519 AAAA... user@host"} {
		if !strings.Contains(ud, want) {
			t.Errorf("user-data missing %q in:\n%s", want, ud)
		}
	}
}

func TestMetaDataFallsBackToInstanceID(t *testing.T) {
	md := metaData(Config{InstanceID: "vm-4"})
	if !strings.Contains(md, "local-hostname: vm-4") {
		t.Errorf("expected hostname to fall back to instance id:\n%s", md)
	}
}

func TestUserDataPasswordAndMultipleKeys(t *testing.T) {
	cfg := Config{
		InstanceID:   "vm-5",
		Hostname:     "db01",
		Password:     "S3cretPass",
		SSHPublicKey: "ssh-ed25519 AAAAkeyone one@host\nssh-rsa AAAAkeytwo two@host",
		SSHKeys:      []string{"ssh-ed25519 AAAAkeythree three@host", "ssh-ed25519 AAAAkeyone one@host"}, // last is a dup
	}
	ud := userData(cfg)
	for _, want := range []string{
		"ssh_pwauth: true",
		"chpasswd:",
		"expire: false",
		"root:S3cretPass",
		"ssh_authorized_keys:",
		"ssh-ed25519 AAAAkeyone one@host",
		"ssh-rsa AAAAkeytwo two@host",
		"ssh-ed25519 AAAAkeythree three@host",
	} {
		if !strings.Contains(ud, want) {
			t.Errorf("user-data missing %q in:\n%s", want, ud)
		}
	}
	// The duplicate key must appear only once.
	if n := strings.Count(ud, "AAAAkeyone"); n != 1 {
		t.Errorf("expected deduped key to appear once, got %d:\n%s", n, ud)
	}
}

func TestUserDataNoPasswordOrKeys(t *testing.T) {
	ud := userData(Config{InstanceID: "vm-6", Hostname: "x"})
	if strings.Contains(ud, "chpasswd") || strings.Contains(ud, "ssh_authorized_keys") {
		t.Errorf("user-data should omit password/keys when none set:\n%s", ud)
	}
	if strings.Contains(ud, "write_files") {
		t.Errorf("user-data should omit write_files when no recipe set:\n%s", ud)
	}
}

func TestUserDataRecipe(t *testing.T) {
	script := "#!/bin/bash\necho 'hello' > /root/recipe.out\n"
	ud := userData(Config{InstanceID: "vm-7", Hostname: "rec", UserData: script})
	if !strings.Contains(ud, "write_files:") || !strings.Contains(ud, "encoding: b64") {
		t.Fatalf("recipe should be written via base64 write_files:\n%s", ud)
	}
	if !strings.Contains(ud, "/var/lib/cloud/scripts/per-instance/maburvm-recipe.sh") {
		t.Fatalf("recipe should target the per-instance scripts dir:\n%s", ud)
	}
	// The base64 of the (trimmed) script must be present so it's preserved verbatim.
	enc := base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(script)))
	if !strings.Contains(ud, enc) {
		t.Fatalf("recipe content (base64) missing from user-data:\n%s", ud)
	}
}
