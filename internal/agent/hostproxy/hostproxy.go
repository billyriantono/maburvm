// Package hostproxy routes web traffic arriving on a node's publish address to
// NAT VMs by hostname: plain HTTP by its Host header (as a reverse proxy, so
// the VM sees X-Forwarded-For), TLS by the SNI of its ClientHello, passed
// through untouched so the node never holds a certificate.
package hostproxy

import (
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

// Route sends one hostname to a VM address; a port of 0 leaves that one out.
type Route struct {
	Hostname  string
	Backend   string
	HTTPPort  int
	HTTPSPort int
}

// FromProto turns the hostnames of a network-config request into routes to
// backend, the VM's primary address.
func FromProto(fwds []*pb.HostnameForward, backend string) []Route {
	var out []Route
	for _, f := range fwds {
		h := Normalize(f.GetHostname())
		if h == "" || backend == "" || (f.GetHttpPort() == 0 && f.GetHttpsPort() == 0) {
			continue
		}
		out = append(out, Route{Hostname: h, Backend: backend, HTTPPort: int(f.GetHttpPort()), HTTPSPort: int(f.GetHttpsPort())})
	}
	return out
}

// Normalize lower-cases a hostname and drops a trailing dot and any port.
func Normalize(h string) string {
	h = strings.TrimSpace(h)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.TrimSuffix(strings.ToLower(h), ".")
}

// maxConns bounds the connections the proxy holds at once on each port, so a
// flood cannot take every file descriptor the agent has.
const maxConns = 4096

// Proxy holds every VM's routes and listens on the publish address's ports 80
// and 443 while there is at least one.
type Proxy struct {
	addr                func() string
	httpPort, httpsPort int

	mu        sync.Mutex
	byVM      map[string][]Route
	listening string
	httpLn    net.Listener
	tlsLn     net.Listener
	httpSrv   *http.Server

	table     atomic.Pointer[map[string]Route]
	transport http.RoundTripper
}

// New makes a proxy that listens on addr() (the node's publish address).
func New(addr func() string) *Proxy {
	p := &Proxy{addr: addr, httpPort: 80, httpsPort: 443, byVM: map[string][]Route{}}
	empty := map[string]Route{}
	p.table.Store(&empty)
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.Proxy = nil
	t.MaxIdleConnsPerHost = 16
	t.ResponseHeaderTimeout = 5 * time.Minute
	p.transport = t
	return p
}

// Set replaces a VM's routes (nil removes them) and starts or stops listening
// to match. A hostname another VM had moves to this one: the panel lets one VM
// at a time verify it. The routes are kept even when listening fails, so a
// later Set or Retry can still bring them up.
func (p *Proxy) Set(vmID string, routes []Route) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range routes {
		for other, rs := range p.byVM {
			if other == vmID {
				continue
			}
			kept := rs[:0:0]
			for _, o := range rs {
				if o.Hostname != r.Hostname {
					kept = append(kept, o)
				} else {
					log.Printf("[HostProxy] %s moves from VM %s to VM %s", r.Hostname, other, vmID)
				}
			}
			p.byVM[other] = kept
		}
	}
	if len(routes) == 0 {
		delete(p.byVM, vmID)
	} else {
		p.byVM[vmID] = append([]Route(nil), routes...)
	}
	table := map[string]Route{}
	for _, rs := range p.byVM {
		for _, r := range rs {
			table[r.Hostname] = r
		}
	}
	p.table.Store(&table)
	return p.ensureListening(len(table) > 0)
}

// Retry listens again if routes exist but the ports could not be opened before.
func (p *Proxy) Retry() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ensureListening(len(*p.table.Load()) > 0)
}

// RetryForever calls Retry every interval for the agent's lifetime, logging
// each new reason it fails.
func (p *Proxy) RetryForever(interval time.Duration) {
	last := ""
	for range time.Tick(interval) {
		msg := ""
		if err := p.Retry(); err != nil {
			msg = err.Error()
		}
		if msg != last && msg != "" {
			log.Printf("[HostProxy] WARNING: %s", msg)
		}
		last = msg
	}
}

func (p *Proxy) lookup(host string) (Route, bool) {
	r, ok := (*p.table.Load())[Normalize(host)]
	return r, ok
}

// ensureListening opens both ports on the publish address when want, and
// closes them when not. Assumes p.mu is held.
func (p *Proxy) ensureListening(want bool) error {
	addr := ""
	if want {
		addr = p.addr()
		if addr == "" {
			p.stop()
			return errors.New("the node has no publish address to route hostnames on")
		}
	}
	if p.listening == addr {
		return nil
	}
	p.stop()
	if !want {
		return nil
	}
	httpLn, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(p.httpPort)))
	if err != nil {
		return fmt.Errorf("the node cannot open port %d on %s for hostnames: %w", p.httpPort, addr, err)
	}
	tlsLn, err := net.Listen("tcp", net.JoinHostPort(addr, strconv.Itoa(p.httpsPort)))
	if err != nil {
		httpLn.Close()
		return fmt.Errorf("the node cannot open port %d on %s for hostnames: %w", p.httpsPort, addr, err)
	}
	p.httpLn, p.tlsLn = limit(httpLn), limit(tlsLn)
	p.httpSrv = &http.Server{
		Handler:           http.HandlerFunc(p.serveHTTP),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func(srv *http.Server, ln net.Listener) { _ = srv.Serve(ln) }(p.httpSrv, p.httpLn)
	go p.serveTLS(p.tlsLn)
	p.listening = addr
	log.Printf("[HostProxy] routing hostnames on %s ports %d and %d", addr, p.httpPort, p.httpsPort)
	return nil
}

// stop closes the listeners. Connections already passed through carry on.
// Assumes p.mu is held.
func (p *Proxy) stop() {
	if p.listening == "" {
		return
	}
	_ = p.httpSrv.Close()
	_ = p.tlsLn.Close()
	p.httpLn, p.tlsLn, p.httpSrv, p.listening = nil, nil, nil, ""
}

func (p *Proxy) serveHTTP(w http.ResponseWriter, r *http.Request) {
	rt, ok := p.lookup(r.Host)
	if !ok || rt.HTTPPort == 0 {
		http.Error(w, "No site is set up for this hostname on this server.", http.StatusNotFound)
		return
	}
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(rt.Backend, strconv.Itoa(rt.HTTPPort))}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
		},
		Transport: p.transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "The server behind this hostname did not answer.", http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
}

func (p *Proxy) serveTLS(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go p.passTLS(c)
	}
}

// passTLS reads the ClientHello's server name and splices the connection,
// hello included, to that hostname's VM.
func (p *Proxy) passTLS(c net.Conn) {
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	name, replay, err := peekServerName(c)
	if err != nil || name == "" {
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	rt, ok := p.lookup(name)
	if !ok || rt.HTTPSPort == 0 {
		return
	}
	backend, err := net.DialTimeout("tcp", net.JoinHostPort(rt.Backend, strconv.Itoa(rt.HTTPSPort)), 10*time.Second)
	if err != nil {
		return
	}
	defer backend.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(backend, replay)
		closeWrite(backend)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, backend)
		closeWrite(c)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else if lc, ok := c.(*limitedConn); ok {
		closeWrite(lc.Conn)
	}
}

// peekServerName reads a TLS ClientHello from r and returns its server name
// and a reader that yields everything read, then the rest of r. The hello is
// parsed by crypto/tls itself, which is then made to stop before answering.
func peekServerName(r io.Reader) (string, io.Reader, error) {
	var seen bytes.Buffer
	var name string
	got := false
	err := tls.Server(readOnlyConn{r: io.TeeReader(r, &seen)}, &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			name, got = h.ServerName, true
			return nil, errHelloRead
		},
	}).Handshake()
	if !got {
		return "", nil, err
	}
	return Normalize(name), io.MultiReader(&seen, r), nil
}

var errHelloRead = errors.New("hello read")

// readOnlyConn feeds crypto/tls the client's bytes and swallows its answers.
type readOnlyConn struct{ r io.Reader }

func (c readOnlyConn) Read(b []byte) (int, error)     { return c.r.Read(b) }
func (readOnlyConn) Write(b []byte) (int, error)      { return 0, io.ErrClosedPipe }
func (readOnlyConn) Close() error                     { return nil }
func (readOnlyConn) LocalAddr() net.Addr              { return nil }
func (readOnlyConn) RemoteAddr() net.Addr             { return nil }
func (readOnlyConn) SetDeadline(time.Time) error      { return nil }
func (readOnlyConn) SetReadDeadline(time.Time) error  { return nil }
func (readOnlyConn) SetWriteDeadline(time.Time) error { return nil }

// maxConnsPerIP bounds one client, so a single address cannot hold every
// connection and shut out every other site on the node.
const maxConnsPerIP = 256

// limit caps a listener at maxConns open connections, perIP from one address.
func limit(ln net.Listener) net.Listener {
	return &limitedListener{Listener: ln, sem: make(chan struct{}, maxConns), perIP: maxConnsPerIP, byIP: map[string]int{}}
}

type limitedListener struct {
	net.Listener
	sem   chan struct{}
	perIP int
	mu    sync.Mutex
	byIP  map[string]int
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		l.sem <- struct{}{}
		c, err := l.Listener.Accept()
		if err != nil {
			<-l.sem
			return nil, err
		}
		ip, _, _ := net.SplitHostPort(c.RemoteAddr().String())
		l.mu.Lock()
		full := l.byIP[ip] >= l.perIP
		if !full {
			l.byIP[ip]++
		}
		l.mu.Unlock()
		if full {
			_ = c.Close()
			<-l.sem
			continue
		}
		return &limitedConn{Conn: c, release: func() {
			l.mu.Lock()
			if l.byIP[ip]--; l.byIP[ip] <= 0 {
				delete(l.byIP, ip)
			}
			l.mu.Unlock()
			<-l.sem
		}}, nil
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
