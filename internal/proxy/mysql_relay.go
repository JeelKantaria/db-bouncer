package proxy

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/metrics"
	"github.com/dbbouncer/dbbouncer/internal/pool"
)

// MySQL server status flags (from Protocol::OK_Packet)
const (
	mysqlStatusInTrans    = uint16(0x0001) // SERVER_STATUS_IN_TRANS
	mysqlStatusAutocommit = uint16(0x0002) // SERVER_STATUS_AUTOCOMMIT
)

// mysqlSessionPinReasons are command types that require pinning the session.
// COM_STMT_PREPARE (0x16): named prepared statements can't be replayed
// COM_SET_OPTION  (0x1b): session variable changes break stateless reuse
const (
	mysqlComStmtClose  byte = 0x19
	mysqlComSetOption  byte = 0x1b
	mysqlComCreateDB   byte = 0x05
	mysqlComDropDB     byte = 0x06
	mysqlComFieldList  byte = 0x04
	mysqlComRefresh    byte = 0x07
	mysqlComProcessKill byte = 0x0c
)

// relayMySQLTransactionMode implements transaction-level connection multiplexing
// for MySQL. The pool must contain pre-authenticated, ready-to-query backend
// connections (produced by pool.authenticateMySQL during dial).
//
// Flow:
//  1. Send synthetic OK to client (auth already done by pool)
//  2. Enter message loop: forward client commands to backend
//  3. After each command, read backend responses until an OK/ERR/EOF
//     with SERVER_STATUS_IN_TRANS == 0 (transaction boundary)
//  4. At a transaction boundary: reset backend via RESET CONNECTION, return to pool
//  5. On COM_QUIT: return backend cleanly, close client
func relayMySQLTransactionMode(
	ctx context.Context,
	clientConn net.Conn,
	tp *pool.TenantPool,
	tenantID string,
	m *metrics.Collector,
) error {
	// Acquire first backend connection
	acquireStart := time.Now()
	pc, err := tp.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring backend connection: %w", err)
	}
	if m != nil {
		m.AcquireDuration(tenantID, "mysql", time.Since(acquireStart))
	}
	backend := pc.Conn()

	// Send synthetic OK to client (sequence 2: after synthetic handshake seq=0 and client response seq=1)
	if err := sendMySQLOK(clientConn, 2); err != nil {
		pc.Close()
		return fmt.Errorf("sending synthetic OK: %w", err)
	}

	var (
		pinned    bool
		pinReason string
		txnStart  time.Time
		seqNum    byte // tracks client-side sequence numbers
	)

	// resetAndReturnMySQL sends RESET CONNECTION to the backend, reads the result,
	// and either returns the connection to the pool or closes it.
	resetAndReturn := func() {
		if err := sendMySQLResetConnection(backend); err != nil {
			slog.Warn("mysql reset connection send failed", "tenant", tenantID, "err", err)
			pc.Close()
			if m != nil {
				m.BackendReset(tenantID, false)
			}
			return
		}
		// Read the OK/ERR response to RESET CONNECTION
		resp, _, err := readMySQLPacket(backend)
		if err != nil || (len(resp) > 0 && resp[0] == 0xff) {
			slog.Warn("mysql reset connection failed", "tenant", tenantID)
			pc.Close()
			if m != nil {
				m.BackendReset(tenantID, false)
			}
			return
		}
		if m != nil {
			m.BackendReset(tenantID, true)
		}
		pc.Return()
	}

	for {
		// Read command from client
		cmdPkt, seq, err := readMySQLPacket(clientConn)
		if err != nil {
			// Client disconnected
			if backend != nil {
				// Send ROLLBACK if mid-transaction, then reset
				if !txnStart.IsZero() {
					_ = sendMySQLQuery(backend, "ROLLBACK")
					drainMySQLUntilOK(backend)
					if m != nil {
						m.DirtyDisconnect(tenantID)
					}
				}
				resetAndReturn()
			}
			return nil
		}
		seqNum = seq

		if len(cmdPkt) == 0 {
			continue
		}

		cmdType := cmdPkt[0]

		// COM_QUIT — clean shutdown
		if cmdType == mysqlComQuit {
			if backend != nil {
				resetAndReturn()
			}
			return nil
		}

		// Detect session-pinning commands
		if !pinned {
			switch cmdType {
			case mysqlComStmtPrepare:
				pinned = true
				pinReason = "prepared_statement"
			case mysqlComSetOption:
				pinned = true
				pinReason = "set_option"
			default:
				// Check for LOCK TABLES / GET_LOCK in query text
				if cmdType == mysqlComQuery && len(cmdPkt) > 1 {
					q := strings.ToUpper(strings.TrimSpace(string(cmdPkt[1:])))
					if strings.HasPrefix(q, "LOCK ") ||
						strings.Contains(q, "GET_LOCK(") ||
						strings.HasPrefix(q, "START TRANSACTION") {
						pinned = true
						pinReason = "lock_or_explicit_txn"
					}
				}
			}
			if pinned && m != nil {
				m.SessionPinned(tenantID, pinReason)
				slog.Debug("mysql session pinned", "tenant", tenantID, "reason", pinReason)
			}
		}

		// If backend was released at previous transaction boundary, re-acquire
		if backend == nil {
			acquireStart = time.Now()
			pc, err = tp.Acquire(ctx)
			if err != nil {
				sendMySQLErrorPkt(clientConn, 1040, "08004", "Too many connections", seqNum+1)
				return fmt.Errorf("re-acquiring backend: %w", err)
			}
			if m != nil {
				m.AcquireDuration(tenantID, "mysql", time.Since(acquireStart))
			}
			backend = pc.Conn()
		}

		// Track transaction start for duration metric
		if txnStart.IsZero() {
			txnStart = time.Now()
		}

		// Forward command to backend (same sequence number)
		if err := writeMySQLPacket(backend, cmdPkt, seqNum); err != nil {
			pc.Close()
			return fmt.Errorf("forwarding command to backend: %w", err)
		}

		// Read all backend response packets until a terminal packet
		// (OK, ERR, or EOF that carries status flags indicating txn boundary)
		atBoundary, err := drainMySQLResponse(clientConn, backend, cmdType)
		if err != nil {
			pc.Close()
			return fmt.Errorf("relaying backend response: %w", err)
		}

		// At transaction boundary and not pinned: release backend to pool
		if atBoundary && !pinned {
			txnDur := time.Since(txnStart)
			txnStart = time.Time{}
			if m != nil {
				m.TransactionCompleted(tenantID, "mysql", txnDur)
			}
			resetAndReturn()
			backend = nil
			pc = nil
		}
	}
}

// drainMySQLResponse reads all response packets from backend and forwards them
// to the client. It returns true when it detects a transaction boundary
// (OK or EOF packet with SERVER_STATUS_IN_TRANS == 0).
//
// MySQL response patterns:
//   - Simple query: OK_Packet or ERR_Packet (terminal)
//   - Result set: column_count + column defs + EOF + rows + EOF (or OK in deprecate_eof mode)
//   - Multi-result set: multiple result sets separated by EOF with SERVER_MORE_RESULTS_EXISTS
func drainMySQLResponse(client, backend net.Conn, cmdType byte) (atBoundary bool, err error) {
	for {
		pkt, seq, err := readMySQLPacket(backend)
		if err != nil {
			return false, err
		}
		if err := writeMySQLPacket(client, pkt, seq); err != nil {
			return false, err
		}
		if len(pkt) == 0 {
			continue
		}
		first := pkt[0]

		// ERR_Packet — always terminal, always at boundary (auto-rollback)
		if first == 0xff {
			return true, nil
		}

		// OK_Packet (0x00) or EOF_Packet (0xfe with len < 9)
		if first == 0x00 || (first == 0xfe && len(pkt) < 9) {
			status := mysqlPacketStatusFlags(pkt, first)
			// SERVER_MORE_RESULTS_EXISTS (0x0008) — more result sets follow
			if status&0x0008 != 0 {
				continue
			}
			// Transaction boundary when IN_TRANS flag is clear
			atBoundary := status&mysqlStatusInTrans == 0
			return atBoundary, nil
		}

		// For result sets: read until we get the final EOF/OK
		// The first non-error, non-OK packet is the column_count (length-encoded int).
		// We need to read: column defs + EOF + rows + EOF
		// We already forwarded the column_count packet above.
		// Continue reading until we hit a terminal packet.
		// (fall through to next iteration)
	}
}

// mysqlPacketStatusFlags extracts the server status flags from an OK or EOF packet.
func mysqlPacketStatusFlags(pkt []byte, first byte) uint16 {
	if first == 0x00 && len(pkt) >= 5 {
		// OK_Packet: 0x00 + affected_rows(lenenc) + last_insert_id(lenenc) + status_flags(2)
		// Skip the two length-encoded integers to get to status flags
		pos := 1
		pos = skipLenEnc(pkt, pos)
		pos = skipLenEnc(pkt, pos)
		if pos+2 <= len(pkt) {
			return binary.LittleEndian.Uint16(pkt[pos : pos+2])
		}
	}
	if first == 0xfe && len(pkt) >= 5 {
		// EOF_Packet: 0xfe + warnings(2) + status_flags(2)
		if len(pkt) >= 5 {
			return binary.LittleEndian.Uint16(pkt[3:5])
		}
	}
	return 0
}

// skipLenEnc advances pos past a length-encoded integer in pkt.
func skipLenEnc(pkt []byte, pos int) int {
	if pos >= len(pkt) {
		return pos
	}
	b := pkt[pos]
	switch {
	case b < 0xfb:
		return pos + 1
	case b == 0xfc:
		return pos + 3
	case b == 0xfd:
		return pos + 4
	case b == 0xfe:
		return pos + 9
	default:
		return pos + 1
	}
}

// drainMySQLUntilOK reads and discards packets until it sees an OK or ERR packet.
func drainMySQLUntilOK(conn net.Conn) {
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	defer conn.SetReadDeadline(time.Time{})
	for {
		pkt, _, err := readMySQLPacket(conn)
		if err != nil {
			return
		}
		if len(pkt) > 0 && (pkt[0] == 0x00 || pkt[0] == 0xff || (pkt[0] == 0xfe && len(pkt) < 9)) {
			return
		}
	}
}

// sendMySQLOK sends a minimal OK_Packet to the client.
func sendMySQLOK(conn net.Conn, seq byte) error {
	// OK: 0x00 + affected_rows=0 + last_insert_id=0 + status=0x0002(autocommit) + warnings=0
	pkt := []byte{0x00, 0x00, 0x00, 0x02, 0x00, 0x00, 0x00}
	return writeMySQLPacket(conn, pkt, seq)
}

// sendMySQLResetConnection sends a COM_RESET_CONNECTION (0x1f) command.
func sendMySQLResetConnection(conn net.Conn) error {
	return writeMySQLPacket(conn, []byte{0x1f}, 0)
}

// sendMySQLQuery sends a COM_QUERY command to the backend.
func sendMySQLQuery(conn net.Conn, query string) error {
	pkt := append([]byte{mysqlComQuery}, []byte(query)...)
	return writeMySQLPacket(conn, pkt, 0)
}

// sendMySQLErrorPkt sends an ERR_Packet to the client.
func sendMySQLErrorPkt(conn net.Conn, code uint16, sqlstate, msg string, seq byte) {
	var pkt []byte
	pkt = append(pkt, 0xff)
	pkt = append(pkt, byte(code), byte(code>>8))
	pkt = append(pkt, '#')
	if len(sqlstate) > 5 {
		sqlstate = sqlstate[:5]
	}
	for len(sqlstate) < 5 {
		sqlstate += "0"
	}
	pkt = append(pkt, []byte(sqlstate)...)
	pkt = append(pkt, []byte(msg)...)
	_ = writeMySQLPacket(conn, pkt, seq)
}
