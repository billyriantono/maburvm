package server

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
)

type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s contextStream) Context() context.Context { return s.ctx }

// The console stream is a VM's keyboard and screen. It must be refused without
// the node token before the handler runs, and the handler itself must refuse a
// stream that somehow arrives without having been authenticated.
func TestConsoleStreamRequiresTheNodeToken(t *testing.T) {
	srv := srvWithToken("secret")
	info := &grpc.StreamServerInfo{FullMethod: pb.NodeAgent_ConsoleStream_FullMethodName}
	if isPublicMethod(info.FullMethod) {
		t.Fatal("the console stream is listed as a public method")
	}

	reached := false
	handler := func(_ interface{}, ss grpc.ServerStream) error {
		reached = true
		if !IsAuthenticated(ss.Context()) {
			t.Error("handler reached with an unauthenticated context")
		}
		return nil
	}

	for name, ctx := range map[string]context.Context{
		"no token":    context.Background(),
		"wrong token": ctxWithMD("authorization", "Bearer nope"),
	} {
		err := srv.streamAuthInterceptor(nil, contextStream{ctx: ctx}, info, handler)
		if status.Code(err) != codes.Unauthenticated || reached {
			t.Fatalf("%s: got err=%v reached=%v, want Unauthenticated before the handler", name, err, reached)
		}
	}

	if err := srv.streamAuthInterceptor(nil, contextStream{ctx: ctxWithMD("authorization", "Bearer secret")}, info, handler); err != nil || !reached {
		t.Fatalf("the node token was refused: err=%v reached=%v", err, reached)
	}

	// Without the interceptor's mark the handler stops before reading anything.
	err := (&NodeAgentService{}).ConsoleStream(&grpc.GenericServerStream[pb.ConsoleData, pb.ConsoleData]{ServerStream: contextStream{ctx: context.Background()}})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("handler accepted an unauthenticated stream: %v", err)
	}
}
