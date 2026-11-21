package libvirt

import (
	"encoding/xml"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/agent/ceph"
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// ErrNoTapDevice means the domain exists but has no tap device, which is the
// case for a stopped VM (libvirt names taps only while it runs).
var ErrNoTapDevice = errors.New("no network interface found")

// IsNoDomain reports whether err is libvirt saying the domain does not exist,
// as opposed to libvirt itself being unreachable (libvirtd down, socket gone).
// The panel marks a VM stopped on NotFound, so only this case may map to it.
func IsNoDomain(err error) bool {
	var lerr libvirt.Error
	return errors.As(err, &lerr) && lerr.Code == libvirt.ERR_NO_DOMAIN
}

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

		return fmt.Errorf("%w for VM %s", ErrNoTapDevice, uuidStr)
	})

	if err != nil {
		return "", err
	}

	return ifaceName, nil
}

// domainXML parses the live libvirt definition of a VM.
func domainXML(uuidStr string) (*libvirtxml.Domain, error) { return domainXMLFlags(uuidStr, 0) }

func domainXMLFlags(uuidStr string, flags libvirt.DomainXMLFlags) (*libvirtxml.Domain, error) {
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
		xmlDesc, err := dom.GetXMLDesc(flags)
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

// GetVMDefinedNICs is GetVMNICs from the persistent definition, which also
// has a card added for the VM's next start that the running VM lacks.
func GetVMDefinedNICs(uuidStr string) ([]VMNIC, error) {
	domain, err := domainXMLFlags(uuidStr, libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return nil, err
	}
	return nicsOf(domain), nil
}

// VMNIC is one of a domain's network interfaces; Tap is empty while it isn't
// running. Bridge and Model are what the definition says.
type VMNIC struct {
	MAC    string
	Tap    string
	Bridge string
	Model  string
}

// GetVMNICs returns every network interface of the VM that has a MAC.
func GetVMNICs(uuidStr string) ([]VMNIC, error) {
	domain, err := domainXML(uuidStr)
	if err != nil {
		return nil, err
	}
	return nicsOf(domain), nil
}

func nicsOf(domain *libvirtxml.Domain) []VMNIC {
	var nics []VMNIC
	if domain.Devices == nil {
		return nil
	}
	for _, iface := range domain.Devices.Interfaces {
		if iface.MAC == nil || iface.MAC.Address == "" {
			continue
		}
		n := VMNIC{MAC: iface.MAC.Address}
		if iface.Target != nil {
			n.Tap = iface.Target.Dev
		}
		if iface.Source != nil && iface.Source.Bridge != nil {
			n.Bridge = iface.Source.Bridge.Bridge
		}
		if iface.Model != nil {
			n.Model = iface.Model.Type
		}
		nics = append(nics, n)
	}
	return nics
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

// DomainBridges returns the host bridges the VM's interfaces are defined on.
func DomainBridges(uuidStr string) ([]string, error) {
	domain, err := domainXML(uuidStr)
	if err != nil {
		return nil, err
	}
	var out []string
	if domain.Devices == nil {
		return nil, nil
	}
	for _, iface := range domain.Devices.Interfaces {
		if iface.Source != nil && iface.Source.Bridge != nil && iface.Source.Bridge.Bridge != "" {
			out = append(out, iface.Source.Bridge.Bridge)
		}
	}
	return out, nil
}

// GetVMPrimaryDiskPath returns the backing file, block device or RBD disk
// path of the VM's first disk (not cdrom) device — the root disk the guest OS lives on.
func GetVMPrimaryDiskPath(uuidStr string) (string, error) {
	domain, err := domainXML(uuidStr)
	if err != nil {
		return "", err
	}
	for _, d := range domain.Devices.Disks {
		if d.Device == "disk" && d.Source != nil && d.Source.File != nil && d.Source.File.File != "" {
			return d.Source.File.File, nil
		}
		if d.Device == "disk" && d.Source != nil && d.Source.Block != nil && d.Source.Block.Dev != "" {
			return d.Source.Block.Dev, nil
		}
		if p, ok := ceph.DiskPath(&d); ok && d.Device == "disk" {
			return p, nil
		}
	}
	return "", fmt.Errorf("no disk found for VM %s", uuidStr)
}
