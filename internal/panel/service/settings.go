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
)

// quotaSettings is the shape stored under the 'quotas' section. The JSON names
// match what the admin page sends.
type quotaSettings struct {
	VPCMaxPerUser        *int `json:"vpcMaxPerUser"`
	FloatingIPMaxPerUser *int `json:"floatingIpMaxPerUser"`
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
