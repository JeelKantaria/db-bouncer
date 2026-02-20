package pool

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
)

// Stats holds connection pool statistics for a tenant.
type Stats struct {
	TenantID   string `json:"tenant_id"`
	DBType     string `json:"db_type"`
	Active     int    `json:"active"`
	Idle       int    `json:"idle"`
	Total      int    `json:"total"`
	Waiting    int    `json:"waiting"`
	MaxConns   int    `json:"max_connections"`
	MinConns   int    `json:"min_connections"`
	Exhausted  int64  `json:"pool_exhausted_total"`
}

// OnPoolExhausted is called when a pool reaches max connections and a goroutine must wait.
type OnPoolExhausted func(tenantID string)

// TenantPool manages connections for a single tenant.
type TenantPool struct {
	mu             sync.Mutex
	tenantID       string
	dbType         string
	host           string
	port           int
	dbname         string
	username       string
	password       string
	minConns       int
	maxConns       int
	idleTimeout    time.Duration
	maxLifetime    time.Duration
	acquireTimeout time.Duration

	idle      []*PooledConn
	active    map[*PooledConn]struct{}
	total     int
	waiting   int
	exhausted int64
	waitCh    chan struct{} // signaled when a connection is returned

	closed          bool
	stopCh          chan struct{}
	onPoolExhausted OnPoolExhausted
}

// NewTenantPool creates a new connection pool for a tenant.
func NewTenantPool(tenantID string, tc config.TenantConfig, defaults config.PoolDefaults) *TenantPool {
	tp := &TenantPool{
		tenantID:       tenantID,
		dbType:         tc.DBType,
		host:           tc.Host,
		port:           tc.Port,
		dbname:         tc.DBName,
		username:       tc.Username,
		password:       tc.Password,
		minConns:       tc.EffectiveMinConnections(defaults),
		maxConns:       tc.EffectiveMaxConnections(defaults),
		idleTimeout:    tc.EffectiveIdleTimeout(defaults),
		maxLifetime:    tc.EffectiveMaxLifetime(defaults),
		acquireTimeout: tc.EffectiveAcquireTimeout(defaults),
		idle:           make([]*PooledConn, 0),
		active:         make(map[*PooledConn]struct{}),
		waitCh:         make(chan struct{}, 1),
		stopCh:         make(chan struct{}),
	}

	// Start idle reaper
	go tp.reapLoop()

	return tp
}

// Acquire gets a connection from the pool, creating one if needed.
func (tp *TenantPool) Acquire() (*PooledConn, error) {
	deadline := time.After(tp.acquireTimeout)

	for {
		tp.mu.Lock()
		if tp.closed {
			tp.mu.Unlock()
			return nil, fmt.Errorf("pool closed for tenant %s", tp.tenantID)
		}

		// Try to get an idle connection
		for len(tp.idle) > 0 {
			pc := tp.idle[len(tp.idle)-1]
			tp.idle = tp.idle[:len(tp.idle)-1]

			// Check if connection is expired
			if pc.IsExpired(tp.maxLifetime) {
				pc.Close()
				tp.total--
				continue
			}

			// Ping to verify connection is alive
			if err := pc.Ping(); err != nil {
				pc.Close()
				tp.total--
				continue
			}

			pc.MarkActive()
			tp.active[pc] = struct{}{}
			tp.mu.Unlock()
			return pc, nil
		}

		// Create a new connection if under limit
		if tp.total < tp.maxConns {
			tp.total++
			tp.mu.Unlock()

			pc, err := tp.dial()
			if err != nil {
				tp.mu.Lock()
				tp.total--
				tp.mu.Unlock()
				return nil, fmt.Errorf("connecting to %s:%d for tenant %s: %w", tp.host, tp.port, tp.tenantID, err)
			}

			pc.MarkActive()
			tp.mu.Lock()
			tp.active[pc] = struct{}{}
			tp.mu.Unlock()
			return pc, nil
		}

		// Pool exhausted, wait for a connection to be returned
		tp.waiting++
		tp.exhausted++
		cb := tp.onPoolExhausted
		tp.mu.Unlock()

		if cb != nil {
			cb(tp.tenantID)
		}

		select {
		case <-tp.waitCh:
			tp.mu.Lock()
			tp.waiting--
			tp.mu.Unlock()
			continue // retry
		case <-deadline:
			tp.mu.Lock()
			tp.waiting--
			tp.mu.Unlock()
			return nil, fmt.Errorf("acquire timeout (%s) for tenant %s: pool exhausted", tp.acquireTimeout, tp.tenantID)
		case <-tp.stopCh:
			tp.mu.Lock()
			tp.waiting--
			tp.mu.Unlock()
			return nil, fmt.Errorf("pool closing for tenant %s", tp.tenantID)
		}
	}
}

// Return releases a connection back to the pool.
func (tp *TenantPool) Return(pc *PooledConn) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	delete(tp.active, pc)

	if tp.closed || pc.IsExpired(tp.maxLifetime) {
		pc.Close()
		tp.total--
		return
	}

	pc.MarkIdle()
	tp.idle = append(tp.idle, pc)

	// Signal waiting goroutines
	select {
	case tp.waitCh <- struct{}{}:
	default:
	}
}

// Stats returns current pool statistics.
func (tp *TenantPool) Stats() Stats {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	return Stats{
		TenantID:  tp.tenantID,
		DBType:    tp.dbType,
		Active:    len(tp.active),
		Idle:      len(tp.idle),
		Total:     tp.total,
		Waiting:   tp.waiting,
		MaxConns:  tp.maxConns,
		MinConns:  tp.minConns,
		Exhausted: tp.exhausted,
	}
}

// Drain closes all idle connections and waits for active ones to be returned.
func (tp *TenantPool) Drain() {
	tp.mu.Lock()

	// Close all idle connections
	for _, pc := range tp.idle {
		pc.Close()
		tp.total--
	}
	tp.idle = tp.idle[:0]

	// Wait for active connections with a timeout
	activeCount := len(tp.active)
	tp.mu.Unlock()

	if activeCount > 0 {
		log.Printf("[pool] draining %d active connections for tenant %s", activeCount, tp.tenantID)
		timeout := time.After(30 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				tp.mu.Lock()
				if len(tp.active) == 0 {
					tp.mu.Unlock()
					return
				}
				tp.mu.Unlock()
			case <-timeout:
				tp.mu.Lock()
				for pc := range tp.active {
					pc.Close()
					tp.total--
				}
				tp.active = make(map[*PooledConn]struct{})
				tp.mu.Unlock()
				log.Printf("[pool] force-closed active connections for tenant %s after drain timeout", tp.tenantID)
				return
			}
		}
	}
}

// Close shuts down the pool.
func (tp *TenantPool) Close() {
	tp.mu.Lock()
	if tp.closed {
		tp.mu.Unlock()
		return
	}
	tp.closed = true
	close(tp.stopCh)
	tp.mu.Unlock()

	tp.Drain()
}

func (tp *TenantPool) dial() (*PooledConn, error) {
	addr := net.JoinHostPort(tp.host, fmt.Sprintf("%d", tp.port))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}
	return NewPooledConn(conn, tp.tenantID, tp.dbType, tp), nil
}

func (tp *TenantPool) reapLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			tp.reapIdle()
		case <-tp.stopCh:
			return
		}
	}
}

func (tp *TenantPool) reapIdle() {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	if len(tp.idle) <= tp.minConns {
		return
	}

	kept := make([]*PooledConn, 0, len(tp.idle))
	for _, pc := range tp.idle {
		if len(kept) < tp.minConns {
			kept = append(kept, pc)
			continue
		}
		if pc.IsIdle(tp.idleTimeout) || pc.IsExpired(tp.maxLifetime) {
			pc.Close()
			tp.total--
		} else {
			kept = append(kept, pc)
		}
	}
	tp.idle = kept
}

// StatsCallback is called periodically with pool stats for each tenant.
type StatsCallback func(stats Stats)

// Manager manages connection pools for all tenants.
type Manager struct {
	mu              sync.RWMutex
	pools           map[string]*TenantPool
	defaults        config.PoolDefaults
	onPoolExhausted OnPoolExhausted
	statsCallback   StatsCallback
	statsStopCh     chan struct{}
}

// NewManager creates a new pool manager.
func NewManager(defaults config.PoolDefaults) *Manager {
	return &Manager{
		pools:       make(map[string]*TenantPool),
		defaults:    defaults,
		statsStopCh: make(chan struct{}),
	}
}

// SetOnPoolExhausted sets the callback for pool exhaustion events.
// Must be called before any pools are created.
func (m *Manager) SetOnPoolExhausted(cb OnPoolExhausted) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onPoolExhausted = cb
}

// StartStatsLoop starts a periodic goroutine that calls the stats callback for each pool.
func (m *Manager) StartStatsLoop(interval time.Duration, cb StatsCallback) {
	m.statsCallback = cb
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				for _, s := range m.AllStats() {
					cb(s)
				}
			case <-m.statsStopCh:
				return
			}
		}
	}()
}

// GetOrCreate returns the pool for a tenant, creating it lazily if needed.
func (m *Manager) GetOrCreate(tenantID string, tc config.TenantConfig) *TenantPool {
	m.mu.RLock()
	if p, ok := m.pools[tenantID]; ok {
		m.mu.RUnlock()
		return p
	}
	m.mu.RUnlock()

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if p, ok := m.pools[tenantID]; ok {
		return p
	}

	p := NewTenantPool(tenantID, tc, m.defaults)
	p.onPoolExhausted = m.onPoolExhausted
	m.pools[tenantID] = p
	log.Printf("[pool] created pool for tenant %s (%s at %s:%d)", tenantID, tc.DBType, tc.Host, tc.Port)
	return p
}

// Get returns the pool for a tenant if it exists.
func (m *Manager) Get(tenantID string) (*TenantPool, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.pools[tenantID]
	return p, ok
}

// Remove closes and removes the pool for a tenant.
func (m *Manager) Remove(tenantID string) bool {
	m.mu.Lock()
	p, ok := m.pools[tenantID]
	if !ok {
		m.mu.Unlock()
		return false
	}
	delete(m.pools, tenantID)
	m.mu.Unlock()

	p.Close()
	log.Printf("[pool] removed pool for tenant %s", tenantID)
	return true
}

// DrainTenant drains connections for a specific tenant.
func (m *Manager) DrainTenant(tenantID string) bool {
	m.mu.RLock()
	p, ok := m.pools[tenantID]
	m.mu.RUnlock()

	if !ok {
		return false
	}
	p.Drain()
	return true
}

// AllStats returns stats for all tenant pools.
func (m *Manager) AllStats() []Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()

	stats := make([]Stats, 0, len(m.pools))
	for _, p := range m.pools {
		stats = append(stats, p.Stats())
	}
	return stats
}

// TenantStats returns stats for a specific tenant pool.
func (m *Manager) TenantStats(tenantID string) (Stats, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	p, ok := m.pools[tenantID]
	if !ok {
		return Stats{}, false
	}
	return p.Stats(), true
}

// UpdateDefaults updates the default pool settings.
func (m *Manager) UpdateDefaults(defaults config.PoolDefaults) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaults = defaults
}

// Close shuts down all pools and stops the stats loop.
func (m *Manager) Close() {
	select {
	case <-m.statsStopCh:
	default:
		close(m.statsStopCh)
	}

	m.mu.Lock()
	pools := m.pools
	m.pools = make(map[string]*TenantPool)
	m.mu.Unlock()

	for _, p := range pools {
		p.Close()
	}
}
