// Package domtune writes the parts of a libvirt domain definition that keep
// one VM from starving the others on its node: CPU and disk caps, and discard
// on thin disks so space a guest frees goes back to the pool.
package domtune

import (
	"os"
	"reflect"
	"regexp"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"libvirt.org/go/libvirtxml"
)

// Limits are a VM's caps as the node applies them. Zero is no limit.
type Limits struct {
	// CPUWeight is the CPU priority against other VMs, 100 = normal.
	CPUWeight int
	// CPUCapPercent caps each vCPU at this share of one host core.
	CPUCapPercent int

	TotalIOPS, ReadIOPS, WriteIOPS    uint64
	TotalBytes, ReadBytes, WriteBytes uint64
}

// Period is the scheduler period, in microseconds, a CPU cap is a share of.
const Period = 100000

// normalized drops read and write limits beside a total of the same kind:
// libvirt refuses both at once.
func (l Limits) normalized() Limits {
	if l.TotalIOPS > 0 {
		l.ReadIOPS, l.WriteIOPS = 0, 0
	}
	if l.TotalBytes > 0 {
		l.ReadBytes, l.WriteBytes = 0, 0
	}
	return l
}

// DefaultShares is the cpu shares a VM has with no weight set: the kernel's
// default, which differs between cgroup versions.
func DefaultShares(cgroupV2 bool) uint64 {
	if cgroupV2 {
		return 100
	}
	return 1024
}

// Shares converts a weight (100 = normal) to the host's shares scale, within
// the range libvirt accepts there. 0 is no weight.
func Shares(weight int, cgroupV2 bool) uint64 {
	if weight <= 0 {
		return 0
	}
	if cgroupV2 {
		// Not 1: libvirt refuses to define a domain with <shares>1</shares>.
		return uint64(min(max(weight, 2), 10000))
	}
	return uint64(min(max(weight*1024/100, 2), 262144))
}

// Quota is the per-vCPU quota for a cap; 0 for none, including a cap of a
// whole core or more, which limits nothing.
func Quota(capPercent int) int64 {
	if capPercent <= 0 || capPercent >= 100 {
		return 0
	}
	return int64(Period) * int64(capPercent) / 100
}

// IOTune is the <iotune> for the disk caps; nil when there are none.
func (l Limits) IOTune() *libvirtxml.DomainDiskIOTune {
	l = l.normalized()
	t := libvirtxml.DomainDiskIOTune{
		TotalIopsSec: l.TotalIOPS, ReadIopsSec: l.ReadIOPS, WriteIopsSec: l.WriteIOPS,
		TotalBytesSec: l.TotalBytes, ReadBytesSec: l.ReadBytes, WriteBytesSec: l.WriteBytes,
	}
	if t == (libvirtxml.DomainDiskIOTune{}) {
		return nil
	}
	return &t
}

// IsDisk reports a disk device, as opposed to a cdrom or floppy. libvirt's
// default device is a disk.
func IsDisk(d libvirtxml.DomainDisk) bool { return d.Device == "" || d.Device == "disk" }

// Apply writes the caps into a definition: shares, period and quota in
// <cputune>, and <iotune> on every disk. A cap at zero is removed, so applying
// no limits returns the VM to the node's defaults.
func Apply(d *libvirtxml.Domain, l Limits, cgroupV2 bool) {
	shares, quota := Shares(l.CPUWeight, cgroupV2), Quota(l.CPUCapPercent)
	if d.CPUTune != nil || shares > 0 || quota > 0 {
		if d.CPUTune == nil {
			d.CPUTune = &libvirtxml.DomainCPUTune{}
		}
		d.CPUTune.Shares, d.CPUTune.Period, d.CPUTune.Quota = nil, nil, nil
		if shares > 0 {
			d.CPUTune.Shares = &libvirtxml.DomainCPUTuneShares{Value: uint(shares)}
		}
		if quota > 0 {
			d.CPUTune.Period = &libvirtxml.DomainCPUTunePeriod{Value: Period}
			d.CPUTune.Quota = &libvirtxml.DomainCPUTuneQuota{Value: quota}
		}
		if reflect.DeepEqual(*d.CPUTune, libvirtxml.DomainCPUTune{}) {
			d.CPUTune = nil
		}
	}
	if d.Devices == nil {
		return
	}
	tune := l.IOTune()
	for i := range d.Devices.Disks {
		if !IsDisk(d.Devices.Disks[i]) {
			continue
		}
		d.Devices.Disks[i].IOTune = nil
		if tune != nil {
			t := *tune
			d.Devices.Disks[i].IOTune = &t
		}
	}
}

// Discard makes a qcow2 disk give back the space its guest frees (TRIM) and
// the space it fills with zeroes. A driver that already says either is left
// as the operator set it.
func Discard(drv *libvirtxml.DomainDiskDriver) {
	if drv == nil || drv.Type != "qcow2" || drv.Discard != "" || drv.DetectZeros != "" {
		return
	}
	drv.Discard, drv.DetectZeros = "unmap", "unmap"
}

var (
	diskElemRe    = regexp.MustCompile(`(?s)<disk\b[^>]*>.*?</disk>`)
	notDiskRe     = regexp.MustCompile(`^<disk\b[^>]*\bdevice=["'](cdrom|floppy|lun)["']`)
	qcow2DriverRe = regexp.MustCompile(`<driver\b[^>]*\btype=["']qcow2["'][^>]*>`)
	discardSetRe  = regexp.MustCompile(`\b(discard|detect_zeroes)=`)
)

// DiscardXML is Discard for a definition held as text, so every redefine can
// pass it on without parsing the whole domain. It changes only the qcow2
// drivers of disk devices that say neither.
func DiscardXML(domXML string) string {
	return diskElemRe.ReplaceAllStringFunc(domXML, func(disk string) string {
		if notDiskRe.MatchString(disk) {
			return disk
		}
		return qcow2DriverRe.ReplaceAllStringFunc(disk, func(el string) string {
			if discardSetRe.MatchString(el) {
				return el
			}
			return el[:len("<driver")] + ` discard="unmap" detect_zeroes="unmap"` + el[len("<driver"):]
		})
	})
}

// CgroupV2 reports whether the host runs the unified cgroup hierarchy, which
// decides the shares scale.
func CgroupV2() bool {
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	return err == nil
}

// FromProto is the node's form of the caps the panel sent; nil is no caps.
func FromProto(l *pb.VMLimits) Limits {
	if l == nil {
		return Limits{}
	}
	u := func(v int64) uint64 { return uint64(max(v, 0)) }
	return Limits{
		CPUWeight:     int(l.GetCpuWeight()),
		CPUCapPercent: int(l.GetCpuCapPercent()),
		TotalIOPS:     u(l.GetTotalIopsSec()), ReadIOPS: u(l.GetReadIopsSec()), WriteIOPS: u(l.GetWriteIopsSec()),
		TotalBytes: u(l.GetTotalBytesSec()), ReadBytes: u(l.GetReadBytesSec()), WriteBytes: u(l.GetWriteBytesSec()),
	}
}
