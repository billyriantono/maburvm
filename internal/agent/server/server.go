package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	healthcollector "github.com/maburvm/panel/internal/agent/health"
	"github.com/maburvm/panel/internal/agent/libvirt"
	"github.com/maburvm/panel/internal/agent/network"
	"github.com/maburvm/panel/internal/agent/vncproxy"
	"github.com/maburvm/panel/internal/shared/config"
	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpclibhealth "google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
)

// Server represents the gRPC server for the agent
type Server struct {
	grpcServer   *grpc.Server
	config       *config.AgentServerConfig
	tlsConfig    *tls.Config
	listener     net.Listener
	healthServer *grpclibhealth.Server
	mu           sync.RWMutex
	started      bool
	activeRPCs   sync.WaitGroup
}

// New creates a new gRPC server instance
func New(cfg *config.AgentServerConfig) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("agent server config is required")
	}

	// Validate configuration
	if err := validateConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	s := &Server{
		config: cfg,
	}

	// Setup TLS configuration
	tlsConfig, err := s.setupTLS()
	if err != nil {
		return nil, fmt.Errorf("failed to setup TLS: %w", err)
	}
	s.tlsConfig = tlsConfig

	return s, nil
}

// validateConfig validates the agent server configuration
func validateConfig(cfg *config.AgentServerConfig) error {
	if cfg.GRPCPort <= 0 || cfg.GRPCPort > 65535 {
		return fmt.Errorf("invalid gRPC port: %d", cfg.GRPCPort)
	}

	// In production, TLS is mandatory
	if cfg.Environment == "production" {
		if cfg.TLSCertFile == "" || cfg.TLSKeyFile == "" {
			return fmt.Errorf("TLS certificate and key files are required in production")
		}
	}

	// Validate bind address - should not be 0.0.0.0 in production
	if cfg.Environment == "production" && cfg.BindAddress == "0.0.0.0" {
		log.Println("WARNING: gRPC server binding to 0.0.0.0 in production. Consider binding to a private interface.")
	}

	return nil
}

// setupTLS configures TLS for the gRPC server
func (s *Server) setupTLS() (*tls.Config, error) {
	// Production: Load certificates from files
	if s.config.Environment == "production" {
		if s.config.TLSCertFile == "" || s.config.TLSKeyFile == "" {
			return nil, fmt.Errorf("TLS certificates required in production mode")
		}

		cert, err := tls.LoadX509KeyPair(s.config.TLSCertFile, s.config.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS certificates: %w", err)
		}

		return &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS13,
			CipherSuites: []uint16{
				tls.TLS_AES_256_GCM_SHA384,
				tls.TLS_CHACHA20_POLY1305_SHA256,
				tls.TLS_AES_128_GCM_SHA256,
			},
			PreferServerCipherSuites: true,
		}, nil
	}

	// Development: Generate self-signed certificate if not exists
	if s.config.TLSCertFile != "" && s.config.TLSKeyFile != "" {
		// Try to load existing certificates
		cert, err := tls.LoadX509KeyPair(s.config.TLSCertFile, s.config.TLSKeyFile)
		if err == nil {
			return &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			}, nil
		}
		log.Printf("Failed to load existing certs, generating self-signed: %v", err)
	}

	// Generate self-signed certificate for development
	return s.generateSelfSignedCert()
}

// generateSelfSignedCert creates a self-signed certificate for development
func (s *Server) generateSelfSignedCert() (*tls.Config, error) {
	log.Println("Generating self-signed TLS certificate for development...")

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to generate private key: %w", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"MaburVM Development"},
			CommonName:   "maburvm-agent",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:              []string{"localhost", "maburvm-agent"},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("failed to create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("failed to load generated key pair: %w", err)
	}

	log.Println("Self-signed certificate generated successfully")

	return &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, // For development only
		MinVersion:         tls.VersionTLS12,
	}, nil
}

// setupInterceptor creates the gRPC interceptor chain
func (s *Server) setupInterceptor() grpc.ServerOption {
	// Chain interceptors: Auth -> Recovery -> Logging
	return grpc.ChainUnaryInterceptor(
		s.loggingInterceptor,
		s.recoveryInterceptor,
		s.authInterceptor,
	)
}

// setupStreamInterceptor creates the gRPC stream interceptor chain
func (s *Server) setupStreamInterceptor() grpc.ServerOption {
	return grpc.ChainStreamInterceptor(
		s.streamLoggingInterceptor,
		s.streamRecoveryInterceptor,
		s.streamAuthInterceptor,
	)
}

// loggingInterceptor logs unary RPC calls
func (s *Server) loggingInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	start := time.Now()

	// Track active RPC
	s.activeRPCs.Add(1)
	defer s.activeRPCs.Done()

	resp, err := handler(ctx, req)

	duration := time.Since(start)
	if err != nil {
		log.Printf("[gRPC] %s | ERROR | %v | %s", info.FullMethod, err, duration)
	} else {
		log.Printf("[gRPC] %s | SUCCESS | %s", info.FullMethod, duration)
	}

	return resp, err
}

// streamLoggingInterceptor logs streaming RPC calls
func (s *Server) streamLoggingInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	start := time.Now()

	// Track active RPC
	s.activeRPCs.Add(1)
	defer s.activeRPCs.Done()

	err := handler(srv, ss)

	duration := time.Since(start)
	if err != nil {
		log.Printf("[gRPC Stream] %s | ERROR | %v | %s", info.FullMethod, err, duration)
	} else {
		log.Printf("[gRPC Stream] %s | SUCCESS | %s", info.FullMethod, duration)
	}

	return err
}

// recoveryInterceptor recovers from panics in unary handlers
func (s *Server) recoveryInterceptor(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp interface{}, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[gRPC PANIC RECOVERED] %s: %v", info.FullMethod, r)
			err = status.Errorf(codes.Internal, "internal server error")
		}
	}()
	return handler(ctx, req)
}

// streamRecoveryInterceptor recovers from panics in stream handlers
func (s *Server) streamRecoveryInterceptor(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[gRPC STREAM PANIC RECOVERED] %s: %v", info.FullMethod, r)
			err = status.Errorf(codes.Internal, "internal server error")
		}
	}()
	return handler(srv, ss)
}

// Start initializes and starts the gRPC server
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return fmt.Errorf("server already started")
	}

	// Create listener on configured bind address and port
	address := fmt.Sprintf("%s:%d", s.config.BindAddress, s.config.GRPCPort)
	lis, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", address, err)
	}
	s.listener = lis

	// Create gRPC server with interceptors and TLS
	opts := []grpc.ServerOption{
		s.setupInterceptor(),
		s.setupStreamInterceptor(),
		grpc.Creds(credentials.NewTLS(s.tlsConfig)),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle:     5 * time.Minute,
			MaxConnectionAge:      2 * time.Hour,
			MaxConnectionAgeGrace: 5 * time.Minute,
			Time:                  10 * time.Second,
			Timeout:               20 * time.Second,
		}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             5 * time.Second,
			PermitWithoutStream: true,
		}),
	}

	s.grpcServer = grpc.NewServer(opts...)

	// Register health check service
	s.healthServer = grpclibhealth.NewServer()
	grpc_health_v1.RegisterHealthServer(s.grpcServer, s.healthServer)
	s.healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)

	// Initialize dependencies for NodeAgent service
	libvirtMgr := libvirt.NewVMManager()
	var networkMgr *network.Manager
	var healthColl *healthcollector.MetricsCollector
	var vncProx *vncproxy.Proxy

	// Try to initialize optional dependencies
	networkMgr, err = network.NewManager()
	if err != nil {
		log.Printf("[Server] Failed to initialize network manager: %v", err)
		networkMgr = nil
	}

	healthColl = healthcollector.NewMetricsCollector()
	vncProx = vncproxy.NewProxy()

	// Register NodeAgent service with dependencies
	nodeAgentSvc := NewNodeAgentService(libvirtMgr, networkMgr, healthColl, vncProx)
	pb.RegisterNodeAgentServer(s.grpcServer, nodeAgentSvc)

	s.started = true

	log.Printf("gRPC server starting on %s (TLS enabled)", address)

	// Start serving in a goroutine
	go func() {
		if err := s.grpcServer.Serve(lis); err != nil && err != grpc.ErrServerStopped {
			log.Printf("gRPC server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the gRPC server
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return nil
	}

	log.Println("Gracefully shutting down gRPC server...")

	// Set health check to not serving
	if s.healthServer != nil {
		s.healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	}

	// Stop accepting new connections
	s.grpcServer.GracefulStop()

	// Wait for active RPCs to complete with timeout
	done := make(chan struct{})
	go func() {
		s.activeRPCs.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Println("All active RPCs completed")
	case <-time.After(s.config.ShutdownTimeout):
		log.Println("Shutdown timeout reached, forcing stop")
		s.grpcServer.Stop()
	}

	s.started = false
	log.Println("gRPC server stopped")

	return nil
}

// ForceStop immediately stops the gRPC server without waiting for active RPCs
func (s *Server) ForceStop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return
	}

	log.Println("Force stopping gRPC server...")
	s.grpcServer.Stop()
	s.started = false
}

// IsRunning returns true if the server is running
func (s *Server) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.started
}

// Address returns the server address
func (s *Server) Address() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// Run is the main entry point for the agent server
func Run() error {
	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Use environment-specific defaults if not configured
	agentServerCfg := &config.AgentServerConfig{
		GRPCPort:        cfg.Agent.GRPCPort,
		BindAddress:     getEnvOrDefault("AGENT_BIND_ADDRESS", "127.0.0.1"),
		TLSCertFile:     getEnvOrDefault("AGENT_TLS_CERT_FILE", ""),
		TLSKeyFile:      getEnvOrDefault("AGENT_TLS_KEY_FILE", ""),
		Environment:     getEnvOrDefault("ENVIRONMENT", "development"),
		ShutdownTimeout: 30 * time.Second,
		AuthToken:       getEnvOrDefault("AGENT_AUTH_TOKEN", ""),
	}

	// Create and start server
	server, err := New(agentServerCfg)
	if err != nil {
		return fmt.Errorf("failed to create server: %w", err)
	}

	if err := server.Start(); err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	// Wait for interrupt signal for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling
	sigCh := make(chan os.Signal, 1)
	setupSignalHandler(sigCh)

	select {
	case sig := <-sigCh:
		log.Printf("Received signal: %v", sig)
	case <-ctx.Done():
	}

	return server.Stop()
}

// getEnvOrDefault returns the environment variable value or default
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// setupSignalHandler sets up signal handling for graceful shutdown
func setupSignalHandler(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
}
