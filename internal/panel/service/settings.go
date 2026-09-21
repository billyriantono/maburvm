package service

import (
	"context"
	"encoding/json"

	"gorm.io/gorm"
)

// System settings live as one JSON row per section in system_settings, edited by
// an administrator under Settings → System and read at runtime. Values that an
// operator may reasonably want to change — quotas, limits, integration URLs —
// belong here rather than in the environment: changing an env var means shell
// access and a container restart, which is the wrong bar for "let this customer
// have one more network".

// SettingsSectionQuotas holds per-account limits.
const SettingsSectionQuotas = "quotas"

// Built-in defaults, used when an administrator has not set a value. They are
// deliberately modest: each VPC costs a network namespace, three veth pairs and
// a bridge on the node, and each floating IP consumes a scarce public address.
const (
	DefaultVPCsPerUser        = 5
	DefaultFloatingIPsPerUser = 3

	// DefaultRAMOvercommitRatio is 0, meaning "no node RAM limit at all" —
	// which is what the panel did before overcommit existed. Defaulting to a
	// real ratio would start rejecting provisioning that works today the
	// moment this ships, so RAM overcommit is opt-in.
	DefaultRAMOvercommitRatio = 0.0
	// DefaultDiskOvercommitRatio is 1.0: admit against the pool's real free
	// space, exactly as PoolFits did before. Raising it trades safety for
	// density, betting that thin-provisioned guests will not all fill up.
	DefaultDiskOvercommitRatio = 1.0
)

// quotaSettings is the shape stored under the 'quotas' section. The JSON names
// match what the admin page sends.
type quotaSettings struct {
	VPCMaxPerUser        *int `json:"vpcMaxPerUser"`
	FloatingIPMaxPerUser *int `json:"floatingIpMaxPerUser"`
	// Overcommit ratios are node-capacity limits rather than per-account ones,
	// but they live in the same section so the admin page keeps a single
	// "limits" form instead of growing a tab for two numbers.
	RAMOvercommitRatio  *float64 `json:"ramOvercommitRatio"`
	DiskOvercommitRatio *float64 `json:"diskOvercommitRatio"`
}

// loadQuotaSettings reads the admin-managed limits. A missing row, unset field
// or unparsable value all fall back to the built-in default rather than to zero,
// which would otherwise lock every customer out of the feature.
func loadQuotaSettings(ctx context.Context, db *gorm.DB) quotaSettings {
	var out quotaSettings
	if db == nil {
		return out
	}
	var raw string
	if err := db.WithContext(ctx).
		Raw("SELECT data::text FROM system_settings WHERE section = ?", SettingsSectionQuotas).
		Scan(&raw).Error; err != nil || raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// VPCsPerUser is how many private networks one account may hold.
func VPCsPerUser(ctx context.Context, db *gorm.DB) int {
	if v := loadQuotaSettings(ctx, db).VPCMaxPerUser; v != nil && *v > 0 {
		return *v
	}
	return DefaultVPCsPerUser
}

// FloatingIPsPerUser is how many floating IPs one account may order.
func FloatingIPsPerUser(ctx context.Context, db *gorm.DB) int {
	if v := loadQuotaSettings(ctx, db).FloatingIPMaxPerUser; v != nil && *v > 0 {
		return *v
	}
	return DefaultFloatingIPsPerUser
}

// RAMOvercommitRatio is how many times a node's physical RAM may be allocated
// to VMs. 0 disables the check entirely (unlimited), which is the default —
// overselling RAM is normal in hosting, and guests rarely touch all of it.
//
// It is worth being deliberate when enabling this: unlike disk, exhausting RAM
// has no graceful failure. The host OOM killer picks a QEMU process and kills
// it, which is an abrupt hard-off for somebody's VM.
func RAMOvercommitRatio(ctx context.Context, db *gorm.DB) float64 {
	if v := loadQuotaSettings(ctx, db).RAMOvercommitRatio; v != nil && *v > 0 {
		return *v
	}
	return DefaultRAMOvercommitRatio
}

// DiskOvercommitRatio multiplies a storage pool's free space when deciding
// whether a new or grown disk fits. qcow2 images are thin-provisioned, so a
// pool can safely carry far more allocated disk than it has bytes — but only
// until the guests actually write. 1.0 keeps admission pinned to real free
// space.
func DiskOvercommitRatio(ctx context.Context, db *gorm.DB) float64 {
	if v := loadQuotaSettings(ctx, db).DiskOvercommitRatio; v != nil && *v > 0 {
		return *v
	}
	return DefaultDiskOvercommitRatio
}
