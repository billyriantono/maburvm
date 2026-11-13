package libvirt

import (
	"regexp"
	"strings"
)

var (
	vncGraphicsRe  = regexp.MustCompile(`<graphics\b[^>]*\btype=['"]vnc['"][^>]*>`)
	graphicsPortRe = regexp.MustCompile(`\bport=['"]-?\d+['"]`)
	graphicsAutoRe = regexp.MustCompile(`\bautoport=['"](yes|no)['"]`)
)

// vncAutoPort switches a domain's VNC console from a fixed port to one libvirt
// picks when the domain starts, and reports whether it changed anything.
//
// Domains imported from another platform carry the port that platform gave
// them (port='5904' autoport='no'). Domains this panel creates use autoport,
// so sooner or later one of them is handed that same port while the imported
// VM is off, and the imported VM then refuses to start with "Failed to reserve
// port 5904". Only the port and autoport attributes are touched: the listen
// address, password and everything else in the definition stay as they were.
func vncAutoPort(domXML string) (string, bool) {
	changed := false
	out := vncGraphicsRe.ReplaceAllStringFunc(domXML, func(tag string) string {
		auto := graphicsAutoRe.FindStringSubmatch(tag)
		port := graphicsPortRe.FindString(tag)
		// No port named at all means libvirt already chooses one.
		if port == "" && (auto == nil || auto[1] == "yes") {
			return tag
		}
		if auto != nil && auto[1] == "yes" && strings.Contains(port, "-1") {
			return tag
		}
		fixed := tag
		if port != "" {
			fixed = graphicsPortRe.ReplaceAllString(fixed, "port='-1'")
		}
		if auto != nil {
			fixed = graphicsAutoRe.ReplaceAllString(fixed, "autoport='yes'")
		} else {
			fixed = strings.Replace(fixed, "<graphics", "<graphics autoport='yes'", 1)
		}
		if fixed != tag {
			changed = true
		}
		return fixed
	})
	return out, changed
}
