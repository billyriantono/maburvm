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
	// DefaultPortForwardsPerVM: each forward takes a port on the node's one
	// public address away from every other VM on that node.
	DefaultPortForwardsPerVM = 20
	// DefaultFirewallRulesPerVM: every VM's rules share one chain on its node,
	// so each rule slows new connections for every tenant there a little.
	DefaultFirewallRulesPerVM = 100

	// What a new customer account may hold before an operator says otherwise:
	// enough for a first server, not an open tab on the nodes.
	DefaultNewClientMaxVMs    = 2
	DefaultNewClientMaxVCPU   = 4
	DefaultNewClientMaxRAMMB  = 8192
	DefaultNewClientMaxDiskGB = 100

	// DefaultRAMOvercommitRatio is 0, meaning "no node RAM limit at all" —
	// which is what the panel did before overcommit existed. Defaulting to a
	// real ratio would start rejecting provisioning that works today the
	// moment this ships, so RAM overcommit is opt-in.
	DefaultRAMOvercommitRatio = 0.0
	// DefaultDiskAllocationRatio is 0, meaning "no cap on allocated disk":
	// live pools already carry more virtual disk than they have bytes, so any
	// real default would refuse orders that work today. The free-space floor
	// in PoolFits applies whatever this is.
	DefaultDiskAllocationRatio = 0.0
)

// quotaSettings is the shape stored under the 'quotas' section. The JSON names
// match what the admin page sends.
type quotaSettings struct {
	VPCMaxPerUser        *int `json:"vpcMaxPerUser"`
	FloatingIPMaxPerUser *int `json:"floatingIpMaxPerUser"`
	PortForwardMaxPerVM  *int `json:"portForwardMaxPerVm"`
	FirewallRuleMaxPerVM *int `json:"firewallRuleMaxPerVm"`
	// What customers may do on their own. Unset = allowed.
	ClientOrderingEnabled   *bool `json:"clientOrderingEnabled"`
	ClientCloneEnabled      *bool `json:"clientCloneEnabled"`
	ClientExtraDisksEnabled *bool `json:"clientExtraDisksEnabled"`
	// Whether a customer may delete a VM a billing service pays for. Unset = no.
	ClientDeleteBilledEnabled *bool `json:"clientDeleteBilledEnabled"`
	// The quota a new customer account starts with. 0 = no limit.
	NewClientMaxVMs    *int `json:"newClientMaxVms"`
	NewClientMaxVCPU   *int `json:"newClientMaxVcpu"`
	NewClientMaxRAMMB  *int `json:"newClientMaxRamMb"`
	NewClientMaxDiskGB *int `json:"newClientMaxDiskGb"`
	// Overcommit ratios are node-capacity limits rather than per-account ones,
	// but they live in the same section so the admin page keeps a single
	// "limits" form instead of growing a tab for two numbers.
	RAMOvercommitRatio *float64 `json:"ramOvercommitRatio"`
	// Stored under its own key: the older diskOvercommitRatio multiplied free
	// space and was saved as 1 by the form's default, which under this meaning
	// would refuse every order on a pool already past 1.0x.
	DiskAllocationRatio *float64 `json:"diskAllocationRatio"`
	// How many vCPUs a node may carry per logical CPU. A node's own ratio wins.
	VCPUAllocationRatio *float64 `json:"vcpuAllocationRatio"`
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
		Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", SettingsSectionQuotas).
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

// PortForwardsPerVM is how many port forwards one VM may hold.
func PortForwardsPerVM(ctx context.Context, db *gorm.DB) int {
	if v := loadQuotaSettings(ctx, db).PortForwardMaxPerVM; v != nil && *v > 0 {
		return *v
	}
	return DefaultPortForwardsPerVM
}

// FirewallRulesPerVM is how many firewall rules one VM may hold.
func FirewallRulesPerVM(ctx context.Context, db *gorm.DB) int {
	if v := loadQuotaSettings(ctx, db).FirewallRuleMaxPerVM; v != nil && *v > 0 {
		return *v
	}
	return DefaultFirewallRulesPerVM
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

// DiskAllocationRatio is how many times a storage pool's capacity may be
// promised to VM disks. qcow2 images are thin, so a pool can carry more
// allocated disk than it has bytes, but only until the guests write: a pool that
// fills pauses every VM on it. 0 disables the cap, which is the default.
func DiskAllocationRatio(ctx context.Context, db *gorm.DB) float64 {
	if v := loadQuotaSettings(ctx, db).DiskAllocationRatio; v != nil && *v > 0 {
		return *v
	}
	return DefaultDiskAllocationRatio
}

// VCPUAllocationRatio is how many vCPUs a node may carry per logical CPU: the
// node's own ratio when it has one, else the panel-wide one. 0 is no cap, the
// default, so nodes already carrying many vCPUs keep taking orders.
func VCPUAllocationRatio(ctx context.Context, db *gorm.DB, nodeRatio float64) float64 {
	if nodeRatio > 0 {
		return nodeRatio
	}
	if v := loadQuotaSettings(ctx, db).VCPUAllocationRatio; v != nil && *v > 0 {
		return *v
	}
	return 0
}
