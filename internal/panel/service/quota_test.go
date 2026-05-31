package service

import (
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
)

func TestEvaluateQuota(t *testing.T) {
	add := models.Resources{CPU: 2, RAM: 2048, Disk: 40}

	tests := []struct {
		name    string
		quota   models.UserQuota
		used    QuotaUsage
		wantErr bool
	}{
		{
			name:  "all unlimited (zeros) allows anything",
			quota: models.UserQuota{},
			used:  QuotaUsage{VMs: 100, VCPU: 999, RAMMB: 9_999_999, DiskGB: 99999},
		},
		{
			name:  "well under every limit",
			quota: models.UserQuota{MaxVMs: 10, MaxVCPU: 32, MaxRAMMB: 65536, MaxDiskGB: 1000},
			used:  QuotaUsage{VMs: 1, VCPU: 4, RAMMB: 4096, DiskGB: 80},
		},
		{
			name:    "VM count limit reached",
			quota:   models.UserQuota{MaxVMs: 2},
			used:    QuotaUsage{VMs: 2},
			wantErr: true,
		},
		{
			name:  "exactly at VM count limit boundary is allowed",
			quota: models.UserQuota{MaxVMs: 3},
			used:  QuotaUsage{VMs: 2}, // 2+1 == 3, ok
		},
		{
			name:    "vCPU would be exceeded",
			quota:   models.UserQuota{MaxVCPU: 5},
			used:    QuotaUsage{VCPU: 4}, // 4+2 = 6 > 5
			wantErr: true,
		},
		{
			name:    "RAM would be exceeded",
			quota:   models.UserQuota{MaxRAMMB: 3000},
			used:    QuotaUsage{RAMMB: 2000}, // 2000+2048 > 3000
			wantErr: true,
		},
		{
			name:    "disk would be exceeded",
			quota:   models.UserQuota{MaxDiskGB: 100},
			used:    QuotaUsage{DiskGB: 70}, // 70+40 > 100
			wantErr: true,
		},
		{
			name:  "one dimension limited, others unlimited, within limit",
			quota: models.UserQuota{MaxVCPU: 8},
			used:  QuotaUsage{VMs: 50, VCPU: 4, RAMMB: 1 << 20, DiskGB: 9999},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := evaluateQuota(&tc.quota, tc.used, add)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrQuotaExceeded)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
