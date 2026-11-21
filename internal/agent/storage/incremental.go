package storage

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// Incremental backups use libvirt's backup API with checkpoints (persistent
// dirty bitmaps in each qcow2 disk). libvirt enabled it by default in 7.2.0,
// once QEMU 6.0 stabilised what it relies on; anything older gets full
// backups.
const (
	minLibvirtIncremental = 7_002_000
	minQemuIncremental    = 6_000_000
)

// CheckpointPrefix marks the checkpoints MaburVM makes.
const CheckpointPrefix = models.BackupCheckpointPrefix

// IncrementalSupport says whether a node can run the backup API, from its
// libvirt and QEMU versions (as libvirt encodes them: major*1e6+minor*1e3+micro)
// and its domain capabilities, which since libvirt 7.2 report backup support.
func IncrementalSupport(libvirtVer, qemuVer uint32, domcaps string) (bool, string) {
	if libvirtVer < minLibvirtIncremental {
		return false, fmt.Sprintf("libvirt %s on this node is older than 7.2", versionString(libvirtVer))
	}
	if qemuVer < minQemuIncremental {
		return false, fmt.Sprintf("QEMU %s on this node is older than 6.0", versionString(qemuVer))
	}
	var caps struct {
		Backup *struct {
			Supported string `xml:"supported,attr"`
		} `xml:"features>backup"`
	}
	if domcaps != "" && xml.Unmarshal([]byte(domcaps), &caps) == nil && caps.Backup != nil && caps.Backup.Supported == "no" {
		return false, "libvirt on this node reports that backups are not supported"
	}
	return true, ""
}

func versionString(v uint32) string {
	return fmt.Sprintf("%d.%d.%d", v/1_000_000, v/1000%1000, v%1000)
}

var checkpointName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

// ValidCheckpointName accepts the names the panel gives checkpoints.
func ValidCheckpointName(n string) bool { return checkpointName.MatchString(n) }

// ChainableDisks reports whether every disk can carry a checkpoint: a dirty
// bitmap is stored in a qcow2 image, so a raw file or a logical volume cannot.
func ChainableDisks(disks []DomainDisk) (bool, string) {
	for _, d := range disks {
		if d.RBD || d.Block || d.Format != "qcow2" {
			return false, fmt.Sprintf("disk %s is %s, and only qcow2 files can track changes", d.Target, describeDisk(d))
		}
	}
	return true, ""
}

func describeDisk(d DomainDisk) string {
	if d.RBD {
		return "an image on Ceph RBD"
	}
	if d.Block {
		return "a block device"
	}
	if d.Format == "" {
		return "of unknown format"
	}
	return "a " + d.Format + " file"
}

type backupDiskXML struct {
	Name   string `xml:"name,attr"`
	Backup string `xml:"backup,attr"`
	Type   string `xml:"type,attr,omitempty"`
	Target *struct {
		File string `xml:"file,attr"`
	} `xml:"target"`
	Driver *struct {
		Type string `xml:"type,attr"`
	} `xml:"driver"`
}

type domainBackupXML struct {
	XMLName     xml.Name        `xml:"domainbackup"`
	Mode        string          `xml:"mode,attr"`
	Incremental string          `xml:"incremental,omitempty"`
	Disks       []backupDiskXML `xml:"disks>disk"`
}

// BackupXML is a push-mode backup of every disk of all into the file outputs
// names for it (by target); a disk without an output is left out. With
// incrementalFrom, only what changed since that checkpoint is written, and the
// rest of each output reads as unallocated.
func BackupXML(all []DomainDisk, outputs map[string]string, incrementalFrom string) (string, error) {
	b := domainBackupXML{Mode: "push", Incremental: incrementalFrom}
	for _, d := range all {
		out, ok := outputs[d.Target]
		if !ok {
			b.Disks = append(b.Disks, backupDiskXML{Name: d.Target, Backup: "no"})
			continue
		}
		disk := backupDiskXML{Name: d.Target, Backup: "yes", Type: "file"}
		disk.Target = &struct {
			File string `xml:"file,attr"`
		}{out}
		disk.Driver = &struct {
			Type string `xml:"type,attr"`
		}{"qcow2"}
		b.Disks = append(b.Disks, disk)
	}
	out, err := xml.Marshal(b)
	return string(out), err
}

type checkpointDiskXML struct {
	Name       string `xml:"name,attr"`
	Checkpoint string `xml:"checkpoint,attr"`
}

type domainCheckpointXML struct {
	XMLName xml.Name            `xml:"domaincheckpoint"`
	Name    string              `xml:"name"`
	Disks   []checkpointDiskXML `xml:"disks>disk"`
}

// CheckpointXML is a checkpoint tracking changes on the given targets of all.
func CheckpointXML(name string, all []DomainDisk, targets []string) (string, error) {
	c := domainCheckpointXML{Name: name}
	for _, d := range all {
		kind := "no"
		if slices.Contains(targets, d.Target) {
			kind = "bitmap"
		}
		c.Disks = append(c.Disks, checkpointDiskXML{Name: d.Target, Checkpoint: kind})
	}
	out, err := xml.Marshal(c)
	return string(out), err
}

// CheckpointTargets lists the disks a checkpoint tracks.
func CheckpointTargets(checkpointXML string) ([]string, error) {
	var c domainCheckpointXML
	if err := xml.Unmarshal([]byte(checkpointXML), &c); err != nil {
		return nil, err
	}
	var out []string
	for _, d := range c.Disks {
		if d.Checkpoint == "bitmap" {
			out = append(out, d.Name)
		}
	}
	return out, nil
}

// SameTargets reports whether two lists name the same disks.
func SameTargets(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// IsBackupImageKey accepts the keys backups are stored under: a .qcow2 file in
// a "backups" folder, never above the destination's root.
func IsBackupImageKey(key string) bool {
	if !strings.HasSuffix(key, ".qcow2") || strings.HasPrefix(key, "/") {
		return false
	}
	parts := strings.Split(key, "/")
	return !slices.Contains(parts, "..") && !slices.Contains(parts, ".") && !slices.Contains(parts, "") &&
		slices.Contains(parts[:len(parts)-1], "backups")
}

// BitmapNames lists the dirty bitmaps in a qcow2 image, from qemu-img info
// --output=json.
func BitmapNames(qemuImgInfo []byte) []string {
	var info struct {
		Spec struct {
			Data struct {
				Bitmaps []struct {
					Name string `json:"name"`
				} `json:"bitmaps"`
			} `json:"data"`
		} `json:"format-specific"`
	}
	if json.Unmarshal(qemuImgInfo, &info) != nil {
		return nil
	}
	var out []string
	for _, b := range info.Spec.Data.Bitmaps {
		out = append(out, b.Name)
	}
	return out
}

// DetachBacking clears the backing file an incremental export may name: it
// is a path on this node, and a restore points the image at the backup under
// it instead.
func DetachBacking(path string) error {
	if b, err := exec.Command("qemu-img", "rebase", "-u", "-f", "qcow2", "-b", "", path).CombinedOutput(); err != nil {
		return fmt.Errorf("qemu-img rebase failed: %w (%s)", err, strings.TrimSpace(string(b)))
	}
	return nil
}

// ChainCommands are the qemu-img runs that turn an incremental backup's
// downloaded layers (the full one first, the top last) into one standalone
// image at out: each layer is pointed at the one under it, then the top is
// read through all of them.
func ChainCommands(layers []string, out string) [][]string {
	var cmds [][]string
	for i := 1; i < len(layers); i++ {
		cmds = append(cmds, []string{"qemu-img", "rebase", "-u", "-f", "qcow2", "-F", "qcow2", "-b", layers[i-1], layers[i]})
	}
	return append(cmds, []string{"qemu-img", "convert", "-f", "qcow2", "-O", "qcow2", layers[len(layers)-1], out})
}

// MeasureRequired reads the bytes a conversion needs from qemu-img measure
// --output=json.
func MeasureRequired(out []byte) (int64, error) {
	var m struct {
		Required *int64 `json:"required"`
	}
	if err := json.Unmarshal(out, &m); err != nil || m.Required == nil {
		return 0, fmt.Errorf("qemu-img measure gave no size: %s", strings.TrimSpace(string(out)))
	}
	return *m.Required, nil
}

// FlattenChain runs ChainCommands. Before the final copy room is asked
// whether the bytes it needs fit. Layers must be absolute paths; out is
// removed when it fails.
func FlattenChain(ctx context.Context, layers []string, out string, room func(need int64) error) error {
	if len(layers) < 2 {
		return fmt.Errorf("a chain needs at least two images")
	}
	for _, l := range layers {
		if !filepath.IsAbs(l) {
			return fmt.Errorf("%s is not an absolute path", l)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Hour)
	defer cancel()
	run := func(c []string) ([]byte, error) {
		b, err := exec.CommandContext(ctx, c[0], c[1:]...).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("%s %s failed: %w (%s)", c[0], c[1], err, strings.TrimSpace(string(b)))
		}
		return b, nil
	}
	cmds := ChainCommands(layers, out)
	for _, c := range cmds[:len(cmds)-1] {
		if _, err := run(c); err != nil {
			return err
		}
	}
	if room != nil {
		b, err := run([]string{"qemu-img", "measure", "--output=json", "-O", "qcow2", layers[len(layers)-1]})
		if err != nil {
			return err
		}
		need, err := MeasureRequired(b)
		if err != nil {
			return err
		}
		if err := room(need); err != nil {
			return err
		}
	}
	if _, err := run(cmds[len(cmds)-1]); err != nil {
		_ = os.Remove(out)
		return err
	}
	return nil
}
