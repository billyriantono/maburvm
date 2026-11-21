package migration

import (
	"encoding/xml"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"

	"github.com/maburvm/panel/internal/agent/ceph"
	"github.com/maburvm/panel/internal/shared/models"
	"libvirt.org/go/libvirtxml"
)

// Drive is one disk or CD drive of a domain.
type Drive struct {
	Target     string
	Device     string // disk or cdrom
	Path       string
	Format     string
	SourceType string // file, block, network, volume; "" for an empty drive
	ReadOnly   bool
	Seed       bool   // the VM's own cloud-init seed image
	Cache      string // the driver's cache mode; "" is the hypervisor's default
}

// Copied reports whether a migration moves the drive's content. A read-only
// drive is not copied: the seed is carried separately, and any other image
// must be detached before the VM can move.
func (d Drive) Copied() bool { return d.Device == "disk" && !d.ReadOnly }

// Drives lists a domain's disk and CD drives in definition order.
func Drives(domXML string) ([]Drive, error) {
	var dom libvirtxml.Domain
	if err := dom.Unmarshal(domXML); err != nil {
		return nil, fmt.Errorf("failed to parse domain XML: %w", err)
	}
	var out []Drive
	if dom.Devices == nil {
		return out, nil
	}
	for _, d := range dom.Devices.Disks {
		if d.Target == nil || d.Target.Dev == "" {
			continue
		}
		dr := Drive{Target: d.Target.Dev, Device: d.Device, ReadOnly: d.ReadOnly != nil}
		if dr.Device == "" {
			dr.Device = "disk"
		}
		if d.Driver != nil {
			dr.Format, dr.Cache = d.Driver.Type, d.Driver.Cache
		}
		if s := d.Source; s != nil {
			switch {
			case s.File != nil:
				dr.SourceType, dr.Path = "file", s.File.File
			case s.Block != nil:
				dr.SourceType, dr.Path = "block", s.Block.Dev
			case s.Network != nil:
				dr.SourceType = "network"
				// An image on RBD is named by its disk path; it is shared
				// storage, and a move never copies it.
				dr.Path, _ = ceph.DiskPath(&d)
			case s.Volume != nil:
				dr.SourceType = "volume"
			}
		}
		dr.Seed = dr.Device == "cdrom" && strings.HasSuffix(dr.Path, "-seed.iso")
		out = append(out, dr)
	}
	return out, nil
}

// CPUOf returns the domain's CPU mode (host-passthrough, host-model or custom)
// and its <cpu> element, for a compatibility check on another host.
func CPUOf(domXML string) (mode, cpuXML string, err error) {
	var dom libvirtxml.Domain
	if err := dom.Unmarshal(domXML); err != nil {
		return "", "", fmt.Errorf("failed to parse domain XML: %w", err)
	}
	if dom.CPU == nil {
		return "custom", "", nil
	}
	mode = dom.CPU.Mode
	if mode == "" {
		mode = "custom"
	}
	if mode != "custom" || dom.CPU.Model == nil {
		return mode, "", nil
	}
	b, err := xml.Marshal(dom.CPU)
	if err != nil {
		return "", "", err
	}
	return mode, string(b), nil
}

// Bridges lists the bridges a domain's interfaces attach to, sorted.
func Bridges(domXML string) ([]string, error) {
	var dom libvirtxml.Domain
	if err := dom.Unmarshal(domXML); err != nil {
		return nil, fmt.Errorf("failed to parse domain XML: %w", err)
	}
	seen := map[string]bool{}
	var out []string
	if dom.Devices != nil {
		for _, i := range dom.Devices.Interfaces {
			if i.Source != nil && i.Source.Bridge != nil && i.Source.Bridge.Bridge != "" && !seen[i.Source.Bridge.Bridge] {
				seen[i.Source.Bridge.Bridge] = true
				out = append(out, i.Source.Bridge.Bridge)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// Dest is where a drive goes on the destination: a file, or a block device
// for a path under /dev. Format is the driver format there; "" keeps it.
type Dest struct {
	Path   string
	Format string
}

// DestOK reports a place a move may name for a drive: a clean absolute path,
// or, for a drive that is not copied, an image on RBD, which stays where it is.
func DestOK(p string, copied bool) bool {
	if ceph.IsPath(p) {
		_, err := models.ParseRBDPath(p)
		return err == nil && !copied
	}
	return filepath.IsAbs(p) && filepath.Clean(p) == p
}

// RewriteForTarget returns the domain with each drive in paths moved to its
// path on the destination, and its console on a port the destination picks
// (a fixed port may be taken there). Every target in paths must exist.
func RewriteForTarget(domXML string, paths map[string]string) (string, error) {
	dests := make(map[string]Dest, len(paths))
	for t, p := range paths {
		dests[t] = Dest{Path: p}
	}
	return RewriteDisks(domXML, dests)
}

// RewriteDisks is RewriteForTarget with a format per drive: a disk moving
// between a file and a logical volume changes both its source and its format.
func RewriteDisks(domXML string, dests map[string]Dest) (string, error) {
	var dom libvirtxml.Domain
	if err := dom.Unmarshal(domXML); err != nil {
		return "", fmt.Errorf("failed to parse domain XML: %w", err)
	}
	if dom.Devices == nil {
		return "", fmt.Errorf("the domain has no devices")
	}
	done := map[string]bool{}
	for i := range dom.Devices.Disks {
		d := &dom.Devices.Disks[i]
		if d.Target == nil {
			continue
		}
		dst, ok := dests[d.Target.Dev]
		if !ok {
			continue
		}
		// An image on RBD stays where it is: the destination reaches the same
		// cluster and opens the same image.
		if p, ok := ceph.DiskPath(d); ok {
			if dst.Path != p {
				return "", fmt.Errorf("drive %s is an image on Ceph RBD, which is not copied by a move", d.Target.Dev)
			}
			done[d.Target.Dev] = true
			continue
		}
		if d.Source == nil || (d.Source.File == nil && d.Source.Block == nil) {
			return "", fmt.Errorf("drive %s is neither a file nor a block device and cannot be moved", d.Target.Dev)
		}
		if err := repoint(d, dst); err != nil {
			return "", err
		}
		done[d.Target.Dev] = true
	}
	targets := make([]string, 0, len(dests))
	for t := range dests {
		targets = append(targets, t)
	}
	sort.Strings(targets)
	for _, t := range targets {
		if !done[t] {
			return "", fmt.Errorf("the domain has no drive %s", t)
		}
	}
	for i := range dom.Devices.Graphics {
		if v := dom.Devices.Graphics[i].VNC; v != nil {
			v.Port, v.AutoPort = 0, "yes"
			// An address of this node cannot be bound on the other one.
			v.Listen = portableListen(v.Listen)
			for j := range v.Listeners {
				if a := v.Listeners[j].Address; a != nil {
					a.Address = portableListen(a.Address)
				}
			}
		}
	}
	return dom.Marshal()
}

// repoint points one drive at dst. A drive that becomes a block device is
// tuned the way storage.DiskDevice defines new ones; one that stays what it
// was keeps its own definition.
func repoint(d *libvirtxml.DomainDisk, dst Dest) error {
	block := strings.HasPrefix(dst.Path, "/dev/")
	format := dst.Format
	if format == "" && d.Driver != nil {
		format = d.Driver.Type
	}
	if block && format != "raw" {
		return fmt.Errorf("drive %s would be a %q block device; a logical volume holds a raw disk", d.Target.Dev, format)
	}
	wasBlock := d.Source.Block != nil
	switch {
	case block && wasBlock:
		d.Source.Block.Dev = dst.Path
	case block:
		d.Source = &libvirtxml.DomainDiskSource{Block: &libvirtxml.DomainDiskSourceBlock{Dev: dst.Path}}
	case d.Source.File != nil:
		d.Source.File.File = dst.Path
	default:
		d.Source = &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: dst.Path}}
	}
	if d.Driver == nil {
		d.Driver = &libvirtxml.DomainDiskDriver{Name: "qemu"}
	}
	if format != "" {
		d.Driver.Type = format
	}
	if block && !wasBlock {
		d.Driver.Cache, d.Driver.IO = "none", "native"
	} else if !block && wasBlock {
		// Native I/O needs the page cache bypassed, which a file need not do.
		d.Driver.Cache, d.Driver.IO = "", ""
	}
	// The image on the destination is whole; a chain from here would name
	// files that are not there.
	d.BackingStore = nil
	return nil
}

// portableListen keeps a console address every node has, and turns any other
// into loopback, where the console is reached through the agent.
func portableListen(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return addr
	}
	return "127.0.0.1"
}
