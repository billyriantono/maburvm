package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

func TestDownsampleVM(t *testing.T) {
	since := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	window := 100 * time.Minute

	// One sample a minute for 100 minutes, with minutes 40-59 missing (VM off).
	var rows []models.VMMetricSample
	for i := 0; i < 100; i++ {
		if i >= 40 && i < 60 {
			continue
		}
		rows = append(rows, models.VMMetricSample{
			ID: uint64(i + 1), VMID: "vm", CPUUsage: float64(i), NetworkRxBytesPerSec: int64(i * 10),
			RecordedAt: since.Add(time.Duration(i) * time.Minute),
		})
	}

	// Fewer rows than points: returned untouched.
	require.Len(t, downsampleVM(rows, since, window, 500), len(rows))

	// 10 slices of 10 minutes; the two slices covering the gap are absent.
	got := downsampleVM(rows, since, window, 10)
	require.Len(t, got, 8)
	require.InDelta(t, 4.5, got[0].CPUUsage, 0.001) // mean of 0..9
	require.Equal(t, int64(45), got[0].NetworkRxBytesPerSec)
	require.Equal(t, since.Add(9*time.Minute), got[0].RecordedAt)
	require.InDelta(t, 34.5, got[3].CPUUsage, 0.001) // 30..39, last before the gap
	require.InDelta(t, 64.5, got[4].CPUUsage, 0.001) // 60..69, first after it
	require.True(t, got[4].RecordedAt.Sub(got[3].RecordedAt) > 20*time.Minute)
	for i := 1; i < len(got); i++ {
		require.True(t, got[i].RecordedAt.After(got[i-1].RecordedAt))
	}
}
