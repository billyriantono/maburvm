package libvirt

import (
	"strings"
	"testing"
)

func TestVNCAutoPort(t *testing.T) {
	// An imported domain: a fixed port, a password, a websocket and a listen address.
	imported := `<domain type='kvm'><name>v1054</name><devices>
    <graphics type='vnc' port='5904' autoport='no' websocket='5704' listen='0.0.0.0' passwd='s3cret'>
      <listen type='address' address='0.0.0.0'/>
    </graphics>
    <video><model type='qxl'/></video>
  </devices></domain>`
	out, changed := vncAutoPort(imported)
	if !changed {
		t.Fatal("a fixed port should be switched to automatic")
	}
	for _, want := range []string{`port='-1'`, `autoport='yes'`, `websocket='5704'`, `passwd='s3cret'`, `listen='0.0.0.0'`, `<listen type='address' address='0.0.0.0'/>`, `<model type='qxl'/>`} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %s to survive, got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "5904") || strings.Contains(out, `autoport='no'`) {
		t.Fatalf("the fixed port is still there:\n%s", out)
	}
	// Doing it again changes nothing.
	if again, changed := vncAutoPort(out); changed || again != out {
		t.Fatal("an automatic port should be left alone")
	}

	// A fixed port with no autoport attribute at all, attributes in another order, double quotes.
	out, changed = vncAutoPort(`<graphics port="5910" type="vnc" listen="127.0.0.1"/>`)
	if !changed || !strings.Contains(out, `port='-1'`) || !strings.Contains(out, `autoport='yes'`) || !strings.Contains(out, `listen="127.0.0.1"`) {
		t.Fatalf("unexpected result: %s", out)
	}

	// What this panel writes, and devices that are not VNC, are untouched.
	for _, same := range []string{
		`<graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'/>`,
		`<graphics type='vnc'/>`,
		`<graphics type='spice' port='5930' autoport='no'/>`,
		`<domain><devices><disk type='file'/></devices></domain>`,
	} {
		if out, changed := vncAutoPort(same); changed || out != same {
			t.Fatalf("should be untouched: %s -> %s", same, out)
		}
	}
}
