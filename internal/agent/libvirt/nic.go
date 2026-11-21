package libvirt

import (
	"encoding/xml"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/google/uuid"
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// ErrNICNotFound is a card the VM does not have.
var ErrNICNotFound = errors.New("the VM has no card with that MAC address")

// findNIC returns the interface of def with mac, nil when there is none.
func findNIC(def *libvirtxml.Domain, mac string) *libvirtxml.DomainInterface {
	if def.Devices == nil {
		return nil
	}
	for i := range def.Devices.Interfaces {
		if m := def.Devices.Interfaces[i].MAC; m != nil && strings.EqualFold(m.Address, mac) {
			return &def.Devices.Interfaces[i]
		}
	}
	return nil
}

func domainDef(dom *libvirt.Domain, flags libvirt.DomainXMLFlags) (*libvirtxml.Domain, error) {
	raw, err := dom.GetXMLDesc(flags)
	if err != nil {
		return nil, fmt.Errorf("failed to get domain XML: %w", err)
	}
	var def libvirtxml.Domain
	if err := xml.Unmarshal([]byte(raw), &def); err != nil {
		return nil, fmt.Errorf("failed to parse domain XML: %w", err)
	}
	return &def, nil
}

// AttachNIC adds a virtio card with mac on bridge to the VM's definition and,
// while it runs, to the running VM. live is false when only the definition
// has it: the VM is off, or its machine had no free slot for a card added
// while running (a q35 machine needs a spare PCIe port), in which case it
// appears at the VM's next start.
func AttachNIC(uuidStr, bridge, mac string, filter *libvirtxml.DomainInterfaceFilterRef) (live bool, err error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return false, fmt.Errorf("invalid UUID format: %w", err)
	}
	iface := libvirtxml.DomainInterface{
		MAC:       &libvirtxml.DomainInterfaceMAC{Address: mac},
		Source:    &libvirtxml.DomainInterfaceSource{Bridge: &libvirtxml.DomainInterfaceSourceBridge{Bridge: bridge}},
		Model:     &libvirtxml.DomainInterfaceModel{Type: "virtio"},
		FilterRef: filter,
	}
	doc, err := iface.Marshal()
	if err != nil {
		return false, err
	}
	err = WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		def, err := domainDef(dom, libvirt.DOMAIN_XML_INACTIVE)
		if err != nil {
			return err
		}
		if findNIC(def, mac) != nil {
			live, _ = dom.IsActive()
			return nil // already there: a retried call
		}
		if active, _ := dom.IsActive(); active {
			lerr := dom.AttachDeviceFlags(doc, libvirt.DOMAIN_DEVICE_MODIFY_CONFIG|libvirt.DOMAIN_DEVICE_MODIFY_LIVE)
			if lerr == nil {
				live = true
				return nil
			}
			log.Printf("[libvirt] VM %s: card %s not added live (%v); added to the definition for its next start", uuidStr, mac, lerr)
		}
		return dom.AttachDeviceFlags(doc, libvirt.DOMAIN_DEVICE_MODIFY_CONFIG)
	})
	return live, err
}

// DetachNIC removes the card with mac from the VM's definition and, while it
// runs, asks the guest to release it. live is false when the running VM keeps
// it until its next stop (the guest did not take the request).
func DetachNIC(uuidStr, mac string) (live bool, err error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return false, fmt.Errorf("invalid UUID format: %w", err)
	}
	err = WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		found := false
		if active, _ := dom.IsActive(); active {
			def, err := domainDef(dom, 0)
			if err != nil {
				return err
			}
			if nic := findNIC(def, mac); nic != nil {
				found = true
				doc, _ := nic.Marshal()
				if lerr := dom.DetachDeviceFlags(doc, libvirt.DOMAIN_DEVICE_MODIFY_LIVE); lerr == nil {
					live = true
				} else {
					log.Printf("[libvirt] VM %s: card %s not removed live (%v); it goes at the VM's next stop", uuidStr, mac, lerr)
				}
			} else {
				live = true
			}
		}
		def, err := domainDef(dom, libvirt.DOMAIN_XML_INACTIVE)
		if err != nil {
			return err
		}
		nic := findNIC(def, mac)
		if nic == nil {
			if !found {
				return ErrNICNotFound
			}
			return nil
		}
		doc, _ := nic.Marshal()
		return dom.DetachDeviceFlags(doc, libvirt.DOMAIN_DEVICE_MODIFY_CONFIG)
	})
	return live, err
}

// SetNICMAC gives the card with mac the MAC newMAC in the definition of a VM
// that is off; it is what the card has from the VM's next start. The card's
// filter parameter follows. A running VM is refused: its guest's network
// settings name the old MAC until they are rewritten for the new one.
func SetNICMAC(uuidStr, mac, newMAC string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		if active, _ := dom.IsActive(); active {
			return fmt.Errorf("the VM is running; stop it before changing a card's MAC address")
		}
		def, err := domainDef(dom, libvirt.DOMAIN_XML_INACTIVE)
		if err != nil {
			return err
		}
		if findNIC(def, newMAC) != nil {
			return nil // already done: a retried call
		}
		nic := findNIC(def, mac)
		if nic == nil {
			return ErrNICNotFound
		}
		nic.MAC.Address = newMAC
		if nic.FilterRef != nil {
			for i := range nic.FilterRef.Parameters {
				if nic.FilterRef.Parameters[i].Name == "MAC" {
					nic.FilterRef.Parameters[i].Value = newMAC
				}
			}
		}
		out, err := xml.Marshal(def)
		if err != nil {
			return fmt.Errorf("failed to marshal domain XML: %w", err)
		}
		newDom, err := redefineDomain(conn, string(out))
		if err != nil {
			return fmt.Errorf("failed to redefine the domain with the new MAC: %w", err)
		}
		newDom.Free()
		return nil
	})
}
