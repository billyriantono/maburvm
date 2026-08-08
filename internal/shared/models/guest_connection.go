package models

import "time"

// AbuseSYNRateThreshold is the rate, in new outbound connections per second,
// above which a guest is flagged for an operator to look at.
//
// It matches the agent's own rate limit deliberately: a guest flagged here is
// one whose traffic the node is already dropping, so the panel never reports a
// problem the node is not actually acting on. A real workload sits far below it
// — the guests that triggered this feature ran at roughly 3,000/s.
const AbuseSYNRateThreshold = 50.0

// GuestConnection is how fast one guest NIC on one node is opening new outbound
// connections, and whether it has been cut off.
//
// Keyed on MAC rather than VM id: the guests worth catching are frequently ones
// the panel does not manage, and an abusive guest may be running a spoofed or
// duplicated address, so the MAC is the only identifier that reliably picks out
// a single machine.
type GuestConnection struct {
	ID     uint64 `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	NodeID string `json:"node_id" gorm:"column:node_id;type:uuid;not null;index"`
	MAC    string `json:"mac" gorm:"column:mac;type:varchar(17);not null"`

	// VMID and VMHostname are empty for guests the panel does not manage. That is
	// not an error — it is the case most worth showing, since nothing else in the
	// panel would reveal such a guest at all.
	VMID          string `json:"vm_id" gorm:"column:vm_id;type:varchar(64);not null;default:''"`
	VMHostname    string `json:"vm_hostname" gorm:"column:vm_hostname;type:varchar(255);not null;default:''"`
	InterfaceName string `json:"interface_name" gorm:"column:interface_name;type:varchar(32);not null;default:''"`

	// SYNTotal is the cumulative counter last read from the node, kept only so
	// the next sample can be differenced against it.
	SYNTotal int64   `json:"syn_total" gorm:"column:syn_total;not null;default:0"`
	SYNRate  float64 `json:"syn_rate" gorm:"column:syn_rate;not null;default:0"`
	PeakRate float64 `json:"peak_rate" gorm:"column:peak_rate;not null;default:0"`

	Quarantined      bool   `json:"quarantined" gorm:"column:quarantined;not null;default:false"`
	QuarantineReason string `json:"quarantine_reason" gorm:"column:quarantine_reason;not null;default:''"`

	// FirstFlaggedAt is set when the guest first exceeds the threshold and
	// cleared when it settles, which is what makes "how long has this gone on"
	// answerable without storing a time series for every quiet guest.
	FirstFlaggedAt *time.Time `json:"first_flagged_at" gorm:"column:first_flagged_at"`
	LastSeenAt     time.Time  `json:"last_seen_at" gorm:"column:last_seen_at;not null"`

	// NodeName comes from a join, not from this table. It must be read-only
	// ("->") rather than ignored ("-"): ignored fields are skipped when scanning
	// too, so the joined value silently never arrives and every row renders
	// without a node.
	NodeName string `json:"node_name" gorm:"column:node_name;->"`
}

func (GuestConnection) TableName() string { return "guest_connections" }

// Flagged reports whether this guest is currently misbehaving.
func (g GuestConnection) Flagged() bool { return g.SYNRate >= AbuseSYNRateThreshold }
