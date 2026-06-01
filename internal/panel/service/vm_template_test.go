package service

import "testing"

func TestIsInstallableImagePath(t *testing.T) {
	cases := map[string]bool{
		"/var/lib/libvirt/templates/ubuntu-24.04.qcow2": true,
		"https://cloud-images.ubuntu.com/noble.img":     true,
		"/imported":     false, // the import placeholder — no real base image
		"":              false,
		"   ":           false,
		"  /imported  ": false,
	}
	for path, want := range cases {
		if got := isInstallableImagePath(path); got != want {
			t.Errorf("isInstallableImagePath(%q) = %v, want %v", path, got, want)
		}
	}
}
