package proxy

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/pool"
)

// newPGBenchPoolSingle creates a TenantPool with one pre-authenticated
// connection injected for benchmarking the relay.
func newPGBenchPoolSingle(b *testing.B, backendConn net.Conn) *pool.TenantPool {
	b.Helper()
	tc := config.TenantConfig{
		DBType:   "postgres",
		Host:     "127.0.0.1",
		Port:     5432,
		DBName:   "bench",
		Username: "user",
	}
	defaults := config.PoolDefaults{
		MinConnections: 0,
		MaxConnections: 1,
		AcquireTimeout: 30 * time.Second,
		PoolMode:       "transaction",
	}
	tp := pool.NewTenantPool("bench_pg", tc, defaults)
	pc := pool.NewPooledConn(backendConn, "bench_pg", "postgres", tp)
	pc.SetAuthenticated(map[string]string{"server_version": "15.0"}, 1, 2)
	tp.InjectTestConn(pc)
	return tp
}

// drainPGSyntheticAuthB drains the synthetic auth sequence sent by the relay
// to the client: AuthOk + ParameterStatus(s) + BackendKeyData + ReadyForQuery('I').
func drainPGSyntheticAuthB(b *testing.B, conn net.Conn) {
	b.Helper()
	for {
		msgType, payload, err := readPGMessage(conn)
		if err != nil {
			b.Fatalf("drainPGSyntheticAuthB: %v", err)
		}
		if msgType == pgMsgReadyForQuery && len(payload) > 0 && payload[0] == 'I' {
			return
		}
	}
}

// drainPGUntilRFQB reads and discards PG messages until ReadyForQuery('I').
func drainPGUntilRFQB(b *testing.B, conn net.Conn) {
	b.Helper()
	for {
		msgType, payload, err := readPGMessage(conn)
		if err != nil {
			b.Fatalf("drainPGUntilRFQB: %v", err)
		}
		if msgType == pgMsgReadyForQuery && len(payload) > 0 && payload[0] == 'I' {
			return
		}
	}
}

// BenchmarkPGRelayTransactionMode measures end-to-end throughput of
// relayPGTransactionMode for single-query transactions using net.Pipe.
//
// The mock backend runs in its own goroutine (no deadlines) because net.Pipe
// is unbuffered: the relay writes DISCARD ALL to the backend inside
// resetAndReturn while the benchmark goroutine reads from the client side.
func BenchmarkPGRelayTransactionMode(b *testing.B) {
	clientConn, clientEnd := net.Pipe()
	backendConn, backendEnd := net.Pipe()

	tp := newPGBenchPoolSingle(b, backendConn)

	backendDone := make(chan struct{})
	go func() {
		defer close(backendDone)
		for {
			msgType, payload, err := readPGMessage(backendEnd)
			if err != nil {
				return
			}
			if msgType == pgMsgTerminate {
				return
			}
			if msgType == pgMsgQuery {
				q := string(payload[:len(payload)-1])
				if q == "DISCARD ALL" {
					writePGMessage(backendEnd, 'C', append([]byte("DISCARD"), 0))
				} else {
					writePGMessage(backendEnd, 'C', append([]byte("SELECT 1"), 0))
				}
				writePGMessage(backendEnd, pgMsgReadyForQuery, []byte{'I'})
			}
		}
	}()

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayPGTransactionMode(context.Background(), clientConn, tp, "bench_pg", nil)
	}()

	b.Cleanup(func() {
		writePGMessage(clientEnd, pgMsgTerminate, nil)
		<-relayDone
		tp.Close()
		backendEnd.Close()
		<-backendDone
	})

	// Drain synthetic auth from relay
	drainPGSyntheticAuthB(b, clientEnd)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		writePGMessage(clientEnd, pgMsgQuery, append([]byte("SELECT 1"), 0))
		drainPGUntilRFQB(b, clientEnd)
	}
}

// Note: session-mode relay (blind io.Copy) benchmarks are omitted because
// net.Pipe's synchronous/unbuffered semantics make it incompatible with Go's
// benchmark calibration passes. Transaction-mode relay benchmarks above are
// the meaningful comparison point for protocol overhead.

// TestPGRelayBenchSetup validates that the benchmark setup works correctly
// for multiple iterations without deadlock or connection loss.
func TestPGRelayBenchSetup(t *testing.T) {
	clientConn, clientEnd := net.Pipe()
	backendConn, backendEnd := net.Pipe()
	defer clientConn.Close()
	defer clientEnd.Close()
	defer backendEnd.Close()

	tc := config.TenantConfig{
		DBType: "postgres", Host: "127.0.0.1", Port: 5432,
		DBName: "bench", Username: "user",
	}
	defaults := config.PoolDefaults{
		MinConnections: 0, MaxConnections: 1,
		AcquireTimeout: 5 * time.Second, PoolMode: "transaction",
	}
	tp := pool.NewTenantPool("bench_pg", tc, defaults)
	defer tp.Close()

	pc := pool.NewPooledConn(backendConn, "bench_pg", "postgres", tp)
	pc.SetAuthenticated(map[string]string{"server_version": "15.0"}, 1, 2)
	tp.InjectTestConn(pc)

	backendDone := make(chan struct{})
	go func() {
		defer close(backendDone)
		for {
			msgType, payload, err := readPGMessage(backendEnd)
			if err != nil {
				return
			}
			if msgType == pgMsgTerminate {
				return
			}
			if msgType == pgMsgQuery {
				q := string(payload[:len(payload)-1])
				if q == "DISCARD ALL" {
					writePGMessage(backendEnd, 'C', append([]byte("DISCARD"), 0))
				} else {
					writePGMessage(backendEnd, 'C', append([]byte("SELECT 1"), 0))
				}
				writePGMessage(backendEnd, pgMsgReadyForQuery, []byte{'I'})
			}
		}
	}()

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayPGTransactionMode(context.Background(), clientConn, tp, "bench_pg", nil)
	}()

	drainSyntheticAuth(t, clientEnd)

	for i := 0; i < 5; i++ {
		writePGMessage(clientEnd, pgMsgQuery, append([]byte("SELECT 1"), 0))
		for {
			msgType, payload, err := readPGMessage(clientEnd)
			if err != nil {
				t.Fatalf("iter %d client read: %v", i, err)
			}
			if msgType == pgMsgReadyForQuery && len(payload) > 0 && payload[0] == 'I' {
				break
			}
		}
	}

	writePGMessage(clientEnd, pgMsgTerminate, nil)
	select {
	case err := <-relayDone:
		if err != nil {
			t.Errorf("relay error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("relay did not exit")
	}
	// Close backendEnd to unblock the mock backend goroutine
	backendEnd.Close()
	<-backendDone
}
