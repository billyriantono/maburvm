package server

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/maburvm/panel/internal/shared/config"
)

func srvWithToken(token string) *Server {
	return &Server{config: &config.AgentServerConfig{AuthToken: token}}
}

func ctxWithMD(pairs ...string) context.Context {
	return metadata.NewIncomingContext(context.Background(), metadata.Pairs(pairs...))
}

func TestValidateToken(t *testing.T) {
	t.Run("missing metadata", func(t *testing.T) {
		if _, err := srvWithToken("secret").validateToken(context.Background()); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("want Unauthenticated, got %v", err)
		}
	})

	t.Run("missing authorization header", func(t *testing.T) {
		if _, err := srvWithToken("secret").validateToken(ctxWithMD("x-node-id", "n1")); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("want Unauthenticated, got %v", err)
		}
	})

	t.Run("wrong token is rejected", func(t *testing.T) {
		_, err := srvWithToken("secret").validateToken(ctxWithMD("authorization", "Bearer nope"))
		if status.Code(err) != codes.PermissionDenied {
			t.Fatalf("want PermissionDenied, got %v", err)
		}
	})

	t.Run("correct token authenticates and returns node id", func(t *testing.T) {
		nodeID, err := srvWithToken("secret").validateToken(ctxWithMD("authorization", "Bearer secret", "x-node-id", "node-42"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if nodeID != "node-42" {
			t.Fatalf("want node-42, got %q", nodeID)
		}
	})

	t.Run("fail closed when no token configured", func(t *testing.T) {
		// Even with a presented token, an agent with no configured token must
		// reject (previously it accepted ANY token — an auth bypass).
		_, err := srvWithToken("").validateToken(ctxWithMD("authorization", "Bearer anything"))
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("want Unauthenticated (fail-closed), got %v", err)
		}
	})
}
