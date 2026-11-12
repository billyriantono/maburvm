package service

import (
	"context"
	"os"
	"testing"
	"time"
)

// Needs the network, so it only runs when asked:
//
//	REPUTATION_LIVE=1 go test ./internal/panel/service/ -run TestBlocklistLive -v
//
// 127.0.0.2 is the address every DNS blocklist lists for testing and 127.0.0.1
// is the one none do. If the first is not "listed", lookups are being refused
// or lost, and a real check would report a burned address as clean.
func TestBlocklistLive(t *testing.T) {
	if os.Getenv("REPUTATION_LIVE") == "" {
		t.Skip("set REPUTATION_LIVE=1 to query the real blocklists")
	}
	s := NewReputationService(nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, zone := range DefaultBlocklists {
		t0 := time.Now()
		servers := s.zoneNameservers(ctx, zone)
		t.Logf("%s: %d nameservers found in %s", zone, len(servers), time.Since(t0).Round(time.Millisecond))
		t0 = time.Now()
		listed, err := s.checkBlocklist(ctx, "2.0.0.127", zone)
		t.Logf("%s: listed lookup took %s", zone, time.Since(t0).Round(time.Millisecond))
		if err != nil || !listed {
			t.Errorf("%s: test address should be listed, got listed=%v err=%v", zone, listed, err)
		}
		t0 = time.Now()
		listed, err = s.checkBlocklist(ctx, "1.0.0.127", zone)
		t.Logf("%s: not-listed lookup took %s", zone, time.Since(t0).Round(time.Millisecond))
		if err != nil || listed {
			t.Errorf("%s: 127.0.0.1 should not be listed, got listed=%v err=%v", zone, listed, err)
		}
	}
}
