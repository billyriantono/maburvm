package libvirt

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/google/uuid"
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
}

// VMInfo holds information about a VM
type VMInfo struct {
	UUID      string
	Name      string
	Status    VMStatus
	State     DomainState
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
func generateDomainXML(config VMConfig) (string, error) {
	// Validate UUID
	vmUUID, err := uuid.Parse(config.UUID)
	if err != nil {
		return "", fmt.Errorf("invalid UUID: %w", err)
	}

	// Calculate memory in KiB
	memoryKiB := config.Memory * 1024

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
		CPU: &libvirtxml.DomainCPU{
			Mode: "host-model",
		},
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
			Disks: []libvirtxml.DomainDisk{
				{
					Device: "disk",
					Driver: &libvirtxml.DomainDiskDriver{
						Name: "qemu",
						Type: "qcow2",
					},
					Source: &libvirtxml.DomainDiskSource{
						File: &libvirtxml.DomainDiskSourceFile{
							File: config.DiskPath,
						},
					},
					Target: &libvirtxml.DomainDiskTarget{
						Dev: "vda",
						Bus: "virtio",
					},
				},
			},
			Interfaces: []libvirtxml.DomainInterface{
				{
					Source: &libvirtxml.DomainInterfaceSource{
						Bridge: &libvirtxml.DomainInterfaceSourceBridge{
							Bridge: config.Bridge,
						},
					},
					Model: &libvirtxml.DomainInterfaceModel{
						Type: "virtio",
					},
				},
			},
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

		status = mapDomainStateToVMStatus(DomainState(state))
		return nil
	})

	if err != nil {
		return VMStatusUnknown, err
	}

	return status, nil
}

// mapDomainStateToVMStatus maps libvirt domain state to VMStatus
func mapDomainStateToVMStatus(state DomainState) VMStatus {
	switch state {
	case DomainStateRunning:
		return VMStatusRunning
	case DomainStateShutoff:
		return VMStatusStopped
	case DomainStatePaused:
		return VMStatusPaused
	case DomainStatePMSuspended:
		return VMStatusSuspended
	case DomainStateCrashed:
		return VMStatusCrashed
	default:
		return VMStatusUnknown
	}
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
		info.State = DomainState(state)
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
		State:     DomainState(state),
		Status:    mapDomainStateToVMStatus(DomainState(state)),
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
