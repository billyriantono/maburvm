package storage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CheckAdoptDisk checks a disk a VM is about to be defined on without having
// been made here: the VM ran on another node, which went down. The disk must
// be a qcow2 file in a shared pool this node is a member of, mounted right
// now; no backup may have left the VM's newest writes in an overlay beside
// it; and no QEMU anywhere may hold it open.
func CheckAdoptDisk(vmID, target, path string) error {
	path = filepath.Clean(path)
	if _, ok := SharedPoolAt(path); !ok {
		return fmt.Errorf("%s is not in a shared pool this node is a member of", path)
	}
	if err := CheckSharedDir(filepath.Dir(path)); err != nil {
		return err
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("disk %s is not at %s as this node sees the shared pool", target, path)
	}
	if left := leftOverlays(vmID, target, filepath.Dir(path)); len(left) > 0 {
		return fmt.Errorf("a backup of the VM was under way when its node went down and left %s: if that file is newer than %s it holds the VM's latest writes, "+
			"which defining the VM now would lose. Commit it into the disk (qemu-img commit) or remove it, then try again", strings.Join(left, ", "), path)
	}
	info, err := NewQCOW2Manager().ImageInfo(path)
	if err != nil {
		return fmt.Errorf("disk %s cannot be read: %w", target, err)
	}
	if info.Format != "qcow2" {
		return fmt.Errorf("disk %s is in %s format; only qcow2 disks are recovered this way", target, info.Format)
	}
	if err := checkBackingBeside(path, info); err != nil {
		return fmt.Errorf("disk %s: %w", target, err)
	}
	held, err := ImageHeldOpen(path)
	if err != nil {
		return fmt.Errorf("disk %s could not be checked for use: %w", target, err)
	}
	if held {
		return fmt.Errorf("disk %s (%s) is open in a running VM: the VM still runs on another node, so the node it was on is not down. Nothing was defined", target, path)
	}
	return nil
}

// checkBackingBeside refuses a disk built on an image outside its own
// directory: this node opens the backing image at the same path, where its own
// disk may hold another file or none (older disks sit on a node's template
// copy). Only an image beside the disk, on the shared storage, is the same file.
func checkBackingBeside(path string, info *ImageInfo) error {
	dir := filepath.Dir(path)
	for p, n := path, 0; info.BackingFile != ""; n++ {
		b := info.FullBackingPath
		if b == "" {
			b = info.BackingFile
		}
		if !filepath.IsAbs(b) {
			b = filepath.Join(filepath.Dir(p), b)
		}
		b = filepath.Clean(b)
		if filepath.Dir(b) != dir {
			return fmt.Errorf("it is built on %s, which is not in the shared pool, so this node would not open the same image; recover the VM from its backup instead", b)
		}
		if n >= 16 {
			return fmt.Errorf("its chain of backing images is too long to check")
		}
		var err error
		if info, err = NewQCOW2Manager().ImageInfo(b); err != nil {
			return fmt.Errorf("its backing image %s cannot be read: %w", b, err)
		}
		p = b
	}
	return nil
}

// leftOverlays are the backup overlays of one of a VM's disks in dir.
func leftOverlays(vmID, target, dir string) []string {
	left, _ := filepath.Glob(filepath.Join(dir, vmID+"-"+target+OverlayMarker+"*"))
	return left
}

// ImageHeldOpen reports an image some QEMU has open for writing. qemu-img
// takes the image's lock to read it, and is refused while a running VM holds
// it, on this node or (the lock being on the shared storage) on another.
func ImageHeldOpen(path string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--output=json", path).CombinedOutput()
	if err == nil {
		return false, nil
	}
	if lockRefused(string(out)) {
		return true, nil
	}
	return false, fmt.Errorf("qemu-img: %w: %s", err, strings.TrimSpace(string(out)))
}

// lockRefused recognises qemu-img's answer for an image another process holds:
// `Failed to get shared "write" lock ... Is another process using the image`.
func lockRefused(out string) bool {
	return strings.Contains(out, "Failed to get") && strings.Contains(out, "lock")
}
