// Package consolepipe carries a VM console connection over a message stream.
// The agent and the panel each run one end: the agent between the console
// socket and the gRPC stream, the panel between that stream and the browser.
package consolepipe

import (
	"errors"
	"io"
)

// Pipe copies bytes between conn and a message stream until either side ends,
// then closes conn. recv returns the next chunk from the stream; send writes
// one to it. The caller ends the stream once Pipe returns, which is also what
// stops the goroutine still waiting in recv.
func Pipe(conn io.ReadWriteCloser, recv func() ([]byte, error), send func([]byte) error) error {
	errc := make(chan error, 2)
	go func() {
		for {
			data, err := recv()
			if err == nil && len(data) > 0 {
				_, err = conn.Write(data)
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				// A copy: the stream may still hold the message after send returns.
				if serr := send(append([]byte(nil), buf[:n]...)); serr != nil {
					errc <- serr
					return
				}
			}
			if err != nil {
				errc <- err
				return
			}
		}
	}()
	err := <-errc
	conn.Close()
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	return err
}
