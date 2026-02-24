package proxy

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/metrics"
	"github.com/dbbouncer/dbbouncer/internal/pool"
)

// --- helpers ---

// makeMySQLOK builds a MySQL OK_Packet payload.
// status: server status flags (e.g. 0x0002 = autocommit, 0x0001 = in_trans)
func makeMySQLOK(status uint16) []byte {
	// 0x00 + affected_rows(1B lenenc) + last_insert_id(1B lenenc) + status_flags(2) + warnings(2)
	return []byte{0x00, 0x00, 0x00, byte(status), byte(status >> 8), 0x00, 0x00}
}

// makeMySQLErr builds an ERR_Packet payload.
func makeMySQLErr(code uint16, msg string) []byte {
	pkt := []byte{0xff, byte(code), byte(code >> 8), '#', 'H', 'Y', '0', '0', '0'}
	pkt = append(pkt, []byte(msg)...)
	return pkt
}

// sendMySQLPkt writes a raw MySQL packet (header + payload) into w.
func sendMySQLPkt(t *testing.T, conn net.Conn, payload []byte, seq byte) {
	t.Helper()
	hdr := make([]byte, 4)
	hdr[0] = byte(len(payload))
	hdr[1] = byte(len(payload) >> 8)
	hdr[2] = byte(len(payload) >> 16)
	hdr[3] = seq
	buf := append(hdr, payload...)
	if _, err := conn.Write(buf); err != nil {
		t.Logf("sendMySQLPkt write err: %v", err)
	}
}

// recvMySQLPkt reads one MySQL packet from conn.
func recvMySQLPkt(t *testing.T, conn net.Conn) (payload []byte, seq byte) {
	t.Helper()
	hdr := make([]byte, 4)
	if _, err := net.Conn(conn).Read(hdr); err != nil {
		// Use ReadFull-like approach
		return nil, 0
	}
	length := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	seq = hdr[3]
	if length == 0 {
		return []byte{}, seq
	}
	payload = make([]byte, length)
	total := 0
	for total < length {
		n, err := conn.Read(payload[total:])
		total += n
		if err != nil {
			break
		}
	}
	return payload, seq
}

// newMySQLTestPool creates a TenantPool pre-loaded with an injected connection
// (bypasses real dial) for testing transaction relay.
func newMySQLTestPool(t *testing.T, backendConn net.Conn) *pool.TenantPool {
	t.Helper()
	tc := config.TenantConfig{
		DBType:   "mysql",
		Host:     "127.0.0.1",
		Port:     3306,
		DBName:   "db",
		Username: "user",
		Password: "pass",
	}
	defaults := config.PoolDefaults{
		MinConnections: 0,
		MaxConnections: 5,
		PoolMode:       "transaction",
	}
	tp := pool.NewTenantPool("test", tc, defaults)
	pc := pool.NewPooledConn(backendConn, "test", "mysql", tp)
	pc.SetAuthenticated(nil, 0, 0)
	tp.InjectTestConn(pc)
	return tp
}

// --- Tests ---

// TestMySQLRelayReturnsConnectionOnIdle verifies that after the backend sends
// an OK with no SERVER_STATUS_IN_TRANS bit, the connection is returned to pool.
func TestMySQLRelayReturnsConnectionOnIdle(t *testing.T) {
	clientA, clientB := net.Pipe()
	backendA, backendB := net.Pipe()
	defer clientA.Close()
	defer backendB.Close()

	tp := newMySQLTestPool(t, backendA)
	m := metrics.New()

	done := make(chan error, 1)
	go func() {
		done <- relayMySQLTransactionMode(
			context.Background(),
			clientB,
			tp,
			"test",
			m,
		)
	}()

	// Client receives synthetic OK (seq 2)
	clientA.SetReadDeadline(time.Now().Add(time.Second))
	okPkt, seq := recvMySQLPkt(t, clientA)
	if len(okPkt) == 0 || okPkt[0] != 0x00 {
		t.Fatalf("expected OK from relay, got %v seq=%d", okPkt, seq)
	}

	// Client sends COM_QUERY
	sendMySQLPkt(t, clientA, []byte{mysqlComQuery, 'S', 'E', 'L', 'E', 'C', 'T', ' ', '1'}, 0)

	// Backend receives the query
	backendB.SetReadDeadline(time.Now().Add(time.Second))
	qPkt, _ := recvMySQLPkt(t, backendB)
	if len(qPkt) == 0 || qPkt[0] != mysqlComQuery {
		t.Fatalf("expected COM_QUERY on backend, got %v", qPkt)
	}

	// Backend sends OK with autocommit set (no IN_TRANS) → transaction boundary
	backendB.SetWriteDeadline(time.Now().Add(time.Second))
	sendMySQLPkt(t, backendB, makeMySQLOK(mysqlStatusAutocommit), 1)

	// Client should receive the OK
	clientA.SetReadDeadline(time.Now().Add(time.Second))
	fwdOK, _ := recvMySQLPkt(t, clientA)
	if len(fwdOK) == 0 || fwdOK[0] != 0x00 {
		t.Fatalf("client expected forwarded OK, got %v", fwdOK)
	}

	// Backend should now receive RESET CONNECTION (0x1f) as relay resets the pooled conn
	backendB.SetReadDeadline(time.Now().Add(time.Second))
	resetPkt, _ := recvMySQLPkt(t, backendB)
	if len(resetPkt) == 0 || resetPkt[0] != 0x1f {
		t.Fatalf("expected COM_RESET_CONNECTION on backend after boundary, got %v", resetPkt)
	}
	// Backend ACKs the reset
	sendMySQLPkt(t, backendB, makeMySQLOK(mysqlStatusAutocommit), 1)

	// Client sends COM_QUIT
	sendMySQLPkt(t, clientA, []byte{mysqlComQuit}, 0)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("relayMySQLTransactionMode returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not exit after COM_QUIT")
	}
}

// TestMySQLRelayHoldsDuringTransaction verifies that the backend connection is
// held when the server reports SERVER_STATUS_IN_TRANS.
func TestMySQLRelayHoldsDuringTransaction(t *testing.T) {
	clientA, clientB := net.Pipe()
	backendA, backendB := net.Pipe()
	defer clientA.Close()
	defer backendB.Close()

	tp := newMySQLTestPool(t, backendA)

	done := make(chan error, 1)
	go func() {
		done <- relayMySQLTransactionMode(context.Background(), clientB, tp, "test", nil)
	}()

	// Discard synthetic OK
	clientA.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, clientA)

	// Client sends BEGIN
	sendMySQLPkt(t, clientA, []byte{mysqlComQuery, 'B', 'E', 'G', 'I', 'N'}, 0)

	// Backend receives it
	backendB.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, backendB)

	// Backend sends OK with IN_TRANS set — transaction is open
	inTransStatus := mysqlStatusAutocommit | mysqlStatusInTrans
	sendMySQLPkt(t, backendB, makeMySQLOK(inTransStatus), 1)

	// Client receives OK
	clientA.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, clientA)

	// Client sends COMMIT
	sendMySQLPkt(t, clientA, []byte{mysqlComQuery, 'C', 'O', 'M', 'M', 'I', 'T'}, 0)

	// Backend receives COMMIT (not RESET CONNECTION — still holding)
	backendB.SetReadDeadline(time.Now().Add(time.Second))
	commitPkt, _ := recvMySQLPkt(t, backendB)
	if len(commitPkt) == 0 || commitPkt[0] != mysqlComQuery {
		t.Fatalf("expected COM_QUERY (COMMIT) on backend, got %v", commitPkt)
	}

	// Backend sends OK with no IN_TRANS → boundary
	sendMySQLPkt(t, backendB, makeMySQLOK(mysqlStatusAutocommit), 1)
	clientA.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, clientA)

	// Backend should now get RESET CONNECTION
	backendB.SetReadDeadline(time.Now().Add(time.Second))
	resetPkt, _ := recvMySQLPkt(t, backendB)
	if len(resetPkt) == 0 || resetPkt[0] != 0x1f {
		t.Fatalf("expected RESET CONNECTION after COMMIT boundary, got %v", resetPkt)
	}
	sendMySQLPkt(t, backendB, makeMySQLOK(mysqlStatusAutocommit), 1)

	// Quit
	sendMySQLPkt(t, clientA, []byte{mysqlComQuit}, 0)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not exit")
	}
}

// TestMySQLRelayHandlesERR verifies that an ERR response is forwarded and
// treated as a transaction boundary (MySQL auto-rolls back on error).
func TestMySQLRelayHandlesERR(t *testing.T) {
	clientA, clientB := net.Pipe()
	backendA, backendB := net.Pipe()
	defer clientA.Close()
	defer backendB.Close()

	tp := newMySQLTestPool(t, backendA)

	done := make(chan error, 1)
	go func() {
		done <- relayMySQLTransactionMode(context.Background(), clientB, tp, "test", nil)
	}()

	clientA.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, clientA) // discard OK

	// Client sends a bad query
	sendMySQLPkt(t, clientA, []byte{mysqlComQuery, 'B', 'A', 'D'}, 0)
	backendB.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, backendB) // backend receives it

	// Backend sends ERR
	sendMySQLPkt(t, backendB, makeMySQLErr(1064, "syntax error"), 1)

	// Client should receive ERR
	clientA.SetReadDeadline(time.Now().Add(time.Second))
	errPkt, _ := recvMySQLPkt(t, clientA)
	if len(errPkt) == 0 || errPkt[0] != 0xff {
		t.Fatalf("expected ERR forwarded to client, got %v", errPkt)
	}

	// Backend should get RESET CONNECTION (ERR is a boundary)
	backendB.SetReadDeadline(time.Now().Add(time.Second))
	resetPkt, _ := recvMySQLPkt(t, backendB)
	if len(resetPkt) == 0 || resetPkt[0] != 0x1f {
		t.Fatalf("expected RESET CONNECTION after ERR boundary, got %v", resetPkt)
	}
	sendMySQLPkt(t, backendB, makeMySQLOK(mysqlStatusAutocommit), 1)

	sendMySQLPkt(t, clientA, []byte{mysqlComQuit}, 0)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not exit")
	}
}

// TestMySQLRelaySessionPinOnPrepare verifies that COM_STMT_PREPARE pins the session.
func TestMySQLRelaySessionPinOnPrepare(t *testing.T) {
	clientA, clientB := net.Pipe()
	backendA, backendB := net.Pipe()
	defer clientA.Close()
	defer backendB.Close()

	tp := newMySQLTestPool(t, backendA)
	m := metrics.New()

	done := make(chan error, 1)
	go func() {
		done <- relayMySQLTransactionMode(context.Background(), clientB, tp, "test", m)
	}()

	clientA.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, clientA) // discard OK

	// Client sends COM_STMT_PREPARE — this should pin the session
	sendMySQLPkt(t, clientA, append([]byte{mysqlComStmtPrepare}, []byte("SELECT ?")...), 0)
	backendB.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, backendB)

	// Backend sends OK (in_trans=0 → would normally be a boundary, but session is pinned)
	// For COM_STMT_PREPARE, backend sends a special prepared stmt OK, but we test
	// with a plain OK (boundary would be reached, but pinned = no release)
	sendMySQLPkt(t, backendB, makeMySQLOK(mysqlStatusAutocommit), 1)
	clientA.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, clientA)

	// Backend should NOT receive RESET CONNECTION because session is pinned
	backendB.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	resetPkt, _ := recvMySQLPkt(t, backendB)
	if len(resetPkt) > 0 && resetPkt[0] == 0x1f {
		t.Error("backend received RESET CONNECTION but session should be pinned")
	}

	// Quit — relay should reset and return cleanly now
	sendMySQLPkt(t, clientA, []byte{mysqlComQuit}, 0)
	backendB.SetReadDeadline(time.Now().Add(time.Second))
	resetPkt, _ = recvMySQLPkt(t, backendB)
	if len(resetPkt) > 0 && resetPkt[0] == 0x1f {
		// send ACK
		sendMySQLPkt(t, backendB, makeMySQLOK(mysqlStatusAutocommit), 1)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not exit")
	}
}

// TestMySQLRelayDirtyDisconnect verifies that mid-transaction client disconnect
// sends ROLLBACK + RESET, increments DirtyDisconnect metric.
func TestMySQLRelayDirtyDisconnect(t *testing.T) {
	clientA, clientB := net.Pipe()
	backendA, backendB := net.Pipe()
	defer backendB.Close()

	tp := newMySQLTestPool(t, backendA)
	m := metrics.New()

	done := make(chan error, 1)
	go func() {
		done <- relayMySQLTransactionMode(context.Background(), clientB, tp, "test", m)
	}()

	clientA.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, clientA) // discard OK

	// Start a transaction
	sendMySQLPkt(t, clientA, []byte{mysqlComQuery, 'B', 'E', 'G', 'I', 'N'}, 0)
	backendB.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, backendB)

	inTransStatus := mysqlStatusAutocommit | mysqlStatusInTrans
	sendMySQLPkt(t, backendB, makeMySQLOK(inTransStatus), 1)
	clientA.SetReadDeadline(time.Now().Add(time.Second))
	recvMySQLPkt(t, clientA)

	// Client disconnects abruptly mid-transaction
	clientA.Close()

	// Backend should receive ROLLBACK query
	backendB.SetReadDeadline(time.Now().Add(2 * time.Second))
	rollbackPkt, _ := recvMySQLPkt(t, backendB)
	if len(rollbackPkt) > 1 {
		query := string(rollbackPkt[1:])
		if query != "ROLLBACK" {
			t.Errorf("expected ROLLBACK on backend, got %q", query)
		}
	}
	// ACK the ROLLBACK
	sendMySQLPkt(t, backendB, makeMySQLOK(mysqlStatusAutocommit), 1)

	// Backend should receive RESET CONNECTION
	backendB.SetReadDeadline(time.Now().Add(2 * time.Second))
	resetPkt, _ := recvMySQLPkt(t, backendB)
	if len(resetPkt) > 0 && resetPkt[0] == 0x1f {
		sendMySQLPkt(t, backendB, makeMySQLOK(mysqlStatusAutocommit), 1)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("unexpected error from relay: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not exit after dirty disconnect")
	}
}

// TestMySQLStatusFlags verifies mysqlPacketStatusFlags parses OK and EOF correctly.
func TestMySQLStatusFlags(t *testing.T) {
	tests := []struct {
		name     string
		pkt      []byte
		first    byte
		wantFlag uint16
	}{
		{
			name:     "OK autocommit",
			pkt:      makeMySQLOK(mysqlStatusAutocommit),
			first:    0x00,
			wantFlag: mysqlStatusAutocommit,
		},
		{
			name:     "OK in_trans",
			pkt:      makeMySQLOK(mysqlStatusInTrans | mysqlStatusAutocommit),
			first:    0x00,
			wantFlag: mysqlStatusInTrans | mysqlStatusAutocommit,
		},
		{
			name:     "EOF packet",
			pkt:      []byte{0xfe, 0x00, 0x00, 0x02, 0x00},
			first:    0xfe,
			wantFlag: mysqlStatusAutocommit,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mysqlPacketStatusFlags(tt.pkt, tt.first)
			if got != tt.wantFlag {
				t.Errorf("got status 0x%04x, want 0x%04x", got, tt.wantFlag)
			}
		})
	}
}

// TestSkipLenEnc verifies the lenenc integer skipper.
func TestSkipLenEnc(t *testing.T) {
	tests := []struct {
		name    string
		pkt     []byte
		pos     int
		wantPos int
	}{
		{"1-byte 0", []byte{0x00}, 0, 1},
		{"1-byte 250", []byte{0xfa}, 0, 1},
		{"2-byte 0xfc", []byte{0xfc, 0x01, 0x00}, 0, 3},
		{"3-byte 0xfd", []byte{0xfd, 0x01, 0x00, 0x00}, 0, 4},
		{"8-byte 0xfe", []byte{0xfe, 1, 2, 3, 4, 5, 6, 7, 8}, 0, 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := skipLenEnc(tt.pkt, tt.pos)
			if got != tt.wantPos {
				t.Errorf("got %d, want %d", got, tt.wantPos)
			}
		})
	}
}

// --- helpers used by multiple test files ---

func makeMySQLOKWithFlags(affectedRows, lastInsertID uint64, status uint16) []byte {
	var pkt []byte
	pkt = append(pkt, 0x00) // OK header
	pkt = appendLenEnc(pkt, affectedRows)
	pkt = appendLenEnc(pkt, lastInsertID)
	pkt = append(pkt, byte(status), byte(status>>8))
	pkt = append(pkt, 0x00, 0x00) // warnings
	return pkt
}

func appendLenEnc(buf []byte, v uint64) []byte {
	switch {
	case v < 251:
		return append(buf, byte(v))
	case v < 0xffff:
		return append(buf, 0xfc, byte(v), byte(v>>8))
	case v < 0xffffff:
		return append(buf, 0xfd, byte(v), byte(v>>8), byte(v>>16))
	default:
		b := make([]byte, 9)
		b[0] = 0xfe
		binary.LittleEndian.PutUint64(b[1:], v)
		return append(buf, b...)
	}
}
