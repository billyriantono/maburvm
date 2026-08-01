package models

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQuotaModeConstants(t *testing.T) {
	assert.Equal(t, QuotaMode("legacy"), QuotaModeLegacy)
	assert.Equal(t, QuotaMode("managed"), QuotaModeManaged)
}

func TestUserQuotaIsManaged(t *testing.T) {
	legacy := UserQuota{QuotaMode: QuotaModeLegacy}
	assert.False(t, legacy.IsManaged())
	assert.False(t, legacy.HasPolicyProvenance())

	managed := UserQuota{QuotaMode: QuotaModeManaged}
	assert.True(t, managed.IsManaged())

	withProvenance := UserQuota{
		QuotaMode:     QuotaModeManaged,
		PolicyID:      strPtr("p1"),
		PolicyVersion: intPtr(3),
	}
	assert.True(t, withProvenance.IsManaged())
	assert.True(t, withProvenance.HasPolicyProvenance())

	managedNoProvenance := UserQuota{QuotaMode: QuotaModeManaged}
	assert.True(t, managedNoProvenance.IsManaged())
	assert.False(t, managedNoProvenance.HasPolicyProvenance())
}

func TestUserQuotaTableName(t *testing.T) {
	assert.Equal(t, "user_quotas", UserQuota{}.TableName())
}

func TestQuotaPolicyTableName(t *testing.T) {
	assert.Equal(t, "quota_policies", QuotaPolicy{}.TableName())
}

func TestQuotaPolicyVersionTableName(t *testing.T) {
	assert.Equal(t, "quota_policy_versions", QuotaPolicyVersion{}.TableName())
}

func TestQuotaPolicyLifecycleConstants(t *testing.T) {
	assert.Equal(t, QuotaPolicyLifecycle("active"), QuotaPolicyActive)
	assert.Equal(t, QuotaPolicyLifecycle("deprecated"), QuotaPolicyDeprecated)
}

// Ensure immutable versions carry strictly positive limits in the model contract
// (the DB also enforces this via CHECK constraints in migration 033).
func TestQuotaPolicyVersionLimitsMustBePositive(t *testing.T) {
	v := QuotaPolicyVersion{Version: 1, MaxVMs: 0, MaxVCPU: 1, MaxRAMMB: 1, MaxDiskGB: 1}
	assert.False(t, v.MaxVMs > 0, "max_vms must be strictly positive")
	assert.True(t, v.MaxVCPU > 0)
	assert.True(t, v.MaxRAMMB > 0)
	assert.True(t, v.MaxDiskGB > 0)
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
