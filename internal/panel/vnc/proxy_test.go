package vnc

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const testVM = "0b6f3c1e-5d0a-4c57-9a55-1f1e6f3f8a10"

// listener stands in for a console port on a node and counts who connects.
func consolePort(t *testing.T) (host string, port int, accepted chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	accepted = make(chan struct{}, 4)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- struct{}{}
			conn.Close()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, accepted
}

func TestConnectGoesThroughTheAgent(t *testing.T) {
	host, port, accepted := consolePort(t)
	local, remote := net.Pipe()
	defer remote.Close()

	s := NewProxyServer(nil, nil, nil, "secret")
	var asked string
	s.SetConsoleDialer(func(_ context.Context, vmID string) (net.Conn, error) {
		asked = vmID
		return local, nil
	})
	conn, via, err := s.connect(context.Background(), &ProxyConnection{VMID: testVM, Host: host, VNCPort: port})
	if err != nil || conn != local || asked != testVM {
		t.Fatalf("conn=%v via=%q err=%v asked=%q", conn, via, err, asked)
	}
	select {
	case <-accepted:
		t.Fatal("the console port was dialled although the agent carried the session")
	case <-time.After(100 * time.Millisecond):
	}
}

// An agent that predates the console stream answers Unimplemented. The console
// must keep working the way it did: straight to the node's console port.
func TestConnectFallsBackForAnOlderAgent(t *testing.T) {
	host, port, accepted := consolePort(t)
	s := NewProxyServer(nil, nil, nil, "secret")
	s.SetConsoleDialer(func(context.Context, string) (net.Conn, error) {
		return nil, status.Error(codes.Unimplemented, "unknown method ConsoleStream")
	})
	conn, _, err := s.connect(context.Background(), &ProxyConnection{VMID: testVM, Host: host, VNCPort: port})
	if err != nil {
		t.Fatalf("no fallback for an older agent: %v", err)
	}
	conn.Close()
	select {
	case <-accepted:
	case <-time.After(2 * time.Second):
		t.Fatal("the console port was never dialled")
	}
}

// Any other answer from the agent is the answer: a disabled console, a VM that
// is off, a node that is down. None of them may turn into a direct connection.
func TestConnectDoesNotFallBackOnARefusal(t *testing.T) {
	host, port, accepted := consolePort(t)
	for _, refusal := range []error{
		status.Error(codes.FailedPrecondition, "the VM is not running"),
		status.Error(codes.Unavailable, "node unreachable"),
		errors.New("console is disabled for this VM"),
	} {
		s := NewProxyServer(nil, nil, nil, "secret")
		s.SetConsoleDialer(func(context.Context, string) (net.Conn, error) { return nil, refusal })
		conn, _, err := s.connect(context.Background(), &ProxyConnection{VMID: testVM, Host: host, VNCPort: port})
		if !errors.Is(err, refusal) || conn != nil {
			t.Fatalf("refusal %q became conn=%v err=%v", refusal, conn, err)
		}
	}
	select {
	case <-accepted:
		t.Fatal("a refused console was reached through the console port")
	case <-time.After(100 * time.Millisecond):
	}
}

// The whole path a browser takes, with the agent's end of the session played by
// a pipe that echoes: the proxy's read loop polls with deadlines, which the
// connection the agent path hands it has to support.
func TestBrowserSessionOverTheAgentConnection(t *testing.T) {
	s := NewProxyServer(nil, nil, nil, "secret")
	s.SetConsoleDialer(func(context.Context, string) (net.Conn, error) {
		local, remote := net.Pipe()
		go func() {
			defer remote.Close()
			io.Copy(remote, remote)
		}()
		return local, nil
	})
	e := echo.New()
	e.GET("/ws/vnc", s.HandleWebSocket)
	srv := httptest.NewServer(e)
	defer srv.Close()

	token, _, err := s.GenerateVNCToken(testVM, "user-1", "", "192.0.2.1", 5900, TokenExpiry)
	if err != nil {
		t.Fatal(err)
	}
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/vnc?token=" + token
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	for _, msg := range []string{"RFB 003.008\n", "second message, after the read loop has timed out at least once"} {
		// Longer than the proxy's 100ms poll, so the echo crosses a deadline.
		time.Sleep(250 * time.Millisecond)
		if err := ws.WriteMessage(websocket.BinaryMessage, []byte(msg)); err != nil {
			t.Fatal(err)
		}
		var got []byte
		ws.SetReadDeadline(time.Now().Add(3 * time.Second))
		for len(got) < len(msg) {
			_, chunk, err := ws.ReadMessage()
			if err != nil {
				t.Fatalf("after %q: %v", got, err)
			}
			got = append(got, chunk...)
		}
		if string(got) != msg {
			t.Fatalf("got %q, want %q", got, msg)
		}
	}

	// Disabling the console ends the session that is open, not only future ones.
	if n := s.CloseVMConnections("some-other-vm"); n != 0 {
		t.Fatalf("closed %d sessions of a VM that has none", n)
	}
	if n := s.CloseVMConnections(testVM); n != 1 {
		t.Fatalf("closed %d sessions, want the one that is open", n)
	}
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("the browser's session stayed open after the console was closed")
	}
}
