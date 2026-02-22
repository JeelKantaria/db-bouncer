package pool

import (
	"net"
	"sync"
	"time"
)

// ConnState represents the state of a pooled connection.
type ConnState int

const (
	ConnStateIdle ConnState = iota
	ConnStateActive
	ConnStateClosed
)

// PooledConn wraps a raw network connection with pooling metadata.
type PooledConn struct {
	mu        sync.Mutex
	conn      net.Conn
	state     ConnState
	createdAt time.Time
	lastUsed  time.Time
	tenantID  string
	dbType    string
	pool      *TenantPool // back-reference for returning to pool
}

// NewPooledConn wraps a net.Conn for pool management.
func NewPooledConn(conn net.Conn, tenantID, dbType string, p *TenantPool) *PooledConn {
	now := time.Now()
	return &PooledConn{
		conn:      conn,
		state:     ConnStateIdle,
		createdAt: now,
		lastUsed:  now,
		tenantID:  tenantID,
		dbType:    dbType,
		pool:      p,
	}
}

// Conn returns the underlying net.Conn.
func (pc *PooledConn) Conn() net.Conn {
	return pc.conn
}

// TenantID returns the tenant this connection belongs to.
func (pc *PooledConn) TenantID() string {
	return pc.tenantID
}

// DBType returns the database type (postgres or mysql).
func (pc *PooledConn) DBType() string {
	return pc.dbType
}

// MarkActive marks this connection as in-use.
func (pc *PooledConn) MarkActive() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.state = ConnStateActive
	pc.lastUsed = time.Now()
}

// MarkIdle marks this connection as idle (returned to pool).
func (pc *PooledConn) MarkIdle() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.state = ConnStateIdle
	pc.lastUsed = time.Now()
}

// State returns the current connection state.
func (pc *PooledConn) State() ConnState {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.state
}

// CreatedAt returns when this connection was established.
func (pc *PooledConn) CreatedAt() time.Time {
	return pc.createdAt
}

// LastUsed returns when this connection was last used.
func (pc *PooledConn) LastUsed() time.Time {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	return pc.lastUsed
}

// IsExpired checks if the connection has exceeded its max lifetime.
func (pc *PooledConn) IsExpired(maxLifetime time.Duration) bool {
	if maxLifetime <= 0 {
		return false
	}
	return time.Since(pc.createdAt) > maxLifetime
}

// IsIdle checks if the connection has been idle longer than the timeout.
func (pc *PooledConn) IsIdle(idleTimeout time.Duration) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if idleTimeout <= 0 {
		return false
	}
	return pc.state == ConnStateIdle && time.Since(pc.lastUsed) > idleTimeout
}

// Close closes the underlying connection and marks it as closed.
func (pc *PooledConn) Close() error {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.state = ConnStateClosed
	return pc.conn.Close()
}

// Ping performs a lightweight health check on the connection.
// A 1-byte read with a short deadline is used. A timeout error means
// the connection is alive (no data pending but not closed). Any other
// error means the connection is dead.
func (pc *PooledConn) Ping() error {
	pc.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buf := make([]byte, 1)
	_, err := pc.conn.Read(buf)
	pc.conn.SetReadDeadline(time.Time{}) // Clear deadline
	if err != nil {
		// timeout is expected (connection is alive), other errors mean it's dead
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			return nil
		}
		return err
	}
	// If we actually read a byte, the connection is alive (unexpected data, but not dead)
	return nil
}

// Return releases this connection back to its pool.
func (pc *PooledConn) Return() {
	if pc.pool != nil {
		pc.pool.Return(pc)
	}
}
