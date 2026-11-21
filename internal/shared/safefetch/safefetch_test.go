package safefetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCheckAddr(t *testing.T) {
	for addr, public := range map[string]bool{
		"93.184.216.34":          true,
		"1.1.1.1":                true,
		"9.9.9.9":                true,
		"2606:4700:4700::1111":   true,
		"0.0.0.0":                false,
		"127.0.0.1":              false,
		"127.8.9.10":             false,
		"10.0.0.5":               false,
		"172.16.0.1":             false,
		"172.31.255.255":         false,
		"192.168.1.1":            false,
		"100.64.0.1":             false, // carrier-grade NAT
		"100.127.255.255":        false,
		"169.254.169.254":        false, // cloud metadata
		"192.0.2.1":              false,
		"198.18.0.1":             false,
		"203.0.113.9":            false,
		"224.0.0.1":              false,
		"255.255.255.255":        false,
		"::":                     false,
		"::1":                    false,
		"fc00::1":                false, // unique-local
		"fd12:3456::1":           false,
		"fe80::1":                false, // link-local
		"fe80::1%eth0":           false,
		"ff02::1":                false,
		"::ffff:127.0.0.1":       false, // IPv4 written as IPv6
		"::ffff:10.0.0.5":        false,
		"::ffff:169.254.169.254": false,
		"::ffff:93.184.216.34":   true,
		"::127.0.0.1":            false, // IPv4-compatible
		"::10.0.0.5":             false,
		"64:ff9b::7f00:1":        false, // NAT64 of 127.0.0.1
		"2002:7f00:1::1":         false, // 6to4 of 127.0.0.1
		"2001::1":                false, // Teredo
		"2001:db8::1":            false,
	} {
		err := CheckAddr(netip.MustParseAddr(addr))
		if (err == nil) != public {
			t.Errorf("%s: public=%v, want %v", addr, err == nil, public)
		}
		if err != nil && !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v is not ErrRefused", addr, err)
		}
	}
	if CheckAddr(netip.Addr{}) == nil {
		t.Error("the zero address was accepted")
	}
}

func TestCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://example.com/debian.iso": true,
		"http://93.184.216.34/x.iso":     true,
		"http://[2606:4700::1111]/x.iso": true,
		"ftp://example.com/x.iso":        false,
		"file:///etc/shadow":             false,
		"/var/lib/libvirt/images/a":      false,
		"http:///x.iso":                  false,
		"http://127.0.0.1/x.iso":         false,
		"http://[::1]:8080/x.iso":        false,
		"http://[::ffff:10.0.0.1]/x.iso": false,
		"http://169.254.169.254/latest":  false,
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if err := CheckURL(u); (err == nil) != ok {
			t.Errorf("%s: ok=%v, want %v", raw, err == nil, ok)
		}
	}
}

func TestCheckResolved(t *testing.T) {
	names := map[string][]string{
		"public.test":   {"93.184.216.34", "2606:4700:4700::1111"},
		"internal.test": {"10.0.0.5"},
		"mixed.test":    {"93.184.216.34", "127.0.0.1"},
		"mapped.test":   {"::ffff:192.168.0.1"},
	}
	lookup := func(_ context.Context, _, host string) ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range names[host] {
			out = append(out, netip.MustParseAddr(a))
		}
		if out == nil {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return out, nil
	}
	for raw, ok := range map[string]bool{
		"https://public.test/a.iso":   true,
		"http://93.184.216.34/a.iso":  true,
		"https://internal.test/a.iso": false,
		"https://mixed.test/a.iso":    false, // one internal address is enough
		"https://mapped.test/a.iso":   false,
		"https://missing.test/a.iso":  false,
		"http://localhost/a.iso":      false,
		"http://10.0.0.5/a.iso":       false,
		"s3://bucket/key":             false,
		"://":                         false,
	} {
		if err := CheckResolved(context.Background(), lookup, raw); (err == nil) != ok {
			t.Errorf("%s: ok=%v, want %v (%v)", raw, err == nil, ok, err)
		}
	}
}

// A name that resolves to loopback is refused when connecting, whatever the
// URL looked like: the test server is only ever reached by name here.
func TestClientRefusesInternalAddressAtDial(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	for _, target := range []string{srv.URL, "http://localhost:" + port + "/"} {
		resp, err := NewClient(5 * time.Second).Get(target)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s: fetched", target)
		}
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v is not ErrRefused", target, err)
		}
	}
	if hit {
		t.Error("the internal server was reached")
	}
}

func TestClientRefusesThisMachinesOwnAddress(t *testing.T) {
	own := LocalNets()
	if len(own) == 0 {
		t.Skip("no interface addresses")
	}
	for _, n := range own {
		addr := n.Addr()
		if CheckAddr(addr) != nil {
			continue // refused as internal anyway
		}
		_, err := NewClient(2 * time.Second).Get("http://" + net.JoinHostPort(addr.String(), "9") + "/")
		if !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v is not ErrRefused", addr, err)
		}
	}
}

func TestCheckNotLocal(t *testing.T) {
	nets := []netip.Prefix{
		netip.MustParsePrefix("93.184.216.34/24"), // a node's address on its guest bridge
		netip.MustParsePrefix("2606:4700:10::1/64"),
		netip.MustParsePrefix("9.9.9.9/32"),
	}
	for addr, ok := range map[string]bool{
		"93.184.216.34":         false, // the machine itself
		"93.184.216.1":          false, // its router
		"93.184.216.200":        false, // a guest on its bridge
		"::ffff:93.184.216.200": false,
		"2606:4700:10::beef":    false,
		"9.9.9.9":               false,
		"9.9.9.10":              true,
		"93.184.217.1":          true,
		"2606:4700:11::1":       true,
		"1.1.1.1":               true,
	} {
		err := CheckNotLocal(nets, netip.MustParseAddr(addr))
		if (err == nil) != ok {
			t.Errorf("%s: ok=%v, want %v", addr, err == nil, ok)
		}
		if err != nil && !errors.Is(err, ErrRefused) {
			t.Errorf("%s: %v is not ErrRefused", addr, err)
		}
	}
}

// allowLoopbackExcept stands in for "public" in tests, whose servers can only
// be on loopback, while still refusing one address to redirect to.
func allowLoopbackExcept(denied string) func(netip.Addr) error {
	return func(addr netip.Addr) error {
		if addr.String() == denied {
			return fmt.Errorf("%w: %s", ErrRefused, addr)
		}
		return nil
	}
}

// A redirect to a name is the case only the connect-time check can catch: the
// URL looks harmless and the address is known only once the name is resolved.
func TestClientChecksRedirectTargetsAtDial(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback")
	}
	reached := 0
	internal := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		io.WriteString(w, "secret")
	}))
	internal.Listener.Close()
	internal.Listener = ln
	internal.Start()
	defer internal.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+port+"/", http.StatusFound)
	}))
	defer public.Close()

	// Without the check the redirect does arrive, so the refusal below is the check's doing.
	resp, err := newClient(5*time.Second, allowLoopbackExcept("")).Get(public.URL)
	if err != nil || reached != 1 {
		t.Skipf("localhost does not reach ::1 here: %v", err)
	}
	resp.Body.Close()

	resp, err = newClient(5*time.Second, allowLoopbackExcept("::1")).Get(public.URL)
	if err == nil {
		resp.Body.Close()
		t.Error("a redirect to an internal address was followed")
	}
	if reached != 1 {
		t.Error("the internal server was reached through a redirect")
	}
}

func TestClientChecksRedirectURLs(t *testing.T) {
	var target string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target, http.StatusFound)
	}))
	defer srv.Close()
	for _, target = range []string{"file:///etc/passwd", "gopher://example.com/", "http://10.0.0.5/", "http://[::ffff:127.0.0.1]/"} {
		resp, err := newClient(5*time.Second, allowLoopbackExcept("")).Get(srv.URL)
		if err == nil {
			resp.Body.Close()
			t.Errorf("redirect to %s was followed", target)
		} else if !errors.Is(err, ErrRefused) {
			t.Errorf("redirect to %s: %v is not ErrRefused", target, err)
		}
	}
}

func TestClientCapsRedirects(t *testing.T) {
	hops := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops++
		http.Redirect(w, r, fmt.Sprintf("/%d", hops), http.StatusFound)
	}))
	defer srv.Close()

	// By name: a redirect to a loopback address written out is refused sooner.
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	resp, err := newClient(5*time.Second, allowLoopbackExcept("")).Get("http://localhost:" + port + "/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("an endless redirect was followed to an answer")
	}
	if !errors.Is(err, ErrRefused) {
		t.Errorf("%v is not ErrRefused", err)
	}
	if hops != MaxRedirects+1 {
		t.Errorf("made %d requests, want %d", hops, MaxRedirects+1)
	}
}

func TestClientIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer srv.Close()
	resp, err := newClient(5*time.Second, allowLoopbackExcept("")).Get(srv.URL)
	if err != nil {
		t.Fatalf("went through the proxy: %v", err)
	}
	resp.Body.Close()
}

func TestLimitReader(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 1000)
	for _, tc := range []struct {
		max  int64
		want int
		err  error
	}{
		{max: 2000, want: 1000},
		{max: 1000, want: 1000}, // exactly the limit is allowed
		{max: 999, err: ErrTooLarge},
		{max: 0, err: ErrTooLarge},
	} {
		// Two reads, so the limit is crossed mid-stream.
		got, err := io.ReadAll(LimitReader(io.MultiReader(bytes.NewReader(data[:500]), bytes.NewReader(data[500:])), tc.max))
		if !errors.Is(err, tc.err) {
			t.Errorf("max %d: err=%v, want %v", tc.max, err, tc.err)
		}
		if tc.err == nil && len(got) != tc.want {
			t.Errorf("max %d: read %d bytes, want %d", tc.max, len(got), tc.want)
		}
		if tc.err != nil && int64(len(got)) > tc.max+1 {
			t.Errorf("max %d: let %d bytes through", tc.max, len(got))
		}
	}
	if _, err := io.ReadAll(LimitReader(strings.NewReader(""), 0)); err != nil {
		t.Errorf("an empty body under a zero limit: %v", err)
	}
}
