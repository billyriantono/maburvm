// Package console holds the VM console logic that needs no libvirt connection,
// so it can be tested on any machine.
package console

import (
	"crypto/rand"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Loopback is where every console socket listens. The panel reaches it through
// the agent, so nothing outside the node needs the port.
const Loopback = "127.0.0.1"

// maxPasswordLen is all the VNC handshake uses; libvirt refuses a longer one.
const maxPasswordLen = 8

var (
	vncOpenTagRe  = regexp.MustCompile(`<graphics\b[^>]*\btype=['"]vnc['"][^>]*>`)
	passwdAttrRe  = regexp.MustCompile(`\s+passwd=(?:'([^']*)'|"([^"]*)")`)
	listenAttrRe  = regexp.MustCompile(`\blisten=(?:'([^']*)'|"([^"]*)")`)
	portAttrRe    = regexp.MustCompile(`\bport=['"](-?\d+)['"]`)
	wsPortAttrRe  = regexp.MustCompile(`\bwebsocket=['"](-?\d+)['"]`)
	listenElemRe  = regexp.MustCompile(`<listen\b[^>]*>`)
	listenAddrRe  = regexp.MustCompile(`\btype=['"]address['"]`)
	addressAttrRe = regexp.MustCompile(`\baddress=(?:'([^']*)'|"([^"]*)")`)
)

// NewPassword returns a random console password.
func NewPassword() string {
	return rand.Text()[:maxPasswordLen]
}

// ClipPassword cuts a password to the length the console can use. The
// characters past it never took part in the handshake.
func ClipPassword(password string) string {
	if len(password) > maxPasswordLen {
		return password[:maxPasswordLen]
	}
	return password
}

// vncElement is one <graphics type='vnc'> element of a domain definition: its
// opening tag and whatever follows up to its end.
type vncElement struct {
	start, tagEnd, end int
}

func vncElements(domXML string) []vncElement {
	var out []vncElement
	for _, loc := range vncOpenTagRe.FindAllStringIndex(domXML, -1) {
		el := vncElement{start: loc[0], tagEnd: loc[1], end: loc[1]}
		if !strings.HasSuffix(domXML[loc[0]:loc[1]], "/>") {
			closing := strings.Index(domXML[loc[1]:], "</graphics>")
			if closing < 0 {
				continue
			}
			el.end = loc[1] + closing + len("</graphics>")
		}
		out = append(out, el)
	}
	return out
}

func attrValue(m []string) string {
	if m == nil {
		return ""
	}
	return m[1] + m[2]
}

func isLoopback(addr string) bool {
	if addr == "localhost" {
		return true
	}
	ip := net.ParseIP(addr)
	return ip != nil && ip.IsLoopback()
}

// SecureXML returns a domain definition whose console needs a password and
// listens on loopback only, and whether anything had to change.
//
// A console without a password is open to whoever reaches the port. A
// definition read without its secrets shows no password even when one is
// stored, so it gets the new one: the panel sets its own each time it opens a
// console, and the stored one only has to exist. Only the graphics element is
// edited; the rest of the definition is returned byte for byte.
func SecureXML(domXML, password string) (string, bool) {
	changed := false
	els := vncElements(domXML)
	for i := len(els) - 1; i >= 0; i-- {
		el := els[i]
		tag, body := domXML[el.start:el.tagEnd], domXML[el.tagEnd:el.end]
		newTag, newBody := tag, body

		if m := passwdAttrRe.FindStringSubmatch(newTag); m == nil {
			newTag = strings.Replace(newTag, "<graphics", "<graphics passwd='"+password+"'", 1)
		} else if attrValue(m) == "" {
			newTag = passwdAttrRe.ReplaceAllLiteralString(newTag, " passwd='"+password+"'")
		}

		listeners := listenElemRe.FindAllString(body, -1)
		addressListeners := 0
		newBody = listenElemRe.ReplaceAllStringFunc(newBody, func(listen string) string {
			if !listenAddrRe.MatchString(listen) {
				return listen
			}
			addressListeners++
			m := addressAttrRe.FindStringSubmatch(listen)
			switch {
			case m == nil:
				return strings.Replace(listen, "<listen", "<listen address='"+Loopback+"'", 1)
			case isLoopback(attrValue(m)):
				return listen
			}
			return addressAttrRe.ReplaceAllLiteralString(listen, "address='"+Loopback+"'")
		})
		// A listen attribute has to agree with the first listen element, and a
		// console listening on a libvirt network or a socket has no address to set.
		switch m := listenAttrRe.FindStringSubmatch(newTag); {
		case m != nil && !isLoopback(attrValue(m)) && (len(listeners) == 0 || addressListeners > 0):
			newTag = listenAttrRe.ReplaceAllLiteralString(newTag, "listen='"+Loopback+"'")
		case m == nil && len(listeners) == 0:
			newTag = strings.Replace(newTag, "<graphics", "<graphics listen='"+Loopback+"'", 1)
		}

		if newTag != tag || newBody != body {
			changed = true
			domXML = domXML[:el.start] + newTag + newBody + domXML[el.end:]
		}
	}
	return domXML, changed
}

// Exposed reports whether SecureXML would change the definition.
func Exposed(domXML string) bool {
	_, changed := SecureXML(domXML, "-")
	return changed
}

// Endpoint is a console socket as a domain definition describes it.
type Endpoint struct {
	// Listen is the address the socket is bound to, empty when the definition
	// does not say.
	Listen string
	Port   int
	// WebsocketPort is the console's second socket, on the same address, when
	// the definition asks for one.
	WebsocketPort int
	HasPassword   bool
}

// Find returns the console of a domain definition, and false when the domain
// has none. Read the definition with its secrets or HasPassword is always false.
func Find(domXML string) (Endpoint, bool) {
	els := vncElements(domXML)
	if len(els) == 0 {
		return Endpoint{}, false
	}
	el := els[0]
	tag, body := domXML[el.start:el.tagEnd], domXML[el.tagEnd:el.end]
	ep := Endpoint{
		Listen:      attrValue(listenAttrRe.FindStringSubmatch(tag)),
		HasPassword: attrValue(passwdAttrRe.FindStringSubmatch(tag)) != "",
	}
	if m := portAttrRe.FindStringSubmatch(tag); m != nil {
		ep.Port, _ = strconv.Atoi(m[1])
	}
	if m := wsPortAttrRe.FindStringSubmatch(tag); m != nil {
		ep.WebsocketPort, _ = strconv.Atoi(m[1])
	}
	if ep.Listen == "" {
		for _, listen := range listenElemRe.FindAllString(body, -1) {
			if listenAddrRe.MatchString(listen) {
				ep.Listen = attrValue(addressAttrRe.FindStringSubmatch(listen))
				break
			}
		}
	}
	return ep, true
}

// Local reports whether only the node itself can reach the socket.
func (e Endpoint) Local() bool { return isLoopback(e.Listen) }

// DialAddress is where the agent connects to reach the console. A socket bound
// to every address answers on loopback; one bound to a single address answers
// only there.
func (e Endpoint) DialAddress() string {
	host := Loopback
	if ip := net.ParseIP(e.Listen); ip != nil && !ip.IsUnspecified() {
		host = e.Listen
	}
	return net.JoinHostPort(host, strconv.Itoa(e.Port))
}
