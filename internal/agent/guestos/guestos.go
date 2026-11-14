// Package guestos holds what differs between the kinds of system a VM can run.
//
// Linux guests are set up through cloud-init and edits to their disk. Other
// systems have no such path, so each gets its own here. The package uses no
// libvirt bindings, which keeps it buildable and testable on any machine.
package guestos

import (
	"regexp"

	"github.com/maburvm/panel/internal/shared/models"
)

const (
	// Linux is the default and is what a VM that does not say is taken to be.
	Linux = models.OSTypeLinux
	// RouterOS is MikroTik's Cloud Hosted Router, versions 6 and 7.
	RouterOS = models.OSTypeRouterOS
	// Windows is Windows Server from an image carrying virtio drivers, the
	// QEMU guest agent and cloudbase-init (scripts/build-windows-template.sh).
	Windows = models.OSTypeWindows
)

// Known reports whether this agent can install the kind. A panel newer than
// the agent may ask for one it has never heard of; that must be refused, not
// installed as if it were Linux.
func Known(osType string) bool {
	return osType == Linux || osType == RouterOS || osType == Windows
}

// Normalize maps a recorded kind to one this agent handles, Linux when unsure.
func Normalize(osType string) string {
	if Known(osType) {
		return osType
	}
	return Linux
}

// MetadataElement is the line recorded in a domain's metadata so that later
// operations (password reset, address change, reinstall) know what is inside
// the VM without being told again.
func MetadataElement(osType string) string {
	return "<maburvm:os_type>" + Normalize(osType) + "</maburvm:os_type>"
}

var osTypeRe = regexp.MustCompile(`<maburvm:os_type>([a-z]+)</maburvm:os_type>`)

// FromDomainXML reads the kind back from a domain definition. A domain that
// does not say (every VM created before this existed, and every import) is
// treated as Linux, which is how all of them were handled before.
func FromDomainXML(domXML string) string {
	if m := osTypeRe.FindStringSubmatch(domXML); m != nil {
		return Normalize(m[1])
	}
	return Linux
}

// Machine is the QEMU machine type a kind of guest needs.
//
// RouterOS 6 runs a kernel from 2012 that cannot drive the modern-only virtio
// devices a q35 machine presents: it never finds its disk and sits at the boot
// loader. The older i440fx machine offers the same devices in a form it
// understands, and RouterOS 7 runs on it equally well, so every RouterOS guest
// gets it and a reinstall between the two versions needs no change of hardware.
func Machine(osType string) string {
	if Normalize(osType) == RouterOS {
		return "pc"
	}
	return "q35"
}
