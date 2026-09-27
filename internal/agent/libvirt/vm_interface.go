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

// domainXML parses the live libvirt definition of a VM.
func domainXML(uuidStr string) (*libvirtxml.Domain, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %w", err)
	}
	var domain libvirtxml.Domain
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		return xml.Unmarshal([]byte(xmlDesc), &domain)
	})
	if err != nil {
		return nil, err
	}
	return &domain, nil
}

// GetVMInterfaceMAC returns the MAC of the VM's first network interface — the
// one the guest's network config matches on.
func GetVMInterfaceMAC(uuidStr string) (string, error) {
	domain, err := domainXML(uuidStr)
	if err != nil {
		return "", err
	}
	for _, iface := range domain.Devices.Interfaces {
		if iface.MAC != nil && iface.MAC.Address != "" {
			return iface.MAC.Address, nil
		}
	}
	return "", fmt.Errorf("no network interface with a MAC found for VM %s", uuidStr)
}

// GetVMInterfaceBridge returns the host bridge the VM's first bridged
// interface is plugged into.
func GetVMInterfaceBridge(uuidStr string) (string, error) {
	domain, err := domainXML(uuidStr)
	if err != nil {
		return "", err
	}
	if iface := primaryBridgedInterface(domain); iface != nil {
		return iface.Source.Bridge.Bridge, nil
	}
	return "", fmt.Errorf("no bridged interface found for VM %s", uuidStr)
}

// GetVMPrimaryDiskPath returns the backing file of the VM's first disk (not
// cdrom) device — the root disk the guest OS lives on.
func GetVMPrimaryDiskPath(uuidStr string) (string, error) {
	domain, err := domainXML(uuidStr)
	if err != nil {
		return "", err
	}
	for _, d := range domain.Devices.Disks {
		if d.Device == "disk" && d.Source != nil && d.Source.File != nil && d.Source.File.File != "" {
			return d.Source.File.File, nil
		}
	}
	return "", fmt.Errorf("no file-backed disk found for VM %s", uuidStr)
}
