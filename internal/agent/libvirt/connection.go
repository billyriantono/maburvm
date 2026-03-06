package libvirt

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/maburvm/panel/internal/shared/config"
	"libvirt.org/go/libvirt"
)

const (
	defaultMinConns       = 2
	defaultMaxConns       = 10
	defaultHealthCheck    = 30 * time.Second
	defaultConnectTimeout = 10 * time.Second
)

var (
	ErrPoolClosed      = errors.New("connection pool is closed")
	ErrConnUnavailable = errors.New("no connection available from pool")
	ErrConnInvalid     = errors.New("connection is invalid or closed")
)

type pooledConn struct {
	conn      *libvirt.Connect
	inUse     int32
	lastUsed  time.Time
	createdAt time.Time
}

func (pc *pooledConn) isHealthy() bool {
	if pc.conn == nil {
		return false
	}
	alive, err := pc.conn.IsAlive()
	if err != nil || !alive {
		return false
	}
	return true
}

func (pc *pooledConn) close() error {
	if pc.conn == nil {
		return nil
	}
	_, err := pc.conn.Close()
	pc.conn = nil
	return err
}

type Pool struct {
	uri                 string
	minConns            int
	maxConns            int
	healthCheckInterval time.Duration
	connectTimeout      time.Duration

	conns     []*pooledConn
	available chan *pooledConn
	mu        sync.RWMutex
	closed    int32

	// Background maintenance
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

var (
	globalPool *Pool
	poolOnce   sync.Once
	poolErr    error
)

func Initialize(cfg config.LibvirtConfig) error {
	poolOnce.Do(func() {
		globalPool, poolErr = NewPool(cfg)
	})
	return poolErr
}

func NewPool(cfg config.LibvirtConfig) (*Pool, error) {
	minConns := cfg.PoolMinSize
	if minConns <= 0 {
		minConns = defaultMinConns
	}
	maxConns := cfg.PoolMaxSize
	if maxConns <= 0 {
		maxConns = defaultMaxConns
	}
	if minConns > maxConns {
		minConns = maxConns
	}

	healthCheck := cfg.HealthCheckInterval
	if healthCheck <= 0 {
		healthCheck = defaultHealthCheck
	}

	connectTimeout := cfg.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = defaultConnectTimeout
	}

	ctx, cancel := context.WithCancel(context.Background())

	p := &Pool{
		uri:                 cfg.URI,
		minConns:            minConns,
		maxConns:            maxConns,
		healthCheckInterval: healthCheck,
		connectTimeout:      connectTimeout,
		conns:               make([]*pooledConn, 0, maxConns),
		available:           make(chan *pooledConn, maxConns),
		ctx:                 ctx,
		cancel:              cancel,
	}

	// Initialize minimum connections
	for i := 0; i < minConns; i++ {
		pc, err := p.createConnection()
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("failed to create initial connection %d: %w", i+1, err)
		}
		p.conns = append(p.conns, pc)
		p.available <- pc
	}

	log.Printf("[LibvirtPool] Initialized with %d connections to %s", minConns, cfg.URI)

	// Start background maintenance
	p.wg.Add(1)
	go p.maintain()

	return p, nil
}

func (p *Pool) createConnection() (*pooledConn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), p.connectTimeout)
	defer cancel()

	connChan := make(chan *libvirt.Connect, 1)
	errChan := make(chan error, 1)

	go func() {
		conn, err := libvirt.NewConnect(p.uri)
		if err != nil {
			errChan <- err
			return
		}
		connChan <- conn
	}()

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("connection timeout: %w", ctx.Err())
	case err := <-errChan:
		return nil, fmt.Errorf("failed to connect to libvirt: %w", err)
	case conn := <-connChan:
		return &pooledConn{
			conn:      conn,
			createdAt: time.Now(),
			lastUsed:  time.Now(),
		}, nil
	}
}

func (p *Pool) maintain() {
	defer p.wg.Done()

	ticker := time.NewTicker(p.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			p.healthCheck()
		}
	}
}

func (p *Pool) healthCheck() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if atomic.LoadInt32(&p.closed) == 1 {
		return
	}

	// Check all connections and replace unhealthy ones
	for i, pc := range p.conns {
		if atomic.LoadInt32(&pc.inUse) == 1 {
			continue // Skip connections in use
		}

		if !pc.isHealthy() {
			log.Printf("[LibvirtPool] Connection %d is unhealthy, replacing...", i)

			// Close unhealthy connection
			pc.close()

			// Create new connection
			newPc, err := p.createConnection()
			if err != nil {
				log.Printf("[LibvirtPool] Failed to replace unhealthy connection: %v", err)
				continue
			}

			// Remove old from available channel (non-blocking drain)
			select {
			case <-p.available:
			default:
			}

			p.conns[i] = newPc
			p.available <- newPc
			log.Printf("[LibvirtPool] Connection %d replaced successfully", i)
		}
	}

	// Ensure minimum connections
	currentCount := len(p.conns)
	if currentCount < p.minConns {
		for i := currentCount; i < p.minConns; i++ {
			pc, err := p.createConnection()
			if err != nil {
				log.Printf("[LibvirtPool] Failed to create connection during maintenance: %v", err)
				continue
			}
			p.conns = append(p.conns, pc)
			p.available <- pc
		}
	}
}

func (p *Pool) Connect() (*libvirt.Connect, error) {
	if atomic.LoadInt32(&p.closed) == 1 {
		return nil, ErrPoolClosed
	}

	select {
	case pc := <-p.available:
		if !pc.isHealthy() {
			// Connection is bad, try to replace it
			pc.close()
			newPc, err := p.createConnection()
			if err != nil {
				// Put nil back so we don't block forever
				select {
				case p.available <- pc:
				default:
				}
				return nil, fmt.Errorf("failed to replace unhealthy connection: %w", err)
			}

			// Replace in pool
			p.mu.Lock()
			for i, existing := range p.conns {
				if existing == pc {
					p.conns[i] = newPc
					break
				}
			}
			p.mu.Unlock()

			pc = newPc
		}

		atomic.StoreInt32(&pc.inUse, 1)
		pc.lastUsed = time.Now()
		return pc.conn, nil

	case <-time.After(p.connectTimeout):
		return nil, ErrConnUnavailable
	}
}

func (p *Pool) Release(conn *libvirt.Connect) error {
	if conn == nil {
		return nil
	}

	if atomic.LoadInt32(&p.closed) == 1 {
		return ErrPoolClosed
	}

	p.mu.RLock()
	defer p.mu.RUnlock()

	for _, pc := range p.conns {
		if pc.conn == conn {
			if atomic.CompareAndSwapInt32(&pc.inUse, 1, 0) {
				pc.lastUsed = time.Now()
				select {
				case p.available <- pc:
				default:
					// Channel full, this shouldn't happen but handle gracefully
					log.Println("[LibvirtPool] Warning: available channel full on release")
				}
			}
			return nil
		}
	}

	return ErrConnInvalid
}

func (p *Pool) WithConnection(fn func(*libvirt.Connect) error) error {
	conn, err := p.Connect()
	if err != nil {
		return err
	}
	defer p.Release(conn)

	return fn(conn)
}

func (p *Pool) Stats() (total, available, inUse int) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	total = len(p.conns)
	for _, pc := range p.conns {
		if atomic.LoadInt32(&pc.inUse) == 1 {
			inUse++
		}
	}
	available = total - inUse
	return
}

func (p *Pool) Close() error {
	if !atomic.CompareAndSwapInt32(&p.closed, 0, 1) {
		return nil // Already closed
	}

	p.cancel()
	p.wg.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Close all connections
	for _, pc := range p.conns {
		if pc.conn != nil {
			pc.close()
		}
	}

	close(p.available)
	p.conns = p.conns[:0]

	log.Println("[LibvirtPool] Pool closed")
	return nil
}

func Connect() (*libvirt.Connect, error) {
	if globalPool == nil {
		return nil, errors.New("libvirt pool not initialized")
	}
	return globalPool.Connect()
}

func Release(conn *libvirt.Connect) error {
	if globalPool == nil {
		return errors.New("libvirt pool not initialized")
	}
	return globalPool.Release(conn)
}

func WithConnection(fn func(*libvirt.Connect) error) error {
	if globalPool == nil {
		return errors.New("libvirt pool not initialized")
	}
	return globalPool.WithConnection(fn)
}

func GetPoolStats() (total, available, inUse int) {
	if globalPool == nil {
		return 0, 0, 0
	}
	return globalPool.Stats()
}

func ClosePool() error {
	if globalPool == nil {
		return nil
	}
	return globalPool.Close()
}
