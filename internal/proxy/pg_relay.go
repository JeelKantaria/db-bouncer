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

// PG message types used in transaction-level relay.
const (
	pgMsgParse byte = 'P' // Parse (extended query protocol)
)

// relayPGTransactionMode handles a client connection using transaction-level
// pooling. Backend connections are acquired from the pool pre-authenticated
// and returned at transaction boundaries (when ReadyForQuery status is 'I').
func relayPGTransactionMode(ctx context.Context, client net.Conn,
	tenantPool *pool.TenantPool, tenantID string,
	m *metrics.Collector) error {

	// Acquire initial backend connection
	acquireStart := time.Now()
	pc, err := tenantPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquiring initial backend: %w", err)
	}
	if m != nil {
		m.AcquireDuration(tenantID, "postgres", time.Since(acquireStart))
	}

	// Send synthetic auth-ok to client
	if err := sendSyntheticAuthOK(client, pc); err != nil {
		pc.Close()
		return fmt.Errorf("sending synthetic auth: %w", err)
	}

	// Return the initial connection to pool — client starts in idle state
	tenantPool.Return(pc)
	pc = nil
	var backend net.Conn

	pinned := false           // session pinning flag
	var txnStart time.Time    // when the current backend was acquired

	for {
		select {
		case <-ctx.Done():
			if pc != nil {
				cleanupBackend(pc, tenantPool, tenantID, m)
			}
			return ctx.Err()
		default:
		}

		// Read message from client
		msgType, payload, err := readPGMessage(client)
		if err != nil {
			// Client disconnected
			if pc != nil {
				// Mid-transaction disconnect — rollback and cleanup
				cleanupBackend(pc, tenantPool, tenantID, m)
			}
			return nil // client disconnect is not an error
		}

		// Handle Terminate ('X') — clean shutdown
		if msgType == pgMsgTerminate {
			if pc != nil {
				resetAndReturn(pc, tenantPool, tenantID, m)
			}
			return nil
		}

		// If no backend held, re-acquire one
		if pc == nil {
			acquireStart = time.Now()
			pc, err = tenantPool.Acquire(ctx)
			if err != nil {
				sendPGErrorToConn(client, "FATAL", "08000", "cannot acquire backend connection")
				return fmt.Errorf("re-acquiring backend: %w", err)
			}
			if m != nil {
				m.AcquireDuration(tenantID, "postgres", time.Since(acquireStart))
			}
			txnStart = time.Now()
			backend = pc.Conn()
		} else {
			backend = pc.Conn()
		}

		// Detect session pinning conditions before forwarding
		if !pinned {
			pinned = detectSessionPin(msgType, payload)
			if pinned {
				reason := pinReason(msgType, payload)
				slog.Info("session pinned", "tenant", tenantID, "reason", reason)
				if m != nil {
					m.SessionPinned(tenantID, reason)
				}
			}
		}

		// Forward client message to backend
		if err := writePGMessage(backend, msgType, payload); err != nil {
			pc.Close()
			pc = nil
			return fmt.Errorf("writing to backend: %w", err)
		}

		// Read backend responses until ReadyForQuery
		for {
			rType, rPayload, err := readPGMessage(backend)
			if err != nil {
				pc.Close()
				pc = nil
				return fmt.Errorf("reading from backend: %w", err)
			}

			// Forward to client
			if err := writePGMessage(client, rType, rPayload); err != nil {
				// Client gone, cleanup backend
				cleanupBackend(pc, tenantPool, tenantID, m)
				pc = nil
				return nil
			}

			if rType == pgMsgReadyForQuery {
				if len(rPayload) >= 1 {
					txnStatus := rPayload[0]
					if txnStatus == 'I' && !pinned {
						// Transaction boundary — release backend to pool
						if m != nil && !txnStart.IsZero() {
							m.TransactionCompleted(tenantID, "postgres", time.Since(txnStart))
						}
						resetAndReturn(pc, tenantPool, tenantID, m)
						pc = nil
						backend = nil
						txnStart = time.Time{}
					}
					// 'T' (in transaction) or 'E' (error) — keep holding
				}
				break
			}
		}
	}
}

// sendSyntheticAuthOK sends a synthetic authentication-ok sequence to the client:
// AuthenticationOk + cached ParameterStatus messages + BackendKeyData + ReadyForQuery('I')
func sendSyntheticAuthOK(client net.Conn, pc *pool.PooledConn) error {
	// AuthenticationOk: R message with type=0
	authOK := make([]byte, 4)
	binary.BigEndian.PutUint32(authOK, 0)
	if err := writePGMessage(client, pgMsgAuthentication, authOK); err != nil {
		return err
	}

	// ParameterStatus messages
	for key, val := range pc.ServerParams() {
		var payload []byte
		payload = append(payload, key...)
		payload = append(payload, 0)
		payload = append(payload, val...)
		payload = append(payload, 0)
		if err := writePGMessage(client, pgMsgParameterStatus, payload); err != nil {
			return err
		}
	}

	// BackendKeyData
	bkd := make([]byte, 8)
	binary.BigEndian.PutUint32(bkd[:4], pc.BackendPID())
	binary.BigEndian.PutUint32(bkd[4:], pc.BackendKey())
	if err := writePGMessage(client, pgMsgBackendKeyData, bkd); err != nil {
		return err
	}

	// ReadyForQuery('I')
	if err := writePGMessage(client, pgMsgReadyForQuery, []byte{'I'}); err != nil {
		return err
	}

	return nil
}

// resetAndReturn sends DISCARD ALL to the backend before returning it to the pool.
// If the reset fails, the connection is closed instead of returned.
func resetAndReturn(pc *pool.PooledConn, tenantPool *pool.TenantPool, tenantID string, m *metrics.Collector) {
	conn := pc.Conn()

	// Send "DISCARD ALL;\0" as a simple query
	query := "DISCARD ALL"
	payload := append([]byte(query), 0)
	if err := writePGMessage(conn, pgMsgQuery, payload); err != nil {
		slog.Debug("reset failed, closing connection", "err", err)
		if m != nil {
			m.BackendReset(tenantID, false)
		}
		pc.Close()
		return
	}

	// Read responses until ReadyForQuery('I')
	for {
		rType, rPayload, err := readPGMessage(conn)
		if err != nil {
			slog.Debug("reset read failed, closing connection", "err", err)
			if m != nil {
				m.BackendReset(tenantID, false)
			}
			pc.Close()
			return
		}
		if rType == pgMsgReadyForQuery {
			if len(rPayload) >= 1 && rPayload[0] == 'I' {
				if m != nil {
					m.BackendReset(tenantID, true)
				}
				pc.Return()
				return
			}
			// Unexpected state after DISCARD ALL
			slog.Debug("unexpected state after DISCARD ALL, closing", "status", string(rPayload))
			if m != nil {
				m.BackendReset(tenantID, false)
			}
			pc.Close()
			return
		}
		if rType == pgMsgErrorResponse {
			slog.Debug("DISCARD ALL returned error, closing connection")
			if m != nil {
				m.BackendReset(tenantID, false)
			}
			pc.Close()
			return
		}
		// Continue reading (CommandComplete, etc.)
	}
}

// cleanupBackend handles a dirty disconnect — sends ROLLBACK + DISCARD ALL
// before closing the connection.
func cleanupBackend(pc *pool.PooledConn, tenantPool *pool.TenantPool, tenantID string, m *metrics.Collector) {
	if m != nil {
		m.DirtyDisconnect(tenantID)
	}

	conn := pc.Conn()

	// Try to send ROLLBACK
	rollback := append([]byte("ROLLBACK"), 0)
	if err := writePGMessage(conn, pgMsgQuery, rollback); err != nil {
		pc.Close()
		return
	}

	// Drain responses until ReadyForQuery
	for {
		rType, _, err := readPGMessage(conn)
		if err != nil {
			pc.Close()
			return
		}
		if rType == pgMsgReadyForQuery {
			break
		}
	}

	// Now try DISCARD ALL and return
	resetAndReturn(pc, tenantPool, tenantID, m)
}

// detectSessionPin checks if a message requires session pinning.
func detectSessionPin(msgType byte, payload []byte) bool {
	// Parse message with named prepared statement (non-empty statement name)
	if msgType == pgMsgParse && len(payload) > 0 {
		// Parse message format: statement_name\0query\0...
		// If statement_name is non-empty, it's a named prepared statement
		if payload[0] != 0 {
			return true
		}
	}

	// LISTEN/NOTIFY detection in simple query messages
	if msgType == pgMsgQuery && len(payload) > 0 {
		// payload is query\0
		query := strings.ToUpper(strings.TrimSpace(string(payload[:len(payload)-1])))
		if strings.HasPrefix(query, "LISTEN") || strings.HasPrefix(query, "NOTIFY") {
			return true
		}
	}

	return false
}

// pinReason returns a human-readable reason for session pinning.
func pinReason(msgType byte, payload []byte) string {
	if msgType == pgMsgParse {
		return "named prepared statement"
	}
	if msgType == pgMsgQuery {
		query := strings.TrimSpace(string(payload[:len(payload)-1]))
		words := strings.Fields(query)
		if len(words) > 0 {
			return strings.ToLower(words[0]) + " command"
		}
	}
	return "unknown"
}

// sendPGErrorToConn sends a PostgreSQL ErrorResponse to a connection.
func sendPGErrorToConn(conn net.Conn, severity, code, message string) {
	var buf []byte
	buf = append(buf, 'S')
	buf = append(buf, severity...)
	buf = append(buf, 0)
	buf = append(buf, 'C')
	buf = append(buf, code...)
	buf = append(buf, 0)
	buf = append(buf, 'M')
	buf = append(buf, message...)
	buf = append(buf, 0)
	buf = append(buf, 0) // terminator

	writePGMessage(conn, pgMsgErrorResponse, buf)
}
