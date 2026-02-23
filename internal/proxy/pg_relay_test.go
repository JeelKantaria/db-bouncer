package proxy

import (
	"context"
	"encoding/binary"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/pool"
)

func txnTestPool(t *testing.T) (*pool.TenantPool, config.PoolDefaults) {
	t.Helper()
	txnMode := "transaction"
	tc := config.TenantConfig{
		DBType: "postgres", Host: "localhost", Port: 5432,
		DBName: "testdb", Username: "testuser", PoolMode: &txnMode,
	}
	defaults := config.PoolDefaults{
		MinConnections: 0, MaxConnections: 2,
		IdleTimeout: 5 * time.Minute, MaxLifetime: 30 * time.Minute,
		AcquireTimeout: 5 * time.Second, PoolMode: "transaction",
	}
	tp := pool.NewTenantPool("test_txn", tc, defaults)
	return tp, defaults
}

func injectAuthConn(t *testing.T, tp *pool.TenantPool, backendConn net.Conn) {
	t.Helper()
	pc := pool.NewPooledConn(backendConn, "test_txn", "postgres", tp)
	pc.SetAuthenticated(
		map[string]string{"server_version": "15.0", "server_encoding": "UTF8"},
		1234, 5678,
	)
	tp.InjectTestConn(pc)
}

func TestPGRelayReturnsConnectionOnIdle(t *testing.T) {
	tp, _ := txnTestPool(t)
	defer tp.Close()

	clientConn, clientEnd := net.Pipe()
	defer clientConn.Close()
	defer clientEnd.Close()

	backendConn, backendEnd := net.Pipe()
	defer backendConn.Close()
	defer backendEnd.Close()

	injectAuthConn(t, tp, backendConn)

	var discardReceived atomic.Bool

	// Backend goroutine: responds to queries
	go func() {
		backendEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			msgType, payload, err := readPGMessage(backendEnd)
			if err != nil {
				return
			}
			if msgType == pgMsgQuery {
				queryStr := string(payload[:len(payload)-1])
				if queryStr == "DISCARD ALL" {
					discardReceived.Store(true)
				}
				writePGMessage(backendEnd, 'C', append([]byte("OK"), 0))
				writePGMessage(backendEnd, pgMsgReadyForQuery, []byte{'I'})
			}
		}
	}()

	// Relay goroutine
	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayPGTransactionMode(context.Background(), clientConn, tp, "test_txn", nil)
	}()

	// Client: read auth, send query, read response, terminate
	clientEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
	drainSyntheticAuth(t, clientEnd)

	writePGMessage(clientEnd, pgMsgQuery, append([]byte("SELECT 1"), 0))

	// Read response (CommandComplete + ReadyForQuery)
	for {
		msgType, payload, err := readPGMessage(clientEnd)
		if err != nil {
			t.Fatalf("client read error: %v", err)
		}
		if msgType == pgMsgReadyForQuery && len(payload) > 0 && payload[0] == 'I' {
			break
		}
	}

	// Allow time for DISCARD ALL + return to complete
	time.Sleep(100 * time.Millisecond)

	stats := tp.Stats()
	if stats.Idle != 1 {
		t.Errorf("expected 1 idle after txn boundary, got %d idle / %d active", stats.Idle, stats.Active)
	}

	if !discardReceived.Load() {
		t.Error("expected DISCARD ALL to be sent to backend before pool return")
	}

	// Terminate
	writePGMessage(clientEnd, pgMsgTerminate, nil)
	clientEnd.Close()

	err := <-relayDone
	if err != nil {
		t.Errorf("relay returned error: %v", err)
	}
}

func TestPGRelayHoldsDuringTransaction(t *testing.T) {
	tp, _ := txnTestPool(t)
	defer tp.Close()

	clientConn, clientEnd := net.Pipe()
	defer clientConn.Close()
	defer clientEnd.Close()

	backendConn, backendEnd := net.Pipe()
	defer backendConn.Close()
	defer backendEnd.Close()

	injectAuthConn(t, tp, backendConn)

	queryCount := 0

	// Backend goroutine
	go func() {
		backendEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			msgType, payload, err := readPGMessage(backendEnd)
			if err != nil {
				return
			}
			if msgType == pgMsgQuery {
				queryStr := string(payload[:len(payload)-1])
				queryCount++

				writePGMessage(backendEnd, 'C', append([]byte(queryStr), 0))

				switch queryStr {
				case "BEGIN":
					writePGMessage(backendEnd, pgMsgReadyForQuery, []byte{'T'}) // in transaction
				case "COMMIT", "DISCARD ALL":
					writePGMessage(backendEnd, pgMsgReadyForQuery, []byte{'I'}) // idle
				default:
					writePGMessage(backendEnd, pgMsgReadyForQuery, []byte{'T'}) // still in transaction
				}
			}
		}
	}()

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayPGTransactionMode(context.Background(), clientConn, tp, "test_txn", nil)
	}()

	clientEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
	drainSyntheticAuth(t, clientEnd)

	// BEGIN
	writePGMessage(clientEnd, pgMsgQuery, append([]byte("BEGIN"), 0))
	for {
		msgType, payload, err := readPGMessage(clientEnd)
		if err != nil {
			t.Fatalf("client read error: %v", err)
		}
		if msgType == pgMsgReadyForQuery && len(payload) > 0 {
			break
		}
	}

	// During transaction: pool should have 0 idle
	time.Sleep(50 * time.Millisecond)
	stats := tp.Stats()
	if stats.Idle != 0 {
		t.Errorf("expected 0 idle during transaction, got %d", stats.Idle)
	}

	// COMMIT
	writePGMessage(clientEnd, pgMsgQuery, append([]byte("COMMIT"), 0))
	for {
		msgType, payload, err := readPGMessage(clientEnd)
		if err != nil {
			t.Fatalf("client read error: %v", err)
		}
		if msgType == pgMsgReadyForQuery && len(payload) > 0 {
			break
		}
	}

	// After commit: connection should be returned
	time.Sleep(100 * time.Millisecond)
	stats = tp.Stats()
	if stats.Idle != 1 {
		t.Errorf("expected 1 idle after COMMIT, got %d", stats.Idle)
	}

	writePGMessage(clientEnd, pgMsgTerminate, nil)
	clientEnd.Close()
	<-relayDone
}

func TestPGRelayHandlesTerminate(t *testing.T) {
	tp, _ := txnTestPool(t)
	defer tp.Close()

	clientConn, clientEnd := net.Pipe()
	defer clientConn.Close()
	defer clientEnd.Close()

	backendConn, backendEnd := net.Pipe()
	defer backendConn.Close()
	defer backendEnd.Close()

	injectAuthConn(t, tp, backendConn)

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayPGTransactionMode(context.Background(), clientConn, tp, "test_txn", nil)
	}()

	clientEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
	drainSyntheticAuth(t, clientEnd)

	// Immediately terminate (no backend held after initial auth)
	writePGMessage(clientEnd, pgMsgTerminate, nil)
	clientEnd.Close()

	err := <-relayDone
	if err != nil {
		t.Errorf("relay returned error: %v", err)
	}
}

func TestPGRelayDirtyDisconnect(t *testing.T) {
	tp, _ := txnTestPool(t)
	defer tp.Close()

	clientConn, clientEnd := net.Pipe()
	defer clientConn.Close()
	defer clientEnd.Close()

	backendConn, backendEnd := net.Pipe()
	defer backendConn.Close()
	defer backendEnd.Close()

	injectAuthConn(t, tp, backendConn)

	var rollbackReceived atomic.Bool

	// Backend goroutine
	go func() {
		backendEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			msgType, payload, err := readPGMessage(backendEnd)
			if err != nil {
				return
			}
			if msgType == pgMsgQuery {
				queryStr := string(payload[:len(payload)-1])
				if queryStr == "ROLLBACK" {
					rollbackReceived.Store(true)
				}
				writePGMessage(backendEnd, 'C', append([]byte(queryStr), 0))
				if queryStr == "BEGIN" {
					writePGMessage(backendEnd, pgMsgReadyForQuery, []byte{'T'})
				} else {
					writePGMessage(backendEnd, pgMsgReadyForQuery, []byte{'I'})
				}
			}
		}
	}()

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayPGTransactionMode(context.Background(), clientConn, tp, "test_txn", nil)
	}()

	clientEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
	drainSyntheticAuth(t, clientEnd)

	// Begin a transaction
	writePGMessage(clientEnd, pgMsgQuery, append([]byte("BEGIN"), 0))
	for {
		msgType, payload, err := readPGMessage(clientEnd)
		if err != nil {
			t.Fatalf("client read error: %v", err)
		}
		if msgType == pgMsgReadyForQuery && len(payload) > 0 {
			break
		}
	}

	// Dirty disconnect: close client without COMMIT/ROLLBACK
	clientEnd.Close()

	<-relayDone

	// Wait for cleanup
	time.Sleep(100 * time.Millisecond)

	if !rollbackReceived.Load() {
		t.Error("expected ROLLBACK to be sent on dirty disconnect")
	}
}

func TestPGRelaySessionPinOnListen(t *testing.T) {
	tp, _ := txnTestPool(t)
	defer tp.Close()

	clientConn, clientEnd := net.Pipe()
	defer clientConn.Close()
	defer clientEnd.Close()

	backendConn, backendEnd := net.Pipe()
	defer backendConn.Close()
	defer backendEnd.Close()

	injectAuthConn(t, tp, backendConn)

	var discardReceived atomic.Bool

	// Backend goroutine
	go func() {
		backendEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			msgType, payload, err := readPGMessage(backendEnd)
			if err != nil {
				return
			}
			if msgType == pgMsgQuery {
				queryStr := string(payload[:len(payload)-1])
				if queryStr == "DISCARD ALL" {
					discardReceived.Store(true)
				}
				writePGMessage(backendEnd, 'C', append([]byte(queryStr), 0))
				writePGMessage(backendEnd, pgMsgReadyForQuery, []byte{'I'})
			}
		}
	}()

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayPGTransactionMode(context.Background(), clientConn, tp, "test_txn", nil)
	}()

	clientEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
	drainSyntheticAuth(t, clientEnd)

	// Send LISTEN — this should pin the session
	writePGMessage(clientEnd, pgMsgQuery, append([]byte("LISTEN my_channel"), 0))
	for {
		msgType, payload, err := readPGMessage(clientEnd)
		if err != nil {
			t.Fatalf("client read error: %v", err)
		}
		if msgType == pgMsgReadyForQuery && len(payload) > 0 {
			break
		}
	}

	// Session is pinned: backend should NOT be returned to pool
	time.Sleep(100 * time.Millisecond)
	stats := tp.Stats()
	if stats.Idle != 0 {
		t.Errorf("expected 0 idle (session pinned), got %d", stats.Idle)
	}

	// DISCARD ALL should NOT have been sent (pinned = no release)
	if discardReceived.Load() {
		t.Error("DISCARD ALL should not be sent when session is pinned")
	}

	// Terminate the pinned session
	writePGMessage(clientEnd, pgMsgTerminate, nil)
	clientEnd.Close()
	<-relayDone
}

func TestPGRelayResetOnReturn(t *testing.T) {
	tp, _ := txnTestPool(t)
	defer tp.Close()

	clientConn, clientEnd := net.Pipe()
	defer clientConn.Close()
	defer clientEnd.Close()

	backendConn, backendEnd := net.Pipe()
	defer backendConn.Close()
	defer backendEnd.Close()

	injectAuthConn(t, tp, backendConn)

	var queriesReceived []string
	var mu sync.Mutex

	// Backend goroutine
	go func() {
		backendEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			msgType, payload, err := readPGMessage(backendEnd)
			if err != nil {
				return
			}
			if msgType == pgMsgQuery {
				queryStr := string(payload[:len(payload)-1])
				mu.Lock()
				queriesReceived = append(queriesReceived, queryStr)
				mu.Unlock()
				writePGMessage(backendEnd, 'C', append([]byte(queryStr), 0))
				writePGMessage(backendEnd, pgMsgReadyForQuery, []byte{'I'})
			}
		}
	}()

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayPGTransactionMode(context.Background(), clientConn, tp, "test_txn", nil)
	}()

	clientEnd.SetReadDeadline(time.Now().Add(5 * time.Second))
	drainSyntheticAuth(t, clientEnd)

	// Send a simple query
	writePGMessage(clientEnd, pgMsgQuery, append([]byte("SELECT 1"), 0))
	for {
		msgType, payload, err := readPGMessage(clientEnd)
		if err != nil {
			t.Fatalf("client read error: %v", err)
		}
		if msgType == pgMsgReadyForQuery && len(payload) > 0 {
			break
		}
	}

	// Wait for reset to complete
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	queries := make([]string, len(queriesReceived))
	copy(queries, queriesReceived)
	mu.Unlock()

	// Should have received SELECT 1 then DISCARD ALL
	if len(queries) < 2 {
		t.Fatalf("expected at least 2 queries, got %d: %v", len(queries), queries)
	}
	if queries[0] != "SELECT 1" {
		t.Errorf("expected first query 'SELECT 1', got %q", queries[0])
	}
	if queries[1] != "DISCARD ALL" {
		t.Errorf("expected second query 'DISCARD ALL', got %q", queries[1])
	}

	writePGMessage(clientEnd, pgMsgTerminate, nil)
	clientEnd.Close()
	<-relayDone
}

func TestDetectSessionPin(t *testing.T) {
	tests := []struct {
		name    string
		msgType byte
		payload []byte
		want    bool
	}{
		{
			name:    "LISTEN command",
			msgType: pgMsgQuery,
			payload: append([]byte("LISTEN my_channel"), 0),
			want:    true,
		},
		{
			name:    "NOTIFY command",
			msgType: pgMsgQuery,
			payload: append([]byte("NOTIFY my_channel"), 0),
			want:    true,
		},
		{
			name:    "SELECT query",
			msgType: pgMsgQuery,
			payload: append([]byte("SELECT 1"), 0),
			want:    false,
		},
		{
			name:    "Named prepared statement",
			msgType: pgMsgParse,
			payload: append([]byte("mystmt"), append([]byte{0}, []byte("SELECT 1")...)...),
			want:    true,
		},
		{
			name:    "Unnamed prepared statement",
			msgType: pgMsgParse,
			payload: append([]byte{0}, []byte("SELECT 1")...),
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectSessionPin(tt.msgType, tt.payload)
			if got != tt.want {
				t.Errorf("detectSessionPin(%c, %q) = %v, want %v", tt.msgType, tt.payload, got, tt.want)
			}
		})
	}
}

// drainSyntheticAuth reads all synthetic auth messages from the client side
// (AuthOk, ParameterStatus messages, BackendKeyData, ReadyForQuery).
func drainSyntheticAuth(t *testing.T, conn net.Conn) {
	t.Helper()
	for {
		msgType, payload, err := readPGMessage(conn)
		if err != nil {
			t.Fatalf("drainSyntheticAuth: read error: %v", err)
		}
		if msgType == pgMsgReadyForQuery {
			if len(payload) > 0 && payload[0] == 'I' {
				return
			}
		}
	}
}

// Helper to build a uint32 in big-endian for PG messages.
func pgUint32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
