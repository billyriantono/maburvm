package libvirt

import (
	"encoding/xml"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/agent/hwprofile"
	"github.com/maburvm/panel/internal/agent/storage"
	"github.com/maburvm/panel/internal/agent/vmhealth"
	"github.com/maburvm/panel/internal/shared/models"
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// applyNewHardware writes a new VM's hardware profile into its definition.
func applyNewHardware(domain *libvirtxml.Domain, c VMConfig) error {
	hwprofile.SetTopology(domain, c.Hardware)
	hwprofile.SetPinning(domain, c.PinnedCPUs, c.Hardware.NUMA, hwprofile.HostNUMANode)
	hwprofile.SetHugepages(domain, c.Hardware.Hugepages)
	hwprofile.SetWatchdog(domain, c.Hardware.Watchdog)
	hwprofile.SetRestartOnCrash(domain, c.Hardware.RestartOnCrash)
	if err := hwprofile.SetFirmware(domain, c.Firmware); err != nil {
		return err
	}
	if c.Hardware.TPM {
		hwprofile.SetTPM(domain)
	}
	return nil
}

// SetHardware changes the named parts of a VM's hardware profile in its saved
// definition (hwprofile.Mutable; all of them when fields is empty). A running
// VM keeps what it booted with until it is powered off and on: restartRequired.
func SetHardware(uuidStr string, hw models.Hardware, cpus []int, fields []string) (restartRequired bool, err error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return false, fmt.Errorf("invalid UUID format: %w", err)
	}
	err = WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		// SECURE keeps the console password in the definition being saved back.
		inactive, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE)
		if err != nil {
			return fmt.Errorf("failed to read the domain: %w", err)
		}
		var def libvirtxml.Domain
		if err := xml.Unmarshal([]byte(inactive), &def); err != nil {
			return fmt.Errorf("failed to parse the domain: %w", err)
		}
		if err := hwprofile.ApplyMutable(&def, hw, cpus, fields, hwprofile.HostNUMANode); err != nil {
			return err
		}
		out, err := xml.Marshal(&def)
		if err != nil {
			return fmt.Errorf("failed to write the domain: %w", err)
		}
		saved, err := redefineDomain(conn, string(out))
		if err != nil {
			return fmt.Errorf("the hypervisor refused the new hardware: %w", err)
		}
		saved.Free()
		restartRequired, _ = dom.IsActive()
		return nil
	})
	return restartRequired, err
}

func fitVCPUs(def *libvirtxml.Domain, vcpus int, cpus []int) bool {
	return hwprofile.FitVCPUs(def, vcpus, cpus, hwprofile.HostNUMANode)
}

// HostFirmware is the UEFI firmware this host has, plain and Secure Boot.
func HostFirmware() (plain, secure *hwprofile.Firmware) {
	return hwprofile.FindFirmware(hwprofile.Exists)
}

// CrashCandidate is a domain marked for restart on crash, as last seen.
type CrashCandidate struct {
	UUID    string
	Crashed bool
	Running bool
}

// RestartOnCrashDomains lists the domains marked for restart on crash and
// whether each is crashed (shut off by a crash, or held in the crashed state)
// or running.
func RestartOnCrashDomains() ([]CrashCandidate, error) {
	var out []CrashCandidate
	err := WithConnection(func(conn *libvirt.Connect) error {
		doms, err := conn.ListAllDomains(0)
		if err != nil {
			return err
		}
		for i := range doms {
			d := &doms[i]
			x, xerr := d.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
			state, reason, serr := d.GetState()
			id, ierr := d.GetUUIDString()
			if xerr == nil && serr == nil && ierr == nil && hwprofile.RestartOnCrash(x) {
				out = append(out, CrashCandidate{
					UUID:    id,
					Crashed: state == libvirt.DOMAIN_CRASHED || (state == libvirt.DOMAIN_SHUTOFF && libvirt.DomainShutoffReason(reason) == libvirt.DOMAIN_SHUTOFF_CRASHED),
					Running: state == libvirt.DOMAIN_RUNNING,
				})
			}
			d.Free()
		}
		return nil
	})
	return out, err
}

// StartCrashed starts a VM the restart-on-crash policy picked. A domain held
// in the crashed state is destroyed first.
func StartCrashed(uuidStr string) error {
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()
		if state, _, err := dom.GetState(); err == nil && state == libvirt.DOMAIN_CRASHED {
			if err := dom.Destroy(); err != nil {
				return fmt.Errorf("failed to clear the crashed state: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return StartVM(uuidStr)
}

// guestAgentTimeoutSec bounds one guest agent call made for the status path.
const guestAgentTimeoutSec = 2

// GuestAgentIPs asks the VM's guest agent for its addresses, giving it at most
// guestAgentTimeoutSec seconds (libvirt's own default can wait for ever on an
// agent that took the command and never answers).
func GuestAgentIPs(uuidStr string) ([]string, error) {
	var ips []string
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		reply, err := dom.QemuAgentCommand(`{"execute":"guest-network-get-interfaces"}`,
			libvirt.DomainQemuAgentCommandTimeout(guestAgentTimeoutSec), 0)
		if err != nil {
			return err
		}
		ips, err = vmhealth.InterfaceIPs(reply)
		return err
	})
	return ips, err
}

// GuestAgentHostname asks the VM's guest agent for its host name, bounded as
// GuestAgentIPs is.
func GuestAgentHostname(uuidStr string) (string, error) {
	var name string
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		reply, err := dom.QemuAgentCommand(`{"execute":"guest-get-host-name"}`,
			libvirt.DomainQemuAgentCommandTimeout(guestAgentTimeoutSec), 0)
		if err != nil {
			return err
		}
		name = vmhealth.HostName(reply)
		return nil
	})
	return name, err
}

// EnterRescue attaches the rescue system's disk at path to a stopped VM and
// boots it first; LeaveRescue takes it out and returns its path to delete.
func EnterRescue(uuidStr, path string) error {
	return editStoppedDomain(uuidStr, func(x string) (string, error) {
		if d, ok, err := storage.FindDrive(x, storage.InstallISOTarget); err == nil && ok && storage.DrivePath(d) != "" {
			return "", fmt.Errorf("an ISO is in the VM's install drive; eject it first")
		}
		return storage.SetRescueDisk(x, path)
	})
}

// LeaveRescue takes the rescue disk out of a stopped VM's definition and
// returns the file it was, "" when the VM had none.
func LeaveRescue(uuidStr string) (string, error) {
	var path string
	err := editStoppedDomain(uuidStr, func(x string) (string, error) {
		var err error
		if path, err = storage.RescueDiskPath(x); err != nil || path == "" {
			return "", err
		}
		return storage.SetRescueDisk(x, "")
	})
	return path, err
}

// editStoppedDomain rewrites a shut-off domain's saved definition with edit;
// an edit returning "" changes nothing.
func editStoppedDomain(uuidStr string, edit func(string) (string, error)) error {
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
			return fmt.Errorf("stop the VM first")
		}
		x, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE)
		if err != nil {
			return fmt.Errorf("failed to read the domain: %w", err)
		}
		edited, err := edit(x)
		if err != nil || edited == "" {
			return err
		}
		saved, err := redefineDomain(conn, edited)
		if err != nil {
			return fmt.Errorf("the hypervisor refused the change: %w", err)
		}
		saved.Free()
		return nil
	})
}

// takeDiskOnlySnapshot snapshots a running VM's qcow2 disks inside the images
// themselves, without its memory and without pausing it, optionally with its
// filesystems frozen through the guest agent, and records the snapshot with
// libvirt (see storage.DiskOnlyMarker).
func takeDiskOnlySnapshot(dom *libvirt.Domain, name string, created int64, quiesce bool) error {
	live, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("failed to read the domain: %w", err)
	}
	disks, err := storage.DomainDisks(live)
	if err != nil {
		return err
	}
	if why := storage.SnapshotBlocker(disks); why != "" {
		return fmt.Errorf("%s", why)
	}
	if len(disks) == 0 {
		return fmt.Errorf("the VM has no disks to snapshot")
	}
	reply, err := dom.QemuMonitorCommand(`{"execute":"query-block"}`, libvirt.DOMAIN_QEMU_MONITOR_COMMAND_DEFAULT)
	if err != nil {
		return fmt.Errorf("failed to list the VM's disks in QEMU: %w", err)
	}
	byTarget, err := storage.QueryBlockNodes(reply, disks)
	if err != nil {
		return err
	}
	nodes := make([]string, len(disks))
	for i, d := range disks {
		nodes[i] = byTarget[d.Target]
	}
	inactive, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE)
	if err != nil {
		return fmt.Errorf("failed to read the domain: %w", err)
	}
	snapXML, err := storage.DiskOnlySnapshotXML(name, created, inactive)
	if err != nil {
		return err
	}

	if quiesce {
		if err := fsFreeze(dom); err != nil {
			return fmt.Errorf("the guest agent could not freeze the filesystems (%v); take the snapshot without quiescing", err)
		}
	}
	_, snapErr := dom.QemuMonitorCommand(storage.InternalSnapshotCommand(name, nodes), libvirt.DOMAIN_QEMU_MONITOR_COMMAND_DEFAULT)
	if quiesce {
		if err := fsThaw(dom); err != nil {
			log.Printf("[Snapshot] snapshot %s: thawing the filesystems failed: %v", name, err)
		}
	}
	if snapErr != nil {
		return fmt.Errorf("QEMU did not take the snapshot: %w", snapErr)
	}
	snap, err := dom.CreateSnapshotXML(snapXML, libvirt.DOMAIN_SNAPSHOT_CREATE_REDEFINE)
	if err != nil {
		// Not recorded, so nothing would ever remove it: take it out again.
		for _, n := range nodes {
			if _, derr := dom.QemuMonitorCommand(storage.DeleteInternalSnapshotCommand(name, n), libvirt.DOMAIN_QEMU_MONITOR_COMMAND_DEFAULT); derr != nil {
				log.Printf("[Snapshot] snapshot %s left in %s after libvirt refused it: %v", name, n, derr)
			}
		}
		return fmt.Errorf("libvirt did not record the snapshot: %w", err)
	}
	snap.Free()
	return nil
}

// snapshotIsDiskOnly reports a snapshot taken by takeDiskOnlySnapshot.
func snapshotIsDiskOnly(snap *libvirt.DomainSnapshot) bool {
	x, err := snap.GetXMLDesc(0)
	return err == nil && storage.IsDiskOnlySnapshot(x)
}

// BootSetup is what a VM's saved definition says about how it boots: with
// UEFI, and with the built-in rescue system's disk attached.
func BootSetup(uuidStr string) (uefi, rescue bool, err error) {
	err = WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		x, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
		if err != nil {
			return fmt.Errorf("failed to read the domain: %w", err)
		}
		var def libvirtxml.Domain
		if err := xml.Unmarshal([]byte(x), &def); err != nil {
			return fmt.Errorf("failed to parse the domain: %w", err)
		}
		uefi = hwprofile.UsesUEFI(&def)
		path, _ := storage.RescueDiskPath(x)
		rescue = path != ""
		return nil
	})
	return uefi, rescue, err
}

// ResetNVRAM removes a stopped UEFI VM's copy of the variable store, so its
// next start begins from the firmware's own (libvirt copies it again).
func ResetNVRAM(uuidStr string) error {
	var path string
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		if active, _ := dom.IsActive(); active {
			return fmt.Errorf("the VM is running")
		}
		x, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
		if err != nil {
			return err
		}
		var def libvirtxml.Domain
		if err := xml.Unmarshal([]byte(x), &def); err != nil {
			return err
		}
		if def.OS != nil && def.OS.NVRam != nil && def.OS.NVRam.Template != "" {
			path = def.OS.NVRam.NVRam
		}
		return nil
	})
	if err != nil || path == "" || !strings.HasPrefix(path, hwprofile.NVRAMDir+"/") {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
