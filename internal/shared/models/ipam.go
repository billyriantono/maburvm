package models

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	IPFamilyIPv4 = "ipv4"
	IPFamilyIPv6 = "ipv6"

	IPAddressStatusAvailable = "available"
	IPAddressStatusReserved  = "reserved"
	IPAddressStatusAssigned  = "assigned"
	IPAddressStatusDisabled  = "disabled"

	// Delivery mode: how the address reaches the VM.
	// Direct = bridged and bound inside the guest (the pre-existing model).
	// Floating = configured on the host and NATed to the VM's own address, so it
	// can be moved between VMs on the node without touching either guest.
	IPDeliveryDirect   = "direct"
	IPDeliveryFloating = "floating"

	// NAT mode for a floating IP.
	// Inbound = DNAT only; conntrack reverses it for replies, and the VM still
	// egresses under its own identity (baseline masquerade or its own public IP).
	// Full = DNAT + SNAT; the VM egresses *as* the floating IP. Only one full-mode
	// floating IP per VM makes sense, since it overrides the egress identity.
	NATModeInbound = "inbound"
	NATModeFull    = "full"
)

// IPPool represents a first-class IPAM pool. It intentionally does not replace
// the VM-attached networks table.
type IPPool struct {
	ID          string  `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name        string  `json:"name" gorm:"type:varchar(255);not null" validate:"required"`
	NodeID      *string `json:"node_id,omitempty" gorm:"type:uuid" validate:"omitempty,uuid"`
	Family      string  `json:"family" gorm:"type:varchar(8);not null;default:'ipv4'" validate:"required,oneof=ipv4 ipv6"`
	CIDR        string  `json:"cidr,omitempty" gorm:"column:cidr;type:cidr" validate:"omitempty,cidr"`
	Gateway     string  `json:"gateway,omitempty" gorm:"type:inet" validate:"omitempty,ip"`
	Bridge      string  `json:"bridge,omitempty" gorm:"type:varchar(64)" validate:"omitempty,max=64"`
	RangeStart  string  `json:"range_start,omitempty" gorm:"type:inet" validate:"omitempty,ip"`
	RangeEnd    string  `json:"range_end,omitempty" gorm:"type:inet" validate:"omitempty,ip"`
	Description string  `json:"description,omitempty" gorm:"type:text"`
	// Orderable opts this pool into customer self-service. Off by default so a
	// pool reserved for infrastructure is never handed out by accident.
	Orderable bool `json:"orderable" gorm:"not null;default:false"`
	// DelegatedPrefixLen turns an IPv6 pool into a routed-prefix pool: every VM
	// gets one /<n> out of the pool CIDR (a /64 per VM from a /48) instead of a
	// single address. Nil = ordinary per-address pool.
	DelegatedPrefixLen *int `json:"delegated_prefix_len,omitempty" gorm:"type:smallint"`
	// LinkPrefix is the router<->node link network when it lies inside the pool
	// (one /<DelegatedPrefixLen>); it is never delegated. Nil: idx 0 is the link.
	LinkPrefix *string `json:"link_prefix,omitempty" gorm:"type:cidr"`
	// NAT marks a pool of private addresses masqueraded behind the node, for
	// VMs on NAT plans. Never handed to a VM that should get a public address.
	NAT bool `json:"nat" gorm:"column:nat;not null;default:false"`
	// RDNSZone is the zone the pool's PTR records are written to when it is
	// not the standard one: an RFC 2317 classless zone for a block smaller
	// than a /24, or a parent zone delegated to the panel's nameserver. Empty
	// keeps the standard zone. Read-only here, written by table name.
	RDNSZone string `json:"rdns_zone" gorm:"column:rdns_zone;->"`
	// Nameservers are the resolvers the pool's guests are given, comma
	// separated (see ParseNameservers); empty keeps DefaultIPv4DNS/DefaultIPv6DNS.
	Nameservers string `json:"nameservers" gorm:"type:text;not null;default:''"`
	// DHCP makes the node answer DHCP for the pool's assigned addresses, each
	// to its own VM's card only, for guests set up without cloud-init.
	DHCP      bool           `json:"dhcp" gorm:"column:dhcp;not null;default:false"`
	CreatedAt time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt gorm.DeletedAt `json:"-" gorm:"index"`

	// Many-to-many: loaded separately via ip_pool_nodes junction table
	// When junction table exists, this takes precedence over NodeID
	NodeIDs []string `json:"node_ids" gorm:"-"`
	// DelegatedCount is how many prefixes a delegated pool has handed out; a
	// delegated pool has no address rows to count instead.
	DelegatedCount int64 `json:"delegated_count,omitempty" gorm:"-"`
}

// IsDelegated reports whether the pool hands out routed prefixes per VM.
func (p *IPPool) IsDelegated() bool { return p.DelegatedPrefixLen != nil && *p.DelegatedPrefixLen > 0 }

const (
	// IPv6LinkLocalGateway is the default route every VM with a delegated prefix
	// uses: the node holds fe80::1 on the bridge, so it never changes per VM.
	IPv6LinkLocalGateway = "fe80::1"
)

// DefaultIPv6DNS is what a guest with a delegated prefix gets as IPv6 resolvers.
var DefaultIPv6DNS = []string{"2606:4700:4700::1111", "2001:4860:4860::8888"}

// VMIPv6Prefix is the routed prefix delegated to one VM from a delegated pool.
// Prefix is the network in CIDR form ("2001:db8:20:5::/64"); Idx is its
// position inside the pool (prefix = pool + idx << (128 - len)), unique per pool.
type VMIPv6Prefix struct {
	ID     string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	PoolID string `json:"pool_id" gorm:"type:uuid;not null"`
	VMID   string `json:"vm_id" gorm:"type:uuid;not null;uniqueIndex"`
	Prefix string `json:"prefix" gorm:"type:cidr;not null"`
	Idx    int    `json:"idx" gorm:"not null"`
	// ExtraAddresses are further addresses inside Prefix the guest holds,
	// comma separated, without a prefix length.
	ExtraAddresses string    `json:"extra_addresses" gorm:"column:extra_addresses;type:text;not null;default:''"`
	CreatedAt      time.Time `json:"created_at" gorm:"not null;default:NOW()"`
}

// MaxIPv6ExtraAddresses caps the further addresses a VM holds in its prefix.
const MaxIPv6ExtraAddresses = 16

// ParseIPv6Extra checks further addresses for a VM's routed prefix: IPv6,
// inside the prefix, never its network address (the subnet-router anycast) or
// ::1 (the guest's own), no duplicates. Returns them in canonical form.
func ParseIPv6Extra(prefix string, addrs []string) ([]string, error) {
	_, n, err := net.ParseCIDR(prefix)
	if err != nil || n.IP.To4() != nil {
		return nil, fmt.Errorf("the VM has no routed IPv6 prefix")
	}
	if len(addrs) > MaxIPv6ExtraAddresses {
		return nil, fmt.Errorf("at most %d further IPv6 addresses", MaxIPv6ExtraAddresses)
	}
	own := net.ParseIP(strings.SplitN(IPv6PrefixAddress(prefix), "/", 2)[0])
	seen := map[string]bool{}
	out := []string{}
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		ip := net.ParseIP(a)
		switch {
		case ip == nil || ip.To4() != nil:
			return nil, fmt.Errorf("%q is not an IPv6 address", a)
		case !n.Contains(ip):
			return nil, fmt.Errorf("%s is not inside %s", ip, prefix)
		case ip.Equal(n.IP):
			return nil, fmt.Errorf("%s is the prefix's own network address", ip)
		case ip.Equal(own):
			return nil, fmt.Errorf("%s is already the VM's main IPv6 address", ip)
		}
		if s := ip.String(); !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out, nil
}

// SplitList reads a comma separated column back into its items.
func SplitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (VMIPv6Prefix) TableName() string { return "vm_ipv6_prefixes" }

func (p *VMIPv6Prefix) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	return nil
}

// Address is the guest's address inside its prefix: the network's ::1, in CIDR
// form so it can be written straight into a network config.
func (p *VMIPv6Prefix) Address() string { return IPv6PrefixAddress(p.Prefix) }

// IPv6PrefixAddress returns "<network>1/<len>" for a prefix like
// "2001:db8:20:5::/64" -> "2001:db8:20:5::1/64". Empty on a malformed input.
func IPv6PrefixAddress(prefix string) string {
	ip, n, err := net.ParseCIDR(prefix)
	if err != nil || ip.To4() != nil {
		return ""
	}
	addr := make(net.IP, len(n.IP))
	copy(addr, n.IP)
	addr[15] |= 1
	ones, _ := n.Mask.Size()
	return fmt.Sprintf("%s/%d", addr, ones)
}

func (IPPool) TableName() string { return "ip_pools" }

func (p *IPPool) BeforeCreate(tx *gorm.DB) error {
	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	if p.Family == "" {
		p.Family = IPFamilyIPv4
	}
	return nil
}

func (p *IPPool) Validate() ValidationErrors { return ValidateStruct(p) }

// IPAddress represents a managed address in an IPAM pool.
type IPAddress struct {
	ID      string  `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	PoolID  string  `json:"pool_id" gorm:"type:uuid;not null;index" validate:"required,uuid"`
	NodeID  *string `json:"node_id,omitempty" gorm:"type:uuid;index" validate:"omitempty,uuid"`
	Address string  `json:"address" gorm:"type:inet;not null" validate:"required,ip"`
	Family  string  `json:"family" gorm:"type:varchar(8);not null;default:'ipv4'" validate:"required,oneof=ipv4 ipv6"`
	Status  string  `json:"status" gorm:"type:varchar(16);not null;default:'available';index" validate:"required,oneof=available reserved assigned disabled"`
	VMID    *string `json:"vm_id,omitempty" gorm:"type:uuid;index" validate:"omitempty,uuid"`
	// DeliveryMode/NATMode describe floating IPs (see the IPDelivery* constants).
	// A direct address keeps NATMode empty. UserID records the tenant that owns a
	// floating IP while it is attached to no VM — a floating IP deliberately
	// survives deletion of the VM it was attached to.
	DeliveryMode string         `json:"delivery_mode" gorm:"type:varchar(16);not null;default:'direct'" validate:"omitempty,oneof=direct floating"`
	NATMode      string         `json:"nat_mode,omitempty" gorm:"type:varchar(16);not null;default:''" validate:"omitempty,oneof=inbound full"`
	UserID       *string        `json:"user_id,omitempty" gorm:"type:uuid;index" validate:"omitempty,uuid"`
	Note         string         `json:"note,omitempty" gorm:"type:text"`
	RDNS         string         `json:"rdns,omitempty" gorm:"column:rdns;type:varchar(253)"` // reverse DNS (PTR) hostname
	CreatedAt    time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt    time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`

	// Region is filled for listings from the node this address lives on. A
	// floating IP can only be attached to a VM on its own node, so a customer who
	// cannot see its location cannot tell which VMs it will work with.
	RegionID      string `json:"region_id,omitempty" gorm:"-"`
	RegionName    string `json:"region_name,omitempty" gorm:"-"`
	RegionCountry string `json:"region_country,omitempty" gorm:"-"`
}

func (IPAddress) TableName() string { return "ip_addresses" }

func (a *IPAddress) BeforeCreate(tx *gorm.DB) error {
	if a.ID == "" {
		a.ID = uuid.New().String()
	}
	if a.Family == "" {
		a.Family = IPFamilyIPv4
	}
	if a.Status == "" {
		a.Status = IPAddressStatusAvailable
	}
	if a.DeliveryMode == "" {
		a.DeliveryMode = IPDeliveryDirect
	}
	return nil
}

func (a *IPAddress) Validate() ValidationErrors { return ValidateStruct(a) }

// IPAssignment is one holding of an address (or IPv6 prefix) by a VM, from
// assigned to released. Rows are written by database triggers on every path
// that assigns or releases an address; Estimated rows were recorded from
// holdings already in place when the history began.
type IPAssignment struct {
	ID         string     `json:"id" gorm:"column:id;primaryKey"`
	Address    string     `json:"address" gorm:"column:address"`
	VMID       *string    `json:"vm_id" gorm:"column:vm_id"`
	VMHostname string     `json:"vm_hostname" gorm:"column:vm_hostname"`
	UserID     *string    `json:"user_id" gorm:"column:user_id"`
	UserEmail  string     `json:"user_email" gorm:"column:user_email"`
	NodeID     *string    `json:"node_id" gorm:"column:node_id"`
	Kind       string     `json:"kind" gorm:"column:kind"`
	AssignedAt time.Time  `json:"assigned_at" gorm:"column:assigned_at"`
	ReleasedAt *time.Time `json:"released_at" gorm:"column:released_at"`
	Estimated  bool       `json:"estimated" gorm:"column:estimated"`
}

func (IPAssignment) TableName() string { return "ip_assignment_history" }

// VMPrefixPTR is a PTR record set for an address inside the IPv6 prefix routed
// to a VM. Those addresses are not rows of ip_addresses: the whole prefix is
// the VM's, and its owner names whichever addresses they use.
type VMPrefixPTR struct {
	ID        string    `json:"id" gorm:"column:id;primaryKey"`
	VMID      string    `json:"vm_id" gorm:"column:vm_id"`
	Address   string    `json:"address" gorm:"column:address"`
	RDNS      string    `json:"rdns" gorm:"column:rdns"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

func (VMPrefixPTR) TableName() string { return "vm_prefix_ptrs" }
