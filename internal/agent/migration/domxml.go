package migration

import (
	"encoding/xml"
	"fmt"
	"net"
	"sort"
	"strings"

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
	Seed       bool // the VM's own cloud-init seed image
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
			dr.Format = d.Driver.Type
		}
		if s := d.Source; s != nil {
			switch {
			case s.File != nil:
				dr.SourceType, dr.Path = "file", s.File.File
			case s.Block != nil:
				dr.SourceType, dr.Path = "block", s.Block.Dev
			case s.Network != nil:
				dr.SourceType = "network"
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

// RewriteForTarget returns the domain with each drive in paths moved to its
// path on the destination, and its console on a port the destination picks
// (a fixed port may be taken there). Every target in paths must exist.
func RewriteForTarget(domXML string, paths map[string]string) (string, error) {
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
		p, ok := paths[d.Target.Dev]
		if !ok {
			continue
		}
		if d.Source == nil || d.Source.File == nil {
			return "", fmt.Errorf("drive %s is not a file and cannot be moved", d.Target.Dev)
		}
		d.Source.File.File = p
		// The file on the destination is a whole image; a chain from here would
		// name files that are not there.
		d.BackingStore = nil
		done[d.Target.Dev] = true
	}
	targets := make([]string, 0, len(paths))
	for t := range paths {
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

// portableListen keeps a console address every node has, and turns any other
// into loopback, where the console is reached through the agent.
func portableListen(addr string) string {
	ip := net.ParseIP(addr)
	if ip == nil || ip.IsLoopback() || ip.IsUnspecified() {
		return addr
	}
	return "127.0.0.1"
}
