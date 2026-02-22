package proxy

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/health"
	"github.com/dbbouncer/dbbouncer/internal/metrics"
	"github.com/dbbouncer/dbbouncer/internal/pool"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

const (
	// PostgreSQL protocol version 3.0
	pgProtoVersionMajor = 3
	pgProtoVersionMinor = 0
	pgProtoVersion      = pgProtoVersionMajor<<16 | pgProtoVersionMinor

	// SSL request magic number
	pgSSLRequestCode = 80877103

	// Message types
	pgMsgAuthentication  byte = 'R'
	pgMsgErrorResponse   byte = 'E'
	pgMsgReadyForQuery   byte = 'Z'
	pgMsgTerminate       byte = 'X'
	pgMsgQuery           byte = 'Q'
	pgMsgParameterStatus byte = 'S'
	pgMsgBackendKeyData  byte = 'K'
)

// PostgresHandler handles PostgreSQL wire protocol connections.
type PostgresHandler struct {
	router      *router.Router
	poolMgr     *pool.Manager
	healthCheck *health.Checker
	metrics     *metrics.Collector
	tlsConfig   *tls.Config
}

// Handle processes a PostgreSQL client connection.
func (h *PostgresHandler) Handle(ctx context.Context, clientConn net.Conn) error {
	// Read the startup message (may upgrade to TLS)
	tenantID, startupMsg, clientConn, err := h.readStartupMessage(clientConn)
	if err != nil {
		return fmt.Errorf("reading startup message: %w", err)
	}

	if tenantID == "" {
		h.sendPGError(clientConn, "FATAL", "08000", "no tenant_id provided in connection options")
		return fmt.Errorf("no tenant_id in startup message")
	}

	log.Printf("[postgres] connection from tenant %s", tenantID)

	// Resolve tenant config
	tc, err := h.router.Resolve(tenantID)
	if err != nil {
		h.sendPGError(clientConn, "FATAL", "08000", fmt.Sprintf("unknown tenant: %s", tenantID))
		return err
	}

	// Check if tenant is paused
	if h.router.IsPaused(tenantID) {
		h.sendPGError(clientConn, "FATAL", "08000", fmt.Sprintf("tenant %s is paused", tenantID))
		return fmt.Errorf("tenant %s is paused", tenantID)
	}

	// Check health
	if h.healthCheck != nil && !h.healthCheck.IsHealthy(tenantID) {
		h.sendPGError(clientConn, "FATAL", "08000", fmt.Sprintf("tenant %s database is unhealthy", tenantID))
		return fmt.Errorf("tenant %s is unhealthy", tenantID)
	}

	// Get a pooled connection
	tenantPool := h.poolMgr.GetOrCreate(tenantID, tc)
	pc, err := tenantPool.Acquire()
	if err != nil {
		h.sendPGError(clientConn, "FATAL", "08000", fmt.Sprintf("cannot connect to database: %s", err))
		return err
	}
	// Always close the backend connection after relay — the protocol state
	// is unknown after bidirectional copy, so it cannot be safely reused.
	defer pc.Close()

	backendConn := pc.Conn()

	if h.metrics != nil {
		h.metrics.ConnectionOpened(tenantID, "postgres")
		defer h.metrics.ConnectionClosed(tenantID, "postgres")
	}

	// Forward the startup message to the backend
	if _, err := backendConn.Write(startupMsg); err != nil {
		return fmt.Errorf("forwarding startup message: %w", err)
	}

	// Relay the authentication phase
	if err := h.relayAuth(clientConn, backendConn); err != nil {
		return fmt.Errorf("auth relay: %w", err)
	}

	// Relay queries/responses until disconnect
	start := time.Now()
	err = relay(ctx, clientConn, backendConn)
	if h.metrics != nil {
		h.metrics.QueryDuration(tenantID, "postgres", time.Since(start))
	}
	return err
}

// readStartupMessage reads the PostgreSQL startup message and extracts the tenant ID.
// Handles SSL negotiation as a loop (max 3 attempts) to prevent stack overflow.
func (h *PostgresHandler) readStartupMessage(conn net.Conn) (string, []byte, net.Conn, error) {
	const maxSSLAttempts = 3
	currentConn := conn

	for attempt := 0; attempt <= maxSSLAttempts; attempt++ {
		// Read message length (4 bytes)
		lenBuf := make([]byte, 4)
		if _, err := io.ReadFull(currentConn, lenBuf); err != nil {
			return "", nil, currentConn, fmt.Errorf("reading startup length: %w", err)
		}
		msgLen := int(binary.BigEndian.Uint32(lenBuf))

		if msgLen < 8 || msgLen > 10000 {
			return "", nil, currentConn, fmt.Errorf("invalid startup message length: %d", msgLen)
		}

		// Read rest of message
		buf := make([]byte, msgLen-4)
		if _, err := io.ReadFull(currentConn, buf); err != nil {
			return "", nil, currentConn, fmt.Errorf("reading startup body: %w", err)
		}

		// Check for SSL request
		protoVersion := binary.BigEndian.Uint32(buf[:4])
		if protoVersion == pgSSLRequestCode {
			if h.tlsConfig != nil {
				// Accept SSL — upgrade the connection
				currentConn.Write([]byte{'S'})
				tlsConn := tls.Server(currentConn, h.tlsConfig)
				if err := tlsConn.Handshake(); err != nil {
					return "", nil, currentConn, fmt.Errorf("TLS handshake failed: %w", err)
				}
				currentConn = tlsConn
			} else {
				// Deny SSL, tell client to retry without SSL
				currentConn.Write([]byte{'N'})
			}
			// Client should retry with a normal startup message
			continue
		}

		// Parse parameters (null-terminated key-value pairs after the protocol version)
		params := make(map[string]string)
		data := buf[4:] // skip protocol version
		for len(data) > 1 {
			// Read key
			keyEnd := 0
			for keyEnd < len(data) && data[keyEnd] != 0 {
				keyEnd++
			}
			if keyEnd >= len(data) {
				break
			}
			key := string(data[:keyEnd])
			data = data[keyEnd+1:]

			// Read value
			valEnd := 0
			for valEnd < len(data) && data[valEnd] != 0 {
				valEnd++
			}
			if valEnd >= len(data) {
				break
			}
			value := string(data[:valEnd])
			data = data[valEnd+1:]

			params[key] = value
		}

		// Extract tenant ID from options parameter: -c tenant_id=xxx
		tenantID := ""
		if options, ok := params["options"]; ok {
			tenantID = parseTenantFromOptions(options)
		}

		// Also check if tenant_id was sent as a direct parameter
		if tenantID == "" {
			if tid, ok := params["tenant_id"]; ok {
				tenantID = tid
			}
		}

		// Also try to extract from username format: tenant__user
		if tenantID == "" {
			if user, ok := params["user"]; ok {
				if tid, _, ok := router.ExtractTenantFromUsername(user); ok {
					tenantID = tid
				}
			}
		}

		// Reconstruct the full startup message
		fullMsg := make([]byte, msgLen)
		copy(fullMsg[:4], lenBuf)
		copy(fullMsg[4:], buf)

		return tenantID, fullMsg, currentConn, nil
	}

	return "", nil, currentConn, fmt.Errorf("too many SSL negotiation attempts")
}

// parseTenantFromOptions extracts tenant_id from PG options string.
// Format: -c tenant_id=xxx
func parseTenantFromOptions(options string) string {
	parts := strings.Fields(options)
	for i, p := range parts {
		if p == "-c" && i+1 < len(parts) {
			kv := parts[i+1]
			if strings.HasPrefix(kv, "tenant_id=") {
				return strings.TrimPrefix(kv, "tenant_id=")
			}
		}
		if strings.HasPrefix(p, "tenant_id=") {
			return strings.TrimPrefix(p, "tenant_id=")
		}
	}
	return ""
}

// relayAuth relays the authentication handshake between client and backend.
func (h *PostgresHandler) relayAuth(client, backend net.Conn) error {
	for {
		// Read message from backend
		msgType, payload, err := readPGMessage(backend)
		if err != nil {
			return fmt.Errorf("reading backend auth: %w", err)
		}

		// Forward to client
		if err := writePGMessage(client, msgType, payload); err != nil {
			return fmt.Errorf("writing to client during auth: %w", err)
		}

		switch msgType {
		case pgMsgErrorResponse:
			return fmt.Errorf("backend auth error")

		case pgMsgReadyForQuery:
			// Authentication complete, backend is ready
			return nil

		case pgMsgAuthentication:
			// Check auth type
			if len(payload) >= 4 {
				authType := binary.BigEndian.Uint32(payload[:4])
				if authType == 0 {
					// AuthenticationOk - continue to read more messages
					continue
				}
				// Backend wants auth from client
				if authType == 3 || authType == 5 {
					// Cleartext (3) or MD5 (5) - single round-trip
					cMsgType, cPayload, err := readPGMessage(client)
					if err != nil {
						return fmt.Errorf("reading client auth response: %w", err)
					}
					if err := writePGMessage(backend, cMsgType, cPayload); err != nil {
						return fmt.Errorf("forwarding client auth: %w", err)
					}
				} else if authType == 10 {
					// SASL (10) - multi-step SCRAM-SHA-256 exchange:
					// Step 1: Backend sends AuthenticationSASL (type 10) with mechanism list (already forwarded)
					// Client responds with SASLInitialResponse (password message 'p')
					cMsgType, cPayload, err := readPGMessage(client)
					if err != nil {
						return fmt.Errorf("reading SASL initial response: %w", err)
					}
					if err := writePGMessage(backend, cMsgType, cPayload); err != nil {
						return fmt.Errorf("forwarding SASL initial response: %w", err)
					}
					// Step 2: Backend sends AuthenticationSASLContinue (type 11)
					bMsgType, bPayload, err := readPGMessage(backend)
					if err != nil {
						return fmt.Errorf("reading SASL continue from backend: %w", err)
					}
					if err := writePGMessage(client, bMsgType, bPayload); err != nil {
						return fmt.Errorf("forwarding SASL continue to client: %w", err)
					}
					// Client responds with SASLResponse
					cMsgType, cPayload, err = readPGMessage(client)
					if err != nil {
						return fmt.Errorf("reading SASL response: %w", err)
					}
					if err := writePGMessage(backend, cMsgType, cPayload); err != nil {
						return fmt.Errorf("forwarding SASL response: %w", err)
					}
					// Step 3: Backend sends AuthenticationSASLFinal (type 12) then AuthenticationOk (type 0)
					// These will be read by the outer loop
				} else if authType == 11 || authType == 12 {
					// SASLContinue (11) or SASLFinal (12) received outside SASL flow
					// This can happen if messages arrive out of expected order; just continue
					continue
				}
			}

		case pgMsgParameterStatus, pgMsgBackendKeyData:
			// These are sent during startup, just forward (already done above)
			continue
		}
	}
}

// readPGMessage reads a single PostgreSQL protocol message (type byte + length + payload).
func readPGMessage(conn net.Conn) (byte, []byte, error) {
	// Read message type (1 byte)
	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, typeBuf); err != nil {
		return 0, nil, err
	}

	// Read message length (4 bytes, includes itself)
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return 0, nil, err
	}
	msgLen := int(binary.BigEndian.Uint32(lenBuf)) - 4

	if msgLen < 0 || msgLen > 1<<24 {
		return 0, nil, fmt.Errorf("invalid message length: %d", msgLen)
	}

	// Read payload
	payload := make([]byte, msgLen)
	if msgLen > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return 0, nil, err
		}
	}

	return typeBuf[0], payload, nil
}

// writePGMessage writes a PostgreSQL protocol message.
func writePGMessage(conn net.Conn, msgType byte, payload []byte) error {
	msgLen := len(payload) + 4
	buf := make([]byte, 1+4+len(payload))
	buf[0] = msgType
	binary.BigEndian.PutUint32(buf[1:5], uint32(msgLen))
	copy(buf[5:], payload)
	_, err := conn.Write(buf)
	return err
}

// sendPGError sends a PostgreSQL ErrorResponse to the client.
func (h *PostgresHandler) sendPGError(conn net.Conn, severity, code, message string) {
	// Build ErrorResponse fields
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
