package libvirt

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/agent/migration"
	"github.com/maburvm/panel/internal/agent/storage"
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// maxSeedImage bounds the seed image carried through the panel; a cloud-init
// seed is well under a megabyte.
const maxSeedImage = 8 << 20

// MigrationDrive is a drive of a VM with what a move needs to know about it.
type MigrationDrive struct {
	migration.Drive
	VirtualSize int64
	FileSize    int64
	BackingFile string
}

// MigrationVM describes a domain for a migration.
type MigrationVM struct {
	State         string
	CPUMode       string
	CPUXML        string
	Drives        []MigrationDrive
	Snapshots     int
	BackupRunning bool
	Bridges       []string
	DomainXML     string // persistent definition, with secrets (payload only)
	SeedImage     []byte // payload only
}

func stateName(s libvirt.DomainState) string {
	switch s {
	case libvirt.DOMAIN_RUNNING:
		return "running"
	case libvirt.DOMAIN_PAUSED:
		return "paused"
	case libvirt.DOMAIN_SHUTOFF:
		return "shutoff"
	case libvirt.DOMAIN_CRASHED:
		return "crashed"
	default:
		return "other"
	}
}

// MigrationInfo describes a domain for a migration. payload adds its
// definition and seed image, which an offline move carries to the destination.
func MigrationInfo(uuidStr string, payload bool) (*MigrationVM, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %w", err)
	}
	out := &MigrationVM{}
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}
		out.State = stateName(state)
		inactive, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		current := inactive
		if state == libvirt.DOMAIN_RUNNING || state == libvirt.DOMAIN_PAUSED {
			// The running CPU is the one to check: host-model is expanded there.
			if current, err = dom.GetXMLDesc(libvirt.DOMAIN_XML_SECURE | libvirt.DOMAIN_XML_MIGRATABLE); err != nil {
				return fmt.Errorf("failed to get running domain XML: %w", err)
			}
		}
		if out.CPUMode, _, err = migration.CPUOf(inactive); err != nil {
			return err
		}
		if _, out.CPUXML, err = migration.CPUOf(current); err != nil {
			return err
		}
		if out.Bridges, err = migration.Bridges(inactive); err != nil {
			return err
		}
		drives, err := migration.Drives(current)
		if err != nil {
			return err
		}
		qimg := storage.NewQCOW2Manager()
		for _, d := range drives {
			md := MigrationDrive{Drive: d}
			if d.SourceType == "file" && d.Path != "" {
				if fi, err := os.Stat(d.Path); err == nil {
					md.FileSize = fi.Size()
				}
				if d.Device == "disk" {
					info, err := qimg.ImageInfo(d.Path)
					if err != nil {
						return fmt.Errorf("drive %s: %w", d.Target, err)
					}
					md.VirtualSize, md.BackingFile = info.VirtualSize, info.BackingFile
				}
			}
			if d.SourceType == "block" && d.Device == "disk" && d.Path != "" {
				// A device is raw: its size is the disk, and all of it is copied.
				if n, err := storage.DeviceSize(d.Path); err == nil {
					md.VirtualSize, md.FileSize = n, n
				}
			}
			out.Drives = append(out.Drives, md)
			if d.Seed && payload {
				if md.FileSize > maxSeedImage {
					return fmt.Errorf("the seed image %s is %d bytes, more than can be carried", d.Path, md.FileSize)
				}
				if out.SeedImage, err = os.ReadFile(d.Path); err != nil {
					return fmt.Errorf("failed to read the seed image: %w", err)
				}
			}
		}
		disks, _ := storage.DomainDisks(current)
		out.BackupRunning = storage.OnBackupOverlay(disks)
		if snaps, err := dom.ListAllSnapshots(0); err == nil {
			out.Snapshots = len(snaps)
			for i := range snaps {
				_ = snaps[i].Free()
			}
		}
		if payload {
			out.DomainXML = inactive
		}
		return nil
	})
	return out, err
}

// HostCPU describes this host's CPU as libvirt sees it.
func HostCPU() (vendor, model string, features []string, err error) {
	err = WithConnection(func(conn *libvirt.Connect) error {
		raw, err := conn.GetCapabilities()
		if err != nil {
			return err
		}
		var caps libvirtxml.Caps
		if err := caps.Unmarshal(raw); err != nil {
			return err
		}
		if caps.Host.CPU == nil {
			return nil
		}
		vendor, model = caps.Host.CPU.Vendor, caps.Host.CPU.Model
		for _, f := range caps.Host.CPU.FeatureFlags {
			features = append(features, f.Name)
		}
		sort.Strings(features)
		return nil
	})
	return
}

// LibvirtVersion is the local libvirt's version, "" when unknown.
func LibvirtVersion() string {
	var v uint32
	_ = WithConnection(func(conn *libvirt.Connect) error {
		var err error
		v, err = conn.GetLibVersion()
		return err
	})
	if v == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d.%d", v/1000000, (v/1000)%1000, v%1000)
}

// CPUCompatible reports whether a guest CPU definition runs on this host. The
// hypervisor's view is asked first; libvirt older than 4.4 has only the host's.
func CPUCompatible(cpuXML string) (bool, error) {
	if cpuXML == "" {
		return true, nil
	}
	var res libvirt.CPUCompareResult
	err := WithConnection(func(conn *libvirt.Connect) error {
		var err error
		res, err = conn.CompareHypervisorCPU("", "x86_64", "", "kvm", cpuXML, 0)
		if err != nil {
			res, err = conn.CompareCPU(cpuXML, 0)
		}
		return err
	})
	if err != nil {
		return false, err
	}
	return res != libvirt.CPU_COMPARE_INCOMPATIBLE, nil
}

// TestRemote opens a libvirt connection to uri and asks for its hostname.
func TestRemote(uri string) error {
	conn, err := libvirt.NewConnect(uri)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.GetHostname()
	return err
}

// LiveMigrate moves a running domain to another node, copying the disks in
// paths whose copy flag is set (to a file, or a logical volume made ready
// there). The source definition is kept (switched to not
// start on boot) until the panel has seen the VM run on the destination. ctx
// ending aborts the move, and the VM carries on here.
func LiveMigrate(ctx context.Context, uuidStr, destURI, dataURI string, disks []migration.Drive, paths map[string]migration.Dest, bandwidthMiB uint64, progress func(total, done int64)) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		if err := refuseDuringBackup(dom); err != nil {
			return err
		}
		// Checkpoints do not move with the VM, and some libvirt versions refuse
		// to migrate one that has any. The panel's next backup is a full one.
		if err := dropCheckpoints(dom); err != nil {
			log.Printf("[Migrate] %s: backup checkpoints not removed: %v", uuidStr, err)
		}
		live, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_SECURE | libvirt.DOMAIN_XML_MIGRATABLE)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		persist, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE | libvirt.DOMAIN_XML_MIGRATABLE)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		if live, err = migration.RewriteDisks(live, paths); err != nil {
			return err
		}
		if persist, err = migration.RewriteDisks(persist, paths); err != nil {
			return err
		}
		var copyTargets []string
		for _, d := range disks {
			copyTargets = append(copyTargets, d.Target)
		}
		params := &libvirt.DomainMigrateParameters{
			URISet: true, URI: dataURI,
			DestXMLSet: true, DestXML: live,
			PersistXMLSet: true, PersistXML: persist,
			MigrateDisksSet: len(copyTargets) > 0, MigrateDisks: copyTargets,
		}
		if bandwidthMiB > 0 {
			params.BandwidthSet, params.Bandwidth = true, bandwidthMiB
		}
		flags := libvirt.MIGRATE_LIVE | libvirt.MIGRATE_PEER2PEER | libvirt.MIGRATE_PERSIST_DEST |
			libvirt.MIGRATE_ABORT_ON_ERROR | libvirt.MIGRATE_AUTO_CONVERGE
		if len(copyTargets) > 0 {
			flags |= libvirt.MIGRATE_NON_SHARED_DISK
		}

		done := make(chan error, 1)
		go func() { done <- dom.MigrateToURI3(destURI, params, flags) }()
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
		aborted := false
		ctxDone := ctx.Done()
		for {
			select {
			case err := <-done:
				if err != nil {
					if aborted {
						return fmt.Errorf("migration cancelled: %w", err)
					}
					return fmt.Errorf("migration failed: %w", err)
				}
				// Until the panel removes it, the copy left here must not boot
				// with the node and run the VM twice.
				if serr := dom.SetAutostart(false); serr != nil {
					log.Printf("[Migrate] WARNING: %s moved but its old definition here still starts on boot: %v", uuidStr, serr)
				}
				return nil
			case <-ctxDone:
				aborted, ctxDone = true, nil
				if err := dom.AbortJob(); err != nil {
					log.Printf("[Migrate] abort of %s: %v", uuidStr, err)
				}
			case <-tick.C:
				if progress == nil {
					continue
				}
				if st, err := dom.GetJobStats(0); err == nil && st.DataTotalSet {
					progress(int64(st.DataTotal), int64(st.DataProcessed))
				}
			}
		}
	})
}

// DefineMigrated defines a VM that arrived offline, set to start with the node.
func DefineMigrated(domXML string) error {
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.DomainDefineXML(sharedTuned(domXML))
		if err != nil {
			return fmt.Errorf("failed to define the VM: %w", err)
		}
		defer dom.Free()
		if err := dom.SetAutostart(true); err != nil {
			log.Printf("[Migrate] WARNING: defined VM does not start on boot: %v", err)
		}
		return nil
	})
}

// ConfirmArrived checks a VM is defined here after a move, makes it start with
// the node, and returns its state.
func ConfirmArrived(uuidStr string) (string, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return "", fmt.Errorf("invalid UUID format: %w", err)
	}
	var state string
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()
		if persistent, err := dom.IsPersistent(); err != nil || !persistent {
			return fmt.Errorf("the VM runs here but has no saved definition")
		}
		s, _, err := dom.GetState()
		if err != nil {
			return err
		}
		state = stateName(s)
		if err := dom.SetAutostart(true); err != nil {
			log.Printf("[Migrate] WARNING: %s does not start on boot here: %v", uuidStr, err)
		}
		// A disk that arrived in a shared pool bypasses the host cache from the
		// VM's next start; the node it came from may not know the pool.
		if saved, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE); err == nil {
			if out, changed, _ := storage.TuneSharedDisks(saved); changed {
				if d, err := conn.DomainDefineXML(out); err != nil {
					log.Printf("[Migrate] WARNING: %s keeps its disk cache mode on shared storage: %v", uuidStr, err)
				} else {
					_ = d.Free()
				}
			}
		}
		return nil
	})
	return state, err
}

// RemoveMovedSource removes the definition and disk files of a VM that has
// moved to another node. It refuses a VM that runs here, and deletes only the
// plain files the definition owned that no other domain uses. Files keep
// reports are on shared storage and are the VM's disks on its new node: they
// stay.
func RemoveMovedSource(uuidStr string, keep func(path string) bool) ([]string, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %w", err)
	}
	var owned []string
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()
		state, _, err := dom.GetState()
		if err != nil {
			return err
		}
		if state != libvirt.DOMAIN_SHUTOFF {
			return fmt.Errorf("the VM is %s on this node, so it was not removed", stateName(state))
		}
		xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
		if err != nil {
			return err
		}
		owned = ownedDiskPaths(xmlDesc)
		flags := libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA | libvirt.DOMAIN_UNDEFINE_MANAGED_SAVE |
			libvirt.DOMAIN_UNDEFINE_NVRAM | libvirt.DOMAIN_UNDEFINE_CHECKPOINTS_METADATA
		if err := dom.UndefineFlags(flags); err != nil {
			if plain := dom.Undefine(); plain != nil {
				return fmt.Errorf("failed to undefine the old copy: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	inUse, err := DiskSourceFiles()
	if err != nil {
		return nil, fmt.Errorf("the old definition is gone, but its files were kept: %w", err)
	}
	var removed []string
	for _, p := range owned {
		p = filepath.Clean(p)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if keep != nil && keep(p) {
			log.Printf("[Migrate] kept %s: the VM uses it on its new node (shared storage)", p)
			continue
		}
		if err != nil || !fi.Mode().IsRegular() || inUse[p] {
			log.Printf("[Migrate] kept %s: not a plain file of the moved VM alone", p)
			continue
		}
		if err := os.Remove(p); err != nil {
			return removed, fmt.Errorf("failed to remove %s: %w", p, err)
		}
		removed = append(removed, p)
	}
	return removed, nil
}

// SetAutostart turns a VM's start with the node on or off. found is false
// when the VM is not defined here.
func SetAutostart(uuidStr string, on bool) (found bool, err error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return false, fmt.Errorf("invalid UUID format: %w", err)
	}
	err = WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			if IsNoDomain(err) {
				return nil
			}
			return err
		}
		defer dom.Free()
		found = true
		return dom.SetAutostart(on)
	})
	return found, err
}

// UndefineStopped removes the definition of a VM that is shut off, and nothing
// else: its files stay.
func UndefineStopped(uuidStr string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()
		state, _, err := dom.GetState()
		if err != nil {
			return err
		}
		if state != libvirt.DOMAIN_SHUTOFF {
			return fmt.Errorf("the VM is %s here", stateName(state))
		}
		flags := libvirt.DOMAIN_UNDEFINE_SNAPSHOTS_METADATA | libvirt.DOMAIN_UNDEFINE_MANAGED_SAVE | libvirt.DOMAIN_UNDEFINE_NVRAM
		if err := dom.UndefineFlags(flags | libvirt.DOMAIN_UNDEFINE_CHECKPOINTS_METADATA); err != nil {
			// A libvirt too old for checkpoints rejects the flag.
			if err2 := dom.UndefineFlags(flags); err2 != nil {
				if plain := dom.Undefine(); plain != nil {
					return err
				}
			}
		}
		return nil
	})
}

// StoppedDrivePath returns the file or device behind a drive of a VM that is
// shut off.
// It opens its own connection: the caller is the migration shell, a separate
// process without the agent's pool.
func StoppedDrivePath(uuidStr, target string) (string, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return "", fmt.Errorf("invalid UUID format: %w", err)
	}
	conn, err := libvirt.NewConnect("qemu:///system")
	if err != nil {
		return "", err
	}
	defer conn.Close()
	dom, err := conn.LookupDomainByUUIDString(uuidStr)
	if err != nil {
		return "", err
	}
	defer dom.Free()
	state, _, err := dom.GetState()
	if err != nil {
		return "", err
	}
	if state != libvirt.DOMAIN_SHUTOFF {
		return "", fmt.Errorf("the VM is %s; its disks are copied only while it is shut off", stateName(state))
	}
	xmlDesc, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return "", err
	}
	drives, err := migration.Drives(xmlDesc)
	if err != nil {
		return "", err
	}
	for _, d := range drives {
		if target == migration.PrimaryDrive && d.Copied() {
			target = d.Target
		}
		if d.Target == target {
			if !d.Copied() || (d.SourceType != "file" && d.SourceType != "block") {
				return "", fmt.Errorf("drive %s is not a file or block disk", target)
			}
			return d.Path, nil
		}
	}
	return "", fmt.Errorf("the VM has no drive %s", target)
}

// DomainState returns a VM's state here; found is false when it is not defined.
func DomainState(uuidStr string) (state string, found bool, err error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return "", false, fmt.Errorf("invalid UUID format: %w", err)
	}
	err = WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			if IsNoDomain(err) {
				return nil
			}
			return err
		}
		defer dom.Free()
		s, _, err := dom.GetState()
		if err != nil {
			return err
		}
		state, found = stateName(s), true
		return nil
	})
	return
}
