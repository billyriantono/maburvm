package models

import (
	"crypto/sha256"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// VMNIC is one network card of a VM, with the MAC it has. Position 0 is the
// card the VM was created with; further cards sit on other bridges (a VPC, a
// second public network).
type VMNIC struct {
	ID         string    `json:"id" gorm:"type:uuid;primaryKey"`
	VMID       string    `json:"vm_id" gorm:"column:vm_id;not null"`
	Position   int       `json:"position" gorm:"not null"`
	Bridge     string    `json:"bridge" gorm:"not null;default:''"`
	MACAddress string    `json:"mac_address" gorm:"column:mac_address;not null"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

func (VMNIC) TableName() string { return "vm_nics" }

func (n *VMNIC) BeforeCreate(tx *gorm.DB) error {
	if n.ID == "" {
		n.ID = uuid.New().String()
	}
	return nil
}

// MaxVMNICs is how many cards a VM may have.
const MaxVMNICs = 4

// Agent features of this area (reported in GetNodeInfo).
const (
	// FeatureGuestDNS: NetworkInterface.dns is written into the guest.
	FeatureGuestDNS = "guest_dns"
	// FeatureDHCPServer: NetworkInterface.dhcp_server is served by DHCP.
	FeatureDHCPServer = "dhcp_server"
	// FeatureVMNICs: the ConfigureNIC call (cards added, removed, MAC set).
	FeatureVMNICs = "vm_nics"
	// FeatureVLANBridges: the SetVLANBridges call.
	FeatureVLANBridges = "vlan_bridges"
	// FeatureIPv6Extra: NetworkInterface.ipv6_extra is routed and configured.
	FeatureIPv6Extra = "ipv6_extra"
)

// DerivedMAC is the MAC a card gets when nobody chose one: QEMU's prefix and
// three bytes of a hash of the VM id (and, after the first card, its
// position), so it works out the same every time.
func DerivedMAC(vmID string, position int) string {
	seed := vmID
	if position > 0 {
		seed = fmt.Sprintf("%s/%d", vmID, position)
	}
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", sum[0], sum[1], sum[2])
}

// NormalizeMAC checks a MAC someone typed and returns it in lower-case colon
// form. It must be six bytes, unicast (a multicast source address is dropped
// by every switch) and not all zeros.
func NormalizeMAC(s string) (string, error) {
	hw, err := net.ParseMAC(strings.TrimSpace(s))
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("%q is not a MAC address such as 52:54:00:12:34:56", s)
	}
	if hw[0]&1 == 1 {
		return "", fmt.Errorf("%s is a multicast address; a card needs a unicast one (an even first byte)", hw)
	}
	if hw.String() == "00:00:00:00:00:00" {
		return "", fmt.Errorf("00:00:00:00:00:00 is not a usable MAC address")
	}
	return hw.String(), nil
}

// ValidInterfaceName reports whether s can be a Linux network interface name
// safe to write into a config file or pass to a tool: 1-15 letters, digits,
// '-', '_' or '.'.
func ValidInterfaceName(s string) bool {
	if s == "" || len(s) > 15 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// DefaultIPv4DNS is what a guest gets as resolvers when its pool names none.
var DefaultIPv4DNS = []string{"1.1.1.1", "8.8.8.8"}

// MaxNameservers is how many resolvers a pool may name; a guest's resolver
// library reads only the first three of each kind.
const MaxNameservers = 4

// ParseNameservers reads resolver addresses separated by commas, spaces or
// new lines and returns them comma separated in canonical form, the way the
// column stores them. Empty is allowed and means the defaults.
func ParseNameservers(s string) (string, error) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == ';' })
	var out []string
	seen := map[string]bool{}
	for _, f := range fields {
		ip := net.ParseIP(f)
		if ip == nil {
			return "", fmt.Errorf("%q is not an IP address", f)
		}
		if ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
			return "", fmt.Errorf("%s cannot be a resolver", ip)
		}
		if !seen[ip.String()] {
			seen[ip.String()] = true
			out = append(out, ip.String())
		}
	}
	if len(out) > MaxNameservers {
		return "", fmt.Errorf("at most %d nameservers", MaxNameservers)
	}
	return strings.Join(out, ","), nil
}
