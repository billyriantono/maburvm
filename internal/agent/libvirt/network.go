package libvirt

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net"
	"path/filepath"
	"strings"

	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// GetVMInterfaceIPs returns the VM's current IPv4/IPv6 addresses, trying the
// guest agent first, then DHCP leases, then the host ARP table — so it works
// even for VMs without a responsive qemu-guest-agent (ARP sees any VM that has
// sent traffic). Loopback addresses are excluded.
func GetVMInterfaceIPs(uuidStr string) ([]string, error) {
	var ips []string
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		seen := map[string]bool{}
		for _, src := range []libvirt.DomainInterfaceAddressesSource{
			libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_AGENT,
			libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_LEASE,
			libvirt.DOMAIN_INTERFACE_ADDRESSES_SRC_ARP,
		} {
			ifaces, lerr := dom.ListAllInterfaceAddresses(src)
			if lerr != nil {
				continue
			}
			for _, iface := range ifaces {
				for _, a := range iface.Addrs {
					ip := strings.TrimSpace(a.Addr)
					if ip == "" || strings.HasPrefix(ip, "127.") || ip == "::1" || seen[ip] {
						continue
					}
					seen[ip] = true
					ips = append(ips, ip)
				}
			}
			if len(ips) > 0 {
				break // first source that yields addresses wins
			}
		}
		return nil
	})
	return ips, err
}

// GuestInterface identifies one NIC of one running domain on this node.
type GuestInterface struct {
	// MAC is the guest-side hardware address — the identifier the host assigns
	// and the guest cannot quietly change, unlike its IP.
	MAC string
	// VMID is the libvirt domain UUID.
	VMID string
	// Name is the domain name, which for imported guests is often just the UUID.
	Name string
	// Target is the host-side tap device (e.g. vnet3), empty if not yet attached.
	Target string
}

// ListGuestInterfaces returns every NIC of every running domain.
//
// Deliberately sourced from libvirt rather than from the panel's VM list: the
// guests that exhausted a node's conntrack table on 2026-08-08 were legacy
// domains the panel had never seen, so anything driven by the panel's own
// records would have reported nothing wrong while the node was falling over.
func ListGuestInterfaces() ([]GuestInterface, error) {
	var out []GuestInterface
	err := WithConnection(func(conn *libvirt.Connect) error {
		doms, err := conn.ListAllDomains(libvirt.CONNECT_LIST_DOMAINS_ACTIVE)
		if err != nil {
			return fmt.Errorf("failed to list domains: %w", err)
		}
		defer func() {
			for i := range doms {
				_ = doms[i].Free()
			}
		}()

		for i := range doms {
			xmlDesc, err := doms[i].GetXMLDesc(0)
			if err != nil {
				continue // a domain that disappears mid-scan is not an error
			}
			var def libvirtxml.Domain
			if err := def.Unmarshal(xmlDesc); err != nil {
				continue
			}
			if def.Devices == nil {
				continue
			}
			for _, iface := range def.Devices.Interfaces {
				if iface.MAC == nil || iface.MAC.Address == "" {
					continue
				}
				gi := GuestInterface{
					MAC:  strings.ToLower(iface.MAC.Address),
					VMID: def.UUID,
					Name: def.Name,
				}
				if iface.Target != nil {
					gi.Target = iface.Target.Dev
				}
				out = append(out, gi)
			}
		}
		return nil
	})
	return out, err
}

// DiskLocations counts, per directory, how many domain disks live there.
//
// Covers every defined domain, running or not, and every domain the panel does
// not manage — which is the point. A node's registered pools describe where the
// panel *thinks* disks are; this describes where they actually are. On a node
// imported from another platform the two can disagree completely, and the first
// symptom of that is a full filesystem nobody was watching.
//
// ISO/cdrom sources are included: they occupy the same filesystem and a large
// ISO library is a real reason a volume fills up.
func DiskLocations() (map[string]int, error) {
	out := map[string]int{}
	err := WithConnection(func(conn *libvirt.Connect) error {
		doms, err := conn.ListAllDomains(0) // 0 = all, active and inactive
		if err != nil {
			return fmt.Errorf("failed to list domains: %w", err)
		}
		defer func() {
			for i := range doms {
				_ = doms[i].Free()
			}
		}()

		for i := range doms {
			xmlDesc, err := doms[i].GetXMLDesc(0)
			if err != nil {
				continue // a domain that disappears mid-scan is not an error
			}
			var def libvirtxml.Domain
			if err := def.Unmarshal(xmlDesc); err != nil || def.Devices == nil {
				continue
			}
			for _, disk := range def.Devices.Disks {
				if disk.Source == nil || disk.Source.File == nil {
					continue // network-backed (RBD/iSCSI) disks have no local path
				}
				if f := strings.TrimSpace(disk.Source.File.File); f != "" {
					out[filepath.Dir(f)]++
				}
			}
		}
		return nil
	})
	return out, err
}

// DefineNetwork creates and starts a managed libvirt network used for private
// VPC segments. mode is "isolated" (no uplink — VMs on it reach only each other
// and the host) or "nat" (outbound via the host). It returns the bridge name
// libvirt manages. Idempotent: an existing network with the same name is reused.
func DefineNetwork(name, mode, bridge, cidr string, dhcp bool) (string, error) {
	if name == "" {
		return "", fmt.Errorf("network name is required")
	}
	if mode == "" {
		mode = "isolated"
	}
	if bridge == "" {
		bridge = generateBridgeName(name)
	}

	var resultBridge string
	err := WithConnection(func(conn *libvirt.Connect) error {
		// Reuse an existing network with this name (idempotent).
		if existing, lookupErr := conn.LookupNetworkByName(name); lookupErr == nil {
			defer existing.Free()
			if active, _ := existing.IsActive(); !active {
				_ = existing.Create()
			}
			resultBridge, _ = existing.GetBridgeName()
			return nil
		}

		netCfg := &libvirtxml.Network{
			Name:   name,
			Bridge: &libvirtxml.NetworkBridge{Name: bridge, STP: "on", Delay: "0"},
		}
		if mode == "nat" {
			netCfg.Forward = &libvirtxml.NetworkForward{Mode: "nat"}
		}
		// isolated → no <forward> element.

		if cidr != "" {
			if gw, prefix, derr := gatewayFromCIDR(cidr); derr == nil && gw != "" {
				ipEntry := libvirtxml.NetworkIP{Address: gw, Prefix: uint(prefix)}
				if dhcp {
					if start, end := dhcpRange(cidr); start != "" {
						ipEntry.DHCP = &libvirtxml.NetworkDHCP{
							Ranges: []libvirtxml.NetworkDHCPRange{{Start: start, End: end}},
						}
					}
				}
				netCfg.IPs = []libvirtxml.NetworkIP{ipEntry}
			}
		}

		xmlDesc, merr := netCfg.Marshal()
		if merr != nil {
			return fmt.Errorf("failed to marshal network XML: %w", merr)
		}
		netObj, derr := conn.NetworkDefineXML(xmlDesc)
		if derr != nil {
			return fmt.Errorf("failed to define network: %w", derr)
		}
		defer netObj.Free()
		if cerr := netObj.Create(); cerr != nil {
			_ = netObj.Undefine()
			return fmt.Errorf("failed to start network: %w", cerr)
		}
		_ = netObj.SetAutostart(true)
		if resultBridge, _ = netObj.GetBridgeName(); resultBridge == "" {
			resultBridge = bridge
		}
		return nil
	})
	return resultBridge, err
}

// UndefineNetwork stops and removes a managed libvirt network (no-op if absent).
func UndefineNetwork(name string) error {
	if name == "" {
		return fmt.Errorf("network name is required")
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		netObj, err := conn.LookupNetworkByName(name)
		if err != nil {
			return nil // already gone
		}
		defer netObj.Free()
		if active, _ := netObj.IsActive(); active {
			_ = netObj.Destroy()
		}
		return netObj.Undefine()
	})
}

// generateBridgeName derives a stable, <=15-char bridge name from a network name.
func generateBridgeName(name string) string {
	sum := sha1.Sum([]byte(name))
	return "mvbr" + hex.EncodeToString(sum[:])[:8]
}

// gatewayFromCIDR returns the first usable address (network+1) and prefix length.
func gatewayFromCIDR(cidr string) (string, int, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", 0, err
	}
	prefix, _ := ipnet.Mask.Size()
	gw := make(net.IP, len(ipnet.IP))
	copy(gw, ipnet.IP)
	gw[len(gw)-1]++
	return gw.String(), prefix, nil
}

// dhcpRange returns a .2–.254 DHCP range for an IPv4 CIDR (empty for IPv6).
func dhcpRange(cidr string) (string, string) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "", ""
	}
	base := ipnet.IP.To4()
	if base == nil {
		return "", ""
	}
	start := make(net.IP, 4)
	copy(start, base)
	start[3] = 2
	end := make(net.IP, 4)
	copy(end, base)
	end[3] = 254
	return start.String(), end.String()
}
