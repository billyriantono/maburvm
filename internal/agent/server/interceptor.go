package server

import (
	"context"
	"crypto/subtle"
	"log"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// contextKey is a custom type for context keys to avoid collisions
type contextKey string

const (
	// ContextKeyNodeID is the key for storing node ID in context
	ContextKeyNodeID contextKey = "node_id"
	// ContextKeyAuthenticated is the key for storing authentication status
	ContextKeyAuthenticated contextKey = "authenticated"
)

// authInterceptor validates authentication tokens for unary RPC calls
func (s *Server) authInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	// Skip authentication for health check and node registration
	if isPublicMethod(info.FullMethod) {
		return handler(ctx, req)
	}

	// Extract and validate token
	nodeID, err := s.validateToken(ctx)
	if err != nil {
		log.Printf("[Auth] Authentication failed for %s: %v", info.FullMethod, err)
		return nil, status.Errorf(codes.Unauthenticated, "invalid or missing authentication token: %v", err)
	}

	// Add node ID to context
	ctx = context.WithValue(ctx, ContextKeyNodeID, nodeID)
	ctx = context.WithValue(ctx, ContextKeyAuthenticated, true)

	return handler(ctx, req)
}

// streamAuthInterceptor validates authentication tokens for streaming RPC calls
func (s *Server) streamAuthInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	// Skip authentication for public methods
	if isPublicMethod(info.FullMethod) {
		return handler(srv, ss)
	}

	// Extract and validate token from context
	ctx := ss.Context()
	nodeID, err := s.validateToken(ctx)
	if err != nil {
		log.Printf("[Auth] Authentication failed for stream %s: %v", info.FullMethod, err)
		return status.Errorf(codes.Unauthenticated, "invalid or missing authentication token: %v", err)
	}

	// Wrap the stream to add node ID to context
	wrappedStream := &authenticatedServerStream{
		ServerStream: ss,
		ctx: context.WithValue(
			context.WithValue(ctx, ContextKeyNodeID, nodeID),
			ContextKeyAuthenticated, true,
		),
	}

	return handler(srv, wrappedStream)
}

// authenticatedServerStream wraps grpc.ServerStream to provide modified context
type authenticatedServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

// Context returns the modified context with authentication data
func (s *authenticatedServerStream) Context() context.Context {
	return s.ctx
}

// isPublicMethod returns true if the method doesn't require authentication
func isPublicMethod(fullMethod string) bool {
	// List of methods that don't require authentication
	publicMethods := []string{
		"/agent.NodeAgent/RegisterNode",
		"/grpc.health.v1.Health/Check",
		"/grpc.health.v1.Health/Watch",
	}

	for _, method := range publicMethods {
		if fullMethod == method {
			return true
		}
	}
	return false
}

// validateToken extracts and validates the authentication token from context
func (s *Server) validateToken(ctx context.Context) (string, error) {
	// Extract metadata from context
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Errorf(codes.Unauthenticated, "missing metadata")
	}

	// Get authorization header
	authHeader := md.Get("authorization")
	if len(authHeader) == 0 {
		return "", status.Errorf(codes.Unauthenticated, "missing authorization header")
	}

	// Parse Bearer token
	token, err := extractBearerToken(authHeader[0])
	if err != nil {
		return "", err
	}

	// Validate against the agent's configured token. Fail closed: if no token is
	// configured there is no valid credential, so reject rather than accept any
	// token (the previous behaviour was an auth bypass). Constant-time compare
	// avoids leaking the token via response timing.
	if s.config.AuthToken == "" {
		return "", status.Errorf(codes.Unauthenticated, "agent has no auth token configured")
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.config.AuthToken)) != 1 {
		return "", status.Errorf(codes.PermissionDenied, "invalid token")
	}

	// Extract node ID from metadata or derive from token
	nodeIDs := md.Get("x-node-id")
	if len(nodeIDs) > 0 && nodeIDs[0] != "" {
		return nodeIDs[0], nil
	}

	// If no node ID provided, use a default or derive from token
	return "unknown", nil
}

// extractBearerToken extracts the token from an Authorization header
func extractBearerToken(authHeader string) (string, error) {
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 {
		return "", status.Errorf(codes.Unauthenticated, "invalid authorization header format")
	}

	if !strings.EqualFold(parts[0], "Bearer") {
		return "", status.Errorf(codes.Unauthenticated, "authorization type must be Bearer")
	}

	token := strings.TrimSpace(parts[1])
	if token == "" {
		return "", status.Errorf(codes.Unauthenticated, "empty token")
	}

	return token, nil
}

// GetNodeIDFromContext extracts the node ID from context
func GetNodeIDFromContext(ctx context.Context) (string, bool) {
	nodeID, ok := ctx.Value(ContextKeyNodeID).(string)
	return nodeID, ok
}

// IsAuthenticated checks if the context has been authenticated
func IsAuthenticated(ctx context.Context) bool {
	authenticated, ok := ctx.Value(ContextKeyAuthenticated).(bool)
	return ok && authenticated
}
