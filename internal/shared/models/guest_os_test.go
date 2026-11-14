package models

import "testing"

func TestGuestImageRef(t *testing.T) {
	url := "https://download.example/routeros/7.24.6/chr-7.24.6.img.zip"
	for _, c := range []struct{ osType, path, ref string }{
		{OSTypeLinux, "https://cloud.example/debian-13.qcow2", "https://cloud.example/debian-13.qcow2"},
		{"", "/var/lib/images/x.qcow2", "/var/lib/images/x.qcow2"},
		{OSTypeRouterOS, url, "guest:routeros:" + url},
		{OSTypeRouterOS, "vm://1b4e28ba-2fa1-11d2-883f-0016d3cca427", "guest:routeros:vm://1b4e28ba-2fa1-11d2-883f-0016d3cca427"},
	} {
		ref := GuestImageRef(c.osType, c.path)
		if ref != c.ref {
			t.Errorf("GuestImageRef(%q, %q) = %q, want %q", c.osType, c.path, ref, c.ref)
		}
		wantType := c.osType
		if wantType == "" {
			wantType = OSTypeLinux
		}
		if gotType, gotPath := SplitGuestImageRef(ref); gotType != wantType || gotPath != c.path {
			t.Errorf("SplitGuestImageRef(%q) = %q, %q", ref, gotType, gotPath)
		}
	}
	// References an older panel sends, and sources that carry colons of their
	// own, are Linux and unchanged.
	for _, ref := range []string{"s3://images/abc", "vm://10.0.0.1/abc", "guest:", "guest::x"} {
		if gotType, gotPath := SplitGuestImageRef(ref); gotType != OSTypeLinux || gotPath != ref {
			t.Errorf("SplitGuestImageRef(%q) = %q, %q", ref, gotType, gotPath)
		}
	}
}
