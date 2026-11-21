package consolepipe

import (
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// A stream stand-in: what the far side sends arrives on in, what Pipe sends
// lands on out.
type fakeStream struct {
	in   chan []byte
	out  chan []byte
	done chan struct{}
}

func newFakeStream() *fakeStream {
	return &fakeStream{in: make(chan []byte, 8), out: make(chan []byte, 8), done: make(chan struct{})}
}

func (s *fakeStream) recv() ([]byte, error) {
	select {
	case b, ok := <-s.in:
		if !ok {
			return nil, io.EOF
		}
		return b, nil
	case <-s.done:
		return nil, errors.New("stream cancelled")
	}
}

func (s *fakeStream) send(b []byte) error {
	s.out <- b
	return nil
}

func TestPipeCarriesBothDirections(t *testing.T) {
	console, far := net.Pipe()
	stream := newFakeStream()
	result := make(chan error, 1)
	go func() { result <- Pipe(far, stream.recv, stream.send) }()

	// Console to stream.
	go console.Write([]byte("RFB 003.008\n"))
	select {
	case got := <-stream.out:
		if string(got) != "RFB 003.008\n" {
			t.Fatalf("stream got %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("console bytes never reached the stream")
	}

	// Stream to console; an empty message (the agent's "connected" signal) writes nothing.
	stream.in <- nil
	stream.in <- []byte("hello")
	buf := make([]byte, 16)
	console.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := console.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], []byte("hello")) {
		t.Fatalf("console got %q, %v", buf[:n], err)
	}

	// The stream ending closes the console side and is not an error.
	close(stream.in)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("clean end reported as %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Pipe did not return when the stream ended")
	}
	if _, err := console.Read(buf); err == nil {
		t.Fatal("console side left open after the stream ended")
	}
}

func TestPipeEndsWhenTheConsoleCloses(t *testing.T) {
	console, far := net.Pipe()
	stream := newFakeStream()
	defer close(stream.done)
	result := make(chan error, 1)
	go func() { result <- Pipe(far, stream.recv, stream.send) }()

	console.Close()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("console closing reported as %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Pipe did not return when the console closed")
	}
}

func TestPipeReportsAStreamFailure(t *testing.T) {
	console, far := net.Pipe()
	defer console.Close()
	boom := errors.New("node went away")
	err := Pipe(far, func() ([]byte, error) { return nil, boom }, func([]byte) error { return nil })
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want the stream's error", err)
	}
}
