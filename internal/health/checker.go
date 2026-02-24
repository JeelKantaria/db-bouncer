package health

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/metrics"
	"github.com/dbbouncer/dbbouncer/internal/pool"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

// Status represents the health status of a tenant's database.
type Status int

const (
	StatusUnknown Status = iota
	StatusHealthy
	StatusUnhealthy
)

func (s Status) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// TenantHealth holds health information for a tenant.
type TenantHealth struct {
	Status              Status    `json:"status"`
	LastCheck           time.Time `json:"last_check"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastError           string    `json:"last_error,omitempty"`
}

// Checker performs periodic health checks on tenant databases.
type Checker struct {
	mu      sync.RWMutex
	tenants map[string]*TenantHealth
	router  *router.Router
	metrics *metrics.Collector
	poolMgr *pool.Manager

	interval          time.Duration
	failureThreshold  int
	connectionTimeout time.Duration

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewChecker creates a new health checker with configurable parameters.
func NewChecker(r *router.Router, m *metrics.Collector, hcCfg config.HealthCheckConfig) *Checker {
	return &Checker{
		tenants:           make(map[string]*TenantHealth),
		router:            r,
		metrics:           m,
		interval:          hcCfg.Interval,
		failureThreshold:  hcCfg.FailureThreshold,
		connectionTimeout: hcCfg.ConnectionTimeout,
		stopCh:            make(chan struct{}),
	}
}

// SetPoolManager wires a pool.Manager into the checker so transaction-mode
// tenants can be health-checked via a real SQL query instead of a raw TCP probe.
func (c *Checker) SetPoolManager(pm *pool.Manager) {
	c.poolMgr = pm
}

// Start begins periodic health checking.
func (c *Checker) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.run()
	}()
	slog.Info("health checker started", "interval", c.interval, "threshold", c.failureThreshold)
}

// Stop stops the health checker. Safe to call multiple times.
func (c *Checker) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
	c.wg.Wait()
	slog.Info("health checker stopped")
}

func (c *Checker) run() {
	// Run immediately on start
	c.checkAll()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.checkAll()
		case <-c.stopCh:
			return
		}
	}
}

func (c *Checker) checkAll() {
	tenants := c.router.ListTenants()

	// Run health checks in parallel with a bounded worker pool.
	const maxWorkers = 10
	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup

	for id, tc := range tenants {
		id, tc := id, tc // capture loop vars
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore slot
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			start := time.Now()
			healthy := c.pingTenant(id, tc)
			elapsed := time.Since(start)
			if c.metrics != nil {
				c.metrics.HealthCheckCompleted(id, elapsed, healthy)
			}
			c.updateStatus(id, healthy)
		}()
	}
	wg.Wait()
}

func (c *Checker) pingTenant(tenantID string, tc config.TenantConfig) bool {
	// For transaction-mode PG tenants with a live pool, use a SQL-level check
	// via an existing pool connection — validates the full query path, not just TCP.
	if tc.DBType == "postgres" && c.poolMgr != nil {
		tp, ok := c.poolMgr.Get(tenantID)
		if ok && tp.PoolMode() == "transaction" {
			return c.pingPostgresViaPool(tenantID, tp)
		}
	}

	addr := net.JoinHostPort(tc.Host, fmt.Sprintf("%d", tc.Port))
	conn, err := net.DialTimeout("tcp", addr, c.connectionTimeout)
	if err != nil {
		if c.metrics != nil {
			c.metrics.HealthCheckError(tenantID, "connection_refused")
		}
		c.setLastError(tenantID, err.Error())
		return false
	}
	defer conn.Close()

	// Protocol-level health check: verify the database actually responds,
	// not just that the TCP port is open.
	switch tc.DBType {
	case "postgres":
		return c.pingPostgres(tenantID, conn)
	case "mysql":
		return c.pingMySQL(tenantID, conn)
	default:
		// Unknown DB type: fall back to read-with-deadline check.
		// A healthy server will either send data or keep the connection open
		// (timeout = healthy). A dead/rejecting server will RST immediately.
		return c.pingTCPRead(tenantID, conn)
	}
}

// pingPostgresViaPool runs "SELECT 1" over a pre-authenticated pool connection,
// giving a full end-to-end health signal. Falls back to TCP probe if the pool
// is exhausted or the acquire times out.
func (c *Checker) pingPostgresViaPool(tenantID string, tp *pool.TenantPool) bool {
	ctx, cancel := context.WithTimeout(context.Background(), c.connectionTimeout)
	defer cancel()

	pc, err := tp.Acquire(ctx)
	if err != nil {
		// Pool exhausted — fall back to TCP probe
		if c.metrics != nil {
			c.metrics.HealthCheckError(tenantID, "pool_exhausted")
		}
		c.setLastError(tenantID, "pool exhausted for health check: "+err.Error())
		return false
	}
	defer tp.Return(pc)

	conn := pc.Conn()
	conn.SetDeadline(time.Now().Add(c.connectionTimeout))

	// Send SELECT 1 as a simple query
	query := append([]byte("SELECT 1"), 0)
	if err := writePGHealthMsg(conn, 'Q', query); err != nil {
		if c.metrics != nil {
			c.metrics.HealthCheckError(tenantID, "write_error")
		}
		c.setLastError(tenantID, "health check write: "+err.Error())
		pc.Close()
		return false
	}

	// Read responses until ReadyForQuery
	for {
		msgType, _, err := readPGHealthMsg(conn)
		if err != nil {
			if c.metrics != nil {
				c.metrics.HealthCheckError(tenantID, "read_error")
			}
			c.setLastError(tenantID, "health check read: "+err.Error())
			pc.Close()
			return false
		}
		if msgType == 'E' { // ErrorResponse
			if c.metrics != nil {
				c.metrics.HealthCheckError(tenantID, "query_error")
			}
			c.setLastError(tenantID, "health check SELECT 1 returned error")
			// Don't close — backend is still functional, just had an error
			return false
		}
		if msgType == 'Z' { // ReadyForQuery
			// Clear the last error on success
			c.setLastError(tenantID, "")
			return true
		}
	}
}

func (c *Checker) setLastError(tenantID, errMsg string) {
	c.mu.Lock()
	th := c.getOrCreate(tenantID)
	if errMsg != "" {
		th.LastError = errMsg
	}
	c.mu.Unlock()
}

// pingPostgres sends a minimal startup message and checks for any response.
func (c *Checker) pingPostgres(tenantID string, conn net.Conn) bool {
	conn.SetDeadline(time.Now().Add(c.connectionTimeout))

	// Send a startup message with protocol version 3.0.
	// Parameters: user=healthcheck, then null terminator.
	params := []byte("user\x00healthcheck\x00\x00")
	msgLen := 4 + 4 + len(params) // length field + protocol version + params
	msg := make([]byte, msgLen)
	msg[0] = byte(msgLen >> 24)
	msg[1] = byte(msgLen >> 16)
	msg[2] = byte(msgLen >> 8)
	msg[3] = byte(msgLen)
	// Protocol version 3.0
	msg[4] = 0
	msg[5] = 3
	msg[6] = 0
	msg[7] = 0
	copy(msg[8:], params)

	if _, err := conn.Write(msg); err != nil {
		c.setLastError(tenantID, fmt.Sprintf("pg write startup: %s", err))
		return false
	}

	// Read at least 1 byte of response. Any response (auth request, error, etc.)
	// means the PostgreSQL server is alive and processing protocol messages.
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		c.setLastError(tenantID, fmt.Sprintf("pg read response: %s", err))
		return false
	}
	return true
}

// pingMySQL reads the initial handshake packet that MySQL sends on connect.
func (c *Checker) pingMySQL(tenantID string, conn net.Conn) bool {
	conn.SetDeadline(time.Now().Add(c.connectionTimeout))

	// MySQL server sends a handshake packet immediately after TCP connect.
	// Read the 4-byte header.
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		c.setLastError(tenantID, fmt.Sprintf("mysql read handshake header: %s", err))
		return false
	}

	payloadLen := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if payloadLen <= 0 || payloadLen > 65535 {
		c.setLastError(tenantID, fmt.Sprintf("mysql invalid handshake length: %d", payloadLen))
		return false
	}

	// Read the handshake payload
	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		c.setLastError(tenantID, fmt.Sprintf("mysql read handshake payload: %s", err))
		return false
	}

	// Check it's a valid handshake (protocol version 10) or error packet
	if len(payload) > 0 && payload[0] == 0xff {
		c.setLastError(tenantID, "mysql server returned error on connect")
		return false
	}
	return true
}

// pingTCPRead verifies a connection is alive by attempting a read with deadline.
// A timeout means the connection is open (healthy). An immediate error means dead.
func (c *Checker) pingTCPRead(tenantID string, conn net.Conn) bool {
	conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := conn.Read(buf)
	if err != nil {
		// Timeout is expected for a healthy connection that doesn't send data first
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return true
		}
		c.setLastError(tenantID, fmt.Sprintf("tcp read: %s", err))
		return false
	}
	// Got data — server is alive
	return true
}

func (c *Checker) updateStatus(tenantID string, healthy bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	th := c.getOrCreate(tenantID)
	th.LastCheck = time.Now()

	if healthy {
		if th.ConsecutiveFailures > 0 {
			slog.Info("tenant recovered", "tenant", tenantID, "failures", th.ConsecutiveFailures)
		}
		th.Status = StatusHealthy
		th.ConsecutiveFailures = 0
		th.LastError = ""
	} else {
		th.ConsecutiveFailures++
		if th.ConsecutiveFailures >= c.failureThreshold {
			if th.Status != StatusUnhealthy {
				slog.Warn("tenant marked unhealthy", "tenant", tenantID, "failures", th.ConsecutiveFailures, "error", th.LastError)
			}
			th.Status = StatusUnhealthy
		}
	}

	if c.metrics != nil {
		c.metrics.SetTenantHealth(tenantID, th.Status == StatusHealthy)
	}
}

func (c *Checker) getOrCreate(tenantID string) *TenantHealth {
	th, ok := c.tenants[tenantID]
	if !ok {
		th = &TenantHealth{Status: StatusUnknown}
		c.tenants[tenantID] = th
	}
	return th
}

// IsHealthy returns whether a tenant is healthy (or unknown, which is treated as healthy).
func (c *Checker) IsHealthy(tenantID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	th, ok := c.tenants[tenantID]
	if !ok {
		return true // unknown = allow through
	}
	return th.Status != StatusUnhealthy
}

// GetStatus returns the health status for a tenant.
func (c *Checker) GetStatus(tenantID string) TenantHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()

	th, ok := c.tenants[tenantID]
	if !ok {
		return TenantHealth{Status: StatusUnknown}
	}
	return *th
}

// GetAllStatuses returns health statuses for all known tenants.
func (c *Checker) GetAllStatuses() map[string]TenantHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]TenantHealth, len(c.tenants))
	for id, th := range c.tenants {
		result[id] = *th
	}
	return result
}

// OverallHealthy returns true if all tenants are healthy.
func (c *Checker) OverallHealthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, th := range c.tenants {
		if th.Status == StatusUnhealthy {
			return false
		}
	}
	return true
}

// RemoveTenant removes health state for a tenant that has been deleted.
func (c *Checker) RemoveTenant(tenantID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.tenants, tenantID)
	if c.metrics != nil {
		c.metrics.RemoveTenant(tenantID)
	}
	slog.Info("removed health state", "tenant", tenantID)
}

// writePGHealthMsg writes a typed PG message (type + length + payload).
func writePGHealthMsg(conn net.Conn, msgType byte, payload []byte) error {
	msgLen := uint32(len(payload) + 4)
	buf := make([]byte, 1+4+len(payload))
	buf[0] = msgType
	binary.BigEndian.PutUint32(buf[1:5], msgLen)
	copy(buf[5:], payload)
	_, err := conn.Write(buf)
	return err
}

// readPGHealthMsg reads a PG message and returns its type and payload.
func readPGHealthMsg(conn net.Conn) (byte, []byte, error) {
	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, typeBuf); err != nil {
		return 0, nil, err
	}
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return 0, nil, err
	}
	payloadLen := int(binary.BigEndian.Uint32(lenBuf)) - 4
	if payloadLen < 0 || payloadLen > 1<<20 {
		return 0, nil, fmt.Errorf("invalid message length: %d", payloadLen)
	}
	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return 0, nil, err
		}
	}
	return typeBuf[0], payload, nil
}

