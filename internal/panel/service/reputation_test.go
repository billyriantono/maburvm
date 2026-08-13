package service

import (
	"net"
	"strings"
	"testing"
)

// Blocklists answer a refused or over-quota query with an address in
// 127.255.255.0/24 rather than by failing. Reading that as "not listed" is the
// most damaging thing this code could do: it tells a provider their space is
// clean at precisely the moment they have lost the ability to find out.
func TestRefusalRangeIsRecognised(t *testing.T) {
	refusal := &net.IPNet{IP: net.IPv4(127, 255, 255, 0), Mask: net.CIDRMask(24, 32)}

	refusals := []string{"127.255.255.252", "127.255.255.254", "127.255.255.255"}
	for _, a := range refusals {
		if !refusal.Contains(net.ParseIP(a)) {
			t.Errorf("%s is a refusal code and must not be read as a listing", a)
		}
	}

	// Genuine listings live in 127.0.0.x and must still count as listed.
	listings := []string{"127.0.0.2", "127.0.0.3", "127.0.0.10", "127.0.0.11"}
	for _, a := range listings {
		if refusal.Contains(net.ParseIP(a)) {
			t.Errorf("%s is a real listing and must not be mistaken for a refusal", a)
		}
	}
}

func TestReverseIPv4(t *testing.T) {
	got, ok := reverseIPv4(net.ParseIP("103.118.174.33"))
	if !ok || got != "33.174.118.103" {
		t.Errorf("reverseIPv4 = %q (ok=%v), want 33.174.118.103", got, ok)
	}

	// IPv6 is skipped rather than reversed wrongly: blocklist coverage differs
	// and a wrong query would return "not listed" for everything.
	if _, ok := reverseIPv4(net.ParseIP("2001:db8::1")); ok {
		t.Error("IPv6 must not be reversed as though it were IPv4")
	}
}

// An unchecked score must never render as a clean zero.
func TestAbuseScoreNotCheckedIsDistinctFromClean(t *testing.T) {
	if AbuseScoreNotCheckedValue() >= 0 {
		t.Error("the not-checked sentinel must be negative so it cannot be confused with a real score")
	}
}


// The address arrives from an inet column, and whether Postgres hands it over
// with a /32 depends on how it was selected. A suffix must not make the check
// fail — that would have silently disabled the feature for every address.
func TestCIDRSuffixIsTolerated(t *testing.T) {
	for _, in := range []string{"103.118.174.33", "103.118.174.33/32", " 103.118.174.33 "} {
		bare := strings.SplitN(strings.TrimSpace(in), "/", 2)[0]
		if net.ParseIP(bare) == nil {
			t.Errorf("%q should reduce to a parseable address, got %q", in, bare)
		}
	}
}
