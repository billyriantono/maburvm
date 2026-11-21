package storage

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"path/filepath"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// Compression of an exported disk, as the panel names it.
const (
	CompressionZlib = "gzip"
	CompressionZstd = "zstd"
	CompressionNone = "none"
)

// NormalizeCompression maps the panel's value to one of the three; anything
// unknown (or empty, from an older panel) is zlib, the old behaviour.
func NormalizeCompression(c string) string {
	switch c {
	case CompressionZstd, CompressionNone:
		return c
	default:
		return CompressionZlib
	}
}

// ExportArgs is the qemu-img command line exporting source to a standalone
// qcow2 at dest. -U (force-share) lets it read an image a running qemu holds
// open: the backing file under a backup overlay, or a live disk when no
// snapshot could be taken. format is the disk's own: a raw disk read without it
// is probed, and a guest that wrote an image header with a backing file into
// its disk would have a host file copied into the backup.
func ExportArgs(compression, format, source, dest string) []string {
	args := []string{"convert", "-U"}
	if format != "" {
		args = append(args, "-f", format)
	}
	args = append(args, "-O", "qcow2")
	switch NormalizeCompression(compression) {
	case CompressionZstd:
		args = append(args, "-c", "-o", "compression_type=zstd")
	case CompressionZlib:
		args = append(args, "-c")
	}
	return append(args, source, dest)
}

// StandaloneImage says why an image, as `qemu-img info --output=json` reports
// it when left to guess the format, would read another file, or nil. A copied
// disk's bytes are the guest's: a raw disk can carry a planted header naming a
// host file as its backing or data file.
func StandaloneImage(info []byte) error {
	var v struct {
		Format   string `json:"format"`
		Backing  string `json:"backing-filename"`
		Specific struct {
			Data struct {
				DataFile string `json:"data-file"`
			} `json:"data"`
		} `json:"format-specific"`
	}
	if err := json.Unmarshal(info, &v); err != nil {
		return fmt.Errorf("unreadable image information: %w", err)
	}
	switch {
	case v.Format != "raw" && v.Format != "qcow2":
		return fmt.Errorf("the disk reads as %q; only raw and qcow2 disks are copied", v.Format)
	case v.Backing != "":
		return fmt.Errorf("the disk names a backing file (%s)", v.Backing)
	case v.Specific.Data.DataFile != "":
		return fmt.Errorf("the disk names a data file (%s)", v.Specific.Data.DataFile)
	}
	return nil
}

// zstdUnsupported reports whether qemu-img refused the zstd option, which a
// build without libzstd does at once.
func zstdUnsupported(output []byte) bool {
	s := strings.ToLower(string(output))
	return strings.Contains(s, "compression_type") || strings.Contains(s, "zstd")
}

// DomainDisk is one disk device of a domain definition.
type DomainDisk struct {
	Target string // vda, vdb...
	Path   string // a file, or a block device
	Format string // the driver type: qcow2, raw
	Block  bool
}

// DomainDisks lists a domain's disks (device "disk" with a file or block
// source), in definition order; the first is the primary one. CD drives and
// the setup disc are not disks.
func DomainDisks(domainXML string) ([]DomainDisk, error) {
	var dom libvirtxml.Domain
	if err := xml.Unmarshal([]byte(domainXML), &dom); err != nil {
		return nil, fmt.Errorf("failed to parse domain XML: %w", err)
	}
	var out []DomainDisk
	if dom.Devices == nil {
		return out, nil
	}
	for _, d := range dom.Devices.Disks {
		if disk, ok := domainDisk(d); ok {
			out = append(out, disk)
		}
	}
	return out, nil
}

func domainDisk(d libvirtxml.DomainDisk) (DomainDisk, bool) {
	if d.Device != "" && d.Device != "disk" || d.Source == nil || d.Target == nil || d.Target.Dev == "" {
		return DomainDisk{}, false
	}
	disk := DomainDisk{Target: d.Target.Dev}
	switch {
	case d.Source.File != nil && d.Source.File.File != "":
		disk.Path = d.Source.File.File
	case d.Source.Block != nil && d.Source.Block.Dev != "":
		disk.Path, disk.Block = d.Source.Block.Dev, true
	default:
		return DomainDisk{}, false
	}
	disk.Format = "raw"
	if d.Driver != nil && d.Driver.Type != "" {
		disk.Format = d.Driver.Type
	}
	return disk, true
}

// DiskDevice is the definition of a disk at target backed by path: a raw block
// device for a /dev path, which gives freed blocks back to a thin pool, and a
// qcow2 file otherwise.
func DiskDevice(path, target string) libvirtxml.DomainDisk {
	d := libvirtxml.DomainDisk{Device: "disk", Target: &libvirtxml.DomainDiskTarget{Dev: target, Bus: "virtio"}}
	if strings.HasPrefix(path, "/dev/") {
		d.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw", Cache: "none", IO: "native", Discard: "unmap", DetectZeros: "unmap"}
		d.Source = &libvirtxml.DomainDiskSource{Block: &libvirtxml.DomainDiskSourceBlock{Dev: path}}
		return d
	}
	d.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "qcow2"}
	d.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: path}}
	return d
}

// OverlayMarker is in the name of every temporary backup overlay. A disk
// found on one (the agent stopped in the middle of a backup) is committed
// back when the agent starts.
const OverlayMarker = ".backup-overlay."

// IsBackupOverlay reports whether path is a temporary backup overlay.
func IsBackupOverlay(path string) bool {
	return strings.Contains(filepath.Base(path), OverlayMarker)
}

// OnBackupOverlay reports whether any of the disks is on a backup overlay.
func OnBackupOverlay(disks []DomainDisk) bool {
	for _, d := range disks {
		if IsBackupOverlay(d.Path) {
			return true
		}
	}
	return false
}

// Overlay is one disk moved onto a temporary overlay for a backup.
type Overlay struct {
	Target     string
	Base       string // the disk's own image, stable while the overlay exists
	BaseFormat string
	Block      bool
	Path       string // the overlay the VM writes to meanwhile
}

// BackupSnapshot plans a disk-only external snapshot that moves every disk of
// the domain onto an overlay in dir, so the disks themselves stop changing and
// can be copied. It returns the snapshot XML and the overlays it creates. CD
// drives take no part.
func BackupSnapshot(domainXML, dir, vmID string, stamp int64) (string, []Overlay, error) {
	var dom libvirtxml.Domain
	if err := xml.Unmarshal([]byte(domainXML), &dom); err != nil {
		return "", nil, fmt.Errorf("failed to parse domain XML: %w", err)
	}
	var b strings.Builder
	var overlays []Overlay
	fmt.Fprintf(&b, "<domainsnapshot><name>backup-%d</name><disks>", stamp)
	if dom.Devices != nil {
		for _, d := range dom.Devices.Disks {
			if d.Target == nil || d.Target.Dev == "" {
				continue
			}
			disk, ok := domainDisk(d)
			// libvirt refuses an external snapshot of a read-only disk, which would
			// fail the whole atomic snapshot; such a disk does not change anyway.
			if !ok || d.ReadOnly != nil {
				fmt.Fprintf(&b, "<disk name=%s snapshot=\"no\"/>", xmlAttr(d.Target.Dev))
				continue
			}
			if IsBackupOverlay(disk.Path) {
				return "", nil, fmt.Errorf("disk %s is still on the overlay of an earlier backup (%s)", disk.Target, disk.Path)
			}
			ov := Overlay{Target: disk.Target, Base: disk.Path, BaseFormat: disk.Format, Block: disk.Block,
				Path: filepath.Join(dir, fmt.Sprintf("%s-%s%s%d.qcow2", vmID, disk.Target, OverlayMarker, stamp))}
			overlays = append(overlays, ov)
			fmt.Fprintf(&b, "<disk name=%s snapshot=\"external\" type=\"file\"><driver type=\"qcow2\"/><source file=%s/></disk>",
				xmlAttr(ov.Target), xmlAttr(ov.Path))
		}
	}
	b.WriteString("</disks></domainsnapshot>")
	if len(overlays) == 0 {
		return "", nil, fmt.Errorf("the domain has no disk to back up")
	}
	return b.String(), overlays, nil
}

// xmlAttr is s as a quoted, escaped XML attribute value.
func xmlAttr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	_ = xml.EscapeText(&b, []byte(s))
	b.WriteByte('"')
	return b.String()
}

// RepointDisk rewrites the disk with target in a domain definition to source
// (a file, or a block device) with the given driver format. Used to take a
// stopped VM off a backup overlay once it has been committed.
func RepointDisk(domainXML, target, source string, block bool, format string) (string, bool, error) {
	var dom libvirtxml.Domain
	if err := xml.Unmarshal([]byte(domainXML), &dom); err != nil {
		return "", false, fmt.Errorf("failed to parse domain XML: %w", err)
	}
	if dom.Devices == nil {
		return domainXML, false, nil
	}
	changed := false
	for i := range dom.Devices.Disks {
		d := &dom.Devices.Disks[i]
		if d.Target == nil || d.Target.Dev != target || d.Source == nil {
			continue
		}
		if block {
			d.Source = &libvirtxml.DomainDiskSource{Block: &libvirtxml.DomainDiskSourceBlock{Dev: source}}
		} else {
			d.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: source}}
		}
		d.BackingStore = nil
		if d.Driver == nil {
			d.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu"}
		}
		d.Driver.Type = format
		changed = true
	}
	if !changed {
		return domainXML, false, nil
	}
	out, err := dom.Marshal()
	return out, true, err
}

// ExtraDiskKey is where the disk with target goes, beside the primary disk's
// backup at primaryKey.
func ExtraDiskKey(primaryKey, target string) string {
	if base, ok := strings.CutSuffix(primaryKey, ".qcow2"); ok {
		return base + "-" + target + ".qcow2"
	}
	return primaryKey + "-" + target
}

// RestoredDiskPath is where a data disk of a VM restored as a new one is put:
// beside its primary disk, named like any other data disk of the VM.
func RestoredDiskPath(dir, vmID, target string) string {
	return filepath.Join(dir, vmID+"-data-"+target+".qcow2")
}

// MissingTargets lists the targets a backup restores that the VM has no disk
// for. A restore in place refuses them: there is nowhere to put the data.
func MissingTargets(want []string, have []DomainDisk) []string {
	present := map[string]bool{}
	for _, d := range have {
		present[d.Target] = true
	}
	var missing []string
	for _, t := range want {
		if !present[t] {
			missing = append(missing, t)
		}
	}
	return missing
}

// How a restored image is put in place of a VM's disk.
const (
	RestoreRename      = "rename"       // a qcow2 file: the downloaded image becomes it
	RestoreConvertFile = "convert-file" // a raw file: converted beside it, then swapped in
	RestoreWriteDevice = "write-device" // a block device: written over, in place
)

// RestoreMethod picks how a backup image replaces the disk.
func RestoreMethod(d DomainDisk) string {
	switch {
	case d.Block:
		return RestoreWriteDevice
	case d.Format == "qcow2":
		return RestoreRename
	default:
		return RestoreConvertFile
	}
}

// SnapshotBlocker says why the VM's disks cannot take a snapshot, or "".
// Snapshots live inside qcow2 images; a raw disk or a logical volume has
// nowhere to keep one, so backups are the way to keep its state.
func SnapshotBlocker(disks []DomainDisk) string {
	for _, d := range disks {
		if d.Block {
			return fmt.Sprintf("snapshots need qcow2 disks, and disk %s is the block device %s (LVM storage); take a backup instead", d.Target, d.Path)
		}
		if d.Format != "qcow2" {
			return fmt.Sprintf("snapshots need qcow2 disks, and disk %s is %s; take a backup instead", d.Target, d.Format)
		}
	}
	return ""
}
