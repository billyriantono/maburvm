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
	MaxRetries        int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	BackoffMultiplier float64
	RequestTimeout    time.Duration
	ConnectionTimeout time.Duration
	TLSCertFile       string
	TLSKeyFile        string
	TLSCAFile         string
	Insecure          bool
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

	if !config.Insecure {
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
		creds := credentials.NewTLS(c.tlsConfig)
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
		timeoutCtx, cancel := context.WithTimeout(authCtx, c.config.RequestTimeout)

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

// NodeMetricsResult holds node metrics retrieved from agent
type NodeMetricsResult struct {
	CPUUsage    float64
	MemoryUsage float64
	DiskUsage   float64
	VMCount     int
	Timestamp   time.Time
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
}

// GetNodeMetrics retrieves metrics from the node agent via GetNodeInfo
func (c *AgentClient) GetNodeMetrics(ctx context.Context, nodeID string) (*NodeMetricsResult, error) {
	node, err := c.getNodeInfo(nodeID)
	if err != nil {
		return nil, err
	}

	var result *NodeMetricsResult
	err = c.executeWithRetry(ctx, node, func(ctx context.Context, client pb.NodeAgentClient) error {
		// Call GetNodeInfo to verify node is reachable and get system info
		resp, err := client.GetNodeInfo(ctx, &pb.GetNodeInfoRequest{})
		if err != nil {
			return err
		}

		if !resp.Success {
			return fmt.Errorf("GetNodeInfo failed: %s", resp.Error.GetMessage())
		}

		// GetNodeInfo provides static info (totals) but not live usage.
		// Live metrics (CPU%, memory used, disk used) come from heartbeat stream.
		// For now, return what we can confirm: node is reachable.
		result = &NodeMetricsResult{
			CPUUsage:    0, // Requires heartbeat cache — not available via GetNodeInfo
			MemoryUsage: 0, // Requires heartbeat cache
			DiskUsage:   0, // Requires heartbeat cache
			VMCount:     0, // Will be filled by service layer from DB
			Timestamp:   time.Now(),
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return result, nil
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
			OSName:         resp.OsInfo.GetOsName(),
			OSVersion:      resp.OsInfo.GetOsVersion(),
			KernelVersion:  resp.OsInfo.GetKernelVersion(),
			Architecture:   resp.OsInfo.GetArchitecture(),
			CPUModel:       resp.CpuInfo.GetModel(),
			CPUCores:       resp.CpuInfo.GetCores(),
			CPUThreads:     resp.CpuInfo.GetThreads(),
			MemoryTotal:    resp.MemoryTotalBytes,
			DiskTotal:      resp.DiskTotalBytes,
			LibvirtVersion: resp.LibvirtVersion,
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
	VMID          string
	ImportedPath  string
	SizeBytes     int64
	Success       bool
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
		_ = existing.Close()
		delete(c.connections, node.ID)
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
