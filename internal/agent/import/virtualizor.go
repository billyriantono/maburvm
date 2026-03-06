// Package vmimport provides VM import functionality from external sources like Virtualizor
package vmimport

import (
	"encoding/xml"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ImportCandidate represents a VM that can be imported from an external source
type ImportCandidate struct {
	// Basic VM Info
	Name   string `json:"name"`
	UUID   string `json:"uuid"`
	CPU    int    `json:"cpu"`
	Memory int    `json:"memory"` // Memory in MB

	// Disk Info
	Disks []DiskInfo `json:"disks"`

	// Network Info
	Networks []NetworkInfo `json:"networks"`

	// VNC Info
	VNC *VNCInfo `json:"vnc,omitempty"`

	// Virtualizor-specific Metadata
	Metadata *VirtualizorMetadata `json:"metadata,omitempty"`

	// Source Information
	SourcePath string `json:"source_path"`
	SourceType string `json:"source_type"`
}

// DiskInfo represents disk configuration from XML
type DiskInfo struct {
	SourceFile string `json:"source_file"`
	Format     string `json:"format"` // qcow2, img, raw, etc.
	Device     string `json:"device"` // disk, cdrom
	Bus        string `json:"bus"`    // virtio, ide, scsi, sata
	TargetDev  string `json:"target_dev"`
	Size       int64  `json:"size,omitempty"` // Size in bytes if available
}

// NetworkInfo represents network interface configuration from XML
type NetworkInfo struct {
	MACAddress string  `json:"mac_address"`
	Bridge     string  `json:"bridge,omitempty"`
	Network    string  `json:"network,omitempty"`
	Type       string  `json:"type"`            // bridge, network, direct, etc.
	Model      string  `json:"model,omitempty"` // virtio, e1000, rtl8139
	IPConfig   *IPInfo `json:"ip_config,omitempty"`
}

// IPInfo represents IP configuration
type IPInfo struct {
	Address string `json:"address,omitempty"`
	Netmask string `json:"netmask,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	Family  string `json:"family,omitempty"` // ipv4, ipv6
}

// VNCInfo represents VNC display configuration
type VNCInfo struct {
	Port     int    `json:"port"`
	Password string `json:"password,omitempty"`
	Listen   string `json:"listen,omitempty"`
	AutoPort bool   `json:"auto_port"`
}

// VirtualizorMetadata represents Virtualizor-specific metadata
type VirtualizorMetadata struct {
	VMID       string            `json:"vm_id,omitempty"`
	OwnerID    string            `json:"owner_id,omitempty"`
	UserID     string            `json:"user_id,omitempty"`
	Plan       string            `json:"plan,omitempty"`
	PlanID     string            `json:"plan_id,omitempty"`
	ServerID   string            `json:"server_id,omitempty"`
	Hostname   string            `json:"hostname,omitempty"`
	CustomData map[string]string `json:"custom_data,omitempty"`
}

// LibvirtDomain represents the root element of a libvirt domain XML
type LibvirtDomain struct {
	XMLName  xml.Name `xml:"domain"`
	Type     string   `xml:"type,attr"`
	Name     string   `xml:"name"`
	UUID     string   `xml:"uuid"`
	Memory   Memory   `xml:"memory"`
	VCPU     VCPU     `xml:"vcpu"`
	Devices  Devices  `xml:"devices"`
	Metadata Metadata `xml:"metadata"`
}

// Memory represents memory configuration
type Memory struct {
	Value uint64 `xml:",chardata"`
	Unit  string `xml:"unit,attr"`
}

// VCPU represents CPU configuration
type VCPU struct {
	Value     uint   `xml:",chardata"`
	Placement string `xml:"placement,attr,omitempty"`
}

// Devices represents device configurations
type Devices struct {
	Disks      []Disk      `xml:"disk"`
	Interfaces []Interface `xml:"interface"`
	Graphics   []Graphic   `xml:"graphics"`
}

// Disk represents a disk device
type Disk struct {
	Device string      `xml:"device,attr"`
	Driver *DiskDriver `xml:"driver"`
	Source *DiskSource `xml:"source"`
	Target *DiskTarget `xml:"target"`
}

// DiskDriver represents disk driver configuration
type DiskDriver struct {
	Name string `xml:"name,attr"`
	Type string `xml:"type,attr"`
}

// DiskSource represents disk source configuration
type DiskSource struct {
	File   string `xml:"file,attr"`
	Pool   string `xml:"pool,attr,omitempty"`
	Volume string `xml:"volume,attr,omitempty"`
}

// DiskTarget represents disk target configuration
type DiskTarget struct {
	Dev string `xml:"dev,attr"`
	Bus string `xml:"bus,attr"`
}

// Interface represents a network interface
type Interface struct {
	Type   string           `xml:"type,attr"`
	MAC    *MACAddress      `xml:"mac"`
	Source *InterfaceSource `xml:"source"`
	Model  *InterfaceModel  `xml:"model"`
	IP     *InterfaceIP     `xml:"ip"`
}

// MACAddress represents MAC address configuration
type MACAddress struct {
	Address string `xml:"address,attr"`
}

// InterfaceSource represents network interface source
type InterfaceSource struct {
	Bridge  string `xml:"bridge,attr,omitempty"`
	Network string `xml:"network,attr,omitempty"`
	Dev     string `xml:"dev,attr,omitempty"`
}

// InterfaceModel represents interface model configuration
type InterfaceModel struct {
	Type string `xml:"type,attr"`
}

// InterfaceIP represents IP configuration within interface
type InterfaceIP struct {
	Address string `xml:"address,attr"`
	Netmask string `xml:"netmask,attr,omitempty"`
	Family  string `xml:"family,attr,omitempty"`
}

// Graphic represents a display graphics configuration
type Graphic struct {
	Type     string `xml:"type,attr"`
	Port     int    `xml:"port,attr,omitempty"`
	AutoPort string `xml:"autoport,attr,omitempty"`
	Listen   string `xml:"listen,attr,omitempty"`
	Password string `xml:"passwd,attr,omitempty"`
}

// Metadata represents domain metadata
type Metadata struct {
	Virtualizor *VirtualizorMetadataXML `xml:"virtualizor"`
	MaburVM     *MaburVMMetadataXML     `xml:"maburvm"`
	Raw         string                  `xml:",innerxml"`
}

// VirtualizorMetadataXML represents Virtualizor-specific metadata XML
type VirtualizorMetadataXML struct {
	VMID     string `xml:"vmid"`
	UserID   string `xml:"userid"`
	Plan     string `xml:"plan"`
	PlanID   string `xml:"planid"`
	ServerID string `xml:"serverid"`
	Hostname string `xml:"hostname"`
}

// MaburVMMetadataXML represents MaburVM metadata XML
type MaburVMMetadataXML struct {
	CreatedAt string `xml:"created_at"`
	VMName    string `xml:"vm_name"`
	VMUUID    string `xml:"vm_uuid"`
}

// ParseError represents a parsing error with context
type ParseError struct {
	Field   string
	Message string
}

func (e ParseError) Error() string {
	return fmt.Sprintf("parse error for field '%s': %s", e.Field, e.Message)
}

// ValidationError represents a validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("validation error for field '%s': %s", e.Field, e.Message)
}

// ParseVirtualizorDomainXML parses a Virtualizor libvirt domain XML file
// and returns an ImportCandidate with extracted information
func ParseVirtualizorDomainXML(xmlPath string) (*ImportCandidate, error) {
	// Read XML file
	xmlData, err := os.ReadFile(xmlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read XML file: %w", err)
	}

	return ParseVirtualizorDomainXMLBytes(xmlData, xmlPath)
}

// ParseVirtualizorDomainXMLBytes parses Virtualizor domain XML from bytes
func ParseVirtualizorDomainXMLBytes(xmlData []byte, sourcePath string) (*ImportCandidate, error) {
	// Parse XML
	var domain LibvirtDomain
	if err := xml.Unmarshal(xmlData, &domain); err != nil {
		return nil, fmt.Errorf("failed to parse XML: %w", err)
	}

	// Validate basic structure
	if err := validateDomainXML(&domain); err != nil {
		return nil, err
	}

	// Build ImportCandidate
	candidate := &ImportCandidate{
		SourcePath: sourcePath,
		SourceType: "virtualizor",
	}

	// Extract basic info
	extractBasicInfo(candidate, &domain)

	// Extract disk info
	extractDiskInfo(candidate, &domain)

	// Extract network info
	extractNetworkInfo(candidate, &domain)

	// Extract VNC info
	extractVNCInfo(candidate, &domain)

	// Extract Virtualizor metadata
	extractMetadata(candidate, &domain)

	// Validate the candidate
	if err := candidate.Validate(); err != nil {
		return nil, err
	}

	return candidate, nil
}

// validateDomainXML validates the parsed domain XML has minimum required fields
func validateDomainXML(domain *LibvirtDomain) error {
	if domain.Name == "" {
		return &ParseError{Field: "name", Message: "domain name is required"}
	}
	if domain.UUID == "" {
		return &ParseError{Field: "uuid", Message: "domain UUID is required"}
	}
	if domain.Memory.Value == 0 {
		return &ParseError{Field: "memory", Message: "memory must be greater than 0"}
	}
	if domain.VCPU.Value == 0 {
		return &ParseError{Field: "vcpu", Message: "vCPU count must be greater than 0"}
	}
	return nil
}

// extractBasicInfo extracts basic VM information
func extractBasicInfo(candidate *ImportCandidate, domain *LibvirtDomain) {
	candidate.Name = strings.TrimSpace(domain.Name)
	candidate.UUID = strings.TrimSpace(domain.UUID)
	candidate.CPU = int(domain.VCPU.Value)
	candidate.Memory = convertMemoryToMB(domain.Memory.Value, domain.Memory.Unit)
}

// extractDiskInfo extracts disk information from domain XML
func extractDiskInfo(candidate *ImportCandidate, domain *LibvirtDomain) {
	for _, disk := range domain.Devices.Disks {
		diskInfo := DiskInfo{
			Device: disk.Device,
		}

		// Extract source file
		if disk.Source != nil && disk.Source.File != "" {
			diskInfo.SourceFile = disk.Source.File
		}

		// Extract format from driver
		if disk.Driver != nil {
			diskInfo.Format = disk.Driver.Type
		}

		// Extract target info
		if disk.Target != nil {
			diskInfo.TargetDev = disk.Target.Dev
			diskInfo.Bus = disk.Target.Bus
		}

		// Only add if we have a source file
		if diskInfo.SourceFile != "" {
			candidate.Disks = append(candidate.Disks, diskInfo)
		}
	}
}

// extractNetworkInfo extracts network interface information
func extractNetworkInfo(candidate *ImportCandidate, domain *LibvirtDomain) {
	for _, iface := range domain.Devices.Interfaces {
		netInfo := NetworkInfo{
			Type: iface.Type,
		}

		// Extract MAC address
		if iface.MAC != nil {
			netInfo.MACAddress = iface.MAC.Address
		}

		// Extract source (bridge/network)
		if iface.Source != nil {
			netInfo.Bridge = iface.Source.Bridge
			netInfo.Network = iface.Source.Network
		}

		// Extract model
		if iface.Model != nil {
			netInfo.Model = iface.Model.Type
		}

		// Extract IP configuration
		if iface.IP != nil && iface.IP.Address != "" {
			netInfo.IPConfig = &IPInfo{
				Address: iface.IP.Address,
				Netmask: iface.IP.Netmask,
				Family:  iface.IP.Family,
			}
			if netInfo.IPConfig.Family == "" {
				netInfo.IPConfig.Family = "ipv4"
			}
		}

		// Only add if we have at least a MAC address
		if netInfo.MACAddress != "" {
			candidate.Networks = append(candidate.Networks, netInfo)
		}
	}
}

// extractVNCInfo extracts VNC configuration
func extractVNCInfo(candidate *ImportCandidate, domain *LibvirtDomain) {
	for _, graphic := range domain.Devices.Graphics {
		if graphic.Type == "vnc" {
			vncInfo := &VNCInfo{
				Port:   graphic.Port,
				Listen: graphic.Listen,
			}

			// Handle autoport
			if graphic.AutoPort == "yes" || graphic.Port == 0 || graphic.Port == -1 {
				vncInfo.AutoPort = true
			}

			// Extract password if present
			if graphic.Password != "" {
				vncInfo.Password = graphic.Password
			}

			candidate.VNC = vncInfo
			break // Only use first VNC graphics
		}
	}
}

// extractMetadata extracts Virtualizor-specific metadata
func extractMetadata(candidate *ImportCandidate, domain *LibvirtDomain) {
	metadata := &VirtualizorMetadata{
		CustomData: make(map[string]string),
	}

	// Extract from Virtualizor metadata if present
	if domain.Metadata.Virtualizor != nil {
		vmeta := domain.Metadata.Virtualizor
		metadata.VMID = vmeta.VMID
		metadata.UserID = vmeta.UserID
		metadata.Plan = vmeta.Plan
		metadata.PlanID = vmeta.PlanID
		metadata.ServerID = vmeta.ServerID
		metadata.Hostname = vmeta.Hostname
	}

	// Try to extract from raw metadata if structured parsing failed
	if domain.Metadata.Raw != "" && metadata.VMID == "" {
		extractMetadataFromRaw(metadata, domain.Metadata.Raw)
	}

	// Only set metadata if we found something
	if metadata.VMID != "" || metadata.UserID != "" || metadata.Plan != "" || len(metadata.CustomData) > 0 {
		candidate.Metadata = metadata
	}
}

// extractMetadataFromRaw attempts to extract metadata from raw XML string
func extractMetadataFromRaw(metadata *VirtualizorMetadata, rawXML string) {
	// Common Virtualizor metadata patterns
	patterns := map[string]*regexp.Regexp{
		"vmid":     regexp.MustCompile(`(?i)<vmid>([^<]+)</vmid>`),
		"userid":   regexp.MustCompile(`(?i)<userid>([^<]+)</userid>`),
		"user_id":  regexp.MustCompile(`(?i)<user_id>([^<]+)</user_id>`),
		"plan":     regexp.MustCompile(`(?i)<plan>([^<]+)</plan>`),
		"planid":   regexp.MustCompile(`(?i)<planid>([^<]+)</planid>`),
		"plan_id":  regexp.MustCompile(`(?i)<plan_id>([^<]+)</plan_id>`),
		"serverid": regexp.MustCompile(`(?i)<serverid>([^<]+)</serverid>`),
		"hostname": regexp.MustCompile(`(?i)<hostname>([^<]+)</hostname>`),
	}

	for key, pattern := range patterns {
		if matches := pattern.FindStringSubmatch(rawXML); len(matches) > 1 {
			value := strings.TrimSpace(matches[1])
			switch key {
			case "vmid":
				metadata.VMID = value
			case "userid", "user_id":
				metadata.UserID = value
				metadata.OwnerID = value
			case "plan":
				metadata.Plan = value
			case "planid", "plan_id":
				metadata.PlanID = value
			case "serverid":
				metadata.ServerID = value
			case "hostname":
				metadata.Hostname = value
			}
			metadata.CustomData[key] = value
		}
	}
}

// convertMemoryToMB converts memory value to MB based on unit
func convertMemoryToMB(value uint64, unit string) int {
	if unit == "" {
		unit = "KiB" // Default libvirt unit
	}

	switch strings.ToLower(unit) {
	case "b":
		return int(value / (1024 * 1024))
	case "kb", "k":
		return int(value / 1024)
	case "mb", "m":
		return int(value)
	case "gb", "g":
		return int(value * 1024)
	case "kib":
		return int(value / 1024)
	case "mib":
		return int(value)
	case "gib":
		return int(value * 1024)
	case "tib":
		return int(value * 1024 * 1024)
	default:
		// Assume KiB (libvirt default)
		return int(value / 1024)
	}
}

// Validate validates the ImportCandidate
func (c *ImportCandidate) Validate() error {
	var errors []string

	// Validate required fields
	if c.Name == "" {
		errors = append(errors, "name is required")
	}

	if c.UUID == "" {
		errors = append(errors, "UUID is required")
	} else if !isValidUUID(c.UUID) {
		errors = append(errors, "UUID format is invalid")
	}

	if c.CPU <= 0 {
		errors = append(errors, "CPU count must be greater than 0")
	}

	if c.Memory <= 0 {
		errors = append(errors, "memory must be greater than 0")
	}

	// Validate disks
	if len(c.Disks) == 0 {
		errors = append(errors, "at least one disk is required")
	} else {
		for i, disk := range c.Disks {
			if disk.SourceFile == "" {
				errors = append(errors, fmt.Sprintf("disk[%d]: source file is required", i))
			}
			if disk.Format == "" {
				// Not an error, but we should note it
				// Format can be detected from file extension
			}
		}
	}

	// Validate networks (optional but validate if present)
	for i, network := range c.Networks {
		if network.MACAddress != "" && !isValidMAC(network.MACAddress) {
			errors = append(errors, fmt.Sprintf("network[%d]: invalid MAC address format", i))
		}
		if network.IPConfig != nil && network.IPConfig.Address != "" {
			if net.ParseIP(network.IPConfig.Address) == nil {
				errors = append(errors, fmt.Sprintf("network[%d]: invalid IP address", i))
			}
		}
	}

	if len(errors) > 0 {
		return &ValidationError{
			Field:   "ImportCandidate",
			Message: strings.Join(errors, "; "),
		}
	}

	return nil
}

// isValidUUID checks if a string is a valid UUID format
func isValidUUID(uuid string) bool {
	pattern := regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	return pattern.MatchString(uuid)
}

// isValidMAC checks if a string is a valid MAC address format
func isValidMAC(mac string) bool {
	// Support various MAC formats: xx:xx:xx:xx:xx:xx, xx-xx-xx-xx-xx-xx, xxxx.xxxx.xxxx
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}$`),
		regexp.MustCompile(`^([0-9A-Fa-f]{2}-){5}[0-9A-Fa-f]{2}$`),
		regexp.MustCompile(`^[0-9A-Fa-f]{4}\.[0-9A-Fa-f]{4}\.[0-9A-Fa-f]{4}$`),
	}

	for _, pattern := range patterns {
		if pattern.MatchString(mac) {
			return true
		}
	}
	return false
}

// DetectDiskFormat attempts to detect disk format from file extension
func DetectDiskFormat(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".qcow2":
		return "qcow2"
	case ".img":
		return "raw"
	case ".raw":
		return "raw"
	case ".vmdk":
		return "vmdk"
	case ".vdi":
		return "vdi"
	case ".iso":
		return "iso"
	default:
		return ""
	}
}

// GetPrimaryDisk returns the primary (first) disk from the candidate
func (c *ImportCandidate) GetPrimaryDisk() *DiskInfo {
	if len(c.Disks) > 0 {
		return &c.Disks[0]
	}
	return nil
}

// GetDiskFormatWithFallback returns the disk format, falling back to extension detection
func (d *DiskInfo) GetDiskFormatWithFallback() string {
	if d.Format != "" {
		return d.Format
	}
	return DetectDiskFormat(d.SourceFile)
}

// HasVNC returns true if VNC is configured
func (c *ImportCandidate) HasVNC() bool {
	return c.VNC != nil
}

// GetVNCDisplay returns the VNC display string (e.g., ":0", ":1")
func (c *ImportCandidate) GetVNCDisplay() string {
	if c.VNC == nil {
		return ""
	}
	if c.VNC.Port >= 5900 {
		return fmt.Sprintf(":%d", c.VNC.Port-5900)
	}
	return fmt.Sprintf(":%d", c.VNC.Port)
}

// GetTotalDiskSize returns the total size of all disks in GB
func (c *ImportCandidate) GetTotalDiskSize() int64 {
	var total int64
	for _, disk := range c.Disks {
		if disk.Size > 0 {
			total += disk.Size
		}
	}
	return total / (1024 * 1024 * 1024) // Convert to GB
}

// ToMap returns the candidate as a map for logging/debugging
func (c *ImportCandidate) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"name":          c.Name,
		"uuid":          c.UUID,
		"cpu":           c.CPU,
		"memory_mb":     c.Memory,
		"disk_count":    len(c.Disks),
		"network_count": len(c.Networks),
		"has_vnc":       c.HasVNC(),
		"source_type":   c.SourceType,
		"source_path":   c.SourcePath,
	}
}
