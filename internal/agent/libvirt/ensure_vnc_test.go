package libvirt

import "testing"

// A Virtualizor-style imported domain defined with NO <graphics> device — the
// exact case that makes the console unproxyable.
const noGraphicsDomainXML = `<domain type='kvm'>
  <name>v1153</name>
  <uuid>db630543-4117-473a-b211-1d35daa73378</uuid>
  <memory unit='KiB'>2097152</memory>
  <vcpu>2</vcpu>
  <os><type arch='x86_64' machine='q35'>hvm</type></os>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <source file='/data/v1153-disk.qcow2'/>
      <target dev='vda' bus='virtio'/>
    </disk>
  </devices>
</domain>`

func TestEnsureVNCInXML_InjectsWhenMissing(t *testing.T) {
	out, added, err := ensureVNCInXML(noGraphicsDomainXML)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !added {
		t.Fatal("expected added=true for a domain with no graphics")
	}
	// extractVNCPort only finds a port when a VNC <graphics> device now exists.
	// Autoport is -1 until libvirt assigns a real port at start; -1 proves the
	// device was injected with autoport.
	if got := extractVNCPort(out); got != -1 {
		t.Fatalf("expected injected VNC autoport port -1, got %d", got)
	}
}

func TestEnsureVNCInXML_NoopWhenPresent(t *testing.T) {
	withVNC := `<domain type='kvm'><name>x</name><uuid>db630543-4117-473a-b211-1d35daa73378</uuid>` +
		`<devices><graphics type='vnc' port='5901' autoport='no' listen='0.0.0.0'/></devices></domain>`
	out, added, err := ensureVNCInXML(withVNC)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if added {
		t.Fatal("expected added=false when VNC already present")
	}
	if out != withVNC {
		t.Fatal("expected input returned unchanged when VNC already present")
	}
}
