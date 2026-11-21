package libvirt

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/agent/storage"
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// AttachISOSlot puts the ISO at isoPath in one of the VM's CD drives (see
// storage.ISOTarget). The saved definition always takes it; a running VM
// takes it at once when it has that drive. A drive cannot be added to a
// running VM, so one that lacks it is told to stop once.
func AttachISOSlot(uuidStr, isoPath string, slot int) error {
	if _, err := os.Stat(isoPath); err != nil {
		return fmt.Errorf("ISO not found at %s: %w", isoPath, err)
	}
	return setISO(uuidStr, isoPath, slot)
}

// DetachISOSlot empties one of the VM's CD drives. The drive stays, so an ISO
// can go back in while the VM runs.
func DetachISOSlot(uuidStr string, slot int) error {
	return setISO(uuidStr, "", slot)
}

func setISO(uuidStr, isoPath string, slot int) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	target, err := storage.ISOTarget(slot)
	if err != nil {
		return err
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}
		if state == libvirt.DOMAIN_RUNNING || state == libvirt.DOMAIN_PAUSED {
			if err := changeLiveMedia(dom, target, isoPath, slot); err != nil {
				return err
			}
		}
		// SECURE keeps the console password in the definition saved back.
		inactive, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		out, err := storage.SetISODrive(inactive, target, isoPath)
		if err != nil {
			return err
		}
		saved, err := redefineDomain(conn, out)
		if err != nil {
			return fmt.Errorf("failed to save the CD drive in the VM's definition: %w", err)
		}
		saved.Free()
		return nil
	})
}

// changeLiveMedia swaps the disc in a running VM's drive, keeping the drive's
// running definition as it is apart from its source.
func changeLiveMedia(dom *libvirt.Domain, target, isoPath string, slot int) error {
	live, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_SECURE)
	if err != nil {
		return fmt.Errorf("failed to get domain XML: %w", err)
	}
	drive, ok, err := storage.FindDrive(live, target)
	if err != nil {
		return err
	}
	if !ok && isoPath == "" {
		return nil // nothing to eject
	}
	if !ok || drive.Device != "cdrom" {
		return fmt.Errorf("the VM has no CD drive %d while it runs; stop it once and attach the ISO, after which ISOs can be changed while it runs", slot)
	}
	policy := ""
	if drive.Source != nil {
		policy = drive.Source.StartupPolicy
	}
	drive.Source = nil
	if isoPath != "" {
		drive.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: isoPath}, StartupPolicy: policy}
	}
	x, err := drive.Marshal()
	if err != nil {
		return err
	}
	if err := dom.UpdateDeviceFlags(x, libvirt.DOMAIN_DEVICE_MODIFY_LIVE|libvirt.DOMAIN_DEVICE_MODIFY_FORCE); err != nil {
		return fmt.Errorf("the running VM did not take the disc: %w", err)
	}
	return nil
}

// ResizeDiskTarget grows one disk of a VM (target; "" is the primary disk) to
// diskGB. live reports a running VM that was given the new size at once;
// size is the disk's size afterwards. Like ResizeDisk it never shrinks, and a
// block device is grown by growBlock.
func ResizeDiskTarget(uuidStr, target string, diskGB int, growBlock func(dev string, want int64) error) (live bool, size int64, err error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return false, 0, fmt.Errorf("invalid UUID format: %w", err)
	}
	if diskGB <= 0 {
		return false, 0, fmt.Errorf("disk size must be positive")
	}
	err = WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		disks, err := storage.DomainDisks(xmlDesc)
		if err != nil {
			return err
		}
		var disk *storage.DomainDisk
		for i := range disks {
			if (target == "" && i == 0) || disks[i].Target == target {
				disk = &disks[i]
				break
			}
		}
		if disk == nil {
			return fmt.Errorf("the VM has no disk %s", target)
		}
		want := int64(diskGB) << 30
		state, _, _ := dom.GetState()
		running := state == libvirt.DOMAIN_RUNNING || state == libvirt.DOMAIN_PAUSED
		// The overlay is a file over the disk's own image: grown alone, its
		// commit back could not fit.
		if err := refuseDuringBackup(dom); err != nil {
			return err
		}
		if disk.Block {
			if have, serr := storage.DeviceSize(disk.Path); serr == nil && want <= have {
				size = have
				return nil
			}
			if growBlock == nil {
				return fmt.Errorf("the disk is the block device %s, which cannot be resized here", disk.Path)
			}
			if err := growBlock(disk.Path, want); err != nil {
				return err
			}
			have, serr := storage.DeviceSize(disk.Path)
			if serr != nil {
				return serr
			}
			size = have
			if running {
				if err := dom.BlockResize(disk.Target, uint64(have), libvirt.DOMAIN_BLOCK_RESIZE_BYTES); err != nil {
					return fmt.Errorf("the volume was grown, but the running VM was not told: %w; it sees the new size after a stop and start", err)
				}
				live = true
			}
			return nil
		}
		info, ierr := storage.NewQCOW2Manager().ImageInfo(disk.Path)
		if ierr == nil && want <= info.VirtualSize {
			size = info.VirtualSize
			return nil
		}
		if running {
			if err := dom.BlockResize(disk.Target, uint64(want), libvirt.DOMAIN_BLOCK_RESIZE_BYTES); err != nil {
				return fmt.Errorf("failed to live-resize disk: %w", err)
			}
			size, live = want, true
			return nil
		}
		if disk.Format != "qcow2" {
			return fmt.Errorf("disk %s is a %s image; only qcow2 files are grown while the VM is off", disk.Target, disk.Format)
		}
		if err := storage.NewQCOW2Manager().ResizeImage(disk.Path, diskGB); err != nil {
			return fmt.Errorf("failed to resize disk image: %w", err)
		}
		size = want
		return nil
	})
	return live, size, err
}

// FindDisk returns a VM's disk at target and whether the VM runs.
func FindDisk(uuidStr, target string) (storage.DomainDisk, bool, error) {
	var out storage.DomainDisk
	var running bool
	if _, err := uuid.Parse(uuidStr); err != nil {
		return out, false, fmt.Errorf("invalid UUID format: %w", err)
	}
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		state, _, err := dom.GetState()
		if err != nil {
			return err
		}
		running = state == libvirt.DOMAIN_RUNNING || state == libvirt.DOMAIN_PAUSED
		x, err := dom.GetXMLDesc(0)
		if err != nil {
			return err
		}
		disks, err := storage.DomainDisks(x)
		if err != nil {
			return err
		}
		for _, d := range disks {
			if d.Target == target {
				out = d
				return nil
			}
		}
		return fmt.Errorf("the VM has no disk %s", target)
	})
	return out, running, err
}

// SnapshotCount is the number of snapshots a VM has.
func SnapshotCount(uuidStr string) (int, error) {
	n := 0
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()
		snaps, err := dom.ListAllSnapshots(0)
		if err != nil {
			return err
		}
		n = len(snaps)
		for i := range snaps {
			_ = snaps[i].Free()
		}
		return nil
	})
	return n, err
}

// RepointStoppedDisk points a shut-off VM's disk at target to its new home in
// the saved definition. It refuses a VM that is not shut off: a disk copied
// while it was off is only whole while it stays off.
func RepointStoppedDisk(uuidStr, target, path, format string) error {
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		if state, _, err := dom.GetState(); err != nil || state != libvirt.DOMAIN_SHUTOFF {
			return fmt.Errorf("the VM was started while its disk was being copied; the copy is discarded and the VM keeps its disk where it was")
		}
		return repointDefinition(conn, dom, target, path, format)
	})
}

func repointDefinition(conn *libvirt.Connect, dom *libvirt.Domain, target, path, format string) error {
	x, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE)
	if err != nil {
		return fmt.Errorf("failed to read the saved definition: %w", err)
	}
	out, changed, err := storage.RepointDisk(x, target, path, strings.HasPrefix(path, "/dev/"), format)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("the VM's definition has no disk %s", target)
	}
	saved, err := redefineDomain(conn, out)
	if err != nil {
		return fmt.Errorf("failed to point %s at %s in the saved definition: %w", target, path, err)
	}
	_ = saved.Free()
	return nil
}

// BlockCopyDisk moves a running VM's disk at target onto destPath, which
// exists already (a qcow2 file or a logical volume at least the disk's size):
// every block is copied while the VM runs, then the VM switches to the copy
// and its saved definition follows. ctx ending cancels the copy, and the VM
// stays on its disk. progress is told the bytes copied of the total.
func BlockCopyDisk(ctx context.Context, uuidStr, target, destPath, destFormat string, progress func(total, done int64)) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	dest := storage.DiskDevice(destPath, target)
	dest.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu", Type: destFormat}
	dest.Target = nil
	destXML, err := dest.Marshal()
	if err != nil {
		return err
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
		// Checkpoints name the old image; the panel's next backup is a full one.
		if err := dropCheckpoints(dom); err != nil {
			log.Printf("[DiskMove] %s: backup checkpoints not removed: %v", uuidStr, err)
		}
		flags := libvirt.DOMAIN_BLOCK_COPY_REUSE_EXT | libvirt.DOMAIN_BLOCK_COPY_TRANSIENT_JOB
		if err := dom.BlockCopy(target, destXML, &libvirt.DomainBlockCopyParameters{}, flags); err != nil {
			return fmt.Errorf("could not start copying %s: %w", target, err)
		}
		onDest := func() bool {
			x, err := dom.GetXMLDesc(0)
			if err != nil {
				return false
			}
			d, ok, _ := storage.FindDrive(x, target)
			return ok && d.Source != nil && ((d.Source.File != nil && d.Source.File.File == destPath) || (d.Source.Block != nil && d.Source.Block.Dev == destPath))
		}
		tick := time.NewTicker(2 * time.Second)
		defer tick.Stop()
	copying:
		for {
			select {
			case <-ctx.Done():
				_ = dom.BlockJobAbort(target, 0) // the VM stays on its disk
				return fmt.Errorf("the copy of %s was cancelled; the VM stays on its disk", target)
			case <-tick.C:
			}
			info, err := dom.GetBlockJobInfo(target, 0)
			if err != nil {
				return fmt.Errorf("could not follow the copy of %s: %w", target, err)
			}
			if info.Type == 0 && info.End == 0 {
				if onDest() {
					break copying
				}
				return fmt.Errorf("the copy of %s stopped before the VM switched to it", target)
			}
			if progress != nil {
				progress(int64(info.End), int64(info.Cur))
			}
			// Every block is copied once Cur reaches End; libvirt may take the
			// switch a moment later, so a refusal is only a reason to look again.
			if info.End > 0 && info.Cur == info.End {
				if err := dom.BlockJobAbort(target, libvirt.DOMAIN_BLOCK_JOB_ABORT_PIVOT); err == nil && onDest() {
					break copying
				}
			}
		}
		return repointDefinition(conn, dom, target, destPath, destFormat)
	})
}
