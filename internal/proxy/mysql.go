package proxy

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/health"
	"github.com/dbbouncer/dbbouncer/internal/metrics"
	"github.com/dbbouncer/dbbouncer/internal/pool"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

const (
	// MySQL packet types
	mysqlComQuit        byte = 0x01
	mysqlComQuery       byte = 0x03
	mysqlComInitDB      byte = 0x02
	mysqlComPing        byte = 0x0e
	mysqlComStmtPrepare byte = 0x16

	// MySQL auth/error
	mysqlOKPacket  byte = 0x00
	mysqlErrPacket byte = 0xff
	mysqlEOFPacket byte = 0xfe
)

// Compile-time interface assertion.
var _ ConnectionHandler = (*MySQLHandler)(nil)

// MySQLHandler handles MySQL wire protocol connections.
type MySQLHandler struct {
	router      *router.Router
	poolMgr     *pool.Manager
	healthCheck *health.Checker
	metrics     *metrics.Collector
}

// Handle processes a MySQL client connection.
func (h *MySQLHandler) Handle(ctx context.Context, clientConn net.Conn) error {
	// Step 1: Connect to a temporary backend to get the initial handshake
	// But first we need to know the tenant. For MySQL, we extract tenant from the
	// client's login request, so we need to do a mini-handshake dance.

	// Read client's first bytes - but MySQL protocol starts with SERVER sending handshake.
	// Since we don't know the tenant yet, we send a synthetic handshake to the client,
	// then parse their response to extract tenant ID.

	// Send a synthetic initial handshake to the client
	if err := h.sendSyntheticHandshake(clientConn); err != nil {
		return fmt.Errorf("sending synthetic handshake: %w", err)
	}

	// Read the client's HandshakeResponse to extract tenant ID and credentials
	tenantID, username, authData, database, clientFlags, handshakeResp, err := h.readHandshakeResponse(clientConn)
	if err != nil {
		return fmt.Errorf("reading handshake response: %w", err)
	}

	_ = username
	_ = authData
	_ = database
	_ = clientFlags

	// After synthetic handshake (seq 0) and client response (seq 1),
	// our error responses should use seq 2.
	const errSeq byte = 2

	if tenantID == "" {
		h.sendMySQLError(clientConn, 1045, "28000", "no tenant_id provided (use tenant__user format or set database to tenant_id)", errSeq)
		return fmt.Errorf("no tenant_id in MySQL connection")
	}

	slog.Info("connection from tenant", "protocol", "mysql", "tenant", tenantID)

	// Resolve tenant config
	tc, err := h.router.Resolve(tenantID)
	if err != nil {
		h.sendMySQLError(clientConn, 1045, "28000", "Access denied", errSeq)
		return err
	}

	// Check if tenant is paused
	if h.router.IsPaused(tenantID) {
		h.sendMySQLError(clientConn, 1045, "08S01", "Access denied", errSeq)
		return fmt.Errorf("tenant %s is paused", tenantID)
	}

	// Check health
	if h.healthCheck != nil && !h.healthCheck.IsHealthy(tenantID) {
		h.sendMySQLError(clientConn, 1045, "08S01", "Access denied", errSeq)
		return fmt.Errorf("tenant %s is unhealthy", tenantID)
	}

	tenantPool := h.poolMgr.GetOrCreate(tenantID, tc)
	defaults := h.router.Defaults()
	poolMode := tc.EffectivePoolMode(defaults)

	// Transaction-level pooling: pool has pre-authenticated connections; we
	// handle the entire session in the relay function.
	if poolMode == "transaction" {
		return relayMySQLTransactionMode(ctx, clientConn, tenantPool, tenantID, h.metrics)
	}

	// Session-level pooling: acquire a raw TCP connection and do the full
	// MySQL handshake dance (existing behavior, unchanged).
	pc, err := tenantPool.Acquire(ctx)
	if err != nil {
		h.sendMySQLError(clientConn, 1045, "08S01", "cannot connect to database", errSeq)
		return err
	}
	// Always close the backend connection after relay — the protocol state
	// is unknown after bidirectional copy, so it cannot be safely reused.
	defer pc.Close()

	backendConn := pc.Conn()

	// Read the real server's handshake
	_, _, err = readMySQLPacket(backendConn)
	if err != nil {
		return fmt.Errorf("reading backend handshake: %w", err)
	}

	// Forward the client's original handshake response to the backend
	if _, err := backendConn.Write(handshakeResp); err != nil {
		return fmt.Errorf("forwarding handshake response to backend: %w", err)
	}

	// Read backend's auth response and its sequence number
	authResp, authSeq, err := readMySQLPacket(backendConn)
	if err != nil {
		return fmt.Errorf("reading backend auth response: %w", err)
	}

	// Forward backend's auth response to client with correct sequence number
	if err := writeMySQLPacket(clientConn, authResp, authSeq); err != nil {
		return fmt.Errorf("forwarding auth response to client: %w", err)
	}

	// Check if auth succeeded
	if len(authResp) > 0 && authResp[0] == mysqlErrPacket {
		return fmt.Errorf("backend auth failed")
	}

	// Relay all subsequent packets
	start := time.Now()
	err = relay(ctx, clientConn, backendConn)
	if h.metrics != nil {
		h.metrics.QueryDuration(tenantID, "mysql", time.Since(start))
	}
	return err
}

// sendSyntheticHandshake sends a minimal MySQL handshake to learn the client's tenant.
func (h *MySQLHandler) sendSyntheticHandshake(conn net.Conn) error {
	// Generate random auth challenge (20 bytes: 8 for part1 + 12 for part2)
	authData := make([]byte, 20)
	if _, err := rand.Read(authData); err != nil {
		return fmt.Errorf("generating auth challenge: %w", err)
	}
	// Ensure no zero bytes in auth data (MySQL protocol uses null terminators)
	for i := range authData {
		if authData[i] == 0 {
			authData[i] = 1
		}
	}

	// Build a MySQL Protocol::Handshake (v10)
	var buf []byte

	// Protocol version
	buf = append(buf, 10)

	// Server version (null-terminated)
	version := "5.7.0-dbbouncer"
	buf = append(buf, version...)
	buf = append(buf, 0)

	// Connection ID
	buf = append(buf, 1, 0, 0, 0)

	// Auth-plugin-data part 1 (8 bytes)
	buf = append(buf, authData[:8]...)

	// Filler
	buf = append(buf, 0)

	// Capability flags (lower 2 bytes)
	// CLIENT_PROTOCOL_41 | CLIENT_SECURE_CONNECTION | CLIENT_PLUGIN_AUTH | CLIENT_CONNECT_WITH_DB
	capLow := uint16(0xf7ff)
	buf = append(buf, byte(capLow), byte(capLow>>8))

	// Character set (utf8)
	buf = append(buf, 33)

	// Status flags
	buf = append(buf, 0x02, 0x00)

	// Capability flags (upper 2 bytes)
	capHigh := uint16(0x0081)
	buf = append(buf, byte(capHigh), byte(capHigh>>8))

	// Length of auth-plugin-data (21 = 8 + 13)
	buf = append(buf, 21)

	// Reserved (10 bytes of 0)
	buf = append(buf, make([]byte, 10)...)

	// Auth-plugin-data part 2 (12 bytes + null terminator)
	buf = append(buf, authData[8:]...)
	buf = append(buf, 0x00)

	// Auth plugin name
	pluginName := "mysql_native_password"
	buf = append(buf, pluginName...)
	buf = append(buf, 0)

	return writeMySQLPacket(conn, buf, 0)
}

// readHandshakeResponse reads the MySQL client's HandshakeResponse and extracts tenant info.
func (h *MySQLHandler) readHandshakeResponse(conn net.Conn) (tenantID, username string, authData []byte, database string, clientFlags uint32, rawPacket []byte, err error) {
	// Read the full MySQL packet (header + payload)
	headerBuf := make([]byte, 4)
	if _, err = io.ReadFull(conn, headerBuf); err != nil {
		return "", "", nil, "", 0, nil, fmt.Errorf("reading packet header: %w", err)
	}

	payloadLen := int(headerBuf[0]) | int(headerBuf[1])<<8 | int(headerBuf[2])<<16
	if payloadLen > 1<<24 || payloadLen < 32 {
		return "", "", nil, "", 0, nil, fmt.Errorf("invalid handshake response length: %d", payloadLen)
	}

	payload := make([]byte, payloadLen)
	if _, err = io.ReadFull(conn, payload); err != nil {
		return "", "", nil, "", 0, nil, fmt.Errorf("reading handshake response: %w", err)
	}

	// Store raw packet for forwarding
	rawPacket = make([]byte, 4+payloadLen)
	copy(rawPacket, headerBuf)
	copy(rawPacket[4:], payload)

	// Parse HandshakeResponse41
	if len(payload) < 32 {
		return "", "", nil, "", 0, rawPacket, fmt.Errorf("handshake response too short")
	}

	clientFlags = binary.LittleEndian.Uint32(payload[0:4])
	// maxPacketSize := binary.LittleEndian.Uint32(payload[4:8])
	// charset := payload[8]
	// reserved: payload[9:32]

	pos := 32

	// Username (null-terminated)
	usernameEnd := pos
	for usernameEnd < len(payload) && payload[usernameEnd] != 0 {
		usernameEnd++
	}
	username = string(payload[pos:usernameEnd])
	pos = usernameEnd + 1

	// Auth data
	if clientFlags&0x00200000 != 0 { // CLIENT_PLUGIN_AUTH_LENENC_CLIENT_DATA
		if pos < len(payload) {
			authLen := int(payload[pos])
			pos++
			if pos+authLen <= len(payload) {
				authData = payload[pos : pos+authLen]
				pos += authLen
			}
		}
	} else if clientFlags&0x00008000 != 0 { // CLIENT_SECURE_CONNECTION
		if pos < len(payload) {
			authLen := int(payload[pos])
			pos++
			if pos+authLen <= len(payload) {
				authData = payload[pos : pos+authLen]
				pos += authLen
			}
		}
	} else {
		// Null-terminated auth data
		authEnd := pos
		for authEnd < len(payload) && payload[authEnd] != 0 {
			authEnd++
		}
		authData = payload[pos:authEnd]
		pos = authEnd + 1
	}

	// Database (if CLIENT_CONNECT_WITH_DB flag is set)
	if clientFlags&0x00000008 != 0 && pos < len(payload) {
		dbEnd := pos
		for dbEnd < len(payload) && payload[dbEnd] != 0 {
			dbEnd++
		}
		database = string(payload[pos:dbEnd])
	}

	// Extract tenant ID from username format: tenant__user
	if tid, realUser, ok := router.ExtractTenantFromUsername(username); ok {
		tenantID = tid
		_ = realUser
	}

	// Or from database name if it looks like a tenant ID
	if tenantID == "" && database != "" {
		// Try resolving database as tenant ID
		if _, resolveErr := h.router.Resolve(database); resolveErr == nil {
			tenantID = database
		}
	}

	return tenantID, username, authData, database, clientFlags, rawPacket, nil
}

// readMySQLPacket reads a single MySQL packet (4-byte header + payload).
// Returns the payload and the sequence number from the header.
func readMySQLPacket(conn net.Conn) ([]byte, byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, 0, err
	}

	payloadLen := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	seqNum := header[3]
	if payloadLen > 1<<24 {
		return nil, 0, fmt.Errorf("mysql packet too large: %d", payloadLen)
	}

	payload := make([]byte, payloadLen)
	if payloadLen > 0 {
		if _, err := io.ReadFull(conn, payload); err != nil {
			return nil, 0, err
		}
	}

	return payload, seqNum, nil
}

// writeMySQLPacket writes a MySQL packet with the given sequence number.
func writeMySQLPacket(conn net.Conn, payload []byte, seqNum byte) error {
	header := make([]byte, 4)
	header[0] = byte(len(payload))
	header[1] = byte(len(payload) >> 8)
	header[2] = byte(len(payload) >> 16)
	header[3] = seqNum

	buf := make([]byte, 4+len(payload))
	copy(buf, header)
	copy(buf[4:], payload)
	_, err := conn.Write(buf)
	return err
}

// sendMySQLError sends a MySQL ERR_Packet to the client with the given sequence number.
func (h *MySQLHandler) sendMySQLError(conn net.Conn, errorCode uint16, sqlState, message string, seqNum byte) {
	var buf []byte

	// ERR packet header
	buf = append(buf, mysqlErrPacket)

	// Error code (2 bytes LE)
	buf = append(buf, byte(errorCode), byte(errorCode>>8))

	// SQL state marker
	buf = append(buf, '#')

	// SQL state (5 bytes)
	state := sqlState
	if len(state) < 5 {
		state = state + "     "
	}
	buf = append(buf, state[:5]...)

	// Error message
	buf = append(buf, message...)

	writeMySQLPacket(conn, buf, seqNum)
}
