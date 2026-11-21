package models

import (
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Where an attached group's rules sit relative to the VM's own rules.
const (
	// FirewallGroupBefore puts the group first: the VM's own rules cannot
	// override it.
	FirewallGroupBefore = "before"
	// FirewallGroupAfter puts the group last: the VM's own rules win.
	FirewallGroupAfter = "after"
)

// FirewallGroup is a named rule set attached to many VMs. UserID nil is a
// group staff manage for everyone; otherwise the customer who owns it.
type FirewallGroup struct {
	ID          string    `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID      *string   `json:"user_id" gorm:"type:uuid"`
	Name        string    `json:"name" gorm:"type:varchar(100);not null"`
	Description string    `json:"description" gorm:"type:text;not null;default:''"`
	CreatedAt   time.Time `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt   time.Time `json:"updated_at" gorm:"not null;default:NOW()"`
}

func (FirewallGroup) TableName() string { return "firewall_groups" }

func (g *FirewallGroup) BeforeCreate(tx *gorm.DB) error {
	if g.ID == "" {
		g.ID = uuid.New().String()
	}
	return nil
}

// Global reports a group staff manage for everyone.
func (g *FirewallGroup) Global() bool { return g.UserID == nil }

// FirewallGroupRule is one rule of a group, with the fields of FirewallRule.
type FirewallGroupRule struct {
	ID        string    `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	GroupID   string    `json:"group_id" gorm:"type:uuid;not null"`
	Protocol  string    `json:"protocol" gorm:"type:varchar(10);not null"`
	PortRange string    `json:"port_range" gorm:"type:varchar(50);not null;default:''"`
	Action    string    `json:"action" gorm:"type:varchar(10);not null"`
	Direction string    `json:"direction" gorm:"type:varchar(10);not null"`
	SourceIP  string    `json:"source_ip" gorm:"type:cidr;default:0.0.0.0/0"`
	Priority  int       `json:"priority" gorm:"not null;default:100"`
	ICMPType  string    `json:"icmp_type" gorm:"column:icmp_type;type:varchar(32);not null;default:''"`
	Log       bool      `json:"log" gorm:"column:log;not null;default:false"`
	Family    string    `json:"family" gorm:"column:family;type:varchar(4);not null;default:''"`
	CreatedAt time.Time `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time `json:"updated_at" gorm:"not null;default:NOW()"`
}

func (FirewallGroupRule) TableName() string { return "firewall_group_rules" }

func (r *FirewallGroupRule) BeforeCreate(tx *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	return nil
}

// AsVMRule is the rule as it is enforced on one VM; the ID stays the group
// rule's, so the node's entries name the rule they came from.
func (r *FirewallGroupRule) AsVMRule(vmID string) FirewallRule {
	return FirewallRule{
		ID: r.ID, VMID: vmID, Protocol: r.Protocol, PortRange: r.PortRange, Action: r.Action,
		Direction: r.Direction, SourceIP: r.SourceIP, Priority: r.Priority, ICMPType: r.ICMPType,
		Log: r.Log, Family: r.Family,
	}
}

// VMFirewallGroup attaches a group to a VM.
type VMFirewallGroup struct {
	VMID      string    `json:"vm_id" gorm:"type:uuid;primaryKey"`
	GroupID   string    `json:"group_id" gorm:"type:uuid;primaryKey"`
	Position  string    `json:"position" gorm:"type:varchar(6);not null;default:'after'"`
	SortOrder int       `json:"sort_order" gorm:"not null;default:0"`
	CreatedAt time.Time `json:"created_at" gorm:"not null;default:NOW()"`
}

func (VMFirewallGroup) TableName() string { return "vm_firewall_groups" }

// AttachedRules is one attached group's rules, as MergeFirewallRules takes them.
type AttachedRules struct {
	Position  string
	SortOrder int
	Rules     []FirewallRule
}

// MergeFirewallRules is the order a VM's rules are enforced in: groups placed
// before its own rules, its own rules, then groups placed after, each group by
// sort order and each set by priority. The node sorts by priority, so the
// result is renumbered in that order (capped at the 1000 the node accepts; it
// keeps equal priorities in the order given).
func MergeFirewallRules(own []FirewallRule, groups []AttachedRules) []FirewallRule {
	if len(groups) == 0 {
		return own
	}
	byPriority := func(rs []FirewallRule) []FirewallRule {
		out := append([]FirewallRule(nil), rs...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].Priority < out[j].Priority })
		return out
	}
	sorted := append([]AttachedRules(nil), groups...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].SortOrder < sorted[j].SortOrder })
	var out []FirewallRule
	side := func(before bool) {
		for _, g := range sorted {
			if (g.Position == FirewallGroupBefore) == before {
				out = append(out, byPriority(g.Rules)...)
			}
		}
	}
	side(true)
	out = append(out, byPriority(own)...)
	side(false)
	for i := range out {
		out[i].Priority = min(i+1, 1000)
	}
	return out
}
