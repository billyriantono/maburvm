// Package client provides gRPC client functionality for panel-agent communication.
package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/maburvm/panel/internal/shared/grpc/pb/api/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const (
	// Circuit breaker states
	stateClosed   int32 = iota // Normal operation
	stateOpen                  // Failing fast
	stateHalfOpen              // Testing if recovered

	// Default configuration values
	defaultMaxRetries                 = 5
	defaultInitialBackoff             = 100 * time.Millisecond
	defaultMaxBackoff                 = 30 * time.Second
	defaultBackoffMultiplier          = 2.0
	defaultRequestTimeout             = 30 * time.Second
	defaultConnectionTimeout          = 10 * time.Second
	defaultCircuitBreakerThreshold    = 5
	defaultCircuitBreakerResetTimeout = 30 * time.Second
	defaultPoolSize                   = 10

	// Metadata keys
	metadataAuthToken = "authorization"
)

// NodeInfo holds connection information for a node
type NodeInfo struct {
	ID         string
	Address    string
	Token      string
	TLSEnabled bool
}

// CircuitBreaker implements the circuit breaker pattern
type CircuitBreaker struct {
	state           int32
	failureCount    int32
	successCount    int32
	threshold       int32
	resetTimeout    time.Duration
	lastFailureTime int64
	mu              sync.RWMutex
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(threshold int32, resetTimeout time.Duration) *CircuitBreaker {
	if threshold <= 0 {
		threshold = defaultCircuitBreakerThreshold
	}
	if resetTimeout <= 0 {
		resetTimeout = defaultCircuitBreakerResetTimeout
	}
	return &CircuitBreaker{
		state:        stateClosed,
		threshold:    threshold,
		resetTimeout: resetTimeout,
	}
}

// Allow checks if the request should proceed
func (cb *CircuitBreaker) Allow() bool {
	state := atomic.LoadInt32(&cb.state)

	if state == stateClosed {
		return true
	}

	if state == stateOpen {
		lastFailure := atomic.LoadInt64(&cb.lastFailureTime)
		if time.Since(time.Unix(0, lastFailure)) > cb.resetTimeout {
			if atomic.CompareAndSwapInt32(&cb.state, stateOpen, stateHalfOpen) {
				atomic.StoreInt32(&cb.successCount, 0)
			}
			return true
		}
		return false
	}

	return true // half-open
}

// RecordSuccess records a successful request
func (cb *CircuitBreaker) RecordSuccess() {
	state := atomic.LoadInt32(&cb.state)

	if state == stateHalfOpen {
		count := atomic.AddInt32(&cb.successCount, 1)
		if count >= 2 { // Require 2 consecutive successes to close
			atomic.StoreInt32(&cb.state, stateClosed)
			atomic.StoreInt32(&cb.failureCount, 0)
			atomic.StoreInt32(&cb.successCount, 0)
		}
	} else if state == stateClosed {
		atomic.StoreInt32(&cb.failureCount, 0)
	}
}

// RecordFailure records a failed request
func (cb *CircuitBreaker) RecordFailure() {
	state := atomic.LoadInt32(&cb.state)

	if state == stateHalfOpen {
		atomic.StoreInt32(&cb.state, stateOpen)
		atomic.StoreInt64(&cb.lastFailureTime, time.Now().UnixNano())
	} else if state == stateClosed {
		count := atomic.AddInt32(&cb.failureCount, 1)
		if count >= cb.threshold {
			atomic.StoreInt32(&cb.state, stateOpen)
			atomic.StoreInt64(&cb.lastFailureTime, time.Now().UnixNano())
		}
	}
}

// State returns the current circuit breaker state
func (cb *CircuitBreaker) State() string {
	state := atomic.LoadInt32(&cb.state)
	switch state {
	case stateClosed:
		return "closed"
	case stateOpen:
		return "open"
	case stateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// PooledConnection wraps a gRPC connection with metadata
type PooledConnection struct {
	conn     *grpc.ClientConn
	client   pb.NodeAgentClient
	nodeID   string
	address  string
	token    string
	cb       *CircuitBreaker
	lastUsed int64
	mu       sync.RWMutex
	closed   bool
}

// IsHealthy checks if the connection is healthy
func (pc *PooledConnection) IsHealthy() bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	if pc.closed || pc.conn == nil {
		return false
	}

	state := pc.conn.GetState()
	return state == connectivity.Ready || state == connectivity.Idle
}

// Close closes the connection
func (pc *PooledConnection) Close() error {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	if pc.closed {
		return nil
	}

	pc.closed = true
	if pc.conn != nil {
		return pc.conn.Close()
	}
	return nil
}

// ClientConfig holds configuration for the agent client
type ClientConfig struct {
	MaxRetries         int
	InitialBackoff     time.Duration
	MaxBackoff         time.Duration
	BackoffMultiplier  float64
	RequestTimeout     time.Duration
	ConnectionTimeout  time.Duration
	TLSCertFile        string
	TLSKeyFile         string
	TLSCAFile          string
	Insecure           bool // No TLS at all (plain text)
	InsecureSkipVerify bool // Use TLS but skip certificate verification
}

// DefaultClientConfig returns default configuration
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		MaxRetries:        defaultMaxRetries,
		InitialBackoff:    defaultInitialBackoff,
		MaxBackoff:        defaultMaxBackoff,
		BackoffMultiplier: defaultBackoffMultiplier,
		RequestTimeout:    defaultRequestTimeout,
		ConnectionTimeout: defaultConnectionTimeout,
		Insecure:          false,
	}
}

// AgentClient manages connections to multiple agent nodes
type AgentClient struct {
	config      *ClientConfig
	connections map[string]*PooledConnection // nodeID -> connection
	mu          sync.RWMutex
	tlsConfig   *tls.Config
	pinning     bool // verify the agent's self-signed cert by pinned fingerprint
}

// NewAgentClient creates a new agent client
func NewAgentClient(config *ClientConfig) (*AgentClient, error) {
	if config == nil {
		config = DefaultClientConfig()
	}

	client := &AgentClient{
		config:      config,
		connections: make(map[string]*PooledConnection),
	}

	if config.InsecureSkipVerify {
		// No CA configured: encrypt and pin the agent's self-signed certificate
		// per node (trust on first use, then verify) instead of blindly trusting
		// any certificate. The per-connection tls.Config is built in dial().
		client.pinning = true
		client.tlsConfig = &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		}
	} else if !config.Insecure {
		tlsConfig, err := client.loadTLSConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load TLS config: %w", err)
		}
		client.tlsConfig = tlsConfig
	}

	return client, nil
}

// loadTLSConfig loads TLS configuration from files
func (c *AgentClient) loadTLSConfig() (*tls.Config, error) {
	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	// Load CA certificate if provided
	if c.config.TLSCAFile != "" {
		caCert, err := os.ReadFile(c.config.TLSCAFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read CA file: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("failed to parse CA certificate")
		}
		config.RootCAs = caCertPool
	}

	// Load client certificate if provided
	if c.config.TLSCertFile != "" && c.config.TLSKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(c.config.TLSCertFile, c.config.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load client certificate: %w", err)
		}
		config.Certificates = []tls.Certificate{cert}
	}

	return config, nil
}

// getConnection gets or creates a connection to a node
func (c *AgentClient) getConnection(ctx context.Context, node NodeInfo) (*PooledConnection, error) {
	c.mu.RLock()
	conn, exists := c.connections[node.ID]
	c.mu.RUnlock()

	if exists && conn.IsHealthy() {
		atomic.StoreInt64(&conn.lastUsed, time.Now().UnixNano())
		return conn, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Double-check after acquiring write lock
	if conn, exists := c.connections[node.ID]; exists && conn.IsHealthy() {
		atomic.StoreInt64(&conn.lastUsed, time.Now().UnixNano())
		return conn, nil
	}

	// Close existing connection if unhealthy
	if exists && conn != nil {
		_ = conn.Close()
	}

	// Create new connection with retry
	newConn, err := c.connectWithRetry(ctx, node)
	if err != nil {
		return nil, err
	}

	c.connections[node.ID] = newConn
	return newConn, nil
}

// connectWithRetry attempts to connect with exponential backoff
func (c *AgentClient) connectWithRetry(ctx context.Context, node NodeInfo) (*PooledConnection, error) {
	backoff := c.config.InitialBackoff

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
				backoff = time.Duration(float64(backoff) * c.config.BackoffMultiplier)
				if backoff > c.config.MaxBackoff {
					backoff = c.config.MaxBackoff
				}
			}
		}

		conn, err := c.dial(ctx, node)
		if err == nil {
			return conn, nil
		}

		if attempt == c.config.MaxRetries {
			return nil, fmt.Errorf("failed to connect after %d attempts: %w", c.config.MaxRetries+1, err)
		}
	}

	return nil, fmt.Errorf("failed to connect to node %s", node.ID)
}

// dial creates a new gRPC connection to a node
func (c *AgentClient) dial(ctx context.Context, node NodeInfo) (*PooledConnection, error) {
	opts := []grpc.DialOption{
		grpc.WithBlock(),
	}

	if c.tlsConfig != nil && node.TLSEnabled {
		var creds credentials.TransportCredentials
		if c.pinning {
			// Per-node certificate pinning (no CA): verify the agent presents the
			// same self-signed cert we recorded, defeating a MITM on this hop.
			creds = NodeTLSCredentials(node.ID, hostOnly(node.Address))
		} else {
			creds = credentials.NewTLS(c.tlsConfig)
		}
		opts = append(opts, grpc.WithTransportCredentials(creds))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	connectCtx, cancel := context.WithTimeout(ctx, c.config.ConnectionTimeout)
	defer cancel()

	conn, err := grpc.DialContext(connectCtx, node.Address, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", node.Address, err)
	}

	return &PooledConnection{
		conn:     conn,
		client:   pb.NewNodeAgentClient(conn),
		nodeID:   node.ID,
		address:  node.Address,
		token:    node.Token,
		cb:       NewCircuitBreaker(defaultCircuitBreakerThreshold, defaultCircuitBreakerResetTimeout),
		lastUsed: time.Now().UnixNano(),
	}, nil
}

// createAuthContext creates a context with authentication metadata
func (c *AgentClient) createAuthContext(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	md := metadata.New(map[string]string{
		metadataAuthToken: "Bearer " + token,
	})
	return metadata.NewOutgoingContext(ctx, md)
}

// executeWithRetry executes a function with retry logic and circuit breaker
func (c *AgentClient) executeWithRetry(ctx context.Context, node NodeInfo, operation func(context.Context, pb.NodeAgentClient) error) error {
	pc, err := c.getConnection(ctx, node)
	if err != nil {
		return err
	}

	// Check circuit breaker
	if !pc.cb.Allow() {
		return fmt.Errorf("circuit breaker is open for node %s", node.ID)
	}

	backoff := c.config.InitialBackoff

	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				backoff = time.Duration(float64(backoff) * c.config.BackoffMultiplier)
				if backoff > c.config.MaxBackoff {
					backoff = c.config.MaxBackoff
				}
			}

			// Reconnect on retry
			c.mu.Lock()
			delete(c.connections, node.ID)
			c.mu.Unlock()

			pc, err = c.getConnection(ctx, node)
			if err != nil {
				return err
			}
		}

		authCtx := c.createAuthContext(ctx, pc.token)
		// RequestTimeout is the default for callers that expressed no opinion, not
		// a ceiling. A caller that set its own, longer deadline has decided how
		// long the operation may take — provisioning a disk from a multi-GB image
		// legitimately runs for many minutes, and capping it at 30s cut the clone
		// off mid-write and left a partial disk on the node.
		timeout := c.config.RequestTimeout
		if deadline, ok := authCtx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining > timeout {
				timeout = remaining
			}
		}
		timeoutCtx, cancel := context.WithTimeout(authCtx, timeout)

		err = operation(timeoutCtx, pc.client)
		cancel()

		if err == nil {
			pc.cb.RecordSuccess()
			return nil
		}

		pc.cb.RecordFailure()

		if attempt == c.config.MaxRetries {
			return fmt.Errorf("operation failed after %d attempts: %w", c.config.MaxRetries+1, err)
		}
	}

	return fmt.Errorf("operation failed")
}

// ExecuteVMCommand sends a VM lifecycle command to a node
func (c *AgentClient) ExecuteVMCommand(ctx context.Context, nodeID string, command *pb.VMCommandRequest) (*pb.VMCommandResponse, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var response *pb.VMCommandResponse
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.ExecuteVMCommand(ctx, command)
		if err != nil {
			return err
		}
		response = resp
		return nil
	})

	if err != nil {
		return nil, err
	}

	return response, nil
}

// GetVMStatus retrieves the current state of a VM
func (c *AgentClient) GetVMStatus(ctx context.Context, nodeID string, vmID string) (*pb.VMStatusResponse, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	req := &pb.VMStatusRequest{
		VmId:           vmID,
		IncludeMetrics: true,
	}

	var response *pb.VMStatusResponse
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.GetVMStatus(ctx, req)
		if err != nil {
			return err
		}
		response = resp
		return nil
	})

	if err != nil {
		return nil, err
	}

	return response, nil
}

// MetricsChannel is a channel that receives VM metrics
type MetricsChannel <-chan *pb.VMMetricsResponse

// StreamVMMetrics opens a streaming connection for real-time VM metrics
func (c *AgentClient) StreamVMMetrics(ctx context.Context, nodeID string, vmIDs []string, intervalMs int32) (MetricsChannel, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	pc, err := c.getConnection(ctx, node)
	if err != nil {
		return nil, err
	}

	if !pc.cb.Allow() {
		return nil, fmt.Errorf("circuit breaker is open for node %s", nodeID)
	}

	req := &pb.VMMetricsRequest{
		VmIds:      vmIDs,
		IntervalMs: intervalMs,
	}

	authCtx := c.createAuthContext(ctx, pc.token)
	stream, err := pc.client.StreamVMMetrics(authCtx, req)
	if err != nil {
		pc.cb.RecordFailure()
		return nil, fmt.Errorf("failed to start metrics stream: %w", err)
	}

	pc.cb.RecordSuccess()

	metricsCh := make(chan *pb.VMMetricsResponse, 100)

	go func() {
		defer close(metricsCh)

		for {
			select {
			case <-ctx.Done():
				return
			default:
				resp, err := stream.Recv()
				if err != nil {
					// Stream ended or error occurred
					return
				}

				select {
				case metricsCh <- resp:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return metricsCh, nil
}

// ApplyNetworkConfig applies network configuration to a VM
func (c *AgentClient) ApplyNetworkConfig(ctx context.Context, nodeID string, config *pb.NetworkConfigRequest) (*pb.NetworkConfigResponse, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var response *pb.NetworkConfigResponse
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.ApplyNetworkConfig(ctx, config)
		if err != nil {
			return err
		}
		response = resp
		return nil
	})

	if err != nil {
		return nil, err
	}

	return response, nil
}

// VNCProxyResult contains the result of starting a VNC proxy
type VNCProxyResult struct {
	WebSocketURL  string
	WebSocketPort int32
	Token         string
	ExpiresAt     time.Time
}

// StartVNCProxy starts a VNC proxy for console access
func (c *AgentClient) StartVNCProxy(ctx context.Context, nodeID string, vmID string, expirySeconds int32) (*VNCProxyResult, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	req := &pb.VNCProxyRequest{
		VmId:          vmID,
		ExpirySeconds: expirySeconds,
	}

	var response *pb.VNCProxyResponse
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.StartVNCProxy(ctx, req)
		if err != nil {
			return err
		}
		response = resp
		return nil
	})

	if err != nil {
		return nil, err
	}

	if !response.Success {
		return nil, fmt.Errorf("failed to start VNC proxy: %s", response.Error.Message)
	}

	return &VNCProxyResult{
		WebSocketURL:  response.WebsocketUrl,
		WebSocketPort: response.WebsocketPort,
		Token:         response.Token,
		ExpiresAt:     response.ExpiresAt.AsTime(),
	}, nil
}

// NodeSystemInfo holds detailed system information about a node
type NodeSystemInfo struct {
	OSName         string
	OSVersion      string
	KernelVersion  string
	Architecture   string
	CPUModel       string
	CPUCores       int32
	CPUThreads     int32
	MemoryTotal    int64
	DiskTotal      int64
	LibvirtVersion string
	// AgentCommit/AgentBuildTime/AgentVersion identify the build running on the
	// node, so a node left on an older agent is visible instead of having to be
	// inspected over SSH.
	AgentCommit    string
	AgentBuildTime string
	AgentVersion   string
	// Live metrics
	CpuPercent           float64
	MemoryUsedBytes      int64
	MemoryUsedPercent    float64
	DiskUsedBytes        int64
	DiskUsedPercent      float64
	NetworkRxBytesPerSec int64
	NetworkTxBytesPerSec int64
	DiskReadBytesPerSec  int64
	DiskWriteBytesPerSec int64
	LoadAvg1             float64
	LoadAvg5             float64
	LoadAvg15            float64
	RunningVmCount       int32
	AvailableCpus        int32
	AvailableMemoryMb    int64
	AvailableDiskGb      int64
}

// GetNodeInfo retrieves detailed system information from a node
func (c *AgentClient) GetNodeInfo(ctx context.Context, nodeID string) (*NodeSystemInfo, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var result *NodeSystemInfo
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.GetNodeInfo(ctx, &pb.GetNodeInfoRequest{})
		if err != nil {
			return err
		}

		if !resp.Success {
			return fmt.Errorf("GetNodeInfo failed: %s", resp.Error.GetMessage())
		}

		result = &NodeSystemInfo{
			OSName:               resp.OsInfo.GetOsName(),
			OSVersion:            resp.OsInfo.GetOsVersion(),
			KernelVersion:        resp.OsInfo.GetKernelVersion(),
			Architecture:         resp.OsInfo.GetArchitecture(),
			CPUModel:             resp.CpuInfo.GetModel(),
			CPUCores:             resp.CpuInfo.GetCores(),
			CPUThreads:           resp.CpuInfo.GetThreads(),
			MemoryTotal:          resp.MemoryTotalBytes,
			DiskTotal:            resp.DiskTotalBytes,
			LibvirtVersion:       resp.LibvirtVersion,
			AgentCommit:          resp.GetAgentCommit(),
			AgentBuildTime:       resp.GetAgentBuildTime(),
			AgentVersion:         resp.GetAgentVersion(),
			CpuPercent:           resp.GetCpuPercent(),
			MemoryUsedBytes:      resp.GetMemoryUsedBytes(),
			MemoryUsedPercent:    resp.GetMemoryUsedPercent(),
			DiskUsedBytes:        resp.GetDiskUsedBytes(),
			DiskUsedPercent:      resp.GetDiskUsedPercent(),
			NetworkRxBytesPerSec: resp.GetNetworkRxBytesPerSec(),
			NetworkTxBytesPerSec: resp.GetNetworkTxBytesPerSec(),
			DiskReadBytesPerSec:  resp.GetDiskReadBytesPerSec(),
			DiskWriteBytesPerSec: resp.GetDiskWriteBytesPerSec(),
			LoadAvg1:             resp.GetLoadAvg_1(),
			LoadAvg5:             resp.GetLoadAvg_5(),
			LoadAvg15:            resp.GetLoadAvg_15(),
			RunningVmCount:       resp.GetRunningVmCount(),
			AvailableCpus:        resp.GetAvailableCpus(),
			AvailableMemoryMb:    resp.GetAvailableMemoryMb(),
			AvailableDiskGb:      resp.GetAvailableDiskGb(),
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}

// LiveMetricsResult holds real-time node metrics without the static system
// info overhead of GetNodeInfo (no exec calls) — suitable for frequent polling.
type LiveMetricsResult struct {
	CpuPercent           float64
	MemoryUsedBytes      int64
	MemoryTotalBytes     int64
	MemoryUsedPercent    float64
	DiskUsedBytes        int64
	DiskTotalBytes       int64
	DiskUsedPercent      float64
	NetworkRxBytesPerSec int64
	NetworkTxBytesPerSec int64
	DiskReadBytesPerSec  int64
	DiskWriteBytesPerSec int64
	LoadAvg1             float64
	LoadAvg5             float64
	LoadAvg15            float64
	RunningVmCount       int32
	AvailableCpus        int32
	AvailableMemoryMb    int64
	AvailableDiskGb      int64

	// ConntrackCount/Max are the node's connection tracking table. A zero Max
	// means the node could not read it, which callers must treat as unknown
	// rather than as an empty table — reporting 0/0 as "healthy" would hide
	// exactly the failure this exists to catch.
	ConntrackCount int64
	ConntrackMax   int64
}

// GetLiveMetrics retrieves real-time system metrics from a node without the
// exec/proc-parsing overhead of GetNodeInfo. Use this for frequent polling
// (e.g. dashboard/monitoring refresh); use GetNodeInfo for static system info.
func (c *AgentClient) GetLiveMetrics(ctx context.Context, nodeID string) (*LiveMetricsResult, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var result *LiveMetricsResult
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.GetLiveMetrics(ctx, &pb.GetLiveMetricsRequest{})
		if err != nil {
			return err
		}

		if !resp.Success {
			return fmt.Errorf("GetLiveMetrics failed: %s", resp.Error.GetMessage())
		}

		result = &LiveMetricsResult{
			CpuPercent:           resp.GetCpuPercent(),
			MemoryUsedBytes:      resp.GetMemoryUsedBytes(),
			MemoryTotalBytes:     resp.GetMemoryTotalBytes(),
			MemoryUsedPercent:    resp.GetMemoryUsedPercent(),
			DiskUsedBytes:        resp.GetDiskUsedBytes(),
			DiskTotalBytes:       resp.GetDiskTotalBytes(),
			DiskUsedPercent:      resp.GetDiskUsedPercent(),
			NetworkRxBytesPerSec: resp.GetNetworkRxBytesPerSec(),
			NetworkTxBytesPerSec: resp.GetNetworkTxBytesPerSec(),
			DiskReadBytesPerSec:  resp.GetDiskReadBytesPerSec(),
			DiskWriteBytesPerSec: resp.GetDiskWriteBytesPerSec(),
			LoadAvg1:             resp.GetLoadAvg_1(),
			LoadAvg5:             resp.GetLoadAvg_5(),
			LoadAvg15:            resp.GetLoadAvg_15(),
			RunningVmCount:       resp.GetRunningVmCount(),
			AvailableCpus:        resp.GetAvailableCpus(),
			AvailableMemoryMb:    resp.GetAvailableMemoryMb(),
			AvailableDiskGb:      resp.GetAvailableDiskGb(),
			ConntrackCount:       resp.GetConntrackCount(),
			ConntrackMax:         resp.GetConntrackMax(),
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}

// ImportDiskResult contains the result of a disk import operation
type ImportDiskResult struct {
	VMID         string
	ImportedPath string
	SizeBytes    int64
	Success      bool
}

// ImportDisk imports a disk image from source to target path on a node
func (c *AgentClient) ImportDisk(ctx context.Context, nodeID string, vmID, sourcePath, targetPath, format, action string) (*ImportDiskResult, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var result *ImportDiskResult
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		req := &pb.DiskImportRequest{
			VmId:       vmID,
			SourcePath: sourcePath,
			TargetPath: targetPath,
			Format:     format,
			Action:     action,
		}

		resp, err := client.ImportDisk(ctx, req)
		if err != nil {
			return err
		}

		if !resp.Success {
			return fmt.Errorf("disk import failed: %s", resp.Error.GetMessage())
		}

		result = &ImportDiskResult{
			VMID:         resp.VmId,
			ImportedPath: resp.ImportedPath,
			SizeBytes:    resp.SizeBytes,
			Success:      resp.Success,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
}
func (c *AgentClient) getNodeInfo(nodeID string) (NodeInfo, error) {
	c.mu.RLock()
	conn, exists := c.connections[nodeID]
	c.mu.RUnlock()

	if !exists {
		return NodeInfo{}, fmt.Errorf("node %s not found in connection registry", nodeID)
	}

	return NodeInfo{
		ID:         conn.nodeID,
		Address:    conn.address,
		Token:      conn.token,
		TLSEnabled: c.tlsConfig != nil,
	}, nil
}

// RegisterNode allows registering a node with the client
func (c *AgentClient) RegisterNode(node NodeInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, exists := c.connections[node.ID]; exists {
		// Update token if changed
		if existing.token != node.Token {
			_ = existing.Close()
			delete(c.connections, node.ID)
		} else {
			return
		}
	}

	// Store a placeholder connection entry so getNodeInfo can find it
	c.connections[node.ID] = &PooledConnection{
		nodeID:   node.ID,
		address:  node.Address,
		token:    node.Token,
		cb:       NewCircuitBreaker(defaultCircuitBreakerThreshold, defaultCircuitBreakerResetTimeout),
		lastUsed: time.Now().UnixNano(),
		closed:   true, // Not yet connected — will connect on first use
	}
}

// GetCircuitBreakerState returns the circuit breaker state for a node
func (c *AgentClient) GetCircuitBreakerState(nodeID string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	conn, exists := c.connections[nodeID]
	if !exists {
		return "", fmt.Errorf("no connection found for node %s", nodeID)
	}

	return conn.cb.State(), nil
}

// Close closes all connections
func (c *AgentClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error
	for _, conn := range c.connections {
		if err := conn.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	c.connections = make(map[string]*PooledConnection)

	if len(errs) > 0 {
		return fmt.Errorf("errors closing connections: %v", errs)
	}
	return nil
}

// RemoveNode removes a node and closes its connection
func (c *AgentClient) RemoveNode(nodeID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, exists := c.connections[nodeID]
	if !exists {
		return nil
	}

	delete(c.connections, nodeID)
	return conn.Close()
}

// NodeAddresses returns a map of node IDs to their addresses
func (c *AgentClient) NodeAddresses() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	addresses := make(map[string]string)
	for id, conn := range c.connections {
		addresses[id] = conn.address
	}

	return addresses
}

// GuestConnectionReport is one guest NIC's cumulative count of attempted new
// outbound connections on a node.
type GuestConnectionReport struct {
	MAC           string
	VMID          string
	InterfaceName string
	SYNPackets    int64
	Quarantined   bool
}

// GetGuestConnections asks a node how fast each of its guests is opening new
// outbound connections. The node answers for every libvirt domain it has,
// including guests the panel does not manage — which is the whole point, since
// those are invisible to every other view in the panel.
func (c *AgentClient) GetGuestConnections(ctx context.Context, nodeID string) ([]GuestConnectionReport, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var out []GuestConnectionReport
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.GetGuestConnections(ctx, &pb.GetGuestConnectionsRequest{})
		if err != nil {
			return err
		}
		if !resp.Success {
			return fmt.Errorf("GetGuestConnections failed: %s", resp.Error.GetMessage())
		}
		out = out[:0]
		for _, g := range resp.GetGuests() {
			out = append(out, GuestConnectionReport{
				MAC:           g.GetMac(),
				VMID:          g.GetVmId(),
				InterfaceName: g.GetInterfaceName(),
				SYNPackets:    g.GetSynPackets(),
				Quarantined:   g.GetQuarantined(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetQuarantine cuts a guest off the network, or puts it back, without stopping
// it. It returns the node's full list afterwards so the caller reconciles
// against the node rather than trusting the panel's own view — the node's
// quarantine file is also meant to be editable by hand.
func (c *AgentClient) SetQuarantine(ctx context.Context, nodeID, mac, reason string, quarantined bool) ([]string, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var macs []string
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.SetQuarantine(ctx, &pb.SetQuarantineRequest{
			Mac:         mac,
			Quarantined: quarantined,
			Reason:      reason,
		})
		if err != nil {
			return err
		}
		if !resp.Success {
			return fmt.Errorf("SetQuarantine failed: %s", resp.Error.GetMessage())
		}
		macs = resp.GetQuarantinedMacs()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return macs, nil
}

// PoolCapacity is one pool path's real filesystem usage on a node.
type PoolCapacity struct {
	Path       string
	Exists     bool
	Total      int64
	Used       int64
	Available  int64
	Filesystem string
}

// DiskLocation is a directory a node's domains actually keep disks in.
type DiskLocation struct {
	Path      string
	DiskCount int
}

// StorageReport is what a node knows about its own storage.
type StorageReport struct {
	Pools         []PoolCapacity
	DiskLocations []DiskLocation
}

// GetStorageReport measures each pool path on the node and reports where its
// domains actually keep their disks.
//
// Paths are measured individually because pools on one node routinely sit on
// different filesystems — deriving them all from the node's root filesystem is
// the bug this replaces.
func (c *AgentClient) GetStorageReport(ctx context.Context, nodeID string, paths []string) (*StorageReport, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var out *StorageReport
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.GetStorageReport(ctx, &pb.GetStorageReportRequest{Paths: paths})
		if err != nil {
			return err
		}
		if !resp.Success {
			return fmt.Errorf("GetStorageReport failed: %s", resp.Error.GetMessage())
		}
		report := &StorageReport{}
		for _, p := range resp.GetPools() {
			report.Pools = append(report.Pools, PoolCapacity{
				Path:       p.GetPath(),
				Exists:     p.GetExists(),
				Total:      p.GetTotalBytes(),
				Used:       p.GetUsedBytes(),
				Available:  p.GetAvailableBytes(),
				Filesystem: p.GetFilesystem(),
			})
		}
		for _, l := range resp.GetDiskLocations() {
			report.DiskLocations = append(report.DiskLocations, DiskLocation{
				Path:      l.GetPath(),
				DiskCount: int(l.GetDiskCount()),
			})
		}
		out = report
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ConsoleAccessResult is what a node reports after enforcing a console change.
type ConsoleAccessResult struct {
	VNCPort int
	// RestartRequired is true when only the persistent definition could be
	// changed: graphics is not hot-pluggable, so a running domain keeps its
	// current listen address until it next boots.
	RestartRequired bool
}

// SetConsoleAccess enforces console enable/disable on the domain, rather than
// only in the panel's own records.
func (c *AgentClient) SetConsoleAccess(ctx context.Context, nodeID, vmID string, enabled bool, vncPassword string) (*ConsoleAccessResult, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var out *ConsoleAccessResult
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.SetConsoleAccess(ctx, &pb.SetConsoleAccessRequest{
			VmId:        vmID,
			Enabled:     enabled,
			VncPassword: vncPassword,
		})
		if err != nil {
			return err
		}
		if !resp.Success {
			return fmt.Errorf("SetConsoleAccess failed: %s", resp.Error.GetMessage())
		}
		out = &ConsoleAccessResult{
			VNCPort:         int(resp.GetVncPort()),
			RestartRequired: resp.GetRestartRequired(),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ExportProgress is one disk export running on a node.
type ExportProgress struct {
	VMID         string
	Kind         string
	SourceBytes  int64
	WrittenBytes int64
	StartedAt    time.Time
}

// GetExportProgress reports the exports a node is running right now.
//
// The panel has no other way to tell a queued capture from one that is three
// hours into compressing a 90 GB disk: both look like a row marked "pending".
func (c *AgentClient) GetExportProgress(ctx context.Context, nodeID string) ([]ExportProgress, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var out []ExportProgress
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		resp, err := client.GetExportProgress(ctx, &pb.GetExportProgressRequest{})
		if err != nil {
			return err
		}
		if !resp.Success {
			return fmt.Errorf("GetExportProgress failed: %s", resp.Error.GetMessage())
		}
		out = out[:0]
		for _, e := range resp.GetExports() {
			out = append(out, ExportProgress{
				VMID:         e.GetVmId(),
				Kind:         e.GetKind(),
				SourceBytes:  e.GetSourceBytes(),
				WrittenBytes: e.GetWrittenBytes(),
				StartedAt:    e.GetStartedAt().AsTime(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
