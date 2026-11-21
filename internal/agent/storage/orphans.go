package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// OrphanMinAge keeps files written this recently out of the orphan list: a
// create, restore or export still at work writes files no domain uses yet.
const OrphanMinAge = 6 * time.Hour

// Orphan is a disk file or logical volume no domain on the node uses.
type Orphan struct {
	Path     string
	Kind     string // "file" or "lv"
	Size     int64
	Modified int64 // unix seconds; 0 for a volume
	Reason   string
	VMID     string
	MaburVM  bool
	Pool     string
}

var (
	uuidPart = `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`
	// The names the agent gives the files it makes in a pool.
	vmDiskName    = regexp.MustCompile(`^(` + uuidPart + `)\.(qcow2|raw|img)$`)
	vmSeedName    = regexp.MustCompile(`^(` + uuidPart + `)-seed\.iso$`)
	vmDataName    = regexp.MustCompile(`^(` + uuidPart + `)-data-[A-Za-z0-9]+\.(qcow2|raw|img)$`)
	vmLeftover    = regexp.MustCompile(`^(` + uuidPart + `)[^/]*\.(backup|restorebak)\.[0-9]+$`)
	vmExport      = regexp.MustCompile(`^(` + uuidPart + `)[^/]*-backup\.qcow2$`)
	vmOverlay     = regexp.MustCompile(`^(` + uuidPart + `)[^/]*` + regexp.QuoteMeta(OverlayMarker) + `[0-9]+\.qcow2$`)
	vmMoveCopy    = regexp.MustCompile(`^(` + uuidPart + `)[^/]*\.move-[0-9a-z]+\.part$`)
	diskExtension = regexp.MustCompile(`(?i)\.(qcow2|raw|img|iso|vmdk|vdi|vhdx?)(\.part)?$`)
)

// ClassifyDiskFile says what a file found in a pool directory looks like, for
// the orphan list: why it is there, the VM its name names, and whether the
// name is one MaburVM gives. ok is false for a file that is not a disk image
// at all, which the list leaves out.
func ClassifyDiskFile(name string) (reason, vmID string, maburvm, ok bool) {
	match := func(re *regexp.Regexp) bool {
		if m := re.FindStringSubmatch(name); m != nil {
			vmID = m[1]
			return true
		}
		return false
	}
	switch {
	case match(vmDiskName):
		return "VM system disk", vmID, true, true
	case match(vmDataName):
		return "VM data disk", vmID, true, true
	case match(vmSeedName):
		return "VM setup disc (cloud-init seed)", vmID, true, true
	case match(vmLeftover):
		return "copy kept by a reinstall or restore that did not finish", vmID, true, true
	case match(vmExport):
		return "backup export left behind", vmID, true, true
	case match(vmOverlay):
		return "backup overlay left behind", vmID, true, true
	case match(vmMoveCopy):
		return "unfinished disk move", vmID, true, true
	case strings.HasPrefix(name, "clone-") && strings.HasSuffix(name, ".qcow2"):
		return "copy pulled for a clone from another node", strings.TrimSuffix(strings.TrimPrefix(name, "clone-"), ".qcow2"), true, true
	case diskExtension.MatchString(name):
		if strings.HasSuffix(name, ".part") {
			return "unfinished download or copy", "", false, true
		}
		return "disk image not made by MaburVM", "", false, true
	}
	return "", "", false, false
}

// FindOrphanFiles lists the disk images directly in each pool directory that
// no domain uses (inUse holds every path a domain refers to), leaving out files
// written within minAge.
func FindOrphanFiles(dirs []string, inUse map[string]bool, minAge time.Duration, now time.Time) []Orphan {
	var out []Orphan
	seen := map[string]bool{}
	for _, dir := range dirs {
		dir = filepath.Clean(dir)
		if seen[dir] || !filepath.IsAbs(dir) || dir == "/" {
			continue
		}
		seen[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.Type().IsRegular() {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if inUse[p] {
				continue
			}
			reason, vmID, mine, ok := ClassifyDiskFile(e.Name())
			if !ok {
				continue
			}
			info, err := e.Info()
			if err != nil || now.Sub(info.ModTime()) < minAge {
				continue
			}
			out = append(out, Orphan{Path: p, Kind: "file", Size: info.Size(), Modified: info.ModTime().Unix(),
				Reason: reason, VMID: vmID, MaburVM: mine, Pool: dir})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ClassifyLV describes a logical volume for the orphan list.
func ClassifyLV(lv LV) (reason, vmID string, maburvm bool) {
	if rest, ok := strings.CutPrefix(lv.Name, "maburvm-vol-"); ok && lv.Tagged() {
		return "volume made from the panel (" + rest + ")", "", true
	}
	if lv.Tagged() && strings.HasPrefix(lv.Name, "maburvm-") && len(lv.Name) > len("maburvm-")+36 {
		id := lv.Name[len("maburvm-") : len("maburvm-")+36]
		if models.OwnsLVMName(id, lv.Name) {
			return "VM disk volume", id, true
		}
	}
	return "logical volume not made by MaburVM", "", false
}

// OrphanLVs lists the ordinary volumes in the pools that no domain uses and
// nothing holds open. inUse holds every device path a domain refers to, by
// any name the caller resolved it to.
func OrphanLVs(lvs []LV, pools []LVMPool, inUse map[string]bool) []Orphan {
	var out []Orphan
	for _, lv := range lvs {
		p, held := HeldBy(lv, pools)
		if !held || !lv.plain() || lv.Open() || inUse[lv.Path()] {
			continue
		}
		reason, vmID, mine := ClassifyLV(lv)
		out = append(out, Orphan{Path: lv.Path(), Kind: "lv", Size: lv.Size, Reason: reason, VMID: vmID, MaburVM: mine, Pool: p.String()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// ParseBackingChain reads `qemu-img info --backing-chain --output=json`: every
// file the image is built on, itself first.
func ParseBackingChain(out []byte) ([]string, error) {
	var chain []struct {
		Filename            string `json:"filename"`
		FullBackingFilename string `json:"full-backing-filename"`
	}
	if err := json.Unmarshal(out, &chain); err != nil {
		return nil, fmt.Errorf("unexpected qemu-img output: %w", err)
	}
	var files []string
	for _, c := range chain {
		for _, f := range []string{c.Filename, c.FullBackingFilename} {
			if f != "" && filepath.IsAbs(f) {
				files = append(files, filepath.Clean(f))
			}
		}
	}
	return files, nil
}

// BackingChain lists the files the image at path is built on, itself first.
func BackingChain(ctx context.Context, path string) ([]string, error) {
	out, err := exec.CommandContext(ctx, "qemu-img", "info", "--backing-chain", "-U", "--output=json", path).Output()
	if err != nil {
		return nil, fmt.Errorf("could not read the image chain of %s: %w", path, err)
	}
	return ParseBackingChain(out)
}
