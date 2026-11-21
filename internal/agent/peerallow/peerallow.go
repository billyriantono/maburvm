// Package peerallow limits which hosts may open a connection to the agent.
package peerallow

import (
	"fmt"
	"log"
	"net"
	"net/netip"
	"strings"
	"sync"
)

// Parse reads IPs and CIDRs separated by commas or spaces.
func Parse(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		if !strings.Contains(f, "/") {
			a, err := netip.ParseAddr(f)
			if err != nil {
				return nil, fmt.Errorf("%q is not an IP address or CIDR", f)
			}
			out = append(out, netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()))
			continue
		}
		p, err := netip.ParsePrefix(f)
		if err != nil {
			return nil, fmt.Errorf("%q is not an IP address or CIDR", f)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// Allowed reports whether addr ("host:port") is loopback or inside a prefix.
func Allowed(addr string, prefixes []netip.Prefix) bool {
	ap, err := netip.ParseAddrPort(addr)
	if err != nil {
		return false
	}
	ip := ap.Addr().Unmap()
	if ip.IsLoopback() {
		return true
	}
	for _, p := range prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Listen wraps l so connections from anywhere else are closed before the TLS
// handshake. Each refused address is logged once.
func Listen(l net.Listener, prefixes []netip.Prefix) net.Listener {
	return &listener{Listener: l, prefixes: prefixes, logged: map[string]bool{}}
}

type listener struct {
	net.Listener
	prefixes []netip.Prefix
	mu       sync.Mutex
	logged   map[string]bool
}

func (l *listener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if Allowed(c.RemoteAddr().String(), l.prefixes) {
			return c, nil
		}
		host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
		l.mu.Lock()
		if !l.logged[host] && len(l.logged) < 1024 {
			l.logged[host] = true
			log.Printf("[Auth] refused a connection from %s: not in AGENT_ALLOWED_PANEL_IPS (logged once per address)", host)
		}
		l.mu.Unlock()
		c.Close()
	}
}
