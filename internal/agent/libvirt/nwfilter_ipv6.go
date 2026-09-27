package libvirt

import (
	"encoding/xml"
	"fmt"
	"log"
	"net"
	"sort"
	"sync/atomic"

	"github.com/google/uuid"
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// Stock clean-traffic drops every IPv6 frame, so a VM with a routed prefix
// gets MaburVM's own root filter: clean-traffic's IPv4/ARP rules verbatim plus
// an ipv6 chain that lets the guest speak only from its own /64 (and
// link-local), and never send router/neighbour advertisements or act as a
// DHCPv6 server. Interfaces without a prefix keep clean-traffic untouched.
const (
	IPv6CleanTrafficFilter = "maburvm-clean-traffic-v6"
	ipv6ChainFilter        = "maburvm-ipv6"
	cleanTrafficFilter     = "clean-traffic"
)

// ipv6CleanTrafficXML is clean-traffic (libvirt 6.0 layout) with maburvm-ipv6
// spliced in before no-other-l2-traffic. Rules inside an 'ipv6' chain use the
// ipv6-specific element names (udp-ipv6, icmpv6).
const ipv6CleanTrafficXML = `<filter name='` + IPv6CleanTrafficFilter + `' chain='root'>
  <filterref filter='no-mac-spoofing'/>
  <filterref filter='no-ip-spoofing'/>
  <rule action='accept' direction='out' priority='-650'><mac protocolid='ipv4'/></rule>
  <filterref filter='allow-incoming-ipv4'/>
  <filterref filter='no-arp-spoofing'/>
  <rule action='accept' direction='inout' priority='-500'><mac protocolid='arp'/></rule>
  <filterref filter='` + ipv6ChainFilter + `'/>
  <filterref filter='no-other-l2-traffic'/>
  <filterref filter='qemu-announce-self'/>
</filter>`

// ipv6ChainXML: direction 'out' is traffic sent by the VM. NA (136) is dropped
// because the host resolves the guest through a permanent neighbour entry, so a
// guest never legitimately answers NS - and dropping it stops one guest from
// claiming fe80::1 or another guest's address.
const ipv6ChainXML = `<filter name='` + ipv6ChainFilter + `' chain='ipv6' priority='-600'>
  <rule action='drop' direction='out' priority='100'><icmpv6 type='134'/></rule>
  <rule action='drop' direction='out' priority='110'><icmpv6 type='136'/></rule>
  <rule action='drop' direction='out' priority='120'><udp-ipv6 srcportstart='547'/></rule>
  <rule action='accept' direction='out' priority='200'><ipv6 srcipaddr='fe80::' srcipmask='10'/></rule>
  <rule action='accept' direction='out' priority='210'><ipv6 srcipaddr='::' srcipmask='128'/></rule>
  <rule action='accept' direction='out' priority='300'><ipv6 srcipaddr='$IPV6_PREFIX' srcipmask='64'/></rule>
  <rule action='drop' direction='out' priority='1000'/>
  <rule action='accept' direction='in' priority='500'/>
</filter>`

// InterfaceFilterRef is the nwfilter binding for a VM interface with
// anti-spoofing on: clean-traffic with IP+MAC, or the IPv6-aware variant with
// the delegated network address as well. Nil when anti-spoofing is off.
func InterfaceFilterRef(antiSpoofing bool, ip, mac, ipv6Prefix string) *libvirtxml.DomainInterfaceFilterRef {
	if !antiSpoofing {
		return nil
	}
	ref := &libvirtxml.DomainInterfaceFilterRef{
		Filter: cleanTrafficFilter,
		Parameters: []libvirtxml.DomainInterfaceFilterParam{
			{Name: "IP", Value: ip},
			{Name: "MAC", Value: mac},
		},
	}
	if network := IPv6PrefixNetwork(ipv6Prefix); network != "" {
		ref.Filter = IPv6CleanTrafficFilter
		ref.Parameters = append(ref.Parameters, libvirtxml.DomainInterfaceFilterParam{Name: "IPV6_PREFIX", Value: network})
	}
	return ref
}

// IPv6PrefixNetwork returns the bare network address of a prefix
// ("2001:db8:20:5::/64" -> "2001:db8:20:5::"), which is what the nwfilter
// variable takes. Empty for anything that is not an IPv6 CIDR.
func IPv6PrefixNetwork(prefix string) string {
	if prefix == "" {
		return ""
	}
	ip, n, err := net.ParseCIDR(prefix)
	if err != nil || ip.To4() != nil {
		return ""
	}
	return n.IP.String()
}

var ipv6FiltersDefined atomic.Bool

// EnsureIPv6NWFilters defines the MaburVM IPv6 filters. Idempotent (defining
// an unchanged filter is a no-op in libvirt) and retried on every call until
// it succeeds once, so a libvirt that was not up at agent start is covered by
// the first VM that needs it.
func EnsureIPv6NWFilters() error {
	if ipv6FiltersDefined.Load() {
		return nil
	}
	err := WithConnection(func(conn *libvirt.Connect) error {
		for _, def := range []string{ipv6ChainXML, ipv6CleanTrafficXML} {
			f, err := conn.NWFilterDefineXML(def)
			if err != nil {
				return fmt.Errorf("define nwfilter: %w", err)
			}
			f.Free()
		}
		return nil
	})
	if err == nil {
		ipv6FiltersDefined.Store(true)
	}
	return err
}

// SyncInterfaceFilter converges the nwfilter on the VM's primary interface
// with its current addressing, for an interface that has anti-spoofing on:
//
//   - a VM with a routed prefix gets the IPv6-aware filter (with IP, MAC and
//     IPV6_PREFIX), whether it had clean-traffic or nothing before;
//   - a VM whose prefix was released goes back to stock clean-traffic;
//   - a VM already on clean-traffic has its IP parameter refreshed when the
//     address changed - the old value would have made no-ip-spoofing drop
//     every packet from the new one;
//   - any other interface (no filter, some other filter) is left exactly as is,
//     so nothing changes for VMs this feature never touched.
//
// The MAC comes from the domain itself, never from the caller: an imported VM
// does not use the panel's deterministic MAC, and a wrong MAC parameter would
// make no-mac-spoofing drop everything.
//
// The persistent definition is updated first; a running domain then has the
// device updated live, so a new or released prefix needs no restart.
func SyncInterfaceFilter(uuidStr, ip, ipv6Prefix string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	if ipv6Prefix != "" {
		if err := EnsureIPv6NWFilters(); err != nil {
			return err
		}
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		// Persistent config first.
		inactive, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		var domain libvirtxml.Domain
		if err := xml.Unmarshal([]byte(inactive), &domain); err != nil {
			return fmt.Errorf("failed to parse domain XML: %w", err)
		}
		iface := primaryBridgedInterface(&domain)
		if iface == nil {
			return fmt.Errorf("no bridged interface on VM %s", uuidStr)
		}
		if iface.MAC == nil || iface.MAC.Address == "" {
			return fmt.Errorf("interface on VM %s has no MAC", uuidStr)
		}
		ref := desiredFilterRef(iface.FilterRef, ip, iface.MAC.Address, ipv6Prefix)
		if ref == nil || filterRefEqual(iface.FilterRef, ref) {
			return nil
		}
		iface.FilterRef = ref
		out, err := xml.Marshal(&domain)
		if err != nil {
			return fmt.Errorf("failed to marshal domain XML: %w", err)
		}
		newDom, err := conn.DomainDefineXML(string(out))
		if err != nil {
			return fmt.Errorf("failed to redefine domain with filter: %w", err)
		}
		newDom.Free()

		// Then the live device, from the LIVE XML so nothing but the filter
		// differs in what libvirt is asked to change.
		state, _, err := dom.GetState()
		if err != nil || state != libvirt.DOMAIN_RUNNING {
			return nil
		}
		live, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get live domain XML: %w", err)
		}
		var liveDom libvirtxml.Domain
		if err := xml.Unmarshal([]byte(live), &liveDom); err != nil {
			return fmt.Errorf("failed to parse live domain XML: %w", err)
		}
		liveIface := primaryBridgedInterface(&liveDom)
		if liveIface == nil {
			return nil
		}
		liveIface.FilterRef = ref
		ifaceXML, err := xml.Marshal(liveIface)
		if err != nil {
			return fmt.Errorf("failed to marshal interface XML: %w", err)
		}
		if err := dom.UpdateDeviceFlags(string(ifaceXML), libvirt.DOMAIN_DEVICE_MODIFY_LIVE); err != nil {
			log.Printf("[libvirt] WARNING: nwfilter for VM %s updated on disk but not live (%v); it applies on next start", uuidStr, err)
		}
		return nil
	})
}

// desiredFilterRef decides what SyncInterfaceFilter should bind given the
// interface's current filter; nil means "leave it alone".
func desiredFilterRef(current *libvirtxml.DomainInterfaceFilterRef, ip, mac, ipv6Prefix string) *libvirtxml.DomainInterfaceFilterRef {
	switch {
	case ipv6Prefix != "":
		return InterfaceFilterRef(true, ip, mac, ipv6Prefix)
	case current != nil && (current.Filter == IPv6CleanTrafficFilter || current.Filter == cleanTrafficFilter):
		return InterfaceFilterRef(true, ip, mac, "")
	default:
		return nil
	}
}

func primaryBridgedInterface(domain *libvirtxml.Domain) *libvirtxml.DomainInterface {
	if domain.Devices == nil {
		return nil
	}
	for i := range domain.Devices.Interfaces {
		if src := domain.Devices.Interfaces[i].Source; src != nil && src.Bridge != nil {
			return &domain.Devices.Interfaces[i]
		}
	}
	return nil
}

// filterRefEqual compares two filter bindings ignoring parameter order.
func filterRefEqual(a, b *libvirtxml.DomainInterfaceFilterRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	if a.Filter != b.Filter || len(a.Parameters) != len(b.Parameters) {
		return false
	}
	key := func(p []libvirtxml.DomainInterfaceFilterParam) []string {
		out := make([]string, len(p))
		for i, x := range p {
			out[i] = x.Name + "=" + x.Value
		}
		sort.Strings(out)
		return out
	}
	ka, kb := key(a.Parameters), key(b.Parameters)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}
