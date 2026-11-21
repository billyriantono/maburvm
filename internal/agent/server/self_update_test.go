//go:build linux

package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"runtime"
	"strings"
	"testing"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"google.golang.org/grpc"
)

type fakeUpdateStream struct {
	grpc.ServerStream
	chunks []*pb.AgentUpdateChunk
	resp   *pb.AgentUpdateResponse
}

func (f *fakeUpdateStream) Context() context.Context { return context.Background() }
func (f *fakeUpdateStream) Recv() (*pb.AgentUpdateChunk, error) {
	if len(f.chunks) == 0 {
		return nil, io.EOF
	}
	c := f.chunks[0]
	f.chunks = f.chunks[1:]
	return c, nil
}
func (f *fakeUpdateStream) SendAndClose(r *pb.AgentUpdateResponse) error { f.resp = r; return nil }

// Every refusal must happen before anything is scheduled.
func TestUpdateAgentRefuses(t *testing.T) {
	zero := strings.Repeat("0", 64)
	cases := map[string][]*pb.AgentUpdateChunk{
		"wrong arch": {{Arch: "sparc", Size: 3, Sha256: zero, Data: []byte("abc")}},
		"bad digest": {{Arch: runtime.GOARCH, Size: 3, Sha256: zero, Data: []byte("abc")}},
		"oversend":   {{Arch: runtime.GOARCH, Size: 2, Sha256: zero, Data: []byte("abc")}},
		"short":      {{Arch: runtime.GOARCH, Size: 9, Sha256: zero, Data: []byte("abc")}},
		"no size":    {{Arch: runtime.GOARCH, Sha256: zero}},
	}
	for name, chunks := range cases {
		s := &fakeUpdateStream{chunks: chunks}
		if err := (&NodeAgentService{}).UpdateAgent(s); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.resp == nil || s.resp.Success {
			t.Fatalf("%s: expected refusal, got %+v", name, s.resp)
		}
	}
}

// A node with AGENT_UPDATE_PUBKEY refuses an unsigned build before anything is
// scheduled. The signature rules themselves are tested in agentsign.
func TestUpdateAgentSignature(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	data := []byte("abc")
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	t.Setenv("AGENT_UPDATE_PUBKEY", base64.StdEncoding.EncodeToString(pub))
	s := &fakeUpdateStream{chunks: []*pb.AgentUpdateChunk{{Arch: runtime.GOARCH, Size: 3, Sha256: digest, Data: data}}}
	if err := (&NodeAgentService{}).UpdateAgent(s); err != nil {
		t.Fatal(err)
	}
	if s.resp == nil || s.resp.Success || !strings.Contains(s.resp.Message, "signed") {
		t.Fatalf("an unsigned update must be refused, got %+v", s.resp)
	}
}
