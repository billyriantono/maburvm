package libvirt

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/agent/storage"
	"libvirt.org/go/libvirt"
	"libvirt.org/go/libvirtxml"
)

// VMConfig holds configuration for creating a new VM
type VMConfig struct {
	Name        string
	UUID        string
	CPU         int
	Memory      int // Memory in MB
	DiskPath    string
	DiskSize    int // Disk size in GB
	NetworkName string
	Bridge      string
	VNCPort     int
	VNCPassword string
	OSType      string
	OSVariant   string
	// CPUModel selects the guest CPU. Empty defaults to a portable, live-migratable
	// model ("kvm64"); "host-model"/"host-passthrough" maximize performance but
	// reduce cross-host migratability; any other value is used as a custom model.
	CPUModel string

	// Network interface configuration
	MACAddress    string // optional; libvirt auto-generates one if empty
	IPAddress     string // assigned IP (recorded on the interface metadata)
	Netmask       int    // CIDR prefix length (e.g. 24)
	Gateway       string // default gateway for the assigned IP
	VLANID        int    // 802.1Q VLAN tag; 0 = untagged
	BandwidthMbps int    // inbound/outbound rate cap in Mbps; 0 = unlimited
	AntiSpoofing  bool   // enable anti-IP hijacking protection (nwfilter + iptables + ebtables)

	// CloudInitISOPath, when set, is attached as a read-only "cidata" cdrom so
	// cloud-init configures the guest (static IP, hostname, SSH key) on boot.
	CloudInitISOPath string
}

// VMInfo holds information about a VM
type VMInfo struct {
	UUID      string
	Name      string
	Status    VMStatus
	State     libvirt.DomainState
	CPU       int
	Memory    uint64
	MaxMemory uint64
	VNCPort   int
	Autostart bool
	CreatedAt time.Time
}

// VMStatus represents the operational status of a VM
type VMStatus string

const (
	VMStatusRunning   VMStatus = "running"
	VMStatusStopped   VMStatus = "stopped"
	VMStatusPaused    VMStatus = "paused"
	VMStatusSuspended VMStatus = "suspended"
	VMStatusCrashed   VMStatus = "crashed"
	VMStatusUnknown   VMStatus = "unknown"
)

// VMManager provides VM lifecycle operations
type VMManager struct {
	storagePool string
	networkPool string
}

// NewVMManager creates a new VM manager instance
func NewVMManager() *VMManager {
	return &VMManager{
		storagePool: "default",
		networkPool: "default",
	}
}

// generateDomainXML creates a libvirt domain XML configuration using libvirtxml
// buildDomainCPU returns the <cpu> element for a guest. An empty model defaults
// to "kvm64" — a portable baseline that live-migrates across heterogeneous hosts
// (used when the user doesn't pick a CPU model, à la Virtualizor). "host-model"
// and "host-passthrough" maximize performance but reduce migratability; any other
// value is treated as a named custom model.
func buildDomainCPU(model string) *libvirtxml.DomainCPU {
	switch model {
	case "host-model":
		return &libvirtxml.DomainCPU{Mode: "host-model"}
	case "host-passthrough":
		return &libvirtxml.DomainCPU{Mode: "host-passthrough"}
	}
	if model == "" {
		model = "kvm64"
	}
	return &libvirtxml.DomainCPU{
		Mode:  "custom",
		Match: "exact",
		Model: &libvirtxml.DomainCPUModel{Fallback: "allow", Value: model},
	}
}

func generateDomainXML(config VMConfig) (string, error) {
	// Validate UUID
	vmUUID, err := uuid.Parse(config.UUID)
	if err != nil {
		return "", fmt.Errorf("invalid UUID: %w", err)
	}

	// Calculate memory in KiB
	memoryKiB := config.Memory * 1024

	// Build the primary network interface on the host bridge. MAC, VLAN tag and
	// bandwidth QoS are applied via the domain XML so libvirt enforces them when
	// the VM starts (no separate tc/bridge-vlan step required at create time).
	iface := libvirtxml.DomainInterface{
		Source: &libvirtxml.DomainInterfaceSource{
			Bridge: &libvirtxml.DomainInterfaceSourceBridge{
				Bridge: config.Bridge,
			},
		},
		Model: &libvirtxml.DomainInterfaceModel{
			Type: "virtio",
		},
	}
	if config.MACAddress != "" {
		iface.MAC = &libvirtxml.DomainInterfaceMAC{Address: config.MACAddress}
	}
	if config.VLANID > 0 {
		iface.VLan = &libvirtxml.DomainInterfaceVLan{
			Tags: []libvirtxml.DomainInterfaceVLanTag{{ID: uint(config.VLANID)}},
		}
	}
	if config.BandwidthMbps > 0 {
		// libvirt expresses bandwidth average in kilobytes/sec: Mbps * 1000 / 8.
		avgIn := config.BandwidthMbps * 125
		avgOut := config.BandwidthMbps * 125
		iface.Bandwidth = &libvirtxml.DomainInterfaceBandwidth{
			Inbound:  &libvirtxml.DomainInterfaceBandwidthParams{Average: &avgIn},
			Outbound: &libvirtxml.DomainInterfaceBandwidthParams{Average: &avgOut},
		}
	}

	// Add libvirt nwfilter for anti-spoofing (Layer 1 protection)
	// Only applied when AntiSpoofing is enabled.
	// This applies clean-traffic filter that prevents IP/MAC spoofing at the hypervisor level
	// The filter uses $IP, $IP6, and $MAC variables that libvirt substitutes at runtime
	if config.AntiSpoofing {
		iface.FilterRef = &libvirtxml.DomainInterfaceFilterRef{
			Filter: "clean-traffic",
			Parameters: []libvirtxml.DomainInterfaceFilterParam{
				{Name: "IP", Value: config.IPAddress},
				{Name: "MAC", Value: config.MACAddress},
			},
		}
	}

	// Primary disk (the cloned template image).
	disks := []libvirtxml.DomainDisk{
		{
			Device: "disk",
			Driver: &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "qcow2"},
			Source: &libvirtxml.DomainDiskSource{
				File: &libvirtxml.DomainDiskSourceFile{File: config.DiskPath},
			},
			Target: &libvirtxml.DomainDiskTarget{Dev: "vda", Bus: "virtio"},
		},
	}
	// Attach the cloud-init NoCloud seed as a read-only cdrom when present so the
	// guest configures its static IP / hostname / SSH key on first boot.
	if config.CloudInitISOPath != "" {
		disks = append(disks, libvirtxml.DomainDisk{
			Device:   "cdrom",
			Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
			Source:   &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: config.CloudInitISOPath}},
			Target:   &libvirtxml.DomainDiskTarget{Dev: "sda", Bus: "sata"},
			ReadOnly: &libvirtxml.DomainDiskReadOnly{},
		})
	}

	domain := &libvirtxml.Domain{
		Type: "kvm",
		Name: config.Name,
		UUID: vmUUID.String(),
		Memory: &libvirtxml.DomainMemory{
			Value: uint(memoryKiB),
			Unit:  "KiB",
		},
		CurrentMemory: &libvirtxml.DomainCurrentMemory{
			Value: uint(memoryKiB),
			Unit:  "KiB",
		},
		VCPU: &libvirtxml.DomainVCPU{
			Value: uint(config.CPU),
		},
		OS: &libvirtxml.DomainOS{
			Type: &libvirtxml.DomainOSType{
				Arch:    "x86_64",
				Machine: "q35",
				Type:    "hvm",
			},
			BootDevices: []libvirtxml.DomainBootDevice{
				{Dev: "hd"},
				{Dev: "cdrom"},
			},
		},
		Features: &libvirtxml.DomainFeatureList{
			ACPI: &libvirtxml.DomainFeature{},
			APIC: &libvirtxml.DomainFeatureAPIC{},
		},
		CPU: buildDomainCPU(config.CPUModel),
		Clock: &libvirtxml.DomainClock{
			Offset: "utc",
			Timer: []libvirtxml.DomainTimer{
				{Name: "rtc", TickPolicy: "catchup"},
				{Name: "pit", TickPolicy: "delay"},
				{Name: "hpet", Present: "no"},
			},
		},
		OnPoweroff: "destroy",
		OnReboot:   "restart",
		OnCrash:    "destroy",
		Devices: &libvirtxml.DomainDeviceList{
			Emulator: "/usr/bin/qemu-system-x86_64",
			Disks:      disks,
			Interfaces: []libvirtxml.DomainInterface{iface},
			Graphics: []libvirtxml.DomainGraphic{
				{
					VNC: &libvirtxml.DomainGraphicVNC{
						Port: config.VNCPort,
						AutoPort: func() string {
							if config.VNCPort == -1 || config.VNCPort == 0 {
								return "yes"
							}
							return "no"
						}(),
						Listen: "0.0.0.0",
					},
				},
			},
			Videos: []libvirtxml.DomainVideo{
				{
					Model: libvirtxml.DomainVideoModel{
						Type:  "qxl",
						Heads: uint(1),
					},
				},
			},
			MemBalloon: &libvirtxml.DomainMemBalloon{
				Model: "virtio",
			},
		},
		Metadata: &libvirtxml.DomainMetadata{
			XML: fmt.Sprintf(`<maburvm:metadata xmlns:maburvm="http://maburvm.local/schema">
				<maburvm:created_at>%s</maburvm:created_at>
				<maburvm:vm_name>%s</maburvm:vm_name>
				<maburvm:vm_uuid>%s</maburvm:vm_uuid>
			</maburvm:metadata>`, time.Now().Format(time.RFC3339), config.Name, config.UUID),
		},
	}

	xmlBytes, err := xml.MarshalIndent(domain, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal domain XML: %w", err)
	}

	return string(xmlBytes), nil
}

// CreateVM creates a new VM with the given configuration
func CreateVM(config VMConfig) (string, error) {
	if config.UUID == "" {
		config.UUID = uuid.New().String()
	}

	// Validate required fields
	if config.Name == "" {
		return "", fmt.Errorf("VM name is required")
	}
	if config.CPU <= 0 {
		config.CPU = 1
	}
	if config.Memory <= 0 {
		config.Memory = 512
	}
	if config.Bridge == "" {
		config.Bridge = "virbr0"
	}

	// Generate domain XML
	xmlConfig, err := generateDomainXML(config)
	if err != nil {
		return "", fmt.Errorf("failed to generate domain XML: %w", err)
	}

	var domainUUID string
	err = WithConnection(func(conn *libvirt.Connect) error {
		// Check if domain with same name already exists
		existingDom, err := conn.LookupDomainByName(config.Name)
		if err == nil {
			existingDom.Free()
			return fmt.Errorf("domain with name '%s' already exists", config.Name)
		}

		// Define the domain
		dom, err := conn.DomainDefineXML(xmlConfig)
		if err != nil {
			return fmt.Errorf("failed to define domain: %w", err)
		}
		defer dom.Free()

		// Get the domain UUID
		domainUUID, err = dom.GetUUIDString()
		if err != nil {
			return fmt.Errorf("failed to get domain UUID: %w", err)
		}

		return nil
	})

	if err != nil {
		return "", err
	}

	return domainUUID, nil
}

// StartVM starts a VM with the given UUID
func StartVM(uuidStr string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}

	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		// Check current state
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}

		// Only start if not already running
		if state == libvirt.DOMAIN_RUNNING {
			return fmt.Errorf("domain is already running")
		}

		// Start the domain
		if err := dom.Create(); err != nil {
			return fmt.Errorf("failed to start domain: %w", err)
		}

		return nil
	})
}

// StopVM stops a VM with optional force flag
func StopVM(uuidStr string, force bool) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}

	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		// Check current state
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}

		// Check if domain is already stopped
		if state == libvirt.DOMAIN_SHUTOFF || state == libvirt.DOMAIN_SHUTDOWN {
			return fmt.Errorf("domain is already stopped")
		}

		// If force is true, use Destroy (immediate shutdown)
		// Otherwise use Shutdown (graceful shutdown via ACPI)
		if force {
			if err := dom.Destroy(); err != nil {
				return fmt.Errorf("failed to force stop domain: %w", err)
			}
		} else {
			if err := dom.Shutdown(); err != nil {
				return fmt.Errorf("failed to shutdown domain: %w", err)
			}
		}

		return nil
	})
}

// RestartVM restarts a VM
func RestartVM(uuidStr string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}

	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		// Check current state
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}

		// If running, reboot; if stopped, start
		if state == libvirt.DOMAIN_RUNNING {
			if err := dom.Reboot(0); err != nil {
				return fmt.Errorf("failed to reboot domain: %w", err)
			}
		} else {
			if err := dom.Create(); err != nil {
				return fmt.Errorf("failed to start domain: %w", err)
			}
		}

		return nil
	})
}

// installISOTarget is the target dev used for an attached install/rescue ISO,
// distinct from the cloud-init seed cdrom (sda).
const installISOTarget = "sdb"

func removeDiskByTarget(disks []libvirtxml.DomainDisk, dev string) []libvirtxml.DomainDisk {
	out := make([]libvirtxml.DomainDisk, 0, len(disks))
	for _, d := range disks {
		if d.Target != nil && d.Target.Dev == dev {
			continue
		}
		out = append(out, d)
	}
	return out
}

// AttachISO attaches an ISO as a bootable cdrom (first in boot order) to a
// stopped VM, for OS install or rescue. Idempotent (replaces any prior ISO).
func AttachISO(uuidStr, isoPath string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	if _, err := os.Stat(isoPath); err != nil {
		return fmt.Errorf("ISO not found at %s: %w", isoPath, err)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		if state, _, err := dom.GetState(); err == nil && state == libvirt.DOMAIN_RUNNING {
			return fmt.Errorf("stop the VM before attaching an install ISO")
		}
		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		var domain libvirtxml.Domain
		if err := xml.Unmarshal([]byte(xmlDesc), &domain); err != nil {
			return fmt.Errorf("failed to parse domain XML: %w", err)
		}
		if domain.Devices == nil {
			return fmt.Errorf("domain has no devices")
		}
		// Per-device boot order requires clearing the OS-level boot list.
		if domain.OS != nil {
			domain.OS.BootDevices = nil
		}
		domain.Devices.Disks = removeDiskByTarget(domain.Devices.Disks, installISOTarget)
		domain.Devices.Disks = append(domain.Devices.Disks, libvirtxml.DomainDisk{
			Device:   "cdrom",
			Driver:   &libvirtxml.DomainDiskDriver{Name: "qemu", Type: "raw"},
			Source:   &libvirtxml.DomainDiskSource{File: &libvirtxml.DomainDiskSourceFile{File: isoPath}},
			Target:   &libvirtxml.DomainDiskTarget{Dev: installISOTarget, Bus: "sata"},
			ReadOnly: &libvirtxml.DomainDiskReadOnly{},
			Boot:     &libvirtxml.DomainDeviceBoot{Order: 1},
		})
		// Primary disk boots second.
		for i := range domain.Devices.Disks {
			if domain.Devices.Disks[i].Device == "disk" {
				domain.Devices.Disks[i].Boot = &libvirtxml.DomainDeviceBoot{Order: 2}
			}
		}
		out, err := xml.Marshal(&domain)
		if err != nil {
			return fmt.Errorf("failed to marshal domain XML: %w", err)
		}
		newDom, err := conn.DomainDefineXML(string(out))
		if err != nil {
			return fmt.Errorf("failed to redefine domain with ISO: %w", err)
		}
		newDom.Free()
		return nil
	})
}

// DetachISO removes an attached install ISO and restores normal disk boot order.
func DetachISO(uuidStr string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		var domain libvirtxml.Domain
		if err := xml.Unmarshal([]byte(xmlDesc), &domain); err != nil {
			return fmt.Errorf("failed to parse domain XML: %w", err)
		}
		if domain.Devices == nil {
			return nil
		}
		domain.Devices.Disks = removeDiskByTarget(domain.Devices.Disks, installISOTarget)
		// Clear per-device boot order and restore OS-level boot list.
		for i := range domain.Devices.Disks {
			domain.Devices.Disks[i].Boot = nil
		}
		if domain.OS != nil {
			domain.OS.BootDevices = []libvirtxml.DomainBootDevice{{Dev: "hd"}, {Dev: "cdrom"}}
		}
		out, err := xml.Marshal(&domain)
		if err != nil {
			return fmt.Errorf("failed to marshal domain XML: %w", err)
		}
		newDom, err := conn.DomainDefineXML(string(out))
		if err != nil {
			return fmt.Errorf("failed to redefine domain: %w", err)
		}
		newDom.Free()
		return nil
	})
}

// ResizeDisk grows the VM's primary disk to diskGB. It only ever grows (a
// qcow2/guest filesystem can't be safely shrunk online), and is a no-op when the
// disk is already at least that size — so it's safe to call on every resize even
// when only CPU/RAM changed. A running VM is resized live via QEMU; a stopped VM
// has its qcow2 file resized directly. The guest still has to grow its partition
// and filesystem (cloud images do this automatically via growpart on boot).
func ResizeDisk(uuidStr string, diskGB int) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	if diskGB <= 0 {
		return fmt.Errorf("disk size must be positive")
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		var domain libvirtxml.Domain
		if err := xml.Unmarshal([]byte(xmlDesc), &domain); err != nil {
			return fmt.Errorf("failed to parse domain XML: %w", err)
		}
		var path, target string
		if domain.Devices != nil {
			for _, d := range domain.Devices.Disks {
				if d.Device == "disk" && d.Target != nil {
					target = d.Target.Dev
					if d.Source != nil && d.Source.File != nil {
						path = d.Source.File.File
					}
					break
				}
			}
		}
		if target == "" || path == "" {
			return fmt.Errorf("no resizable primary disk found")
		}

		want := int64(diskGB) * 1024 * 1024 * 1024
		// Grow-only + idempotent: skip when already at/above the requested size.
		if info, ierr := storage.NewQCOW2Manager().ImageInfo(path); ierr == nil && want <= info.VirtualSize {
			return nil
		}

		if state, _, _ := dom.GetState(); state == libvirt.DOMAIN_RUNNING {
			if err := dom.BlockResize(target, uint64(want), libvirt.DOMAIN_BLOCK_RESIZE_BYTES); err != nil {
				return fmt.Errorf("failed to live-resize disk: %w", err)
			}
			return nil
		}
		if err := storage.NewQCOW2Manager().ResizeImage(path, diskGB); err != nil {
			return fmt.Errorf("failed to resize disk image: %w", err)
		}
		return nil
	})
}

// nextFreeVirtioTarget returns the next unused virtio disk target (vdb, vdc, …).
// vda is reserved for the primary disk. Returns "" when all slots are taken.
func nextFreeVirtioTarget(domain *libvirtxml.Domain) string {
	used := map[string]bool{}
	if domain.Devices != nil {
		for _, d := range domain.Devices.Disks {
			if d.Target != nil {
				used[d.Target.Dev] = true
			}
		}
	}
	for c := byte('b'); c <= 'z'; c++ {
		dev := "vd" + string(c)
		if !used[dev] {
			return dev
		}
	}
	return ""
}

// AttachDisk attaches an existing qcow2 image to the domain as the next free
// virtio target. It persists to the domain config and, when the VM is running,
// hot-plugs it live. Returns the assigned target device (e.g. "vdb").
func AttachDisk(uuidStr, diskPath string) (string, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return "", fmt.Errorf("invalid UUID format: %w", err)
	}
	if _, err := os.Stat(diskPath); err != nil {
		return "", fmt.Errorf("disk image not found at %s: %w", diskPath, err)
	}
	var device string
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		var domain libvirtxml.Domain
		if err := xml.Unmarshal([]byte(xmlDesc), &domain); err != nil {
			return fmt.Errorf("failed to parse domain XML: %w", err)
		}
		device = nextFreeVirtioTarget(&domain)
		if device == "" {
			return fmt.Errorf("no free virtio disk slot available")
		}

		diskXML := fmt.Sprintf(`<disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='%s'/><target dev='%s' bus='virtio'/></disk>`, diskPath, device)
		flags := libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
		if state, _, _ := dom.GetState(); state == libvirt.DOMAIN_RUNNING {
			flags |= libvirt.DOMAIN_DEVICE_MODIFY_LIVE
		}
		if err := dom.AttachDeviceFlags(diskXML, flags); err != nil {
			return fmt.Errorf("failed to attach disk: %w", err)
		}
		return nil
	})
	return device, err
}

// DetachDisk detaches the disk at the given target device (e.g. "vdb") from the
// domain (config + live when running). The primary disk (vda) cannot be detached.
func DetachDisk(uuidStr, device, diskPath string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	if device == "" || device == "vda" {
		return fmt.Errorf("refusing to detach primary or empty disk target %q", device)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		diskXML := fmt.Sprintf(`<disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='%s'/><target dev='%s' bus='virtio'/></disk>`, diskPath, device)
		flags := libvirt.DOMAIN_DEVICE_MODIFY_CONFIG
		if state, _, _ := dom.GetState(); state == libvirt.DOMAIN_RUNNING {
			flags |= libvirt.DOMAIN_DEVICE_MODIFY_LIVE
		}
		if err := dom.DetachDeviceFlags(diskXML, flags); err != nil {
			return fmt.Errorf("failed to detach disk: %w", err)
		}
		return nil
	})
}

// SuspendVM pauses a running VM (keeps it in memory).
func SuspendVM(uuidStr string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}
		if state == libvirt.DOMAIN_PAUSED {
			return nil // already paused
		}
		if state != libvirt.DOMAIN_RUNNING {
			return fmt.Errorf("domain is not running")
		}
		if err := dom.Suspend(); err != nil {
			return fmt.Errorf("failed to suspend domain: %w", err)
		}
		return nil
	})
}

// MigrateVM live-migrates a domain to a destination libvirt URI using
// peer-to-peer migration (the source daemon drives the transfer). The domain is
// persisted on the destination and undefined on the source on success.
//
// destURI example: "qemu+ssh://root@203.0.113.131/system".
// When copyStorage is true, full block migration is used (no shared storage).
func MigrateVM(uuidStr, destURI string, live, copyStorage bool) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	if destURI == "" {
		return fmt.Errorf("destination URI is required")
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		flags := libvirt.MIGRATE_PEER2PEER | libvirt.MIGRATE_PERSIST_DEST | libvirt.MIGRATE_UNDEFINE_SOURCE
		if live {
			flags |= libvirt.MIGRATE_LIVE
		}
		if copyStorage {
			// Full block migration: copy the disk(s) to the destination since
			// the nodes do not share storage.
			flags |= libvirt.MIGRATE_NON_SHARED_DISK
		}

		params := &libvirt.DomainMigrateParameters{}
		if err := dom.MigrateToURI3(destURI, params, flags); err != nil {
			return fmt.Errorf("migration to %s failed: %w", destURI, err)
		}
		return nil
	})
}

// ResumeVM resumes a paused VM.
func ResumeVM(uuidStr string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}
		if state == libvirt.DOMAIN_RUNNING {
			return nil // already running
		}
		if state != libvirt.DOMAIN_PAUSED {
			return fmt.Errorf("domain is not paused")
		}
		if err := dom.Resume(); err != nil {
			return fmt.Errorf("failed to resume domain: %w", err)
		}
		return nil
	})
}

// ResizeVM updates a (stopped) VM's vCPU and memory allocation in its
// persistent config. Takes effect on next boot.
func ResizeVM(uuidStr string, vcpus int, memoryMB int) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	if vcpus <= 0 && memoryMB <= 0 {
		return fmt.Errorf("nothing to resize")
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		// Apply to persistent config (affects next boot); avoids live-resize edge cases.
		if memoryMB > 0 {
			memKiB := uint64(memoryMB) * 1024
			if err := dom.SetMemoryFlags(memKiB, libvirt.DOMAIN_MEM_CONFIG|libvirt.DOMAIN_MEM_MAXIMUM); err != nil {
				return fmt.Errorf("failed to set max memory: %w", err)
			}
			if err := dom.SetMemoryFlags(memKiB, libvirt.DOMAIN_MEM_CONFIG); err != nil {
				return fmt.Errorf("failed to set memory: %w", err)
			}
		}
		if vcpus > 0 {
			if err := dom.SetVcpusFlags(uint(vcpus), libvirt.DOMAIN_VCPU_CONFIG|libvirt.DOMAIN_VCPU_MAXIMUM); err != nil {
				return fmt.Errorf("failed to set max vcpus: %w", err)
			}
			if err := dom.SetVcpusFlags(uint(vcpus), libvirt.DOMAIN_VCPU_CONFIG); err != nil {
				return fmt.Errorf("failed to set vcpus: %w", err)
			}
		}
		return nil
	})
}

// DeleteVM deletes a VM and its associated storage
func DeleteVM(uuidStr string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}

	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		// Check current state and stop if running
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}

		if state == libvirt.DOMAIN_RUNNING || state == libvirt.DOMAIN_PAUSED {
			if err := dom.Destroy(); err != nil {
				return fmt.Errorf("failed to destroy running domain: %w", err)
			}
		}

		// Get domain XML to find disk paths for cleanup
		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}

		// Parse XML to find disk paths
		diskPaths := extractDiskPaths(xmlDesc)

		// Undefine the domain
		if err := dom.Undefine(); err != nil {
			return fmt.Errorf("failed to undefine domain: %w", err)
		}

		// Delete storage files
		for _, path := range diskPaths {
			if path != "" {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					// Log warning but don't fail
					fmt.Printf("Warning: failed to delete disk %s: %v\n", path, err)
				}
			}
		}

		return nil
	})
}

// extractDiskPaths extracts disk file paths from domain XML
func extractDiskPaths(xmlDesc string) []string {
	var paths []string
	var domain libvirtxml.Domain
	if err := xml.Unmarshal([]byte(xmlDesc), &domain); err != nil {
		return paths
	}

	for _, disk := range domain.Devices.Disks {
		if disk.Source != nil && disk.Source.File != nil {
			paths = append(paths, disk.Source.File.File)
		}
	}

	return paths
}

// PrimaryDiskPath returns the source file path of the VM's primary disk
// (the device='disk' entry, not cdrom/seed images).
func PrimaryDiskPath(uuidStr string) (string, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return "", fmt.Errorf("invalid UUID format: %w", err)
	}
	var path string
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}
		var domain libvirtxml.Domain
		if err := xml.Unmarshal([]byte(xmlDesc), &domain); err != nil {
			return fmt.Errorf("failed to parse domain XML: %w", err)
		}
		if domain.Devices != nil {
			for _, disk := range domain.Devices.Disks {
				if disk.Device == "disk" && disk.Source != nil && disk.Source.File != nil && disk.Source.File.File != "" {
					path = disk.Source.File.File
					return nil
				}
			}
		}
		return fmt.Errorf("no primary disk found for domain %s", uuidStr)
	})
	if err != nil {
		return "", err
	}
	return path, nil
}

// GetVMStatus returns the current status of a VM
func GetVMStatus(uuidStr string) (VMStatus, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return VMStatusUnknown, fmt.Errorf("invalid UUID format: %w", err)
	}

	var status VMStatus
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}

		status = mapDomainStateToVMStatus(libvirt.DomainState(state))
		return nil
	})

	if err != nil {
		return VMStatusUnknown, err
	}

	return status, nil
}

// mapDomainStateToVMStatus maps libvirt domain state to VMStatus
func mapDomainStateToVMStatus(state libvirt.DomainState) VMStatus {
	switch state {
	case libvirt.DOMAIN_RUNNING:
		return VMStatusRunning
	case libvirt.DOMAIN_SHUTOFF:
		return VMStatusStopped
	case libvirt.DOMAIN_PAUSED:
		return VMStatusPaused
	case libvirt.DOMAIN_PMSUSPENDED:
		return VMStatusSuspended
	case libvirt.DOMAIN_CRASHED:
		return VMStatusCrashed
	default:
		return VMStatusUnknown
	}
}

// mapSnapshotStateToVMStatus maps snapshot state string from XML to VMStatus
func mapSnapshotStateToVMStatus(stateStr string) VMStatus {
	switch stateStr {
	case "running":
		return VMStatusRunning
	case "shutoff":
		return VMStatusStopped
	case "paused":
		return VMStatusPaused
	case "pmsuspended":
		return VMStatusSuspended
	case "crashed":
		return VMStatusCrashed
	default:
		return VMStatusUnknown
	}
}

// extractInterfaceNames extracts network interface names from domain XML
func extractInterfaceNames(xmlDesc string) []string {
	// Match <interface type='...'> elements and get the target device name
	re := regexp.MustCompile(`<interface[^>]*type=['"](\w+)['"][^>]*>[\s\S]*?<target[^>]*dev=['"]([^'"]+)['"]`)
	matches := re.FindAllStringSubmatch(xmlDesc, -1)
	
	var names []string
	seen := make(map[string]bool)
	for _, match := range matches {
		if len(match) > 2 {
			if !seen[match[2]] {
				names = append(names, match[2])
				seen[match[2]] = true
			}
		}
	}
	return names
}

// GetVMInfo returns detailed information about a VM
func GetVMInfo(uuidStr string) (*VMInfo, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %w", err)
	}

	var info VMInfo
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		// Get basic info
		name, err := dom.GetName()
		if err != nil {
			return fmt.Errorf("failed to get domain name: %w", err)
		}
		info.Name = name
		info.UUID = uuidStr

		// Get state
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}
		info.State = libvirt.DomainState(state)
		info.Status = mapDomainStateToVMStatus(info.State)

		// Get autostart
		autostart, err := dom.GetAutostart()
		if err != nil {
			autostart = false
		}
		info.Autostart = autostart

		// Get info (CPU, memory)
		domInfo, err := dom.GetInfo()
		if err == nil {
			info.CPU = int(domInfo.NrVirtCpu)
			info.Memory = domInfo.Memory * 1024 // Convert KiB to bytes
			info.MaxMemory = domInfo.MaxMem * 1024
		}

		// Get VNC port from XML
		xmlDesc, err := dom.GetXMLDesc(0)
		if err == nil {
			info.VNCPort = extractVNCPort(xmlDesc)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &info, nil
}

// extractVNCPort extracts VNC port from domain XML
func extractVNCPort(xmlDesc string) int {
	var domain libvirtxml.Domain
	if err := xml.Unmarshal([]byte(xmlDesc), &domain); err != nil {
		return 0
	}

	for _, graphic := range domain.Devices.Graphics {
		if graphic.VNC != nil {
			return graphic.VNC.Port
		}
	}

	return 0
}

// ListVMs returns a list of all VMs
func ListVMs() ([]VMInfo, error) {
	var vms []VMInfo

	err := WithConnection(func(conn *libvirt.Connect) error {
		// Get all domains (including inactive)
		// CONNECT_LIST_DOMAINS_ACTIVE = 1, CONNECT_LIST_DOMAINS_INACTIVE = 2
		doms, err := conn.ListAllDomains(3) // 1 | 2 = 3
		if err != nil {
			return fmt.Errorf("failed to list domains: %w", err)
		}
		defer func() {
			for _, dom := range doms {
				dom.Free()
			}
		}()

		for _, dom := range doms {
			info, err := getVMInfoFromDomain(&dom)
			if err != nil {
				continue // Skip domains we can't read
			}
			vms = append(vms, *info)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return vms, nil
}

// getVMInfoFromDomain extracts VMInfo from a libvirt Domain
func getVMInfoFromDomain(dom *libvirt.Domain) (*VMInfo, error) {
	name, err := dom.GetName()
	if err != nil {
		return nil, err
	}

	uuidStr, err := dom.GetUUIDString()
	if err != nil {
		return nil, err
	}

	state, _, err := dom.GetState()
	if err != nil {
		return nil, err
	}

	autostart, _ := dom.GetAutostart()

	info := &VMInfo{
		Name:      name,
		UUID:      uuidStr,
		State:     libvirt.DomainState(state),
		Status:    mapDomainStateToVMStatus(libvirt.DomainState(state)),
		Autostart: autostart,
	}

	// Get extended info if available
	domInfo, err := dom.GetInfo()
	if err == nil {
		info.CPU = int(domInfo.NrVirtCpu)
		info.Memory = domInfo.Memory * 1024
		info.MaxMemory = domInfo.MaxMem * 1024
	}

	// Get VNC port
	xmlDesc, err := dom.GetXMLDesc(0)
	if err == nil {
		info.VNCPort = extractVNCPort(xmlDesc)
	}

	return info, nil
}

// RebuildVM replaces the VM's disk with a template copy
func RebuildVM(uuidStr string, templatePath string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}

	if templatePath == "" {
		return fmt.Errorf("template path is required")
	}

	if _, err := os.Stat(templatePath); os.IsNotExist(err) {
		return fmt.Errorf("template not found: %s", templatePath)
	}

	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		// Get current state
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}

		wasRunning := state == libvirt.DOMAIN_RUNNING

		// Stop the VM if running
		if wasRunning {
			if err := dom.Destroy(); err != nil {
				return fmt.Errorf("failed to stop domain for rebuild: %w", err)
			}
		}

		// Get domain XML to find disk path
		xmlDesc, err := dom.GetXMLDesc(0)
		if err != nil {
			return fmt.Errorf("failed to get domain XML: %w", err)
		}

		diskPaths := extractDiskPaths(xmlDesc)
		if len(diskPaths) == 0 {
			return fmt.Errorf("no disk found for domain")
		}

		diskPath := diskPaths[0]

		// Backup old disk (optional - remove if not needed)
		backupPath := diskPath + ".backup." + strconv.FormatInt(time.Now().Unix(), 10)
		if err := os.Rename(diskPath, backupPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to backup old disk: %w", err)
		}

		// Copy template to new disk location
		if err := copyFile(templatePath, diskPath); err != nil {
			// Restore backup on failure
			os.Rename(backupPath, diskPath)
			return fmt.Errorf("failed to copy template: %w", err)
		}

		// Remove backup on success
		os.Remove(backupPath)

		// Restart if was running
		if wasRunning {
			if err := dom.Create(); err != nil {
				return fmt.Errorf("failed to restart domain after rebuild: %w", err)
			}
		}

		return nil
	})
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	// Ensure destination directory exists
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	if err != nil {
		return err
	}

	return destFile.Sync()
}

// String returns the string representation of VMStatus
func (s VMStatus) String() string {
	return string(s)
}

// VMSnapshot represents a VM snapshot
type VMSnapshot struct {
	UUID        string
	Name        string
	Description string
	CreatedAt   time.Time
	State       VMStatus
	Size        int64
}

// CreateSnapshot creates a new snapshot for a VM
func CreateSnapshot(uuidStr, name, description string) (*VMSnapshot, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %w", err)
	}
	if name == "" {
		return nil, fmt.Errorf("snapshot name is required")
	}

	var snapshot VMSnapshot
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}

		snapshotXML := fmt.Sprintf(`
<domainsnapshot>
	<name>%s</name>
	<description>%s</description>
	<creationTime>%d</creationTime>
</domainsnapshot>`, name, description, time.Now().Unix())

		snap, err := dom.CreateSnapshotXML(snapshotXML, libvirt.DOMAIN_SNAPSHOT_CREATE_ATOMIC)
		if err != nil {
			return fmt.Errorf("failed to create snapshot: %w", err)
		}
		defer snap.Free()

		snapName, _ := snap.GetName()
		snapshot = VMSnapshot{
			UUID:        snapName,
			Name:        snapName,
			Description: description,
			CreatedAt:   time.Now(),
			State:       mapDomainStateToVMStatus(libvirt.DomainState(state)),
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// ListSnapshots returns all snapshots for a VM
func ListSnapshots(uuidStr string) ([]VMSnapshot, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %w", err)
	}

	var snapshots []VMSnapshot
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		snaps, err := dom.ListAllSnapshots(0)
		if err != nil {
			return fmt.Errorf("failed to list snapshots: %w", err)
		}
		defer func() {
			for _, snap := range snaps {
				snap.Free()
			}
		}()

		for _, snap := range snaps {
			name, err := snap.GetName()
			if err != nil {
				continue
			}
			// Get snapshot XML to extract creation time and state
			// (GetInfo was removed in newer libvirt Go bindings)
			xmlDesc, err := snap.GetXMLDesc(0)
			if err != nil {
				continue
			}
			
			// Parse creationTime from XML: <creationTime>1234567890</creationTime>
			var createdAt time.Time
			if matches := regexp.MustCompile(`<creationTime>(\d+)</creationTime>`).FindStringSubmatch(xmlDesc); len(matches) > 1 {
				if ts, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
					createdAt = time.Unix(ts, 0)
				}
			}
			
			// Parse state from XML: <state>running</state>
			state := mapDomainStateToVMStatus(0) // default to stopped
			if matches := regexp.MustCompile(`<state>(\w+)</state>`).FindStringSubmatch(xmlDesc); len(matches) > 1 {
				state = mapSnapshotStateToVMStatus(matches[1])
			}
			
			snapshots = append(snapshots, VMSnapshot{
				UUID:      name,
				Name:      name,
				CreatedAt: createdAt,
				State:     state,
			})
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return snapshots, nil
}

// RestoreSnapshot restores a VM to a snapshot
func RestoreSnapshot(uuidStr, snapshotName string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	if snapshotName == "" {
		return fmt.Errorf("snapshot name is required")
	}

	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		snap, err := dom.SnapshotLookupByName(snapshotName, 0)
		if err != nil {
			return fmt.Errorf("snapshot not found: %w", err)
		}
		defer snap.Free()

		if err := snap.RevertToSnapshot(libvirt.DOMAIN_SNAPSHOT_REVERT_RUNNING); err != nil {
			return fmt.Errorf("failed to revert to snapshot: %w", err)
		}
		return nil
	})
}

// DeleteSnapshot deletes a snapshot
func DeleteSnapshot(uuidStr, snapshotName string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	if snapshotName == "" {
		return fmt.Errorf("snapshot name is required")
	}

	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		snap, err := dom.SnapshotLookupByName(snapshotName, 0)
		if err != nil {
			return fmt.Errorf("snapshot not found: %w", err)
		}
		defer snap.Free()

		if err := snap.Delete(libvirt.DOMAIN_SNAPSHOT_DELETE_CHILDREN); err != nil {
			return fmt.Errorf("failed to delete snapshot: %w", err)
		}
		return nil
	})
}

// VMStats holds real-time VM statistics
type VMStats struct {
	CPUTime        uint64
	MemoryActual   int64
	MemoryRSS      int64
	SwapIn         int64
	SwapOut        int64
	NetRXBytes     int64
	NetTXBytes     int64
	NetRXPackets   int64
	NetTXPackets   int64
	DiskReadBytes  int64
	DiskWriteBytes int64
	DiskReadOps    int64
	DiskWriteOps   int64
	NumCPUs        int
}

// GetVMStats returns real-time statistics for a VM
func GetVMStats(uuidStr string) (*VMStats, error) {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return nil, fmt.Errorf("invalid UUID format: %w", err)
	}

	var stats VMStats
	err := WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		// Get CPU stats
		cpuStats, err := dom.GetCPUStats(-1, 1, 0)
		if err == nil && len(cpuStats) > 0 {
			stats.CPUTime = cpuStats[0].CpuTime
		}

		// Get vCPU count
		info, err := dom.GetInfo()
		if err == nil {
			stats.NumCPUs = int(info.NrVirtCpu)
		}

		// Get memory stats
		memStats, err := dom.MemoryStats(uint32(libvirt.DOMAIN_MEMORY_STAT_NR), 0)
		if err == nil {
			for _, stat := range memStats {
				switch stat.Tag {
				case int32(libvirt.DOMAIN_MEMORY_STAT_ACTUAL_BALLOON):
					stats.MemoryActual = int64(stat.Val) * 1024
				case int32(libvirt.DOMAIN_MEMORY_STAT_RSS):
					stats.MemoryRSS = int64(stat.Val) * 1024
				case int32(libvirt.DOMAIN_MEMORY_STAT_SWAP_IN):
					stats.SwapIn = int64(stat.Val) * 1024
				case int32(libvirt.DOMAIN_MEMORY_STAT_SWAP_OUT):
					stats.SwapOut = int64(stat.Val) * 1024
				}
			}
		}

		// Get interface stats - parse from XML instead of InterfaceAddresses
		// (InterfaceAddresses is not available in current libvirt Go bindings)
		xmlDesc, err := dom.GetXMLDesc(0)
		if err == nil {
			// Extract interface names from XML using regex
			ifaceNames := extractInterfaceNames(xmlDesc)
			for _, ifaceName := range ifaceNames {
				ifStats, err := dom.InterfaceStats(ifaceName)
				if err == nil {
					stats.NetRXBytes += int64(ifStats.RxBytes)
					stats.NetTXBytes += int64(ifStats.TxBytes)
					stats.NetRXPackets += int64(ifStats.RxPackets)
					stats.NetTXPackets += int64(ifStats.TxPackets)
				}
			}
		}

		// Get block stats (moved up since we need XML for interfaces above)
		if err == nil {
			var domain libvirtxml.Domain
			if err := xml.Unmarshal([]byte(xmlDesc), &domain); err == nil {
				for _, disk := range domain.Devices.Disks {
					if disk.Target != nil {
						blkStats, err := dom.BlockStats(disk.Target.Dev)
						if err == nil {
							stats.DiskReadBytes += int64(blkStats.RdBytes)
							stats.DiskWriteBytes += int64(blkStats.WrBytes)
							stats.DiskReadOps += int64(blkStats.RdReq)
							stats.DiskWriteOps += int64(blkStats.WrReq)
						}
					}
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}
	return &stats, nil
}

// SetVNCPassword sets the VNC password for a running VM using QEMU monitor command
func SetVNCPassword(uuidStr string, password string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}

	if password == "" {
		return fmt.Errorf("password cannot be empty")
	}

	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()

		// Check if domain is running
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}
		if libvirt.DomainState(state) != libvirt.DOMAIN_RUNNING {
			return fmt.Errorf("VM must be running to set VNC password")
		}

		// Set the VNC password via QMP (structured JSON) rather than HMP. HMP is a
		// shell-like parser, so a password containing spaces or characters like
		// $ ^ & * # would be mangled — QEMU would store a different value than the
		// one the browser presents, causing "Authentication failed". QMP passes the
		// password as a JSON string argument, so any byte is preserved verbatim.
		payload, err := json.Marshal(map[string]interface{}{
			"execute": "set_password",
			"arguments": map[string]interface{}{
				"protocol": "vnc",
				"password": password,
			},
		})
		if err != nil {
			return fmt.Errorf("failed to encode set_password command: %w", err)
		}
		if _, err = dom.QemuMonitorCommand(string(payload), libvirt.DOMAIN_QEMU_MONITOR_COMMAND_DEFAULT); err != nil {
			return fmt.Errorf("failed to set VNC password: %w", err)
		}

		return nil
	})
}

// SetVMPassword sets a guest user's password via the qemu-guest-agent (the VM
// must be running with the guest agent installed — true for cloud images).
func SetVMPassword(uuidStr, user, password string) error {
	if _, err := uuid.Parse(uuidStr); err != nil {
		return fmt.Errorf("invalid UUID format: %w", err)
	}
	if user == "" {
		user = "root"
	}
	if password == "" {
		return fmt.Errorf("password cannot be empty")
	}
	return WithConnection(func(conn *libvirt.Connect) error {
		dom, err := conn.LookupDomainByUUIDString(uuidStr)
		if err != nil {
			return fmt.Errorf("domain not found: %w", err)
		}
		defer dom.Free()
		state, _, err := dom.GetState()
		if err != nil {
			return fmt.Errorf("failed to get domain state: %w", err)
		}
		if state != libvirt.DOMAIN_RUNNING {
			return fmt.Errorf("VM must be running to reset password (guest agent required)")
		}
		// flags 0 = plaintext password (libvirt hashes via guest agent).
		if err := dom.SetUserPassword(user, password, 0); err != nil {
			return fmt.Errorf("failed to set %s password (qemu-guest-agent required in guest): %w", user, err)
		}
		return nil
	})
}
