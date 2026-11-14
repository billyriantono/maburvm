package service

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// A node that is down must cost one dial, not one per page load.
func TestCheckNodeHealthAnswersFromCache(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	s := &NodeService{agentPort: port, timeout: time.Second}
	node := &models.Node{IPAddress: "127.0.0.1"}

	if on, _ := s.CheckNodeHealth(context.Background(), node); !on {
		t.Fatal("listening node reported offline")
	}
	ln.Close() // the node goes away; the recent answer is still served
	start := time.Now()
	if on, _ := s.CheckNodeHealth(context.Background(), node); !on {
		t.Fatal("fresh answer was not reused")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("cached answer waited on the network")
	}
}
