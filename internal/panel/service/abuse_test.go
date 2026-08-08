package service

import (
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
)

func TestConnectionRate(t *testing.T) {
	tests := []struct {
		name              string
		prev, current     int64
		elapsed           float64
		want              float64
		whyItMattersIfNot string
	}{
		{
			name: "steady counter over a known interval",
			prev: 1_000, current: 4_000, elapsed: 60,
			want: 50,
		},
		{
			name: "counter reset by an agent restart reads as no sample",
			prev: 9_000_000, current: 12, elapsed: 60,
			want:              0,
			whyItMattersIfNot: "a negative delta would render as a huge or negative rate",
		},
		{
			name: "no elapsed time reads as no sample",
			prev: 1_000, current: 4_000, elapsed: 0,
			want:              0,
			whyItMattersIfNot: "dividing by zero would produce +Inf and flag every guest",
		},
		{
			name: "an idle guest rates zero, not unknown",
			prev: 500, current: 500, elapsed: 30,
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := connectionRate(tt.prev, tt.current, tt.elapsed)
			if got != tt.want {
				t.Errorf("connectionRate(%d, %d, %v) = %v, want %v. %s",
					tt.prev, tt.current, tt.elapsed, got, tt.want, tt.whyItMattersIfNot)
			}
		})
	}
}

// The threshold is what decides whether a paying customer gets flagged, so pin
// the boundary rather than leaving it to whichever comparison someone writes
// next.
func TestFlaggedAtThreshold(t *testing.T) {
	below := models.GuestConnection{SYNRate: models.AbuseSYNRateThreshold - 0.1}
	at := models.GuestConnection{SYNRate: models.AbuseSYNRateThreshold}
	above := models.GuestConnection{SYNRate: models.AbuseSYNRateThreshold + 1}

	if below.Flagged() {
		t.Error("a guest just under the threshold must not be flagged")
	}
	if !at.Flagged() {
		t.Error("the threshold itself is the rate the node already drops, so it counts as flagged")
	}
	if !above.Flagged() {
		t.Error("a guest over the threshold must be flagged")
	}
}
