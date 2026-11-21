package storage

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"libvirt.org/go/libvirtxml"
)

// A disk-only snapshot of a running VM is taken by QEMU straight into each
// qcow2 disk (blockdev-snapshot-internal-sync, no memory written, no pause),
// then recorded with libvirt as a snapshot of a shut-off VM: libvirt itself
// refuses an internal snapshot of a running VM without its memory. Reverting
// one powers the VM off, puts the disks back with qemu-img and starts it.

// DiskOnlyMarker is the description of such a snapshot, which is how a revert
// knows to start the VM again afterwards.
const DiskOnlyMarker = "Disk-only snapshot of a running VM (no memory state)"

// QueryBlockNodes maps each disk's target to the block node QEMU knows it by,
// from a query-block reply, matching on the image file.
func QueryBlockNodes(reply string, disks []DomainDisk) (map[string]string, error) {
	var r struct {
		Return []struct {
			Device   string `json:"device"`
			Inserted *struct {
				File     string `json:"file"`
				NodeName string `json:"node-name"`
			} `json:"inserted"`
		} `json:"return"`
	}
	if err := json.Unmarshal([]byte(reply), &r); err != nil {
		return nil, fmt.Errorf("unexpected query-block reply: %w", err)
	}
	out := map[string]string{}
	for _, d := range disks {
		for _, b := range r.Return {
			if b.Inserted == nil || b.Inserted.File != d.Path {
				continue
			}
			node := b.Inserted.NodeName
			if node == "" {
				node = b.Device
			}
			if node != "" {
				out[d.Target] = node
			}
		}
		if out[d.Target] == "" {
			return nil, fmt.Errorf("QEMU does not list disk %s (%s)", d.Target, d.Path)
		}
	}
	return out, nil
}

// InternalSnapshotCommand is the QMP transaction that snapshots the nodes
// together, so the disks are consistent with each other.
func InternalSnapshotCommand(name string, nodes []string) string {
	type action struct {
		Type string            `json:"type"`
		Data map[string]string `json:"data"`
	}
	acts := make([]action, len(nodes))
	for i, n := range nodes {
		acts[i] = action{Type: "blockdev-snapshot-internal-sync", Data: map[string]string{"device": n, "name": name}}
	}
	b, _ := json.Marshal(map[string]any{"execute": "transaction", "arguments": map[string]any{"actions": acts}})
	return string(b)
}

// DeleteInternalSnapshotCommand removes one node's internal snapshot again,
// for a snapshot libvirt would not record.
func DeleteInternalSnapshotCommand(name, node string) string {
	b, _ := json.Marshal(map[string]any{"execute": "blockdev-snapshot-delete-internal-sync",
		"arguments": map[string]string{"device": node, "name": name}})
	return string(b)
}

// DiskOnlySnapshotXML is the snapshot definition libvirt records for a
// disk-only snapshot: the qcow2 disks in internal, every other drive left out,
// and the domain's own saved definition (with its secrets) to come back to.
func DiskOnlySnapshotXML(name string, created int64, domainXML string) (string, error) {
	var dom libvirtxml.Domain
	if err := xml.Unmarshal([]byte(domainXML), &dom); err != nil {
		return "", fmt.Errorf("failed to parse domain XML: %w", err)
	}
	var disks strings.Builder
	if dom.Devices != nil {
		for _, d := range dom.Devices.Disks {
			if d.Target == nil || d.Target.Dev == "" {
				continue
			}
			mode := "no"
			if dd, ok := domainDisk(d); ok && !dd.Block && dd.Format == "qcow2" && d.ReadOnly == nil {
				mode = "internal"
			}
			disks.WriteString("<disk name=" + xmlAttr(d.Target.Dev) + " snapshot=" + xmlAttr(mode) + "/>")
		}
	}
	var b strings.Builder
	b.WriteString("<domainsnapshot><name>")
	_ = xml.EscapeText(&b, []byte(name))
	b.WriteString("</name><description>" + DiskOnlyMarker + "</description><state>shutoff</state><creationTime>" +
		strconv.FormatInt(created, 10) + "</creationTime><memory snapshot='no'/><disks>")
	b.WriteString(disks.String())
	b.WriteString("</disks>")
	b.WriteString(domainXML)
	b.WriteString("</domainsnapshot>")
	return b.String(), nil
}

// IsDiskOnlySnapshot reports a snapshot definition written by DiskOnlySnapshotXML.
func IsDiskOnlySnapshot(snapshotXML string) bool {
	return strings.Contains(snapshotXML, "<description>"+DiskOnlyMarker+"</description>")
}

// RescueTarget is the drive the built-in rescue system's disk is attached at.
const RescueTarget = "vdz"

// SetRescueDisk attaches the rescue system's disk at path to a domain
// definition and boots it first, the VM's own disks after it; "" takes it out
// and puts the usual "disk, then CD" order back.
func SetRescueDisk(domainXML, path string) (string, error) {
	var dom libvirtxml.Domain
	if err := xml.Unmarshal([]byte(domainXML), &dom); err != nil {
		return "", fmt.Errorf("failed to parse domain XML: %w", err)
	}
	if dom.Devices == nil || dom.OS == nil {
		return "", fmt.Errorf("the domain has no devices")
	}
	disks := dom.Devices.Disks[:0]
	for _, d := range dom.Devices.Disks {
		if d.Target != nil && d.Target.Dev == RescueTarget {
			continue
		}
		disks = append(disks, d)
	}
	if path != "" {
		disks = append(disks, DiskDevice(path, RescueTarget))
	}
	next := uint(2)
	for i := range disks {
		d := &disks[i]
		d.Boot = nil
		if path == "" {
			continue
		}
		switch {
		case d.Target != nil && d.Target.Dev == RescueTarget:
			d.Boot = &libvirtxml.DomainDeviceBoot{Order: 1}
		case d.Device == "" || d.Device == "disk":
			d.Boot = &libvirtxml.DomainDeviceBoot{Order: next}
			next++
		}
	}
	dom.Devices.Disks = disks
	if path != "" {
		dom.OS.BootDevices = nil
	} else {
		dom.OS.BootDevices = []libvirtxml.DomainBootDevice{{Dev: "hd"}, {Dev: "cdrom"}}
	}
	return dom.Marshal()
}

// RescueDiskPath is the rescue disk of a domain definition, "" when none.
func RescueDiskPath(domainXML string) (string, error) {
	d, ok, err := FindDrive(domainXML, RescueTarget)
	if err != nil || !ok {
		return "", err
	}
	return DrivePath(d), nil
}
