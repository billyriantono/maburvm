package storage

import (
	"encoding/xml"
	"fmt"

	"libvirt.org/go/libvirtxml"
)

// The CD drives ISOs go in: the install drive, which boots first while it
// holds an ISO, and a second one for a drivers disc beside an installer. The
// setup disc (cloud-init seed) is sda.
const (
	InstallISOTarget = "sdb"
	DriversISOTarget = "sdc"
)

// ISOTarget is the drive of an ISO slot (1, or 0 for the default, and 2).
func ISOTarget(slot int) (string, error) {
	switch slot {
	case 0, 1:
		return InstallISOTarget, nil
	case 2:
		return DriversISOTarget, nil
	}
	return "", fmt.Errorf("there is no ISO drive %d; drive 1 is the install drive and drive 2 the drivers drive", slot)
}

// CDROMDevice is a read-only CD drive at target holding the ISO at path, or
// empty for "". A drive whose ISO has gone (a customer's download is evicted
// once nothing uses it) starts empty instead of stopping the VM from starting.
func CDROMDevice(target, path string) libvirtxml.DomainDisk {
	d := libvirtxml.DomainDisk{
		Device:   "cdrom",
		Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
		Target:   &libvirtxml.DomainDiskTarget{Dev: target, Bus: "sata"},
		ReadOnly: &libvirtxml.DomainDiskReadOnly{},
	}
	if path != "" {
		d.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: path}, StartupPolicy: "optional"}
	}
	return d
}

// FindDrive returns the drive at target of a domain definition.
func FindDrive(domainXML, target string) (libvirtxml.DomainDisk, bool, error) {
	var dom libvirtxml.Domain
	if err := xml.Unmarshal([]byte(domainXML), &dom); err != nil {
		return libvirtxml.DomainDisk{}, false, fmt.Errorf("failed to parse domain XML: %w", err)
	}
	if dom.Devices != nil {
		for _, d := range dom.Devices.Disks {
			if d.Target != nil && d.Target.Dev == target {
				return d, true, nil
			}
		}
	}
	return libvirtxml.DomainDisk{}, false, nil
}

// DrivePath is the file a drive holds; "" for an empty drive.
func DrivePath(d libvirtxml.DomainDisk) string {
	if d.Source == nil || d.Source.File == nil {
		return ""
	}
	return d.Source.File.File
}

// SetISODrive puts the ISO at path ("" ejects) into the CD drive at target of
// a domain definition, adding the drive when it has none. The install drive
// holding an ISO boots first, then the disks; with it empty the domain boots
// from its disk again as it did before.
func SetISODrive(domainXML, target, path string) (string, error) {
	var dom libvirtxml.Domain
	if err := xml.Unmarshal([]byte(domainXML), &dom); err != nil {
		return "", fmt.Errorf("failed to parse domain XML: %w", err)
	}
	if dom.Devices == nil {
		return "", fmt.Errorf("the domain has no devices")
	}
	drive := CDROMDevice(target, path)
	replaced := false
	for i := range dom.Devices.Disks {
		d := &dom.Devices.Disks[i]
		if d.Target == nil || d.Target.Dev != target {
			continue
		}
		if d.Device != "cdrom" {
			return "", fmt.Errorf("%s is a %s, not a CD drive", target, d.Device)
		}
		drive.Target.Bus = d.Target.Bus
		*d = drive
		replaced = true
	}
	// Emptying a drive the domain never had adds none.
	if !replaced && path != "" {
		dom.Devices.Disks = append(dom.Devices.Disks, drive)
	}
	if target == InstallISOTarget {
		applyBootOrder(&dom, path != "")
	}
	return dom.Marshal()
}

// applyBootOrder boots the install drive first and the disks after it
// (per-device order, which rules out the domain-wide boot list), or puts the
// domain-wide "disk, then CD" order back.
func applyBootOrder(dom *libvirtxml.Domain, isoFirst bool) {
	next := uint(2) // libvirt refuses two devices with the same order
	for i := range dom.Devices.Disks {
		d := &dom.Devices.Disks[i]
		d.Boot = nil
		if !isoFirst {
			continue
		}
		switch {
		case d.Target != nil && d.Target.Dev == InstallISOTarget:
			d.Boot = &libvirtxml.DomainDeviceBoot{Order: 1}
		case d.Device == "" || d.Device == "disk":
			d.Boot = &libvirtxml.DomainDeviceBoot{Order: next}
			next++
		}
	}
	if dom.OS == nil {
		return
	}
	if isoFirst {
		dom.OS.BootDevices = nil
	} else {
		dom.OS.BootDevices = []libvirtxml.DomainBootDevice{{Dev: "hd"}, {Dev: "cdrom"}}
	}
}
