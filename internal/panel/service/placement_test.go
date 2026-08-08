package service

import (
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
)

const gib = int64(1024 * 1024 * 1024)

func TestPoolFits(t *testing.T) {
	tests := []struct {
		name   string
		pool   *models.StoragePool
		diskGB int
		want   bool
		why    string
	}{
		{
			name:   "room for the disk and the reserve",
			pool:   &models.StoragePool{TotalSpace: 900 * gib, AvailableSpace: 214 * gib},
			diskGB: 40,
			want:   true,
		},
		{
			name:   "fits the disk but not the reserve",
			pool:   &models.StoragePool{TotalSpace: 900 * gib, AvailableSpace: 45 * gib},
			diskGB: 40,
			want:   false,
			why:    "a disk is thin on day one and grows; placing it with no headroom fills the pool later, under load",
		},
		{
			name:   "nowhere near",
			pool:   &models.StoragePool{TotalSpace: 900 * gib, AvailableSpace: 2 * gib},
			diskGB: 40,
			want:   false,
		},
		{
			name:   "unknown pool is not a full pool",
			pool:   nil,
			diskGB: 40,
			want:   true,
			why:    "the panel not having synced a node's storage must not stop orders on a healthy node",
		},
		{
			name:   "unmeasured pool is not a full pool",
			pool:   &models.StoragePool{TotalSpace: 0, AvailableSpace: 0},
			diskGB: 40,
			want:   true,
			why:    "zero total means no reading was taken, not that the disk is full",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := PoolFits(tt.pool, tt.diskGB); got != tt.want {
				t.Errorf("PoolFits(%v GB) = %v, want %v. %s", tt.diskGB, got, tt.want, tt.why)
			}
		})
	}
}

// The reserve has to be big enough to act in: delete a volume, take a backup,
// let a thin disk grow. Pin it so nobody quietly trims it to nothing.
func TestHeadroomIsMeaningful(t *testing.T) {
	if diskHeadroomBytes < 10*gib {
		t.Errorf("headroom of %d bytes leaves no room to recover a full pool", diskHeadroomBytes)
	}

	// A pool with exactly the disk's size free must be rejected.
	pool := &models.StoragePool{TotalSpace: 500 * gib, AvailableSpace: 40 * gib}
	if PoolFits(pool, 40) {
		t.Error("a pool with room for the disk and nothing else must not be chosen")
	}
}
