package netrules

import (
	"strings"
	"testing"
	"time"
)

func TestNullRouteFileRoundTrip(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	routes := []NullRoute{
		{IP: "203.0.113.9", Until: now.Add(30 * time.Minute), Reason: "inbound flood\n# forged line"},
		{IP: "2001:db8::5", Until: now.Add(time.Hour)},
	}
	content := FormatNullRoutes(routes) +
		"198.51.100.1 1799999000   # expired an hour ago\n" +
		"not-an-ip 1900000000\n" +
		"127.0.0.1 1900000000\n" +
		"203.0.113.9 " + "1800003600" + "   # extended by hand\n"

	got, expired := ParseNullRoutes(content, now)
	if expired != 1 {
		t.Fatalf("expired = %d, want 1", expired)
	}
	if len(got) != 2 {
		t.Fatalf("got %d routes, want 2: %+v", len(got), got)
	}
	byIP := map[string]NullRoute{}
	for _, r := range got {
		byIP[r.IP] = r
	}
	if r := byIP["203.0.113.9"]; r.Until.Unix() != 1800003600 || r.Reason != "extended by hand" {
		t.Errorf("later line must win, got %+v", r)
	}
	if _, ok := byIP["2001:db8::5"]; !ok {
		t.Errorf("IPv6 entry lost: %+v", got)
	}
	if strings.Count(FormatNullRoutes(routes), "\n") != 4 {
		t.Errorf("a newline in a reason must not add a line:\n%s", FormatNullRoutes(routes))
	}
}

func TestNullRouteIP(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		v6   bool
		ok   bool
	}{
		{"203.0.113.9", "203.0.113.9", false, true},
		{" 2001:DB8::1 ", "2001:db8::1", true, true},
		{"::ffff:203.0.113.9", "203.0.113.9", false, true},
		{"0.0.0.0", "", false, false},
		{"fe80::1", "", false, false},
		{"203.0.113.0/24", "", false, false},
	} {
		got, v6, err := NullRouteIP(tc.in)
		if (err == nil) != tc.ok || got != tc.want || v6 != tc.v6 {
			t.Errorf("NullRouteIP(%q) = %q, %v, %v", tc.in, got, v6, err)
		}
	}
}

func TestSetNullRoute(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	list := SetNullRoute(nil, "203.0.113.9", now.Add(time.Minute), "a", true)
	list = SetNullRoute(list, "203.0.113.9", now.Add(time.Hour), "b", true)
	if len(list) != 1 || list[0].Reason != "b" {
		t.Fatalf("re-adding must replace, got %+v", list)
	}
	if list = SetNullRoute(list, "203.0.113.9", time.Time{}, "", false); len(list) != 0 {
		t.Fatalf("lift must remove, got %+v", list)
	}
}
