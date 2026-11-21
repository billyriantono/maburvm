package client

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
)

// A node that accepts the connection and then says nothing must not hold up a
// call to another node: the collector checks every node at once and would
// count the healthy ones as silent.
func TestHungNodeDoesNotBlockOtherNodes(t *testing.T) {
	hung, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer hung.Close()
	go func() {
		for {
			conn, err := hung.Accept()
			if err != nil {
				return
			}
			defer conn.Close() // held open, never answered
		}
	}()

	healthy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer()
	go srv.Serve(healthy)
	defer srv.Stop()

	c, err := NewAgentClient(&ClientConfig{Insecure: true, ConnectionTimeout: 5 * time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	hungCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hungDone := make(chan error, 1)
	go func() {
		_, err := c.getConnection(hungCtx, NodeInfo{ID: "hung", Address: hung.Addr().String()})
		hungDone <- err
	}()
	time.Sleep(200 * time.Millisecond) // let the hung dial get going

	start := time.Now()
	ctx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	c.RegisterNode(NodeInfo{ID: "ok", Address: healthy.Addr().String()})
	pc, err := c.getConnection(ctx, NodeInfo{ID: "ok", Address: healthy.Addr().String()})
	if err != nil {
		t.Fatalf("healthy node behind a hung one: %v after %s", err, time.Since(start))
	}
	if !pc.IsHealthy() {
		t.Fatal("healthy node's connection is not usable")
	}
	// Asking again reuses it instead of dialing a second time.
	again, err := c.getConnection(ctx, NodeInfo{ID: "ok", Address: healthy.Addr().String()})
	if err != nil || again != pc {
		t.Fatalf("connection not reused: %v", err)
	}

	cancel()
	if err := <-hungDone; err == nil {
		t.Fatal("the hung node connected")
	}
}
