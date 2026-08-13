package health

import "testing"

// The first sample after a restart used to subtract zero from a counter that had
// been accumulating since the node booted, publishing the node's entire lifetime
// traffic as an instantaneous rate. One such sample was recorded on a live node
// at 4.6e17 bytes/sec — 460 exabytes per second — and because a chart scales to
// its peak, that single point flattened every real measurement to the axis.
func TestCounterRate(t *testing.T) {
	tests := []struct {
		name              string
		previous, current uint64
		havePrevious      bool
		interval          float64
		want              int64
		why               string
	}{
		{
			name: "normal delta",
			previous: 1_000, current: 11_000, havePrevious: true, interval: 10,
			want: 1_000,
		},
		{
			name: "no previous reading yields no rate",
			previous: 0, current: 4_600_000_000_000_000, havePrevious: false, interval: 10,
			want: 0,
			why:  "a lifetime counter is not a rate, and reporting it as one poisons every chart drawn from the series",
		},
		{
			name: "counter reset does not wrap",
			previous: 9_000_000, current: 12, havePrevious: true, interval: 10,
			want: 0,
			why:  "these are unsigned; the subtraction would wrap to something astronomical rather than go negative",
		},
		{
			name: "zero interval yields no rate",
			previous: 1_000, current: 2_000, havePrevious: true, interval: 0,
			want: 0,
		},
		{
			name: "idle counter is a real zero",
			previous: 5_000, current: 5_000, havePrevious: true, interval: 10,
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := counterRate(tt.previous, tt.current, tt.havePrevious, tt.interval)
			if got != tt.want {
				t.Errorf("counterRate(%d, %d, %v, %v) = %d, want %d. %s",
					tt.previous, tt.current, tt.havePrevious, tt.interval, got, tt.want, tt.why)
			}
		})
	}
}

// No plausible interval can produce a rate beyond the hardware, so a sane
// delta must never come out absurd.
func TestCounterRateStaysPlausible(t *testing.T) {
	// 10 GbE fully saturated for a 30-second interval.
	const tenGigE = uint64(1_250_000_000)
	got := counterRate(0, tenGigE*30, true, 30)
	if got != int64(tenGigE) {
		t.Errorf("a saturated 10GbE link should read %d B/s, got %d", tenGigE, got)
	}
}
