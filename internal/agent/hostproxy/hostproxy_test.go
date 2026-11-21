package hostproxy

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// testProxy listens on loopback with ports the kernel picks.
func testProxy() *Proxy {
	p := New(func() string { return "127.0.0.1" })
	p.httpPort, p.httpsPort = 0, 0
	return p
}

func hostPort(t *testing.T, u string) (string, int) {
	t.Helper()
	h, p, err := net.SplitHostPort(strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://"))
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(p)
	return h, n
}

func get(t *testing.T, addr, host string) (int, string, http.Header) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/path?q=1", nil)
	req.Host = host
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header
}

// HTTP goes to the VM its Host header names, with the client's address and
// the original host passed on; an unknown host gets a plain 404.
func TestHTTPRoutesByHost(t *testing.T) {
	seen := make(chan *http.Request, 4)
	backend := func(name string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen <- r
			fmt.Fprint(w, name+" "+r.URL.RequestURI())
		}))
	}
	a, b := backend("a"), backend("b")
	defer a.Close()
	defer b.Close()
	ah, ap := hostPort(t, a.URL)
	bh, bp := hostPort(t, b.URL)

	p := testProxy()
	defer p.Set("vm-a", nil)
	if err := p.Set("vm-a", []Route{{Hostname: "a.example.com", Backend: ah, HTTPPort: ap}}); err != nil {
		t.Fatal(err)
	}
	if err := p.Set("vm-b", []Route{{Hostname: "b.example.com", Backend: bh, HTTPPort: bp}}); err != nil {
		t.Fatal(err)
	}
	addr := p.httpLn.Addr().String()

	code, body, _ := get(t, addr, "A.Example.com:80")
	if code != 200 || body != "a /path?q=1" {
		t.Fatalf("a: %d %q", code, body)
	}
	r := <-seen
	if r.Host != "A.Example.com:80" || r.Header.Get("X-Forwarded-For") != "127.0.0.1" || r.Header.Get("X-Forwarded-Host") == "" {
		t.Fatalf("backend saw host %q, headers %v", r.Host, r.Header)
	}
	if code, body, _ = get(t, addr, "b.example.com"); code != 200 || body != "b /path?q=1" {
		t.Fatalf("b: %d %q", code, body)
	}
	<-seen
	if code, _, _ = get(t, addr, "c.example.com"); code != http.StatusNotFound {
		t.Fatalf("unknown host: %d", code)
	}
}

// A client-supplied X-Forwarded-For is replaced, not trusted.
func TestHTTPDropsSpoofedForwardedFor(t *testing.T) {
	got := make(chan string, 1)
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got <- r.Header.Get("X-Forwarded-For") }))
	defer back.Close()
	h, port := hostPort(t, back.URL)
	p := testProxy()
	defer p.Set("vm", nil)
	if err := p.Set("vm", []Route{{Hostname: "x.example.com", Backend: h, HTTPPort: port}}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://"+p.httpLn.Addr().String()+"/", nil)
	req.Host = "x.example.com"
	req.Header.Set("X-Forwarded-For", "6.6.6.6")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if xff := <-got; xff != "127.0.0.1" {
		t.Fatalf("X-Forwarded-For = %q", xff)
	}
}

// TLS is passed through by SNI: the client completes its handshake with the
// VM's own certificate, which the node never sees.
func TestTLSPassthroughBySNI(t *testing.T) {
	back := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "secure "+r.Host) }))
	defer back.Close()
	h, port := hostPort(t, back.URL)
	p := testProxy()
	defer p.Set("vm", nil)
	if err := p.Set("vm", []Route{{Hostname: "example.com", Backend: h, HTTPSPort: port}}); err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(back.Certificate())
	conn, err := tls.Dial("tcp", p.tlsLn.Addr().String(), &tls.Config{ServerName: "example.com", RootCAs: roots})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if !conn.ConnectionState().PeerCertificates[0].Equal(back.Certificate()) {
		t.Fatal("the client must see the VM's certificate")
	}
	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "secure example.com" {
		t.Fatalf("body %q", b)
	}

	// An unknown name is closed without an answer.
	if _, err := tls.Dial("tcp", p.tlsLn.Addr().String(), &tls.Config{ServerName: "other.example.com", RootCAs: roots}); err == nil {
		t.Fatal("an unknown server name must not reach any VM")
	}
}

// A hostname belongs to the VM set last; removing every route closes the
// ports so nothing listens for nothing.
func TestSetMovesHostnamesAndStopsWhenEmpty(t *testing.T) {
	p := testProxy()
	if err := p.Set("vm-a", []Route{{Hostname: "h.example.com", Backend: "10.0.0.2", HTTPPort: 80}}); err != nil {
		t.Fatal(err)
	}
	if err := p.Set("vm-b", []Route{{Hostname: "h.example.com", Backend: "10.0.0.3", HTTPPort: 80}}); err != nil {
		t.Fatal(err)
	}
	if r, _ := p.lookup("h.example.com"); r.Backend != "10.0.0.3" || len(p.byVM["vm-a"]) != 0 {
		t.Fatalf("route = %+v, vm-a = %v", r, p.byVM["vm-a"])
	}
	addr := p.httpLn.Addr().String()
	if err := p.Set("vm-b", nil); err != nil {
		t.Fatal(err)
	}
	if p.listening != "" {
		t.Fatal("no routes left: the proxy must stop listening")
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatal("the port must be closed")
	}
}

// Without a publish address, or with the port taken, Set says why and keeps
// the routes for a later retry.
func TestSetReportsWhyItCannotListen(t *testing.T) {
	p := New(func() string { return "" })
	if err := p.Set("vm", []Route{{Hostname: "h.example.com", Backend: "10.0.0.2", HTTPPort: 80}}); err == nil || !strings.Contains(err.Error(), "publish address") {
		t.Fatalf("err = %v", err)
	}
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port := hostPort(t, busy.Addr().String())
	q := New(func() string { return "127.0.0.1" })
	q.httpPort, q.httpsPort = port, 0
	if err := q.Set("vm", []Route{{Hostname: "h.example.com", Backend: "10.0.0.2", HTTPPort: 80}}); err == nil || !strings.Contains(err.Error(), "cannot open port") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := q.lookup("h.example.com"); !ok {
		t.Fatal("routes must be kept for a retry")
	}
	busy.Close()
	if err := q.Retry(); err != nil {
		t.Fatalf("retry once the port is free: %v", err)
	}
	q.Set("vm", nil)
}

func TestFromProtoAndNormalize(t *testing.T) {
	rs := FromProto([]*pb.HostnameForward{
		{Hostname: "WWW.Example.COM.", HttpPort: 8080, HttpsPort: 8443},
		{Hostname: "off.example.com"},
		{Hostname: "", HttpPort: 80},
	}, "10.0.0.9")
	if len(rs) != 1 || rs[0] != (Route{Hostname: "www.example.com", Backend: "10.0.0.9", HTTPPort: 8080, HTTPSPort: 8443}) {
		t.Fatalf("routes = %+v", rs)
	}
	if FromProto([]*pb.HostnameForward{{Hostname: "a.example.com", HttpPort: 80}}, "") != nil {
		t.Fatal("no backend address, no route")
	}
	for in, want := range map[string]string{"A.b.C:443": "a.b.c", "x.example.": "x.example", " y.example ": "y.example"} {
		if got := Normalize(in); got != want {
			t.Fatalf("Normalize(%q) = %q", in, got)
		}
	}
}

// A connection that is not TLS gives no server name and is not routed.
func TestPeekServerNameRejectsPlainText(t *testing.T) {
	name, _, err := peekServerName(strings.NewReader("GET / HTTP/1.1\r\nHost: a\r\n\r\n"))
	if err == nil || name != "" {
		t.Fatalf("name %q err %v", name, err)
	}
}

// One address holds at most perIP connections; past that it is closed at
// once, and a slot it frees is usable again.
func TestListenerCapsConnectionsPerAddress(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := limit(raw).(*limitedListener)
	ln.perIP = 2
	defer ln.Close()
	accepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- c
		}
	}()
	dial := func() net.Conn {
		c, err := net.Dial("tcp", raw.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	c1, c2, c3 := dial(), dial(), dial()
	defer c1.Close()
	defer c2.Close()
	s1, s2 := <-accepted, <-accepted
	_ = c3.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c3.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("third connection from one address: %v, want it closed", err)
	}
	s1.Close()
	c4 := dial()
	defer c4.Close()
	select {
	case s4 := <-accepted:
		s4.Close()
	case <-time.After(5 * time.Second):
		t.Fatal("a freed slot was not reused")
	}
	s2.Close()
}
