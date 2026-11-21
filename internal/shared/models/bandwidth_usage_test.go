package models

import (
	"testing"
	"time"
)

func TestTransferPeriod(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		now        string
		day        int
		start, end string
	}{
		{"2026-11-21T10:00:00Z", 1, "2026-11-01T00:00:00Z", "2026-11-30T23:59:59Z"},
		{"2026-11-21T10:00:00Z", 0, "2026-11-01T00:00:00Z", "2026-11-30T23:59:59Z"},
		{"2026-11-21T10:00:00Z", 15, "2026-11-15T00:00:00Z", "2026-12-14T23:59:59Z"},
		{"2026-11-14T23:59:59Z", 15, "2026-10-15T00:00:00Z", "2026-11-14T23:59:59Z"},
		{"2026-01-10T00:00:00Z", 28, "2025-12-28T00:00:00Z", "2026-01-27T23:59:59Z"},
		{"2026-11-15T00:00:00Z", 15, "2026-11-15T00:00:00Z", "2026-12-14T23:59:59Z"},
		// A time in another zone is placed by its UTC instant.
		{"2026-11-15T03:00:00+07:00", 15, "2026-10-15T00:00:00Z", "2026-11-14T23:59:59Z"},
	} {
		start, end := TransferPeriod(at(tc.now), tc.day)
		if !start.Equal(at(tc.start)) || !end.Equal(at(tc.end)) {
			t.Errorf("TransferPeriod(%s, %d) = %s .. %s, want %s .. %s", tc.now, tc.day, start, end, tc.start, tc.end)
		}
	}
}

func TestCountedBytes(t *testing.T) {
	if CountedBytes(TransferCountBoth, 3, 4) != 7 || CountedBytes(TransferCountOut, 3, 4) != 4 ||
		CountedBytes(TransferCountIn, 3, 4) != 3 || CountedBytes("", 3, 4) != 7 {
		t.Fatal("wrong direction counted")
	}
}
