package models

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// What a VM's firewall does with inbound traffic that no rule matched, once
// the VM has at least one rule. A VM with no rules is not filtered at all.
const (
	FirewallDefaultDrop   = "drop"
	FirewallDefaultAccept = "accept"
)

// Address families a firewall rule can be limited to; empty means both.
const (
	FirewallFamilyV4 = "v4"
	FirewallFamilyV6 = "v6"
)

// FirewallRule represents a firewall rule for a VM
type FirewallRule struct {
	ID        string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	VMID      string `json:"vm_id" gorm:"type:uuid;not null" validate:"required,uuid"`
	Protocol  string `json:"protocol" gorm:"type:varchar(10);not null" validate:"required,oneof=tcp udp icmp all"`
	PortRange string `json:"port_range" gorm:"type:varchar(50)" validate:"omitempty,port_range"`
	Action    string `json:"action" gorm:"type:varchar(10);not null" validate:"required,oneof=allow deny reject"`
	Direction string `json:"direction" gorm:"type:varchar(10);not null" validate:"required,oneof=inbound outbound"`
	SourceIP  string `json:"source_ip" gorm:"type:cidr;default:0.0.0.0/0" validate:"omitempty,ip_or_cidr"`
	Priority  int    `json:"priority" gorm:"type:integer;default:100" validate:"required,min=1,max=1000"`
	// ICMPType narrows an icmp rule to one type: a name such as "echo-request",
	// or "type[/code]" in numbers, which needs Family because the numbers differ
	// between IPv4 and IPv6.
	ICMPType string `json:"icmp_type" gorm:"column:icmp_type;type:varchar(32);not null;default:''" validate:"omitempty,max=32"`
	// Log writes packets the rule matches to the node's kernel log.
	Log bool `json:"log" gorm:"column:log;not null;default:false"`
	// Family limits the rule to IPv4 or IPv6; empty applies it to both.
	Family    string         `json:"family" gorm:"column:family;type:varchar(4);not null;default:''" validate:"omitempty,oneof=v4 v6"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`
}

// TableName specifies the table name for FirewallRule
func (FirewallRule) TableName() string {
	return "firewall_rules"
}

// BeforeCreate hook for FirewallRule
func (f *FirewallRule) BeforeCreate(tx *gorm.DB) error {
	if f.ID == "" {
		f.ID = uuid.New().String()
	}
	return nil
}

// GetID returns the UUID representation of ID
func (f *FirewallRule) GetID() uuid.UUID {
	id, _ := uuid.Parse(f.ID)
	return id
}

// GetVMID returns the UUID representation of VMID
func (f *FirewallRule) GetVMID() uuid.UUID {
	id, _ := uuid.Parse(f.VMID)
	return id
}

// Validate validates the FirewallRule struct
func (f *FirewallRule) Validate() ValidationErrors {
	errs := ValidateStruct(f)
	if err := f.CheckCombination(); err != nil {
		errs = append(errs, ValidationError{Field: "icmp_type", Message: err.Error()})
	}
	return errs
}

// CheckCombination reports a rule whose fields are each valid but do not fit
// together, in words an operator can act on.
func (f *FirewallRule) CheckCombination() error {
	if f.ICMPType != "" {
		if f.Protocol != "icmp" {
			return fmt.Errorf("an ICMP type only fits a rule for the icmp protocol")
		}
		v4, v6, ok := ICMPTypeFamilies(f.ICMPType)
		if !ok {
			return fmt.Errorf("unknown ICMP type %q: use a name such as echo-request, or a number", f.ICMPType)
		}
		switch f.Family {
		case "":
			if ICMPTypeIsNumeric(f.ICMPType) {
				return fmt.Errorf("ICMP type numbers mean different messages in IPv4 and IPv6: pick one family")
			}
			if !v4 || !v6 {
				return fmt.Errorf("ICMP type %q exists in only one family: pick IPv4 or IPv6", f.ICMPType)
			}
		case FirewallFamilyV4:
			if !v4 {
				return fmt.Errorf("ICMP type %q does not exist in IPv4", f.ICMPType)
			}
		case FirewallFamilyV6:
			if !v6 {
				return fmt.Errorf("ICMP type %q does not exist in IPv6", f.ICMPType)
			}
		}
	}
	if src := strings.TrimSpace(f.SourceIP); src != "" && f.Family != "" {
		host := strings.SplitN(src, "/", 2)[0]
		if ip := net.ParseIP(host); ip != nil {
			isV4 := ip.To4() != nil
			// 0.0.0.0/0 is the default "anywhere", not an IPv4-only source.
			anywhere := src == "0.0.0.0/0" || src == "::/0"
			if !anywhere && isV4 != (f.Family == FirewallFamilyV4) {
				return fmt.Errorf("the source %s is not an %s address", src, map[bool]string{true: "IPv4", false: "IPv6"}[f.Family == FirewallFamilyV4])
			}
		}
	}
	return nil
}

// icmpNames are the ICMP type names iptables and ip6tables both accept, with
// the families each exists in.
var icmpNames = map[string][2]bool{
	"echo-request":            {true, true},
	"echo-reply":              {true, true},
	"destination-unreachable": {true, true},
	"time-exceeded":           {true, true},
	"parameter-problem":       {true, true},
	"redirect":                {true, true},
	"router-advertisement":    {true, true},
	"router-solicitation":     {true, true},
	"source-quench":           {true, false},
	"timestamp-request":       {true, false},
	"timestamp-reply":         {true, false},
	"packet-too-big":          {false, true},
	"neighbour-solicitation":  {false, true},
	"neighbour-advertisement": {false, true},
}

// ICMPTypeFamilies says in which families an ICMP type can be matched. A
// number ("8", "3/4") is valid in both, but means a different type in each, so
// callers require an explicit family for one.
func ICMPTypeFamilies(t string) (v4, v6, ok bool) {
	if f, known := icmpNames[t]; known {
		return f[0], f[1], true
	}
	parts := strings.Split(t, "/")
	if len(parts) > 2 {
		return false, false, false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 || p != strconv.Itoa(n) {
			return false, false, false
		}
	}
	return true, true, true
}

// ICMPTypeIsNumeric reports whether t is given as numbers rather than a name.
func ICMPTypeIsNumeric(t string) bool {
	_, named := icmpNames[t]
	return t != "" && !named
}
