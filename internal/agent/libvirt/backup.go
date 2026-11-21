package libvirt

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/agent/storage"
	"libvirt.org/go/libvirt"
)

// How a backup's copy was taken, reported to the panel.
const (
	ConsistencyStopped = "stopped" // the VM was off
	ConsistencyFrozen  = "frozen"  // disk snapshot with the guest's file systems frozen
	ConsistencyCrash   = "crash"   // disk snapshot without a freeze: like a power cut
	ConsistencyLive    = "live"    // read while the VM wrote to it
)

// commitTimeout bounds committing one overlay back into its disk. The overlay
// holds only what the guest wrote during the export.
const commitTimeout = time.Hour

// BackupDisks lists the VM's disks as it runs now, primary first.
func BackupDisks(uuidStr string) ([]storage.DomainDisk, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %w", err)
	}
	var disks []storage.DomainDisk
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		x, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		disks, err = storage.DomainDisks(x)
		return err
	})
	if err == nil && len(disks) == 0 {
		err = fmt.Errorf("no disk found for domain %s", uuidStr)
	}
	return disks, err
}

// BackupHold keeps a running VM's disks still for a backup: until Release the
// guest writes to temporary overlays, and each disk's own image does not
// change.
type BackupHold struct {
	uuid        string
	Overlays    []storage.Overlay
	Consistency string
}

// HoldDisksForBackup moves a running VM's disks onto overlays in dir with one
// disk-only snapshot of all of them, freezing the guest's file systems through
// its guest agent while it is taken when the agent answers. It returns nil for
// a VM that is not running: its disks are still already.
func HoldDisksForBackup(uuidStr, dir string) (*BackupHold, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %w", err)
	}
	var hold *BackupHold
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		if active, err := dom.IsActive(); err != nil || !active {
			return err
		}
		x, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		snapXML, overlays, err := storage.BackupSnapshot(x, dir, uuidStr, time.Now().UnixNano())
		if err != nil {
			return err
		}
		flags := libvirt.DOMAIN_SNAPSHOT_CREATE_DISK_ONLY | libvirt.DOMAIN_SNAPSHOT_CREATE_NO_METADATA | libvirt.DOMAIN_SNAPSHOT_CREATE_ATOMIC
		consistency := ConsistencyFrozen
		held, err := snapshotOntoOverlays(dom, snapXML, overlays, flags|libvirt.DOMAIN_SNAPSHOT_CREATE_QUIESCE)
		if !held && !errors.Is(err, errDisksUnknown) {
			log.Printf("[Backup] VM %s: guest file systems not frozen (%v); taking the snapshot without", uuidStr, err)
			consistency = ConsistencyCrash
			held, err = snapshotOntoOverlays(dom, snapXML, overlays, flags)
		}
		if !held {
			return fmt.Errorf("failed to take the backup snapshot: %w", err)
		}
		if err != nil {
			log.Printf("[Backup] VM %s: the backup snapshot was taken, but libvirt reported: %v", uuidStr, err)
		}
		if consistency == ConsistencyFrozen {
			// libvirt thaws on its own, and reports a failed thaw as a failed
			// snapshot; a guest left frozen hangs on its next write.
			if terr := dom.FSThaw(nil, 0); terr != nil && err != nil {
				log.Printf("[Backup] WARNING: VM %s: its file systems may still be frozen: %v", uuidStr, terr)
			}
		}
		hold = &BackupHold{uuid: uuidStr, Overlays: overlays, Consistency: consistency}
		return nil
	})
	return hold, err
}

// errDisksUnknown: a snapshot call failed and the VM's disks could not be read
// back, so whether they are on the overlays is not known.
var errDisksUnknown = errors.New("the VM's disks could not be read back")

// snapshotOntoOverlays takes the backup snapshot and reports whether the VM's
// disks are on the overlays afterwards. A failure is checked against the disks:
// libvirt reports one for a snapshot it took when only the thaw after it
// failed, and an overlay the VM writes to must never be deleted.
func snapshotOntoOverlays(dom *libvirt.Domain, snapXML string, overlays []storage.Overlay, flags libvirt.DomainSnapshotCreateFlags) (bool, error) {
	snap, err := dom.CreateSnapshotXML(snapXML, flags)
	if err == nil {
		_ = snap.Free()
		return true, nil
	}
	x, xerr := dom.GetXMLDesc(0)
	var disks []storage.DomainDisk
	if xerr == nil {
		disks, xerr = storage.DomainDisks(x)
	}
	if xerr != nil {
		return false, fmt.Errorf("%w after a failed snapshot (%v): %v", errDisksUnknown, err, xerr)
	}
	if storage.OnBackupOverlay(disks) {
		return true, err
	}
	for _, ov := range overlays {
		_ = os.Remove(ov.Path)
	}
	return false, err
}

// ErrBackupRunning refuses a change to a VM's disks while a backup holds them
// on temporary overlays: it would land on an overlay and be lost, or strand the
// VM on it.
var ErrBackupRunning = errors.New("a backup of this VM is copying its disks; try again when it has finished")

// refuseDuringBackup returns ErrBackupRunning while a disk of the domain is on
// a backup overlay.
func refuseDuringBackup(dom *libvirt.Domain) error {
	x, err := dom.GetXMLDesc(0)
	if err != nil {
		return fmt.Errorf("failed to get domain XML: %w", err)
	}
	disks, err := storage.DomainDisks(x)
	if err != nil {
		return err
	}
	if storage.OnBackupOverlay(disks) {
		return ErrBackupRunning
	}
	return nil
}

// RefuseDuringBackup is refuseDuringBackup for a VM by UUID.
func RefuseDuringBackup(uuidStr string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		return refuseDuringBackup(dom)
	})
}

// backupBases lists the file images under a domain's backup overlays: a VM
// deleted mid-backup owns them too.
func backupBases(xmlDesc string) []string {
	disks, _ := storage.DomainDisks(xmlDesc)
	var out []string
	for _, d := range disks {
		if !storage.IsBackupOverlay(d.Path) {
			continue
		}
		if ov, err := overlayFromFile(d.Target, d.Path); err == nil && !ov.Block {
			out = append(out, ov.Base)
		}
	}
	return out
}

// Release commits every overlay back into its disk and removes it, leaving
// the VM on its own disks. It goes through every disk and reports what
// failed; a disk that could not be committed now stays on its overlay, which
// holds every write, and is committed when the agent next starts.
func (h *BackupHold) Release() error {
	if h == nil {
		return nil
	}
	var errs []error
	for _, ov := range h.Overlays {
		if err := commitOverlay(h.uuid, ov); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// commitOverlay puts one disk back on its own image: a block commit and pivot
// while the VM runs, qemu-img commit when it is off (it may have been shut down
// during the export).
func commitOverlay(uuidStr string, ov storage.Overlay) error {
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		active, err := dom.IsActive()
		if err != nil {
			return err
		}
		// Only a disk still on the overlay is committed. Committing one again
		// after its pivot would write its old blocks over newer ones.
		switch {
		case active && onOverlay(dom, ov, 0):
			err = activeCommit(dom, ov)
		case !active && onOverlay(dom, ov, libvirt.DOMAIN_XML_INACTIVE):
			err = offlineCommit(ov)
		}
		if err != nil {
			return err
		}
		// A VM started while its overlay was committed offline writes to it
		// again: keep it, and commit it live on the next try.
		startedOnOverlay := func() error {
			if now, aerr := dom.IsActive(); !active && (aerr != nil || (now && onOverlay(dom, ov, 0))) {
				return fmt.Errorf("VM %s was started on backup overlay %s while it was being committed; it is committed again later", uuidStr, ov.Path)
			}
			return nil
		}
		if err := startedOnOverlay(); err != nil {
			return err
		}
		// libvirt puts the saved definition back on pivot; a definition left on
		// a removed overlay would leave a VM that cannot start, so make sure.
		if err := repointSaved(conn, dom, ov); err != nil {
			return err
		}
		if err := startedOnOverlay(); err != nil {
			return err
		}
		if err := os.Remove(ov.Path); err != nil && !os.IsNotExist(err) {
			log.Printf("[Backup] VM %s: overlay %s committed but not removed: %v", uuidStr, ov.Path, err)
		}
		return nil
	})
}

func activeCommit(dom *libvirt.Domain, ov storage.Overlay) error {
	if err := dom.BlockCommit(ov.Target, "", "", 0, libvirt.DOMAIN_BLOCK_COMMIT_ACTIVE|libvirt.DOMAIN_BLOCK_COMMIT_SHALLOW); err != nil {
		// A commit started by an earlier attempt may still be running.
		if info, ierr := dom.GetBlockJobInfo(ov.Target, 0); ierr != nil || (info.Type == 0 && info.End == 0) {
			return fmt.Errorf("could not start committing %s back into %s: %w", ov.Target, ov.Base, err)
		}
	}
	deadline := time.Now().Add(commitTimeout)
	for time.Now().Before(deadline) {
		info, err := dom.GetBlockJobInfo(ov.Target, 0)
		if err != nil {
			return fmt.Errorf("could not follow the commit of %s: %w", ov.Target, err)
		}
		if info.Type == 0 && info.End == 0 {
			if onOverlay(dom, ov, 0) {
				return fmt.Errorf("the commit of %s stopped before the VM was moved back to %s", ov.Target, ov.Base)
			}
			return nil
		}
		// An active commit never ends by itself: once every block is copied it
		// waits to be pivoted. libvirt may report the copy done a moment before
		// it accepts the pivot, so a refusal is only a reason to look again.
		if info.End > 0 && info.Cur == info.End {
			if err := dom.BlockJobAbort(ov.Target, libvirt.DOMAIN_BLOCK_JOB_ABORT_PIVOT); err == nil && !onOverlay(dom, ov, 0) {
				return nil
			}
		}
		time.Sleep(time.Second)
	}
	_ = dom.BlockJobAbort(ov.Target, 0) // the VM stays on the overlay, which has every write
	return fmt.Errorf("committing %s back into %s did not finish within %s; the VM still runs on %s and the agent commits it when it next starts",
		ov.Target, ov.Base, commitTimeout, ov.Path)
}

// onOverlay reports whether the VM's running (flags 0) or saved definition
// still has the disk on the overlay. Unreadable counts as yes.
func onOverlay(dom *libvirt.Domain, ov storage.Overlay, flags libvirt.DomainXMLFlags) bool {
	x, err := dom.GetXMLDesc(flags)
	if err != nil {
		return true
	}
	disks, err := storage.DomainDisks(x)
	if err != nil {
		return true
	}
	for _, d := range disks {
		if d.Target == ov.Target {
			return d.Path == ov.Path
		}
	}
	return false
}

func offlineCommit(ov storage.Overlay) error {
	if _, err := os.Stat(ov.Path); os.IsNotExist(err) {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), commitTimeout)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "qemu-img", "commit", ov.Path).CombinedOutput(); err != nil {
		return fmt.Errorf("could not commit %s back into %s: %w (output: %s)", ov.Path, ov.Base, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// repointSaved rewrites the saved definition's disk from the overlay back to
// its own image, when it still names the overlay.
func repointSaved(conn *libvirt.Connect, dom *libvirt.Domain, ov storage.Overlay) error {
	x, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE | libvirt.DOMAIN_XML_SECURE)
	if err != nil {
		return fmt.Errorf("failed to read the saved definition: %w", err)
	}
	disks, err := storage.DomainDisks(x)
	if err != nil {
		return err
	}
	for _, d := range disks {
		if d.Target != ov.Target || d.Path != ov.Path {
			continue
		}
		out, changed, err := storage.RepointDisk(x, ov.Target, ov.Base, ov.Block, ov.BaseFormat)
		if err != nil || !changed {
			return err
		}
		redefined, err := redefineDomain(conn, out)
		if err != nil {
			return fmt.Errorf("failed to point %s back at %s in the saved definition: %w", ov.Target, ov.Base, err)
		}
		_ = redefined.Free()
	}
	return nil
}

// RecoverBackupOverlays commits back every disk an interrupted backup left on
// an overlay, and removes overlays nothing uses any more. Run when the agent
// starts, before any backup can be running.
func RecoverBackupOverlays() error {
	type stuck struct {
		uuid string
		ov   storage.Overlay
	}
	var found []stuck
	dirs := map[string]bool{}
	err := WithConnection(func(conn *libvirt.Connect) error {
		doms, err := conn.ListAllDomains(0)
		if err != nil {
			return fmt.Errorf("failed to list domains: %w", err)
		}
		defer func() {
			for i := range doms {
				_ = doms[i].Free()
			}
		}()
		for i := range doms {
			id, err := doms[i].GetUUIDString()
			if err != nil {
				continue
			}
			seen := map[string]bool{}
			for _, flags := range []libvirt.DomainXMLFlags{0, libvirt.DOMAIN_XML_INACTIVE} {
				x, err := doms[i].GetXMLDesc(flags)
				if err != nil {
					continue
				}
				disks, _ := storage.DomainDisks(x)
				for _, d := range disks {
					if !d.Block {
						dirs[filepath.Dir(d.Path)] = true
					}
					if !storage.IsBackupOverlay(d.Path) || seen[d.Target] {
						continue
					}
					seen[d.Target] = true
					ov, err := overlayFromFile(d.Target, d.Path)
					if err != nil {
						log.Printf("[Backup] VM %s: disk %s is on backup overlay %s, whose own image cannot be read: %v", id, d.Target, d.Path, err)
						continue
					}
					found = append(found, stuck{uuid: id, ov: ov})
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range found {
		log.Printf("[Backup] VM %s: disk %s was left on backup overlay %s; committing it back into %s", s.uuid, s.ov.Target, s.ov.Path, s.ov.Base)
		if err := commitOverlay(s.uuid, s.ov); err != nil {
			errs = append(errs, fmt.Errorf("VM %s disk %s: %w", s.uuid, s.ov.Target, err))
		}
	}
	// Overlays no domain uses are what a commit left behind before removing them.
	if inUse, err := DiskSourceFiles(); err == nil {
		for dir := range dirs {
			matches, _ := filepath.Glob(filepath.Join(dir, "*"+storage.OverlayMarker+"*"))
			for _, m := range matches {
				if !inUse[filepath.Clean(m)] {
					if err := os.Remove(m); err == nil {
						log.Printf("[Backup] removed unused backup overlay %s", m)
					}
				}
			}
		}
	}
	return errors.Join(errs...)
}

// overlayFromFile describes an overlay found on a disk from the overlay file
// itself: its backing file is the disk's own image.
func overlayFromFile(target, path string) (storage.Overlay, error) {
	info, err := storage.NewQCOW2Manager().ImageInfo(path)
	if err != nil {
		return storage.Overlay{}, err
	}
	base := info.FullBackingPath
	if base == "" {
		base = info.BackingFile
	}
	if base == "" {
		return storage.Overlay{}, fmt.Errorf("%s has no backing file", path)
	}
	format := info.BackingFormat
	if format == "" {
		format = "qcow2"
	}
	block := false
	if st, err := os.Stat(base); err == nil {
		block = st.Mode()&os.ModeDevice != 0
	}
	return storage.Overlay{Target: target, Base: base, BaseFormat: format, Block: block, Path: path}, nil
}

// dropSnapshots removes every snapshot of a domain. Called before its disks
// are replaced (reinstall, restore): a snapshot lives inside the image being
// thrown away, and a record left behind would offer a revert that cannot
// work. Each is deleted with its data where it can be, and as a record only
// where not.
func dropSnapshots(dom *libvirt.Domain) error {
	snaps, err := dom.ListAllSnapshots(0)
	if err != nil {
		return fmt.Errorf("failed to list snapshots: %w", err)
	}
	var errs []error
	for i := range snaps {
		name, _ := snaps[i].GetName()
		if err := snaps[i].Delete(0); err != nil {
			if merr := snaps[i].Delete(libvirt.DOMAIN_SNAPSHOT_DELETE_METADATA_ONLY); merr != nil {
				errs = append(errs, fmt.Errorf("snapshot %s: %w", name, merr))
			}
		}
		_ = snaps[i].Free()
	}
	return errors.Join(errs...)
}

// DropSnapshots removes every snapshot of a VM (see dropSnapshots).
func DropSnapshots(uuidStr string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		return dropSnapshots(dom)
	})
}
