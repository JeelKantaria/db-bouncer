package proxy

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/pool"
)

// newMySQLBenchPool creates a TenantPool with one pre-authenticated MySQL
// connection injected for benchmarking the relay.
func newMySQLBenchPool(b *testing.B, backendConn net.Conn) *pool.TenantPool {
	b.Helper()
	tc := config.TenantConfig{
		DBType:   "mysql",
		Host:     "127.0.0.1",
		Port:     3306,
		DBName:   "bench",
		Username: "user",
		Password: "pass",
	}
	defaults := config.PoolDefaults{
		MinConnections: 0,
		MaxConnections: 1,
		AcquireTimeout: 30 * time.Second,
		PoolMode:       "transaction",
	}
	tp := pool.NewTenantPool("bench_mysql", tc, defaults)
	pc := pool.NewPooledConn(backendConn, "bench_mysql", "mysql", tp)
	pc.SetAuthenticated(nil, 0, 0)
	tp.InjectTestConn(pc)
	return tp
}

// BenchmarkMySQLRelayTransactionMode measures throughput of
// relayMySQLTransactionMode for single-query transactions using net.Pipe.
//
// The mock backend runs in a goroutine to avoid net.Pipe deadlocks caused by
// the relay writing RESET CONNECTION synchronously inside resetAndReturn.
func BenchmarkMySQLRelayTransactionMode(b *testing.B) {
	clientA, clientB := net.Pipe()
	backendA, backendB := net.Pipe()

	tp := newMySQLBenchPool(b, backendA)
	okAutocommit := makeMySQLOK(mysqlStatusAutocommit)

	backendDone := make(chan struct{})
	go func() {
		defer close(backendDone)
		for {
			pkt, _, err := readMySQLPacket(backendB)
			if err != nil || len(pkt) == 0 {
				return
			}
			switch pkt[0] {
			case mysqlComQuery:
				writeMySQLPacket(backendB, okAutocommit, 1)
				resetPkt, _, err := readMySQLPacket(backendB)
				if err != nil || len(resetPkt) == 0 {
					return
				}
				writeMySQLPacket(backendB, okAutocommit, 1)
			case mysqlComQuit:
				return
			}
		}
	}()

	relayDone := make(chan error, 1)
	go func() {
		relayDone <- relayMySQLTransactionMode(context.Background(), clientB, tp, "bench_mysql", nil)
	}()

	b.Cleanup(func() {
		writeMySQLPacket(clientA, []byte{mysqlComQuit}, 0)
		<-relayDone
		tp.Close()
		backendB.Close()
		<-backendDone
		clientA.Close()
	})

	// Drain synthetic OK
	mysqlDrainPacket(clientA)
	query := buildMySQLQuery("SELECT 1")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := writeMySQLPacket(clientA, query, 0); err != nil {
			b.Fatal(err)
		}
		mysqlDrainPacket(clientA)
	}
}

// Note: session-mode relay (blind io.Copy) benchmarks are omitted because
// net.Pipe's synchronous/unbuffered semantics are incompatible with Go's
// benchmark calibration passes. The transaction-mode relay benchmark above
// covers the meaningful protocol overhead comparison.

// BenchmarkMySQLStatusFlagsParsing measures the cost of parsing OK packet
// status flags (called on every response in transaction mode).
func BenchmarkMySQLStatusFlagsParsing(b *testing.B) {
	pkt := makeMySQLOK(mysqlStatusAutocommit)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = mysqlPacketStatusFlags(pkt, 0x00)
	}
}

// --- MySQL wire helpers ---

// buildMySQLQuery builds a COM_QUERY payload.
func buildMySQLQuery(query string) []byte {
	return append([]byte{mysqlComQuery}, []byte(query)...)
}

// mysqlDrainPacket reads and discards exactly one MySQL packet.
func mysqlDrainPacket(conn net.Conn) {
	hdr := make([]byte, 4)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return
	}
	length := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	if length > 0 {
		io.CopyN(io.Discard, conn, int64(length))
	}
}
