package console

import (
	"encoding/xml"
	"strings"
	"testing"
)

const pw = "S3CRETPW"

func domain(graphics string) string {
	return `<domain type='kvm'><name>v1</name><devices><disk type='file'><source file='/vm/a.qcow2'/></disk>` +
		graphics + `<video><model type='qxl'/></video></devices></domain>`
}

// The definition this panel wrote before the fix: every address, no password.
func TestSecureXMLClosesAnOpenConsole(t *testing.T) {
	in := domain(`<graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'><listen type='address' address='0.0.0.0'/></graphics>`)
	out, changed := SecureXML(in, pw)
	if !changed {
		t.Fatal("an open console was left as it was")
	}
	want := domain(`<graphics passwd='S3CRETPW' type='vnc' port='-1' autoport='yes' listen='127.0.0.1'><listen type='address' address='127.0.0.1'/></graphics>`)
	if out != want {
		t.Fatalf("got  %s\nwant %s", out, want)
	}
	if err := xml.Unmarshal([]byte(out), new(struct{})); err != nil {
		t.Fatalf("result is not XML: %v", err)
	}
	if again, changed := SecureXML(out, "OTHERPW1"); changed || again != out {
		t.Fatal("a secured definition was edited again, which would replace the stored password on every pass")
	}
	if !Exposed(in) || Exposed(out) || !Exposed(domain(`<graphics type='vnc' port='-1' passwd='' listen='127.0.0.1'/>`)) {
		t.Fatal("Exposed disagrees with SecureXML")
	}
}

func TestSecureXMLShapes(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"self-closing, imported with a node address and a fixed port",
			`<graphics type="vnc" port="5904" autoport="no" listen="203.0.113.7"/>`,
			`<graphics passwd='S3CRETPW' type="vnc" port="5904" autoport="no" listen='127.0.0.1'/>`},
		{"no listen at all falls to the host default, which may be every address",
			`<graphics type='vnc' port='-1' autoport='yes'/>`,
			`<graphics listen='127.0.0.1' passwd='S3CRETPW' type='vnc' port='-1' autoport='yes'/>`},
		{"a stored password is kept",
			`<graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0' passwd='keepme12'/>`,
			`<graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1' passwd='keepme12'/>`},
		{"an empty password is no password",
			`<graphics type='vnc' port='-1' passwd='' listen='127.0.0.1'/>`,
			`<graphics type='vnc' port='-1' passwd='S3CRETPW' listen='127.0.0.1'/>`},
		{"listen element without the attribute",
			`<graphics type='vnc' port='-1'><listen type='address' address='::'/></graphics>`,
			`<graphics passwd='S3CRETPW' type='vnc' port='-1'><listen type='address' address='127.0.0.1'/></graphics>`},
		{"a listen element with no address takes the host default",
			`<graphics type='vnc' port='-1'><listen type='address'/></graphics>`,
			`<graphics passwd='S3CRETPW' type='vnc' port='-1'><listen address='127.0.0.1' type='address'/></graphics>`},
		{"a console on a libvirt network has no address to rewrite",
			`<graphics type='vnc' port='-1'><listen type='network' network='default'/></graphics>`,
			`<graphics passwd='S3CRETPW' type='vnc' port='-1'><listen type='network' network='default'/></graphics>`},
	}
	for _, c := range cases {
		out, changed := SecureXML(domain(c.in), pw)
		if !changed || out != domain(c.want) {
			t.Errorf("%s:\n got  %s\n want %s", c.name, out, domain(c.want))
		}
	}
}

func TestSecureXMLLeavesOtherGraphicsAlone(t *testing.T) {
	// The spice element after a self-closing vnc one must not be taken for its body.
	in := domain(`<graphics type='vnc' port='-1' listen='127.0.0.1' passwd='keepme12'/><graphics type='spice' autoport='yes'><listen type='address' address='0.0.0.0'/></graphics>`)
	if out, changed := SecureXML(in, pw); changed || out != in {
		t.Fatalf("edited a definition that needed nothing: %s", out)
	}
	if out, changed := SecureXML(domain(``), pw); changed || out != domain(``) {
		t.Fatal("a domain with no console was edited")
	}
}

func TestFind(t *testing.T) {
	if _, ok := Find(domain(``)); ok {
		t.Fatal("found a console in a domain that has none")
	}

	open, ok := Find(domain(`<graphics type='vnc' port='5903' autoport='yes' listen='0.0.0.0'><listen type='address' address='0.0.0.0'/></graphics>`))
	if !ok || open.Port != 5903 || open.HasPassword || open.Local() {
		t.Fatalf("open console read as %+v", open)
	}
	if got := open.DialAddress(); got != "127.0.0.1:5903" {
		t.Fatalf("a socket on every address is reached on loopback, got %s", got)
	}

	safe, _ := Find(domain(`<graphics type='vnc' port='5900' autoport='yes' listen='127.0.0.1' passwd='abc'/>`))
	if !safe.HasPassword || !safe.Local() || safe.DialAddress() != "127.0.0.1:5900" {
		t.Fatalf("secured console read as %+v", safe)
	}

	pinned, _ := Find(domain(`<graphics type='vnc' port='5907'><listen type='address' address='203.0.113.7'/></graphics>`))
	if pinned.Local() || pinned.DialAddress() != "203.0.113.7:5907" {
		t.Fatalf("a socket on one address answers only there, got %+v %s", pinned, pinned.DialAddress())
	}

	ws, _ := Find(domain(`<graphics type='vnc' port='5901' autoport='yes' websocket='5700' listen='0.0.0.0'/>`))
	if ws.Port != 5901 || ws.WebsocketPort != 5700 {
		t.Fatalf("websocket socket read as %+v", ws)
	}

	// autoport must not be read as the port.
	unstarted, _ := Find(domain(`<graphics type='vnc' autoport='yes' listen='127.0.0.1'/>`))
	if unstarted.Port != 0 {
		t.Fatalf("port read from autoport: %d", unstarted.Port)
	}
}

func TestPasswords(t *testing.T) {
	a, b := NewPassword(), NewPassword()
	if len(a) != 8 || a == b || strings.ContainsAny(a, `'"<>&`) {
		t.Fatalf("bad generated passwords %q %q", a, b)
	}
	if ClipPassword("0123456789") != "01234567" || ClipPassword("short") != "short" {
		t.Fatal("ClipPassword must cut to the eight characters the console uses")
	}
}
