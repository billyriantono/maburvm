package libvirt

import (
	"encoding/xml"
	"fmt"

	"github.com/google/uuid"
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// mbpsToKBps converts a Mbps rate to the kilobytes/sec unit libvirt uses for
// interface bandwidth (1 Mbps = 1000/8 = 125 KB/s). This matches the conversion
// used when the <bandwidth> element is written into the domain XML at create
// time, so live updates and the persisted config stay consistent.
func mbpsToKBps(mbps int) uint {
	if mbps <= 0 {
		return 0
	}
	return uint(mbps) * 125
}

// SetInterfaceBandwidth live-updates a VM's primary NIC QoS in BOTH directions
// via libvirt (equivalent to `virsh domiftune --inbound --outbound`).
//
// This is the ONLY correct way to change a running VM's upload speed: libvirt
// enforces inbound (guest download) with an HTB qdisc on the host tap's egress
// AND outbound (guest upload) with ingress policing/IFB on the tap. The agent's
// manual `tc` HTB shapes only the tap's egress, so it can change download but
// never upload — which is why raising the limit there left upload pinned at the
// value baked into the domain XML at creation.
//
// mbps <= 0 clears the limit (unlimited) for both directions. The change is
// applied to the live domain when running and always written to the persistent
// config so it survives reboots.
func SetInterfaceBandwidth(uuidStr string, mbps int) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}

	avg := mbpsToKBps(mbps)

	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		dev, err := primaryInterfaceDev(dom)
		if err != nil {
			return err
		}

		// Set=true with value 0 is how libvirt clears a direction's limit.
		params := &libvirt.DomainInterfaceParameters{
			BandwidthInAverageSet:  true,
			BandwidthInAverage:     avg,
			BandwidthOutAverageSet: true,
			BandwidthOutAverage:    avg,
		}

		// Persist to config always; also apply live when the domain is running.
		flags := libvirt.DOMAIN_AFFECT_CONFIG
		if active, aerr := dom.IsActive(); aerr == nil && active {
			flags |= libvirt.DOMAIN_AFFECT_LIVE
		}

		if err := dom.SetInterfaceParameters(dev, params, flags); err != nil {
			return fmt.Errorf("failed to set interface bandwidth for VM %s (%s): %w", uuidStr, dev, err)
		}
		return nil
	})
}

// primaryInterfaceDev returns the target device name (e.g. "vnet0") of a
// domain's first network interface, parsed from its live XML.
func primaryInterfaceDev(dom *libvirt.Domain) (string, error) {
	xmlDesc, err := dom.GetXMLDesc(0)
	if err != nil {
		return "", fmt.Errorf("failed to get domain XML: %w", err)
	}
	var domain libvirtxml.Domain
	if err := xml.Unmarshal([]byte(xmlDesc), &domain); err != nil {
		return "", fmt.Errorf("failed to parse domain XML: %w", err)
	}
	for _, iface := range domain.Devices.Interfaces {
		if iface.Target != nil && iface.Target.Dev != "" {
			return iface.Target.Dev, nil
		}
	}
	return "", fmt.Errorf("no network interface with a target device found")
}
