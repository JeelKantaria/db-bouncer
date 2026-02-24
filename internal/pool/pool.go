package pool

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
)

// Stats holds connection pool statistics for a tenant.
type Stats struct {
	TenantID  string `json:"tenant_id"`
	DBType    string `json:"db_type"`
	PoolMode  string `json:"pool_mode"`
	Active    int    `json:"active"`
	Idle      int    `json:"idle"`
	Total     int    `json:"total"`
	Waiting   int    `json:"waiting"`
	MaxConns  int    `json:"max_connections"`
	MinConns  int    `json:"min_connections"`
	Exhausted int64  `json:"pool_exhausted_total"`
}

// OnPoolExhausted is called when a pool reaches max connections and a goroutine must wait.
type OnPoolExhausted func(tenantID string)

// TenantPool manages connections for a single tenant.
type TenantPool struct {
	mu             sync.Mutex
	cond           *sync.Cond // broadcast when a connection is returned
	tenantID       string
	dbType         string
	host           string
	port           int
	dbname         string
	username       string
	password       string
	poolMode       string
	minConns       int
	maxConns       int
	idleTimeout    time.Duration
	maxLifetime    time.Duration
	acquireTimeout time.Duration
	dialTimeout    time.Duration

	idle      []*PooledConn
	active    map[*PooledConn]struct{}
	total     int
	waiting   int
	exhausted int64

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
		poolMode:       tc.EffectivePoolMode(defaults),
		minConns:       tc.EffectiveMinConnections(defaults),
		maxConns:       tc.EffectiveMaxConnections(defaults),
		idleTimeout:    tc.EffectiveIdleTimeout(defaults),
		maxLifetime:    tc.EffectiveMaxLifetime(defaults),
		acquireTimeout: tc.EffectiveAcquireTimeout(defaults),
		dialTimeout:    tc.EffectiveDialTimeout(defaults),
		idle:           make([]*PooledConn, 0),
		active:         make(map[*PooledConn]struct{}),
		stopCh:         make(chan struct{}),
	}
	tp.cond = sync.NewCond(&tp.mu)

	// Start idle reaper
	go tp.reapLoop()

	// Pre-warm connections in background
	if tp.minConns > 0 {
		go tp.warmUp()
	}

	return tp
}

// warmUp pre-creates minConns idle connections so the pool is ready for traffic.
func (tp *TenantPool) warmUp() {
	for i := 0; i < tp.minConns; i++ {
		tp.mu.Lock()
		if tp.closed || tp.total >= tp.minConns {
			tp.mu.Unlock()
			return
		}
		tp.total++
		tp.mu.Unlock()

		pc, err := tp.dial(context.Background())
		if err != nil {
			tp.mu.Lock()
			tp.total--
			tp.mu.Unlock()
			slog.Warn("warm-up connection failed", "index", i+1, "total", tp.minConns, "tenant", tp.tenantID, "err", err)
			return
		}

		// For transaction-mode PG pools, authenticate during warm-up
		if tp.poolMode == "transaction" && tp.dbType == "postgres" {
			if err := tp.authenticatePG(pc); err != nil {
				pc.Close()
				tp.mu.Lock()
				tp.total--
				tp.mu.Unlock()
				slog.Warn("warm-up PG auth failed", "index", i+1, "total", tp.minConns, "tenant", tp.tenantID, "err", err)
				return
			}
		}

		tp.mu.Lock()
		if tp.closed {
			tp.mu.Unlock()
			pc.Close()
			return
		}
		pc.MarkIdle()
		tp.idle = append(tp.idle, pc)
		tp.mu.Unlock()
	}
	slog.Info("pre-warmed connections", "count", tp.minConns, "tenant", tp.tenantID)
}

// Acquire gets a connection from the pool, creating one if needed.
// The context is used for cancellation and deadline propagation.
func (tp *TenantPool) Acquire(ctx context.Context) (*PooledConn, error) {
	deadlineAt := time.Now().Add(tp.acquireTimeout)

	// If the context has an earlier deadline, use that instead.
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadlineAt) {
		deadlineAt = ctxDeadline
	}

	tp.mu.Lock()
	for {
		// Check context cancellation
		select {
		case <-ctx.Done():
			tp.mu.Unlock()
			return nil, ctx.Err()
		default:
		}

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

			// Skip Ping for pre-authenticated connections — they have proper
			// PG protocol state and Ping's 1-byte read would corrupt it.
			if !pc.IsAuthenticated() {
				if err := pc.Ping(); err != nil {
					pc.Close()
					tp.total--
					continue
				}
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

			pc, err := tp.dial(ctx)
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

		// Wait with timeout using sync.Cond
		tp.mu.Lock()
		remaining := time.Until(deadlineAt)
		if remaining <= 0 {
			tp.waiting--
			tp.mu.Unlock()
			return nil, fmt.Errorf("acquire timeout (%s) for tenant %s: pool exhausted", tp.acquireTimeout, tp.tenantID)
		}

		// Set up a timer to wake us if we time out
		timer := time.AfterFunc(remaining, func() {
			tp.cond.Broadcast()
		})
		tp.cond.Wait() // releases mu, waits for signal, reacquires mu
		timer.Stop()

		tp.waiting--

		if tp.closed {
			tp.mu.Unlock()
			return nil, fmt.Errorf("pool closing for tenant %s", tp.tenantID)
		}

		if time.Now().After(deadlineAt) {
			tp.mu.Unlock()
			return nil, fmt.Errorf("acquire timeout (%s) for tenant %s: pool exhausted", tp.acquireTimeout, tp.tenantID)
		}

		// Retry from the top of the loop (mu is held)
	}
}

// InjectTestConn adds a pre-built PooledConn directly into the pool's idle list.
// This is only intended for testing — it bypasses dial() and authentication.
func (tp *TenantPool) InjectTestConn(pc *PooledConn) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	pc.MarkIdle()
	tp.idle = append(tp.idle, pc)
	tp.total++
	tp.cond.Signal()
}

// Return releases a connection back to the pool.
func (tp *TenantPool) Return(pc *PooledConn) {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	delete(tp.active, pc)

	if tp.closed || pc.IsExpired(tp.maxLifetime) {
		pc.Close()
		tp.total--
		tp.cond.Signal()
		return
	}

	pc.MarkIdle()
	tp.idle = append(tp.idle, pc)

	// Wake one waiting goroutine — Signal() avoids the thundering herd problem
	// where Broadcast() would wake all waiters only for N-1 to go back to sleep.
	// Broadcast() is reserved for Close() and timeout wakeups.
	tp.cond.Signal()
}

// Stats returns current pool statistics.
func (tp *TenantPool) Stats() Stats {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	return Stats{
		TenantID:  tp.tenantID,
		DBType:    tp.dbType,
		PoolMode:  tp.poolMode,
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
		slog.Info("draining active connections", "count", activeCount, "tenant", tp.tenantID)
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
				slog.Warn("force-closed active connections after drain timeout", "tenant", tp.tenantID)
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
	tp.cond.Broadcast() // wake any goroutines waiting in Acquire
	tp.mu.Unlock()

	tp.Drain()
}

func (tp *TenantPool) dial(ctx context.Context) (*PooledConn, error) {
	addr := net.JoinHostPort(tp.host, fmt.Sprintf("%d", tp.port))
	dialer := net.Dialer{
		Timeout:   tp.dialTimeout,
		KeepAlive: 30 * time.Second,
	}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	pc := NewPooledConn(conn, tp.tenantID, tp.dbType, tp)

	// For transaction-mode PG pools, authenticate during dial
	if tp.poolMode == "transaction" && tp.dbType == "postgres" {
		if err := tp.authenticatePG(pc); err != nil {
			pc.Close()
			return nil, fmt.Errorf("PG auth during dial: %w", err)
		}
	}

	return pc, nil
}

// PoolMode returns the pool mode for this tenant pool.
func (tp *TenantPool) PoolMode() string {
	return tp.poolMode
}

// Password returns the configured password for the backend database.
func (tp *TenantPool) Password() string {
	return tp.password
}

// authenticatePG performs the PostgreSQL startup and authentication handshake
// on a raw connection, producing a ready-to-query connection. It sends the
// startup message, handles auth challenges, and collects ParameterStatus and
// BackendKeyData. The connection is ready for queries when this returns nil.
func (tp *TenantPool) authenticatePG(pc *PooledConn) error {
	conn := pc.Conn()

	// Build startup message: length(4) + protocol(4) + params + \0
	var body []byte
	// Protocol version 3.0
	ver := make([]byte, 4)
	binary.BigEndian.PutUint32(ver, 3<<16|0) // v3.0
	body = append(body, ver...)

	// user parameter
	body = append(body, "user"...)
	body = append(body, 0)
	body = append(body, tp.username...)
	body = append(body, 0)

	// database parameter
	body = append(body, "database"...)
	body = append(body, 0)
	body = append(body, tp.dbname...)
	body = append(body, 0)

	// terminator
	body = append(body, 0)

	// Prepend length
	msgLen := make([]byte, 4)
	binary.BigEndian.PutUint32(msgLen, uint32(4+len(body)))
	startupMsg := append(msgLen, body...)

	if _, err := conn.Write(startupMsg); err != nil {
		return fmt.Errorf("sending startup message: %w", err)
	}

	// Read responses until ReadyForQuery
	params := make(map[string]string)
	var backendPID, backendKey uint32

	for {
		// Read message type (1 byte)
		typeBuf := make([]byte, 1)
		if _, err := io.ReadFull(conn, typeBuf); err != nil {
			return fmt.Errorf("reading message type: %w", err)
		}
		msgType := typeBuf[0]

		// Read message length (4 bytes, includes itself)
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(conn, lenBuf); err != nil {
			return fmt.Errorf("reading message length: %w", err)
		}
		payloadLen := int(binary.BigEndian.Uint32(lenBuf)) - 4
		if payloadLen < 0 || payloadLen > 1<<24 {
			return fmt.Errorf("invalid message length: %d", payloadLen)
		}

		payload := make([]byte, payloadLen)
		if payloadLen > 0 {
			if _, err := io.ReadFull(conn, payload); err != nil {
				return fmt.Errorf("reading payload: %w", err)
			}
		}

		switch msgType {
		case 'R': // Authentication
			if len(payload) < 4 {
				return fmt.Errorf("authentication message too short")
			}
			authType := binary.BigEndian.Uint32(payload[:4])
			switch authType {
			case 0: // AuthenticationOk
				continue
			case 3: // AuthenticationCleartextPassword
				if err := tp.sendPasswordMessage(conn, tp.password); err != nil {
					return err
				}
			case 5: // AuthenticationMD5Password
				if len(payload) < 8 {
					return fmt.Errorf("MD5 auth message too short")
				}
				salt := payload[4:8]
				md5Pass := computeMD5Password(tp.username, tp.password, salt)
				if err := tp.sendPasswordMessage(conn, md5Pass); err != nil {
					return err
				}
			case 10: // AuthenticationSASL (SCRAM-SHA-256)
				if err := scramSHA256Auth(conn, tp.username, tp.password, payload); err != nil {
					return fmt.Errorf("SCRAM-SHA-256 auth: %w", err)
				}
			default:
				return fmt.Errorf("unsupported auth type: %d", authType)
			}

		case 'S': // ParameterStatus
			// key\0value\0
			key, val := parseNullTerminatedPair(payload)
			if key != "" {
				params[key] = val
			}

		case 'K': // BackendKeyData
			if len(payload) >= 8 {
				backendPID = binary.BigEndian.Uint32(payload[:4])
				backendKey = binary.BigEndian.Uint32(payload[4:8])
			}

		case 'Z': // ReadyForQuery
			if len(payload) >= 1 && payload[0] == 'I' {
				pc.SetAuthenticated(params, backendPID, backendKey)
				return nil
			}
			return fmt.Errorf("unexpected transaction status after auth: %c", payload[0])

		case 'E': // ErrorResponse
			errMsg := parseErrorMessage(payload)
			return fmt.Errorf("backend error during auth: %s", errMsg)

		default:
			// Skip unknown messages during startup
			continue
		}
	}
}

// sendPasswordMessage sends a PG password message ('p').
func (tp *TenantPool) sendPasswordMessage(conn net.Conn, password string) error {
	payload := append([]byte(password), 0)
	msgLen := len(payload) + 4
	buf := make([]byte, 1+4+len(payload))
	buf[0] = 'p'
	binary.BigEndian.PutUint32(buf[1:5], uint32(msgLen))
	copy(buf[5:], payload)
	_, err := conn.Write(buf)
	return err
}

// parseNullTerminatedPair parses a "key\0value\0" buffer.
func parseNullTerminatedPair(data []byte) (string, string) {
	for i := 0; i < len(data); i++ {
		if data[i] == 0 {
			key := string(data[:i])
			rest := data[i+1:]
			for j := 0; j < len(rest); j++ {
				if rest[j] == 0 {
					return key, string(rest[:j])
				}
			}
			return key, string(rest)
		}
	}
	return "", ""
}

// parseErrorMessage extracts the message ('M') field from a PG ErrorResponse payload.
func parseErrorMessage(payload []byte) string {
	for i := 0; i < len(payload); i++ {
		fieldType := payload[i]
		if fieldType == 0 {
			break
		}
		i++
		end := i
		for end < len(payload) && payload[end] != 0 {
			end++
		}
		if fieldType == 'M' {
			return string(payload[i:end])
		}
		i = end
	}
	return "unknown error"
}

// computeMD5Password computes the PostgreSQL MD5 password hash.
// Formula: "md5" + md5(md5(password + user) + salt)
func computeMD5Password(user, password string, salt []byte) string {
	h1 := md5.Sum([]byte(password + user))
	hex1 := hex.EncodeToString(h1[:])
	h2 := md5.Sum(append([]byte(hex1), salt...))
	return "md5" + hex.EncodeToString(h2[:])
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

	// Reap oldest connections first (front of the slice).
	// Keep at least minConns, preserving the newest (back of the slice).
	kept := make([]*PooledConn, 0, len(tp.idle))
	excess := len(tp.idle) - tp.minConns
	for i, pc := range tp.idle {
		if i < excess && (pc.IsIdle(tp.idleTimeout) || pc.IsExpired(tp.maxLifetime)) {
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
	closeOnce       sync.Once
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
	slog.Info("created pool", "tenant", tenantID, "db_type", tc.DBType, "host", tc.Host, "port", tc.Port)
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
	slog.Info("removed pool", "tenant", tenantID)
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

// Close shuts down all pools and stops the stats loop. Safe to call multiple times.
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		close(m.statsStopCh)
	})

	m.mu.Lock()
	pools := m.pools
	m.pools = make(map[string]*TenantPool)
	m.mu.Unlock()

	for _, p := range pools {
		p.Close()
	}
}
