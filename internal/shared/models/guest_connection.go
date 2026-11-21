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

	// New inbound connections and packets each way, per second, with the
	// cumulative counters they were differenced from. Zero from agents that do
	// not report them.
	SYNInTotal      int64   `json:"syn_in_total" gorm:"column:syn_in_total;not null;default:0"`
	SYNInRate       float64 `json:"syn_in_rate" gorm:"column:syn_in_rate;not null;default:0"`
	PacketsOutTotal int64   `json:"packets_out_total" gorm:"column:packets_out_total;not null;default:0"`
	PacketsInTotal  int64   `json:"packets_in_total" gorm:"column:packets_in_total;not null;default:0"`
	PPSOut          float64 `json:"pps_out" gorm:"column:pps_out;not null;default:0"`
	PPSIn           float64 `json:"pps_in" gorm:"column:pps_in;not null;default:0"`

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

// What an abuse event is about: the reading a guest went over, or a staff
// action taken without one.
const (
	AbuseOutboundSYN = "outbound_syn"
	AbuseOutboundPPS = "outbound_pps"
	AbuseInboundSYN  = "inbound_syn"
	AbuseInboundPPS  = "inbound_pps"
	AbuseManual      = "manual"
)

// AbuseKinds are the readings watched, in the order they are evaluated.
var AbuseKinds = []string{AbuseOutboundSYN, AbuseOutboundPPS, AbuseInboundSYN, AbuseInboundPPS}

// What was done about an abuse event.
const (
	AbuseActionAlert      = "alert"
	AbuseActionRateLimit  = "rate_limit"
	AbuseActionQuarantine = "quarantine"
	AbuseActionNullRoute  = "null_route"
)

// Where the action of an abuse event stands.
const (
	AbuseStateNone     = "none"
	AbuseStateActive   = "active"
	AbuseStateReleased = "released"
	AbuseStateExpired  = "expired"
	AbuseStateFailed   = "failed"
)

// AbuseSample is one reading kept as evidence: the rates and the cumulative
// counters they came from.
type AbuseSample struct {
	At         time.Time `json:"at"`
	SYNOut     float64   `json:"syn_out"`
	SYNIn      float64   `json:"syn_in"`
	PPSOut     float64   `json:"pps_out"`
	PPSIn      float64   `json:"pps_in"`
	SYNTotal   int64     `json:"syn_total"`
	SYNInTotal int64     `json:"syn_in_total"`
	PacketsOut int64     `json:"packets_out_total"`
	PacketsIn  int64     `json:"packets_in_total"`
}

// AbuseEvent is one episode of a guest over a threshold, from the first
// reading over it until it is back under, with what was done about it.
type AbuseEvent struct {
	ID         string  `json:"id" gorm:"column:id;primaryKey"`
	NodeID     string  `json:"node_id" gorm:"column:node_id"`
	MAC        string  `json:"mac" gorm:"column:mac"`
	VMID       string  `json:"vm_id" gorm:"column:vm_id"`
	VMHostname string  `json:"vm_hostname" gorm:"column:vm_hostname"`
	Kind       string  `json:"kind" gorm:"column:kind"`
	Metric     float64 `json:"metric" gorm:"column:metric"`
	Peak       float64 `json:"peak" gorm:"column:peak"`
	Threshold  float64 `json:"threshold" gorm:"column:threshold"`
	// Samples is how many readings in a row were over the threshold.
	Samples     int    `json:"samples" gorm:"column:samples"`
	Action      string `json:"action" gorm:"column:action"`
	ActionState string `json:"action_state" gorm:"column:action_state"`
	// Targets are the addresses a null-route covers, comma separated.
	Targets    string        `json:"targets" gorm:"column:targets"`
	Detail     string        `json:"detail" gorm:"column:detail"`
	Evidence   []AbuseSample `json:"evidence" gorm:"column:evidence;serializer:json"`
	StartedAt  time.Time     `json:"started_at" gorm:"column:started_at"`
	LastSeenAt time.Time     `json:"last_seen_at" gorm:"column:last_seen_at"`
	EndedAt    *time.Time    `json:"ended_at" gorm:"column:ended_at"`
	ExpiresAt  *time.Time    `json:"expires_at" gorm:"column:expires_at"`
	ReleasedAt *time.Time    `json:"released_at" gorm:"column:released_at"`
	ReleasedBy string        `json:"released_by" gorm:"column:released_by"`
	// NodeName comes from a join.
	NodeName string `json:"node_name" gorm:"column:node_name;->"`
}

func (AbuseEvent) TableName() string { return "abuse_events" }
