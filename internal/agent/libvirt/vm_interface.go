
package libvirt

import (
	"encoding/xml"
	"fmt"

	"github.com/google/uuid"
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// GetVMInterfaceName returns the network interface name (vnetX) for a VM
// by querying libvirt domain XML
func GetVMInterfaceName(uuidStr string) (string, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return "", fmt.Errorf("invalid UUID format: %w", err)
	}

	var ifaceName string
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		// Get domain XML
		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}

		// Parse XML to find interface target device
		var domain libvirtxml.Domain
		if err := xml.Unmarshal([]byte(xmlDesc), &domain); err != nil {
			return fmt.Errorf("failed to parse domain XML: %w", err)
		}

		for _, iface := range domain.Devices.Interfaces {
			if iface.Target != nil && iface.Target.Dev != "" {
				ifaceName = iface.Target.Dev
				return nil
			}
		}

		return fmt.Errorf("no network interface found for VM %s", uuidStr)
	})

	if err != nil {
		return "", err
	}

	return ifaceName, nil
}
