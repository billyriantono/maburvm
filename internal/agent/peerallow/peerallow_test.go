package peerallow

import (
	"net"
	"testing"
	"time"
)

func TestParseAndAllowed(t *testing.T) {
	p, err := Parse("192.0.2.10, 10.20.0.0/16 2001:db8::1")
	if err != nil || len(p) != 3 {
		t.Fatalf("Parse: %v %v", p, err)
	}
	for addr, want := range map[string]bool{
		"192.0.2.10:40000":      true,
		"10.20.3.4:1":           true,
		"[2001:db8::1]:5":       true,
		"[::ffff:192.0.2.10]:5": true, // IPv4 on a dual-stack socket
		"127.0.0.1:9":           true, // the node itself, always
		"[::1]:9":               true,
		"192.0.2.11:40000":      false,
		"198.51.100.7:50051":    false,
		"not-an-address":        false,
	} {
		if got := Allowed(addr, p); got != want {
			t.Errorf("Allowed(%s) = %v, want %v", addr, got, want)
		}
	}
	if _, err := Parse("10.0.0.1,panel.example.com"); err == nil {
		t.Fatal("a hostname must be refused, not silently dropped")
	}
}

func TestListenClosesOthers(t *testing.T) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	// Loopback is always let through, so the listener hands the connection on.
	l := Listen(raw, nil)
	go func() {
		c, err := net.Dial("tcp", raw.Addr().String())
		if err == nil {
			time.Sleep(50 * time.Millisecond)
			c.Close()
		}
	}()
	c, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}
