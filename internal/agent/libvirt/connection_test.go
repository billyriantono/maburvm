package libvirt

import (
	"sync"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/shared/config"
	"libvirt.org/go/libvirt"
)

func TestNewPool(t *testing.T) {
	cfg := config.LibvirtConfig{
		URI:                 "qemu:///system",
		PoolMinSize:         2,
		PoolMaxSize:         10,
		HealthCheckInterval: 30 * time.Second,
		ConnectTimeout:      5 * time.Second,
	}

	pool, err := NewPool(cfg)
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer pool.Close()

	// Test pool configuration
	if pool.minConns != 2 {
		t.Errorf("Expected minConns=2, got %d", pool.minConns)
	}
	if pool.maxConns != 10 {
		t.Errorf("Expected maxConns=10, got %d", pool.maxConns)
	}
	if pool.uri != "qemu:///system" {
		t.Errorf("Expected URI='qemu:///system', got %s", pool.uri)
	}
}

func TestPoolWithMinGreaterThanMax(t *testing.T) {
	cfg := config.LibvirtConfig{
		URI:         "qemu:///system",
		PoolMinSize: 15, // Greater than max
		PoolMaxSize: 10,
	}

	pool, err := NewPool(cfg)
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer pool.Close()

	// Should adjust minConns to maxConns
	if pool.minConns != 10 {
		t.Errorf("Expected minConns to be adjusted to 10, got %d", pool.minConns)
	}
}

func TestPoolDefaults(t *testing.T) {
	cfg := config.LibvirtConfig{
		URI: "qemu:///system",
		// Using zero values to test defaults
	}

	pool, err := NewPool(cfg)
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer pool.Close()

	if pool.minConns != defaultMinConns {
		t.Errorf("Expected default minConns=%d, got %d", defaultMinConns, pool.minConns)
	}
	if pool.maxConns != defaultMaxConns {
		t.Errorf("Expected default maxConns=%d, got %d", defaultMaxConns, pool.maxConns)
	}
}

func TestPoolStats(t *testing.T) {
	cfg := config.LibvirtConfig{
		URI:         "qemu:///system",
		PoolMinSize: 2,
		PoolMaxSize: 5,
	}

	pool, err := NewPool(cfg)
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}
	defer pool.Close()

	total, available, inUse := pool.Stats()

	if total != 2 {
		t.Errorf("Expected total=2, got %d", total)
	}
	if available != 2 {
		t.Errorf("Expected available=2, got %d", available)
	}
	if inUse != 0 {
		t.Errorf("Expected inUse=0, got %d", inUse)
	}
}

func TestPoolClose(t *testing.T) {
	cfg := config.LibvirtConfig{
		URI:         "qemu:///system",
		PoolMinSize: 2,
		PoolMaxSize: 5,
	}

	pool, err := NewPool(cfg)
	if err != nil {
		t.Fatalf("Failed to create pool: %v", err)
	}

	if err := pool.Close(); err != nil {
		t.Errorf("Failed to close pool: %v", err)
	}

	// Test double close
	if err := pool.Close(); err != nil {
		t.Errorf("Double close should return nil, got: %v", err)
	}

	// Test closed pool operations
	_, err = pool.Connect()
	if err != ErrPoolClosed {
		t.Errorf("Expected ErrPoolClosed, got %v", err)
	}
}

func TestPooledConnHealth(t *testing.T) {
	// Test with nil connection
	pc := &pooledConn{conn: nil}
	if pc.isHealthy() {
		t.Error("Expected nil connection to be unhealthy")
	}
}

func TestGlobalPoolNotInitialized(t *testing.T) {
	// Reset global pool for testing
	globalPool = nil
	poolOnce = sync.Once{}

	_, err := Connect()
	if err == nil {
		t.Error("Expected error when pool not initialized")
	}

	err = Release(nil)
	if err == nil {
		t.Error("Expected error when pool not initialized")
	}

	err = WithConnection(func(conn *libvirt.Connect) error {
		return nil
	})
	if err == nil {
		t.Error("Expected error when pool not initialized")
	}
}

func TestInitialize(t *testing.T) {
	// Reset for testing
	globalPool = nil
	poolOnce = sync.Once{}

	cfg := config.LibvirtConfig{
		URI:         "qemu:///system",
		PoolMinSize: 2,
		PoolMaxSize: 5,
	}

	err := Initialize(cfg)
	if err != nil {
		t.Fatalf("Failed to initialize pool: %v", err)
	}

	// Test double initialization (should use same instance)
	err = Initialize(cfg)
	if err != nil {
		t.Logf("Second initialization returned: %v", err)
	}

	// Cleanup
	ClosePool()
	globalPool = nil
	poolOnce = sync.Once{}
}

func TestEventManager(t *testing.T) {
	em, err := NewEventManager()
	if err != nil {
		t.Fatalf("Failed to create event manager: %v", err)
	}

	// Test callback registration
	testCallback := func(details DomainEventDetails) {}

	em.RegisterCallback(libvirt.DOMAIN_EVENT_STARTED, testCallback)

	em.mu.RLock()
	callbacks := em.callbacks[libvirt.DOMAIN_EVENT_STARTED]
	em.mu.RUnlock()

	if len(callbacks) != 1 {
		t.Errorf("Expected 1 callback, got %d", len(callbacks))
	}

	// Test unregister
	em.UnregisterAllCallbacks(libvirt.DOMAIN_EVENT_STARTED)

	em.mu.RLock()
	callbacks = em.callbacks[libvirt.DOMAIN_EVENT_STARTED]
	em.mu.RUnlock()

	if len(callbacks) != 0 {
		t.Errorf("Expected 0 callbacks after unregister, got %d", len(callbacks))
	}

	// Test double stop
	if err := em.Stop(); err != nil {
		t.Errorf("First stop failed: %v", err)
	}
	if err := em.Stop(); err != nil {
		t.Errorf("Second stop should succeed: %v", err)
	}
}

func TestDomainStateString(t *testing.T) {
	tests := []struct {
		state    libvirt.DomainState
		expected string
	}{
		{libvirt.DOMAIN_NOSTATE, "nostate"},
		{libvirt.DOMAIN_RUNNING, "running"},
		{libvirt.DOMAIN_BLOCKED, "blocked"},
		{libvirt.DOMAIN_PAUSED, "paused"},
		{libvirt.DOMAIN_SHUTDOWN, "shutdown"},
		{libvirt.DOMAIN_SHUTOFF, "shutoff"},
		{libvirt.DOMAIN_CRASHED, "crashed"},
		{libvirt.DOMAIN_PMSUSPENDED, "pmsuspended"},
		{libvirt.DomainState(999), "unknown"},
	}

	for _, test := range tests {
		result := DomainStateString(test.state)
		if result != test.expected {
			t.Errorf("State %d: expected %q, got %q", test.state, test.expected, result)
		}
	}
}

func TestGetEventDetailString(t *testing.T) {
	tests := []struct {
		event  libvirt.DomainEventType
		detail int
		want   string
	}{
		{libvirt.DOMAIN_EVENT_DEFINED, 0, "Added"},
		{libvirt.DOMAIN_EVENT_DEFINED, 1, "Updated"},
		{libvirt.DOMAIN_EVENT_UNDEFINED, 0, "Removed"},
		{libvirt.DOMAIN_EVENT_STARTED, 0, "Booted"},
		{libvirt.DOMAIN_EVENT_STARTED, 1, "Migrated"},
		{libvirt.DOMAIN_EVENT_SUSPENDED, 0, "Paused"},
		{libvirt.DOMAIN_EVENT_RESUMED, 0, "Unpaused"},
		{libvirt.DOMAIN_EVENT_STOPPED, 0, "Shutdown"},
		{libvirt.DOMAIN_EVENT_STOPPED, 1, "Destroyed"},
		{libvirt.DOMAIN_EVENT_SHUTDOWN, 0, "Finished"},
		{libvirt.DOMAIN_EVENT_PMSUSPENDED, 0, "Memory"},
		{libvirt.DOMAIN_EVENT_CRASHED, 0, "Panicked"},
		{libvirt.DomainEventType(999), 0, "Unknown event"},
	}

	for _, test := range tests {
		got := getEventDetailString(test.event, test.detail)
		if got != test.want {
			t.Errorf("Event %d, detail %d: want %q, got %q", test.event, test.detail, test.want, got)
		}
	}
}

func BenchmarkPoolStats(b *testing.B) {
	cfg := config.LibvirtConfig{
		URI:         "qemu:///system",
		PoolMinSize: 10,
		PoolMaxSize: 10,
	}

	pool, _ := NewPool(cfg)
	defer pool.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pool.Stats()
	}
}
