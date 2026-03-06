package vmimport

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Sample Virtualizor libvirt domain XML for testing
const sampleVirtualizorXML = `<?xml version="1.0" encoding="UTF-8"?>
<domain type="kvm">
  <name>test-vm-123</name>
  <uuid>12345678-1234-1234-1234-123456789abc</uuid>
  <memory unit="KiB">2097152</memory>
  <vcpu>4</vcpu>
  <devices>
    <disk type="file" device="disk">
      <driver name="qemu" type="qcow2"/>
      <source file="/var/lib/libvirt/images/test-vm-123.qcow2"/>
      <target dev="vda" bus="virtio"/>
    </disk>
    <disk type="file" device="cdrom">
      <driver name="qemu" type="raw"/>
      <source file="/var/lib/libvirt/images/seed.iso"/>
      <target dev="hda" bus="ide"/>
    </disk>
    <interface type="bridge">
      <mac address="52:54:00:12:34:56"/>
      <source bridge="br0"/>
      <model type="virtio"/>
    </interface>
    <graphics type="vnc" port="5901" listen="0.0.0.0" passwd="secret123"/>
  </devices>
  <metadata>
    <virtualizor>
      <vmid>123</vmid>
      <userid>456</userid>
      <plan>premium</plan>
      <planid>10</planid>
      <serverid>server-node-1</serverid>
      <hostname>test.example.com</hostname>
    </virtualizor>
  </metadata>
</domain>`

// Minimal valid XML
const minimalValidXML = `<?xml version="1.0" encoding="UTF-8"?>
<domain type="kvm">
  <name>minimal-vm</name>
  <uuid>aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee</uuid>
  <memory unit="KiB">1048576</memory>
  <vcpu>2</vcpu>
  <devices>
    <disk type="file" device="disk">
      <driver name="qemu" type="raw"/>
      <source file="/var/lib/libvirt/images/minimal.img"/>
      <target dev="vda" bus="virtio"/>
    </disk>
  </devices>
</domain>`

// XML with network interface but no bridge
const networkNoBridgeXML = `<?xml version="1.0" encoding="UTF-8"?>
<domain type="kvm">
  <name>network-test</name>
  <uuid>11111111-2222-3333-4444-555555555555</uuid>
  <memory unit="MiB">2048</memory>
  <vcpu>2</vcpu>
  <devices>
    <disk type="file" device="disk">
      <driver name="qemu" type="qcow2"/>
      <source file="/var/lib/libvirt/images/network-test.qcow2"/>
      <target dev="vda" bus="virtio"/>
    </disk>
    <interface type="network">
      <mac address="52:54:00:AB:CD:EF"/>
      <source network="default"/>
      <model type="e1000"/>
    </interface>
  </devices>
</domain>`

// XML with VNC autoport
const vncAutoportXML = `<?xml version="1.0" encoding="UTF-8"?>
<domain type="kvm">
  <name>vnc-autoport</name>
  <uuid>77777777-8888-9999-AAAA-BBBBBBBBBBBB</uuid>
  <memory unit="GiB">4</memory>
  <vcpu>2</vcpu>
  <devices>
    <disk type="file" device="disk">
      <driver name="qemu" type="qcow2"/>
      <source file="/var/lib/libvirt/images/vnc.qcow2"/>
      <target dev="vda" bus="virtio"/>
    </disk>
    <graphics type="vnc" port="-1" autoport="yes" listen="127.0.0.1"/>
  </devices>
</domain>`

// XML with raw metadata (fallback parsing)
const rawMetadataXML = `<?xml version="1.0" encoding="UTF-8"?>
<domain type="kvm">
  <name>raw-meta</name>
  <uuid>cccccccc-dddd-eeee-ffff-000000000000</uuid>
  <memory unit="KiB">1048576</memory>
  <vcpu>1</vcpu>
  <devices>
    <disk type="file" device="disk">
      <source file="/var/lib/libvirt/images/raw-meta.qcow2"/>
      <target dev="vda" bus="virtio"/>
    </disk>
  </devices>
  <metadata>
    <custom>
      <vmid>999</vmid>
      <user_id>777</user_id>
      <plan_id>5</plan_id>
      <hostname>raw.example.com</hostname>
    </custom>
  </metadata>
</domain>`

// Invalid XML - missing required fields
const invalidXMLMissingUUID = `<?xml version="1.0" encoding="UTF-8"?>
<domain type="kvm">
  <name>no-uuid</name>
  <memory unit="KiB">1048576</memory>
  <vcpu>2</vcpu>
  <devices>
    <disk type="file" device="disk">
      <source file="/var/lib/libvirt/images/test.img"/>
      <target dev="vda" bus="virtio"/>
    </disk>
  </devices>
</domain>`

// Invalid XML - malformed
const malformedXML = `<?xml version="1.0" encoding="UTF-8"?>
<domain type="kvm">
  <name>broken
  <uuid>invalid-uuid
</domain>`

// XML with IP configuration
const xmlWithIPConfig = `<?xml version="1.0" encoding="UTF-8"?>
<domain type="kvm">
  <name>ip-config</name>
  <uuid>aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb</uuid>
  <memory unit="KiB">1048576</memory>
  <vcpu>1</vcpu>
  <devices>
    <disk type="file" device="disk">
      <source file="/var/lib/libvirt/images/ip.qcow2"/>
      <target dev="vda" bus="virtio"/>
    </disk>
    <interface type="bridge">
      <mac address="52:54:00:11:22:33"/>
      <source bridge="br0"/>
      <ip address="192.168.1.100" netmask="255.255.255.0" family="ipv4"/>
    </interface>
  </devices>
</domain>`

func TestParseVirtualizorDomainXMLBytes(t *testing.T) {
	tests := []struct {
		name        string
		xmlData     string
		wantErr     bool
		errContains string
		validate    func(t *testing.T, candidate *ImportCandidate)
	}{
		{
			name:    "valid full Virtualizor XML",
			xmlData: sampleVirtualizorXML,
			wantErr: false,
			validate: func(t *testing.T, c *ImportCandidate) {
				if c.Name != "test-vm-123" {
					t.Errorf("expected name 'test-vm-123', got '%s'", c.Name)
				}
				if c.UUID != "12345678-1234-1234-1234-123456789abc" {
					t.Errorf("expected UUID '12345678-1234-1234-1234-123456789abc', got '%s'", c.UUID)
				}
				if c.CPU != 4 {
					t.Errorf("expected CPU 4, got %d", c.CPU)
				}
				if c.Memory != 2048 { // 2097152 KiB = 2048 MB
					t.Errorf("expected Memory 2048 MB, got %d", c.Memory)
				}
				if len(c.Disks) != 2 {
					t.Errorf("expected 2 disks, got %d", len(c.Disks))
				}
				if c.Disks[0].Format != "qcow2" {
					t.Errorf("expected disk format 'qcow2', got '%s'", c.Disks[0].Format)
				}
				if c.Disks[0].SourceFile != "/var/lib/libvirt/images/test-vm-123.qcow2" {
					t.Errorf("expected disk path '/var/lib/libvirt/images/test-vm-123.qcow2', got '%s'", c.Disks[0].SourceFile)
				}
				if len(c.Networks) != 1 {
					t.Errorf("expected 1 network, got %d", len(c.Networks))
				}
				if c.Networks[0].MACAddress != "52:54:00:12:34:56" {
					t.Errorf("expected MAC '52:54:00:12:34:56', got '%s'", c.Networks[0].MACAddress)
				}
				if c.Networks[0].Bridge != "br0" {
					t.Errorf("expected bridge 'br0', got '%s'", c.Networks[0].Bridge)
				}
				if c.VNC == nil {
					t.Error("expected VNC to be present")
				} else {
					if c.VNC.Port != 5901 {
						t.Errorf("expected VNC port 5901, got %d", c.VNC.Port)
					}
					if c.VNC.Password != "secret123" {
						t.Errorf("expected VNC password 'secret123', got '%s'", c.VNC.Password)
					}
					if c.VNC.Listen != "0.0.0.0" {
						t.Errorf("expected VNC listen '0.0.0.0', got '%s'", c.VNC.Listen)
					}
				}
				if c.Metadata == nil {
					t.Error("expected metadata to be present")
				} else {
					if c.Metadata.VMID != "123" {
						t.Errorf("expected VMID '123', got '%s'", c.Metadata.VMID)
					}
					if c.Metadata.UserID != "456" {
						t.Errorf("expected UserID '456', got '%s'", c.Metadata.UserID)
					}
					if c.Metadata.Plan != "premium" {
						t.Errorf("expected Plan 'premium', got '%s'", c.Metadata.Plan)
					}
					if c.Metadata.Hostname != "test.example.com" {
						t.Errorf("expected Hostname 'test.example.com', got '%s'", c.Metadata.Hostname)
					}
				}
			},
		},
		{
			name:    "minimal valid XML",
			xmlData: minimalValidXML,
			wantErr: false,
			validate: func(t *testing.T, c *ImportCandidate) {
				if c.Name != "minimal-vm" {
					t.Errorf("expected name 'minimal-vm', got '%s'", c.Name)
				}
				if c.CPU != 2 {
					t.Errorf("expected CPU 2, got %d", c.CPU)
				}
				if c.Memory != 1024 { // 1048576 KiB = 1024 MB
					t.Errorf("expected Memory 1024 MB, got %d", c.Memory)
				}
				if len(c.Disks) != 1 {
					t.Errorf("expected 1 disk, got %d", len(c.Disks))
				}
				if len(c.Networks) != 0 {
					t.Errorf("expected 0 networks, got %d", len(c.Networks))
				}
				if c.VNC != nil {
					t.Error("expected no VNC config")
				}
				if c.Metadata != nil {
					t.Error("expected no metadata")
				}
			},
		},
		{
			name:    "network without bridge using network type",
			xmlData: networkNoBridgeXML,
			wantErr: false,
			validate: func(t *testing.T, c *ImportCandidate) {
				if len(c.Networks) != 1 {
					t.Fatalf("expected 1 network, got %d", len(c.Networks))
				}
				if c.Networks[0].Type != "network" {
					t.Errorf("expected type 'network', got '%s'", c.Networks[0].Type)
				}
				if c.Networks[0].Network != "default" {
					t.Errorf("expected network 'default', got '%s'", c.Networks[0].Network)
				}
				if c.Networks[0].MACAddress != "52:54:00:AB:CD:EF" {
					t.Errorf("expected MAC '52:54:00:AB:CD:EF', got '%s'", c.Networks[0].MACAddress)
				}
				if c.Networks[0].Model != "e1000" {
					t.Errorf("expected model 'e1000', got '%s'", c.Networks[0].Model)
				}
				// Memory unit is MiB, should be 2048 MB
				if c.Memory != 2048 {
					t.Errorf("expected Memory 2048 MB (from MiB), got %d", c.Memory)
				}
			},
		},
		{
			name:    "VNC with autoport",
			xmlData: vncAutoportXML,
			wantErr: false,
			validate: func(t *testing.T, c *ImportCandidate) {
				if c.VNC == nil {
					t.Fatal("expected VNC to be present")
				}
				if !c.VNC.AutoPort {
					t.Error("expected AutoPort to be true")
				}
				if c.VNC.Listen != "127.0.0.1" {
					t.Errorf("expected listen '127.0.0.1', got '%s'", c.VNC.Listen)
				}
				// Memory unit is GiB, should be 4096 MB
				if c.Memory != 4096 {
					t.Errorf("expected Memory 4096 MB (from GiB), got %d", c.Memory)
				}
			},
		},
		{
			name:    "raw metadata fallback parsing",
			xmlData: rawMetadataXML,
			wantErr: false,
			validate: func(t *testing.T, c *ImportCandidate) {
				if c.Metadata == nil {
					t.Fatal("expected metadata to be present")
				}
				if c.Metadata.VMID != "999" {
					t.Errorf("expected VMID '999', got '%s'", c.Metadata.VMID)
				}
				if c.Metadata.UserID != "777" {
					t.Errorf("expected UserID '777', got '%s'", c.Metadata.UserID)
				}
				if c.Metadata.PlanID != "5" {
					t.Errorf("expected PlanID '5', got '%s'", c.Metadata.PlanID)
				}
				if c.Metadata.Hostname != "raw.example.com" {
					t.Errorf("expected Hostname 'raw.example.com', got '%s'", c.Metadata.Hostname)
				}
			},
		},
		{
			name:        "missing UUID",
			xmlData:     invalidXMLMissingUUID,
			wantErr:     true,
			errContains: "UUID is required",
		},
		{
			name:        "malformed XML",
			xmlData:     malformedXML,
			wantErr:     true,
			errContains: "failed to parse XML",
		},
		{
			name:    "XML with IP configuration",
			xmlData: xmlWithIPConfig,
			wantErr: false,
			validate: func(t *testing.T, c *ImportCandidate) {
				if len(c.Networks) != 1 {
					t.Fatalf("expected 1 network, got %d", len(c.Networks))
				}
				if c.Networks[0].IPConfig == nil {
					t.Fatal("expected IP config to be present")
				}
				if c.Networks[0].IPConfig.Address != "192.168.1.100" {
					t.Errorf("expected IP '192.168.1.100', got '%s'", c.Networks[0].IPConfig.Address)
				}
				if c.Networks[0].IPConfig.Netmask != "255.255.255.0" {
					t.Errorf("expected netmask '255.255.255.0', got '%s'", c.Networks[0].IPConfig.Netmask)
				}
				if c.Networks[0].IPConfig.Family != "ipv4" {
					t.Errorf("expected family 'ipv4', got '%s'", c.Networks[0].IPConfig.Family)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate, err := ParseVirtualizorDomainXMLBytes([]byte(tt.xmlData), "test.xml")
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error, got nil")
					return
				}
				if tt.errContains != "" && !contains(err.Error(), tt.errContains) {
					t.Errorf("expected error containing '%s', got '%s'", tt.errContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if tt.validate != nil {
				tt.validate(t, candidate)
			}
		})
	}
}

func TestParseVirtualizorDomainXML(t *testing.T) {
	// Create a temporary directory for test files
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		content  string
		wantErr  bool
		validate func(t *testing.T, candidate *ImportCandidate)
	}{
		{
			name:    "valid file",
			content: minimalValidXML,
			wantErr: false,
			validate: func(t *testing.T, c *ImportCandidate) {
				if c.Name != "minimal-vm" {
					t.Errorf("expected name 'minimal-vm', got '%s'", c.Name)
				}
			},
		},
		{
			name:    "non-existent file",
			content: "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var xmlPath string
			if tt.content != "" {
				xmlPath = filepath.Join(tmpDir, tt.name+".xml")
				if err := os.WriteFile(xmlPath, []byte(tt.content), 0644); err != nil {
					t.Fatalf("failed to write test file: %v", err)
				}
			} else {
				xmlPath = filepath.Join(tmpDir, "non-existent.xml")
			}

			candidate, err := ParseVirtualizorDomainXML(xmlPath)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if tt.validate != nil {
				tt.validate(t, candidate)
			}
		})
	}
}

func TestDetectDiskFormat(t *testing.T) {
	tests := []struct {
		filename string
		want     string
	}{
		{"/path/to/disk.qcow2", "qcow2"},
		{"/path/to/disk.img", "raw"},
		{"/path/to/disk.raw", "raw"},
		{"/path/to/disk.vmdk", "vmdk"},
		{"/path/to/disk.vdi", "vdi"},
		{"/path/to/disk.iso", "iso"},
		{"/path/to/disk.unknown", ""},
		{"/path/to/disk", ""},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got := DetectDiskFormat(tt.filename)
			if got != tt.want {
				t.Errorf("DetectDiskFormat(%q) = %q, want %q", tt.filename, got, tt.want)
			}
		})
	}
}

func TestConvertMemoryToMB(t *testing.T) {
	tests := []struct {
		value uint64
		unit  string
		want  int
	}{
		{1048576, "KiB", 1024},
		{2097152, "KiB", 2048},
		{1024, "MiB", 1024},
		{2048, "MiB", 2048},
		{2, "GiB", 2048},
		{4, "GiB", 4096},
		{1048576, "k", 1024},
		{1024, "M", 1024},
		{2, "G", 2048},
		{1048576, "", 1024}, // Default is KiB
		{1073741824, "b", 1024},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d%s", tt.value, tt.unit), func(t *testing.T) {
			got := convertMemoryToMB(tt.value, tt.unit)
			if got != tt.want {
				t.Errorf("convertMemoryToMB(%d, %q) = %d, want %d", tt.value, tt.unit, got, tt.want)
			}
		})
	}
}

func TestIsValidUUID(t *testing.T) {
	tests := []struct {
		uuid string
		want bool
	}{
		{"12345678-1234-1234-1234-123456789abc", true},
		{"AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE", true},
		{"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", true},
		{"12345678-1234-1234-1234-123456789ab", false},   // Too short
		{"12345678-1234-1234-1234-123456789abcd", false}, // Too long
		{"not-a-uuid", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.uuid, func(t *testing.T) {
			got := isValidUUID(tt.uuid)
			if got != tt.want {
				t.Errorf("isValidUUID(%q) = %v, want %v", tt.uuid, got, tt.want)
			}
		})
	}
}

func TestIsValidMAC(t *testing.T) {
	tests := []struct {
		mac  string
		want bool
	}{
		{"52:54:00:12:34:56", true},
		{"52-54-00-12-34-56", true},
		{"5254.0012.3456", true},
		{"AA:BB:CC:DD:EE:FF", true},
		{"aa:bb:cc:dd:ee:ff", true},
		{"52:54:00:12:34", false},       // Too short
		{"52:54:00:12:34:56:78", false}, // Too long
		{"not-a-mac", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.mac, func(t *testing.T) {
			got := isValidMAC(tt.mac)
			if got != tt.want {
				t.Errorf("isValidMAC(%q) = %v, want %v", tt.mac, got, tt.want)
			}
		})
	}
}

func TestImportCandidateMethods(t *testing.T) {
	candidate := &ImportCandidate{
		Name:   "test",
		UUID:   "12345678-1234-1234-1234-123456789abc",
		CPU:    2,
		Memory: 2048,
		Disks: []DiskInfo{
			{SourceFile: "/path/to/disk1.qcow2", Format: "qcow2", Size: 10737418240}, // 10GB
			{SourceFile: "/path/to/disk2.img", Format: "raw", Size: 21474836480},     // 20GB
		},
		Networks: []NetworkInfo{
			{MACAddress: "52:54:00:12:34:56"},
		},
		VNC: &VNCInfo{Port: 5901},
	}

	t.Run("GetPrimaryDisk", func(t *testing.T) {
		disk := candidate.GetPrimaryDisk()
		if disk == nil {
			t.Fatal("expected primary disk")
		}
		if disk.SourceFile != "/path/to/disk1.qcow2" {
			t.Errorf("expected first disk, got %s", disk.SourceFile)
		}
	})

	t.Run("GetTotalDiskSize", func(t *testing.T) {
		size := candidate.GetTotalDiskSize()
		if size != 30 { // 10GB + 20GB = 30GB
			t.Errorf("expected total size 30GB, got %d", size)
		}
	})

	t.Run("HasVNC", func(t *testing.T) {
		if !candidate.HasVNC() {
			t.Error("expected HasVNC() to be true")
		}
	})

	t.Run("GetVNCDisplay", func(t *testing.T) {
		display := candidate.GetVNCDisplay()
		if display != ":1" {
			t.Errorf("expected display ':1', got '%s'", display)
		}
	})

	t.Run("ToMap", func(t *testing.T) {
		m := candidate.ToMap()
		if m["name"] != "test" {
			t.Errorf("expected name 'test', got %v", m["name"])
		}
		if m["cpu"] != 2 {
			t.Errorf("expected cpu 2, got %v", m["cpu"])
		}
	})
}

func TestDiskInfoGetDiskFormatWithFallback(t *testing.T) {
	tests := []struct {
		name     string
		disk     DiskInfo
		expected string
	}{
		{
			name:     "format specified",
			disk:     DiskInfo{Format: "qcow2", SourceFile: "/path/to/disk.img"},
			expected: "qcow2",
		},
		{
			name:     "format from extension",
			disk:     DiskInfo{Format: "", SourceFile: "/path/to/disk.qcow2"},
			expected: "qcow2",
		},
		{
			name:     "no format available",
			disk:     DiskInfo{Format: "", SourceFile: "/path/to/disk"},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.disk.GetDiskFormatWithFallback()
			if got != tt.expected {
				t.Errorf("GetDiskFormatWithFallback() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestValidationError(t *testing.T) {
	err := &ValidationError{
		Field:   "test_field",
		Message: "test message",
	}
	expected := "validation error for field 'test_field': test message"
	if err.Error() != expected {
		t.Errorf("expected error '%s', got '%s'", expected, err.Error())
	}
}

func TestParseError(t *testing.T) {
	err := &ParseError{
		Field:   "test_field",
		Message: "test message",
	}
	expected := "parse error for field 'test_field': test message"
	if err.Error() != expected {
		t.Errorf("expected error '%s', got '%s'", expected, err.Error())
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(substr) <= len(s) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
