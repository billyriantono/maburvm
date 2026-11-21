package libvirt

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/agent/storage"
	"libvirt.org/go/libvirt"
)

// ErrNotRunning: the backup API reads a running VM only.
var ErrNotRunning = errors.New("the VM is not running")

// IncrementalSupport reports whether this node's libvirt and QEMU can run the
// backup API with checkpoints, and why not.
func IncrementalSupport() (bool, string) {
	ok, why := false, ""
	err := WithConnection(func(conn *libvirt.Connect) error {
		lib, err := conn.GetLibVersion()
		if err != nil {
			return err
		}
		hv, err := conn.GetVersion()
		if err != nil {
			return err
		}
		caps, _ := conn.GetDomainCapabilities("", "", "", "kvm", 0)
		ok, why = storage.IncrementalSupport(lib, hv, caps)
		return nil
	})
	if err != nil {
		return false, fmt.Sprintf("could not read the libvirt version: %v", err)
	}
	return ok, why
}

// IsRunning reports whether the VM runs now.
func IsRunning(uuidStr string) (bool, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return false, fmt.Errorf("invalid UUID format: %w", err)
	}
	running := false
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		running, err = dom.IsActive()
		return err
	})
	return running, err
}

// CheckpointUsable reports whether the VM still has the checkpoint, tracking
// exactly these disks; why says what is wrong when not.
func CheckpointUsable(uuidStr, name string, targets []string) (bool, string) {
	ok, why := false, ""
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()
		cp, err := dom.CheckpointLookupByName(name, 0)
		if err != nil {
			why = "the previous backup's checkpoint is no longer on the VM (it was recreated, moved, restored or reinstalled)"
			return nil
		}
		defer cp.Free()
		x, err := cp.GetXMLDesc(0)
		if err != nil {
			return err
		}
		have, err := storage.CheckpointTargets(x)
		if err != nil {
			return err
		}
		if !storage.SameTargets(have, targets) {
			why = "the VM's disks changed since the previous backup"
			return nil
		}
		ok = true
		return nil
	})
	if err != nil {
		return false, fmt.Sprintf("could not read the VM's checkpoints: %v", err)
	}
	return ok, why
}

// apiPoll is how often a running backup job is looked at.
const apiPoll = 2 * time.Second

// RunBackup copies the VM's disks with libvirt's backup API: each disk with an
// output (by target) into that file, as it was the moment the job started.
// With incrementalFrom only what changed since that checkpoint is copied. With
// checkpoint a new one is created with the backup, over the same disks. The
// guest's file systems are frozen through its agent while the job starts. A
// failed or cancelled job removes the checkpoint it made, so the next backup
// from the older one still sees every change. It returns the consistency.
func RunBackup(ctx context.Context, uuidStr string, all []storage.DomainDisk, outputs map[string]string, incrementalFrom, checkpoint string, progress func(done, total int64)) (string, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return "", fmt.Errorf("invalid UUID format: %w", err)
	}
	backupXML, err := storage.BackupXML(all, outputs, incrementalFrom)
	if err != nil {
		return "", err
	}
	var checkpointXML string
	if checkpoint != "" {
		targets := make([]string, 0, len(outputs))
		for t := range outputs {
			targets = append(targets, t)
		}
		if checkpointXML, err = storage.CheckpointXML(checkpoint, all, targets); err != nil {
			return "", err
		}
	}
	consistency := ConsistencyFrozen
	err = WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		if active, err := dom.IsActive(); err != nil || !active {
			if err == nil {
				err = ErrNotRunning
			}
			return err
		}
		// Only the export lock's holder starts a backup here, so one already
		// running was left by an agent that has since restarted.
		if _, err := dom.BackupGetXMLDesc(0); err == nil {
			log.Printf("[Backup] VM %s: aborting a backup job nothing follows any more", uuidStr)
			_ = dom.AbortJob()
			waitJobGone(dom, time.Minute)
		}
		if ferr := dom.FSFreeze(nil, 0); ferr != nil {
			log.Printf("[Backup] VM %s: guest file systems not frozen (%v); backing up without", uuidStr, ferr)
			consistency = ConsistencyCrash
		}
		berr := dom.BackupBegin(backupXML, checkpointXML, 0)
		if consistency == ConsistencyFrozen {
			thaw(dom, uuidStr)
		}
		if berr != nil {
			return fmt.Errorf("libvirt refused the backup: %w", berr)
		}
		return waitBackup(ctx, dom, progress)
	})
	if err != nil && checkpoint != "" {
		if derr := DeleteCheckpoint(uuidStr, checkpoint); derr != nil {
			log.Printf("[Backup] VM %s: checkpoint %s of a failed backup kept: %v", uuidStr, checkpoint, derr)
		}
	}
	return consistency, err
}

// thaw unfreezes the guest; one left frozen hangs on its next write.
func thaw(dom *libvirt.Domain, uuidStr string) {
	var err error
	for i := 0; i < 5; i++ {
		if err = dom.FSThaw(nil, 0); err == nil {
			return
		}
		time.Sleep(time.Second)
	}
	log.Printf("[Backup] WARNING: VM %s: its file systems may still be frozen: %v", uuidStr, err)
}

// waitBackup follows the job until it ends; ctx ending aborts it.
func waitBackup(ctx context.Context, dom *libvirt.Domain, progress func(done, total int64)) error {
	t := time.NewTicker(apiPoll)
	defer t.Stop()
	aborted := false
	for {
		info, err := dom.GetJobInfo()
		if err != nil {
			return fmt.Errorf("could not follow the backup job: %w", err)
		}
		if info.Type == libvirt.DOMAIN_JOB_NONE {
			break
		}
		if progress != nil {
			if st, serr := dom.GetJobStats(0); serr == nil && st.DataTotalSet {
				progress(int64(st.DataProcessed), int64(st.DataTotal))
			}
		}
		select {
		case <-ctx.Done():
			if !aborted {
				aborted = true
				_ = dom.AbortJob()
			}
		case <-t.C:
		}
	}
	st, err := dom.GetJobStats(libvirt.DOMAIN_JOB_STATS_COMPLETED)
	if err != nil {
		return fmt.Errorf("the backup job ended and its result could not be read: %w", err)
	}
	if aborted {
		return fmt.Errorf("the backup was cancelled: %w", ctx.Err())
	}
	if st.OperationSet && st.Operation != libvirt.DOMAIN_JOB_OPERATION_BACKUP {
		return errors.New("the backup job ended and libvirt reported another job's result")
	}
	if st.Type != libvirt.DOMAIN_JOB_COMPLETED {
		msg := "no reason given"
		if st.ErrorMessageSet && st.ErrorMessage != "" {
			msg = st.ErrorMessage
		}
		return fmt.Errorf("the backup job failed: %s", msg)
	}
	return nil
}

func waitJobGone(dom *libvirt.Domain, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if info, err := dom.GetJobInfo(); err != nil || info.Type == libvirt.DOMAIN_JOB_NONE {
			return
		}
		time.Sleep(time.Second)
	}
}

// DeleteCheckpoint removes one checkpoint. Its changes are merged into its
// parent's, so a backup from the parent still copies them.
func DeleteCheckpoint(uuidStr, name string) error {
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()
		cp, err := dom.CheckpointLookupByName(name, 0)
		if err != nil {
			return nil // never made, or already gone
		}
		defer cp.Free()
		return cp.Delete(0)
	})
}

// PruneCheckpoints removes MaburVM's checkpoints other than keep. Only keep is
// built on next, and every bitmap left in a disk costs a little on each write.
func PruneCheckpoints(uuidStr, keep string) error {
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()
		cps, err := dom.ListAllCheckpoints(libvirt.DOMAIN_CHECKPOINT_LIST_TOPOLOGICAL)
		if err != nil {
			return err
		}
		var errs []error
		for i := range cps {
			name, _ := cps[i].GetName()
			if name != keep && strings.HasPrefix(name, storage.CheckpointPrefix) {
				if err := cps[i].Delete(0); err != nil {
					errs = append(errs, fmt.Errorf("checkpoint %s: %w", name, err))
				}
			}
			_ = cps[i].Free()
		}
		return errors.Join(errs...)
	})
}

// HasCheckpoints reports whether the VM has any checkpoint.
func HasCheckpoints(uuidStr string) bool {
	n := 0
	_ = WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return err
		}
		defer dom.Free()
		cps, err := dom.ListAllCheckpoints(0)
		for i := range cps {
			_ = cps[i].Free()
		}
		n = len(cps)
		return err
	})
	return n > 0
}

// dropCheckpoints removes every checkpoint of a domain. Called before its disks
// change in a way the bitmaps do not see (restore, reinstall, a snapshot taken
// or reverted, a move): the next backup must then be a full one. libvirt
// also refuses some of those operations while checkpoints exist.
func dropCheckpoints(dom *libvirt.Domain) error {
	cps, err := dom.ListAllCheckpoints(libvirt.DOMAIN_CHECKPOINT_LIST_ROOTS)
	if err != nil {
		var lverr libvirt.Error
		if errors.As(err, &lverr) && (lverr.Code == libvirt.ERR_NO_SUPPORT || lverr.Code == libvirt.ERR_OPERATION_UNSUPPORTED) {
			return nil // a libvirt without checkpoints has none
		}
		return fmt.Errorf("failed to list checkpoints: %w", err)
	}
	var errs []error
	metadataOnly := false
	for i := range cps {
		name, _ := cps[i].GetName()
		if err := cps[i].Delete(libvirt.DOMAIN_CHECKPOINT_DELETE_CHILDREN); err != nil {
			if merr := cps[i].Delete(libvirt.DOMAIN_CHECKPOINT_DELETE_CHILDREN | libvirt.DOMAIN_CHECKPOINT_DELETE_METADATA_ONLY); merr != nil {
				errs = append(errs, fmt.Errorf("checkpoint %s: %w", name, merr))
			} else {
				metadataOnly = true
			}
		}
		_ = cps[i].Free()
	}
	if metadataOnly {
		removeStaleBitmaps(dom)
	}
	return errors.Join(errs...)
}

// removeStaleBitmaps takes MaburVM's bitmaps out of a shut-off VM's qcow2
// disks when libvirt removed only the checkpoint records.
func removeStaleBitmaps(dom *libvirt.Domain) {
	if active, err := dom.IsActive(); err != nil || active {
		return
	}
	x, err := dom.GetXMLDesc(libvirt.DOMAIN_XML_INACTIVE)
	if err != nil {
		return
	}
	disks, _ := storage.DomainDisks(x)
	for _, d := range disks {
		if d.Block || d.Format != "qcow2" {
			continue
		}
		out, err := exec.Command("qemu-img", "info", "--output=json", "-U", d.Path).Output()
		if err != nil {
			continue
		}
		for _, name := range storage.BitmapNames(out) {
			if strings.HasPrefix(name, storage.CheckpointPrefix) {
				_ = exec.Command("qemu-img", "bitmap", "--remove", d.Path, name).Run()
			}
		}
	}
}

// DropCheckpoints removes every checkpoint of a VM (see dropCheckpoints).
func DropCheckpoints(uuidStr string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		return dropCheckpoints(dom)
	})
}
