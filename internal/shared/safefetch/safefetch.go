// Package safefetch fetches a URL a customer typed from a machine that can
// reach things the customer must not: services on the host itself, the
// management network, other tenants' private addresses. Every address is
// checked at the moment of connecting, after the name has been resolved, so a
// name that resolves somewhere internal and a public URL that redirects there
// are both refused.
package safefetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"
)

// ErrRefused marks a URL or address this package will not fetch from. Asking
// again does not change the answer.
var ErrRefused = errors.New("fetch refused")

// ErrTooLarge is returned by a LimitReader once its limit is passed.
var ErrTooLarge = errors.New("the file is over the size limit")

// MaxRedirects is how many redirects a fetch follows before giving up.
const MaxRedirects = 5

// global6 is the only IPv6 space handed out for public use. Everything outside
// it (loopback, unique-local, link-local, multicast, IPv4-compatible and NAT64
// forms) is internal or special by definition.
var global6 = netip.MustParsePrefix("2000::/3")

// nonPublic lists the ranges that are not public Internet hosts.
var nonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network", unspecified
	netip.MustParsePrefix("10.0.0.0/8"),      // private
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT
	netip.MustParsePrefix("127.0.0.0/8"),     // loopback
	netip.MustParsePrefix("169.254.0.0/16"),  // link-local, cloud metadata
	netip.MustParsePrefix("172.16.0.0/12"),   // private
	netip.MustParsePrefix("192.0.0.0/24"),    // protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // documentation
	netip.MustParsePrefix("192.88.99.0/24"),  // 6to4 relay
	netip.MustParsePrefix("192.168.0.0/16"),  // private
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // documentation
	netip.MustParsePrefix("203.0.113.0/24"),  // documentation
	netip.MustParsePrefix("224.0.0.0/4"),     // multicast
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved, broadcast
	netip.MustParsePrefix("2001::/23"),       // Teredo and other protocol assignments
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4, carries an IPv4 address
	netip.MustParsePrefix("3fff::/20"),       // documentation
}

// CheckAddr refuses an address that is not a host on the public Internet.
func CheckAddr(addr netip.Addr) error {
	// An IPv4 address written as IPv6 (::ffff:10.0.0.1) is that IPv4 address.
	addr = addr.Unmap().WithZone("")
	if !addr.IsValid() {
		return fmt.Errorf("%w: invalid address", ErrRefused)
	}
	if addr.Is6() && !global6.Contains(addr) {
		return fmt.Errorf("%w: %s is not a public address", ErrRefused, addr)
	}
	for _, p := range nonPublic {
		if p.Contains(addr) {
			return fmt.Errorf("%w: %s is not a public address", ErrRefused, addr)
		}
	}
	return nil
}

// CheckURL refuses anything but an http(s) URL with a host, and a host written
// as an address that is not public. A host name is not resolved here.
func CheckURL(u *url.URL) error {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return fmt.Errorf("%w: only http and https URLs are fetched", ErrRefused)
	}
	if addr, err := netip.ParseAddr(u.Hostname()); err == nil {
		return CheckAddr(addr)
	}
	return nil
}

// Lookup resolves a host name; net.DefaultResolver.LookupNetIP is one.
type Lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)

// CheckResolved is CheckURL for a raw URL, plus a lookup of its host name: it
// refuses a name that does not resolve or that resolves to any address that is
// not public.
//
// This is an early, friendly answer and not the defence. The name can resolve
// differently a second later or on another machine, and the URL can redirect;
// only the checks a Client makes while connecting cover those.
func CheckResolved(ctx context.Context, lookup Lookup, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: not a URL", ErrRefused)
	}
	if err := CheckURL(u); err != nil {
		return err
	}
	if _, err := netip.ParseAddr(u.Hostname()); err == nil {
		return nil // an address, already checked
	}
	addrs, err := lookup(ctx, "ip", u.Hostname())
	if err != nil || len(addrs) == 0 {
		return fmt.Errorf("%w: %s does not resolve", ErrRefused, u.Hostname())
	}
	for _, a := range addrs {
		if err := CheckAddr(a); err != nil {
			return err
		}
	}
	return nil
}

// LocalNets returns the networks this machine's interfaces are on, each still
// carrying the machine's own address in it.
func LocalNets() []netip.Prefix {
	ifaddrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []netip.Prefix
	for _, a := range ifaddrs {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		addr, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		ones, bits := n.Mask.Size()
		if addr = addr.Unmap(); addr.Is4() && bits == 128 {
			ones -= 96 // an IPv4 address carried with an IPv6-length mask
		}
		if p := netip.PrefixFrom(addr, ones); p.IsValid() {
			out = append(out, p)
		}
	}
	return out
}

// CheckNotLocal refuses an address on one of nets, which includes the machine's
// own addresses. Its public address reaches services that listen on every
// interface, exactly as loopback does; and a neighbour on a network it is
// attached to (a guest on its bridge, the router, another node) sees the
// request arrive from the machine itself, past any firewall between that
// neighbour and the Internet.
func CheckNotLocal(nets []netip.Prefix, addr netip.Addr) error {
	addr = addr.Unmap().WithZone("")
	for _, n := range nets {
		if n.Contains(addr) {
			return fmt.Errorf("%w: %s is on this machine's own network", ErrRefused, addr)
		}
	}
	return nil
}

// NewClient returns an HTTP client for URLs a customer supplied. It connects
// only to public addresses outside this machine's own networks, follows at
// most MaxRedirects redirects and holds each one to the same rules, and ignores
// any proxy in the environment (the check would otherwise be made on the proxy).
func NewClient(timeout time.Duration) *http.Client {
	return newClient(timeout, func(addr netip.Addr) error {
		if err := CheckAddr(addr); err != nil {
			return err
		}
		return CheckNotLocal(LocalNets(), addr)
	})
}

func newClient(timeout time.Duration, check func(netip.Addr) error) *http.Client {
	dialer := &net.Dialer{
		Timeout: 30 * time.Second,
		// Runs for every connection attempt with the address the name resolved
		// to, which is the only place a check cannot be raced by a second lookup.
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %s is not an address", ErrRefused, address)
			}
			return check(ap.Addr())
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         dialer.DialContext,
			ForceAttemptHTTP2:   true,
			TLSHandshakeTimeout: 15 * time.Second,
			DisableKeepAlives:   true, // one download per client
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return fmt.Errorf("%w: more than %d redirects", ErrRefused, MaxRedirects)
			}
			return CheckURL(req.URL)
		},
	}
}

// LimitReader reads from r and fails with ErrTooLarge once more than max bytes
// have come through, so a limit holds for a body whose length was not declared
// or was declared falsely.
func LimitReader(r io.Reader, max int64) io.Reader {
	return &limitReader{r: r, left: max}
}

type limitReader struct {
	r    io.Reader
	left int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.left < 0 {
		return 0, ErrTooLarge
	}
	// One byte past the limit is enough to know the body is too long.
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		return n, ErrTooLarge
	}
	return n, err
}
