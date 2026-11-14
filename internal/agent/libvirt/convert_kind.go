package libvirt

import (
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"

	"github.com/maburvm/panel/internal/agent/guestos"
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// A domain is laid out for one kind of system: RouterOS 6 needs the older
// machine type, Windows needs its clock and Hyper-V settings, Linux and Windows
// need the setup disc and RouterOS must not have it. Reinstalling a VM with
// another kind of system therefore rewrites those parts of its definition,
// and nothing else: its name, UUID, size, disks, network card (MAC, bridge,
// limits, filter) and console stay as they are.

// guestAddressRe matches the bus position libvirt assigned to a device. Those
// positions describe the old machine type's buses; with them removed libvirt
// lays the devices out again for the new one.
var guestAddressRe = regexp.MustCompile(`\s*<address type='(?:pci|drive|virtio-serial|usb|ccid|isa)'[^>]*/>`)

var metadataOSTypeRe = regexp.MustCompile(`<maburvm:os_type>[a-z]*</maburvm:os_type>`)

// tuneForKind sets the clock, CPU features and input devices a kind needs,
// replacing whatever another kind left behind.
func tuneForKind(domain *libvirtxml.Domain, kind string) {
	timers := []libvirtxml.DomainTimer{
		{Name: "rtc", TickPolicy: "catchup"},
		{Name: "pit", TickPolicy: "delay"},
		{Name: "hpet", Present: "no"},
	}
	if domain.Features == nil {
		domain.Features = &libvirtxml.DomainFeatureList{ACPI: &libvirtxml.DomainFeature{}, APIC: &libvirtxml.DomainFeatureAPIC{}}
	}
	domain.Features.HyperV = nil
	if domain.Devices != nil {
		domain.Devices.Inputs = nil
	}
	// Windows runs noticeably better when told it is on a hypervisor it knows:
	// without these it burns CPU on timers and its clock drifts. The hardware
	// clock stays on UTC like every other VM's; the image is built to read it
	// that way (RealTimeIsUniversal), which keeps the time right whatever time
	// zone the owner sets. The tablet makes the console's pointer land where
	// the cursor is.
	if guestos.Normalize(kind) == guestos.Windows {
		on := libvirtxml.DomainFeatureState{State: "on"}
		domain.Features.HyperV = &libvirtxml.DomainFeatureHyperV{
			Relaxed:   &on,
			VAPIC:     &on,
			Spinlocks: &libvirtxml.DomainFeatureHyperVSpinlocks{DomainFeatureState: on, Retries: 8191},
			VPIndex:   &on,
			Runtime:   &on,
			Synic:     &on,
			STimer:    &libvirtxml.DomainFeatureHyperVSTimer{DomainFeatureState: on},
		}
		timers = append(timers, libvirtxml.DomainTimer{Name: "hypervclock", Present: "yes"})
		if domain.Devices != nil {
			domain.Devices.Inputs = []libvirtxml.DomainInput{{Type: "tablet", Bus: "usb"}}
		}
	}
	domain.Clock = &libvirtxml.DomainClock{Offset: "utc", Timer: timers}
}

// seedHint is the SMBIOS serial that makes cloud-init look for the setup disc.
func seedHint(domain *libvirtxml.Domain) {
	domain.OS.SMBios = &libvirtxml.DomainSMBios{Mode: "sysinfo"}
	domain.SysInfo = []libvirtxml.DomainSysInfo{{
		SMBIOS: &libvirtxml.DomainSysInfoSMBIOS{
			System: &libvirtxml.DomainSysInfoSystem{
				Entry: []libvirtxml.DomainSysInfoEntry{{Name: "serial", Value: "ds=nocloud"}},
			},
		},
	}}
}

// convertDomainKind rewrites an inactive domain definition for another kind
// of system. seedPath is the setup disc to attach, or "" for none.
func convertDomainKind(inactiveXML, kind, seedPath string) (string, error) {
	kind = guestos.Normalize(kind)
	var domain libvirtxml.Domain
	if err := xml.Unmarshal([]byte(guestAddressRe.ReplaceAllString(inactiveXML, "")), &domain); err != nil {
		return "", fmt.Errorf("failed to parse domain XML: %w", err)
	}
	if domain.OS == nil || domain.OS.Type == nil || domain.Devices == nil {
		return "", fmt.Errorf("the domain definition has no machine or devices to convert")
	}
	domain.OS.Type.Machine = guestos.Machine(kind)
	// Controllers belong to the machine type too; libvirt adds the ones the
	// remaining devices need.
	domain.Devices.Controllers = nil
	// So does the watchdog libvirt adds by itself to a q35 machine: the older
	// machine type has no such chip and the definition is refused with it.
	if w := domain.Devices.Watchdog; w != nil && w.Model == "itco" {
		domain.Devices.Watchdog = nil
	}

	// The setup disc: out with the old one, in with the new one when this kind reads it.
	disks := domain.Devices.Disks[:0]
	used := map[string]bool{}
	for _, d := range domain.Devices.Disks {
		if d.Device == "cdrom" && d.Source != nil && d.Source.File != nil && strings.HasSuffix(d.Source.File.File, "-seed.iso") {
			continue
		}
		if d.Target != nil {
			used[d.Target.Dev] = true
		}
		disks = append(disks, d)
	}
	domain.OS.SMBios, domain.SysInfo = nil, nil
	if seedPath != "" && kind != guestos.RouterOS {
		dev := "sda"
		for _, c := range "abcdefgh" {
			if dev = "sd" + string(c); !used[dev] {
				break
			}
		}
		disks = append(disks, libvirtxml.DomainDisk{
			Device:   "cdrom",
			Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
			Source:   &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: seedPath}},
			Target:   &libvirtxml.DomainDiskTarget{Dev: dev, Bus: "sata"},
			ReadOnly: &libvirtxml.DomainDiskReadOnly{},
		})
		seedHint(&domain)
	}
	domain.Devices.Disks = disks

	tuneForKind(&domain, kind)

	element := guestos.MetadataElement(kind)
	switch {
	case domain.Metadata == nil || !strings.Contains(domain.Metadata.XML, "maburvm:metadata"):
		extra := `<maburvm:metadata xmlns:maburvm="http://maburvm.local/schema">` + element + `</maburvm:metadata>`
		if domain.Metadata == nil {
			domain.Metadata = &libvirtxml.DomainMetadata{}
		}
		domain.Metadata.XML += extra
	case metadataOSTypeRe.MatchString(domain.Metadata.XML):
		domain.Metadata.XML = metadataOSTypeRe.ReplaceAllString(domain.Metadata.XML, element)
	default:
		domain.Metadata.XML = strings.Replace(domain.Metadata.XML, "</maburvm:metadata>", element+"</maburvm:metadata>", 1)
	}

	out, err := xml.MarshalIndent(&domain, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to write domain XML: %w", err)
	}
	return string(out), nil
}

// ConvertDomainKind redefines a VM for another kind of system and returns a
// function that puts the previous definition back, for when the reinstall it
// is part of fails. The VM keeps running on its old definition until it is
// next started; the reinstall stops and starts it.
func ConvertDomainKind(uuidStr, kind, seedPath string) (restore func() error, err error) {
	var previous string
	err = WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		// SECURE keeps the console password in the definition being saved back.
		previous, err = dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		converted, err := convertDomainKind(previous, kind, seedPath)
		if err != nil {
			return err
		}
		saved, err := redefineDomain(conn, converted)
		if err != nil {
			return fmt.Errorf("the hypervisor refused the definition for a %s system: %w", kind, err)
		}
		saved.Free()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return func() error {
		return WithConnection(func(conn *libvirt.Connect) error {
			saved, err := redefineDomain(conn, previous)
			if err != nil {
				return err
			}
			saved.Free()
			return nil
		})
	}, nil
}
