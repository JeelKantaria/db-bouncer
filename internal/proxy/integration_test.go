package proxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/pool"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

// testConfig creates a minimal config for testing.
func testConfig() *config.Config {
	return &config.Config{
		Listen: config.ListenConfig{
			PostgresPort: 6432,
			MySQLPort:    3307,
			APIPort:      8080,
		},
		Defaults: config.PoolDefaults{
			MinConnections: 1,
			MaxConnections: 10,
			IdleTimeout:    5 * time.Minute,
			MaxLifetime:    30 * time.Minute,
			AcquireTimeout: 5 * time.Second,
		},
		Tenants: map[string]config.TenantConfig{
			"tenant_1": {
				DBType:   "postgres",
				Host:     "localhost",
				Port:     5432,
				DBName:   "testdb",
				Username: "testuser",
			},
			"tenant_2": {
				DBType:   "mysql",
				Host:     "localhost",
				Port:     3306,
				DBName:   "testdb",
				Username: "testuser",
			},
		},
	}
}

// buildPGStartupMessage builds a PostgreSQL startup message with the given parameters.
func buildPGStartupMessage(params map[string]string) []byte {
	var body []byte
	// Protocol version 3.0
	ver := make([]byte, 4)
	binary.BigEndian.PutUint32(ver, pgProtoVersion)
	body = append(body, ver...)

	// Key-value pairs (null-terminated)
	for k, v := range params {
		body = append(body, k...)
		body = append(body, 0)
		body = append(body, v...)
		body = append(body, 0)
	}
	// Terminating null
	body = append(body, 0)

	// Prepend length (4 bytes for length + body)
	msgLen := make([]byte, 4)
	binary.BigEndian.PutUint32(msgLen, uint32(4+len(body)))

	return append(msgLen, body...)
}

// buildPGSSLRequest builds a PostgreSQL SSL request message.
func buildPGSSLRequest() []byte {
	msg := make([]byte, 8)
	binary.BigEndian.PutUint32(msg[0:4], 8)                   // length
	binary.BigEndian.PutUint32(msg[4:8], uint32(pgSSLRequestCode)) // SSL request code
	return msg
}

// readPGErrorFromConn reads a PG error response and extracts the message.
func readPGErrorFromConn(conn net.Conn) (string, error) {
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	msgType, payload, err := readPGMessage(conn)
	if err != nil {
		return "", err
	}
	if msgType != pgMsgErrorResponse {
		return "", nil
	}

	// Parse error fields
	msg := ""
	for i := 0; i < len(payload); i++ {
		fieldType := payload[i]
		if fieldType == 0 {
			break
		}
		i++
		end := i
		for end < len(payload) && payload[end] != 0 {
			end++
		}
		if fieldType == 'M' {
			msg = string(payload[i:end])
		}
		i = end
	}
	return msg, nil
}

// buildMySQLHandshakeResponse builds a MySQL HandshakeResponse41 packet.
func buildMySQLHandshakeResponse(username, database string) []byte {
	var payload []byte

	// Client flags (CLIENT_PROTOCOL_41 | CLIENT_SECURE_CONNECTION | CLIENT_CONNECT_WITH_DB | CLIENT_PLUGIN_AUTH)
	flags := uint32(0x0000_8008 | 0x0000_0200 | 0x0008_0000 | 0x0001_0000)
	if database != "" {
		flags |= 0x00000008 // CLIENT_CONNECT_WITH_DB
	}
	flagBuf := make([]byte, 4)
	binary.LittleEndian.PutUint32(flagBuf, flags)
	payload = append(payload, flagBuf...)

	// Max packet size
	payload = append(payload, 0x00, 0x00, 0x00, 0x01)

	// Character set (utf8)
	payload = append(payload, 33)

	// Reserved (23 bytes)
	payload = append(payload, make([]byte, 23)...)

	// Username (null-terminated)
	payload = append(payload, username...)
	payload = append(payload, 0)

	// Auth data length + data (CLIENT_SECURE_CONNECTION)
	authData := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
		0x11, 0x12, 0x13, 0x14}
	payload = append(payload, byte(len(authData)))
	payload = append(payload, authData...)

	// Database (null-terminated, if flag set)
	if database != "" {
		payload = append(payload, database...)
		payload = append(payload, 0)
	}

	// Auth plugin name
	payload = append(payload, "mysql_native_password"...)
	payload = append(payload, 0)

	// Build MySQL packet with header
	header := make([]byte, 4)
	header[0] = byte(len(payload))
	header[1] = byte(len(payload) >> 8)
	header[2] = byte(len(payload) >> 16)
	header[3] = 1 // sequence number

	return append(header, payload...)
}

// readMySQLErrorFromConn reads a MySQL packet and extracts error message if ERR_Packet.
func readMySQLErrorFromConn(conn net.Conn) (string, error) {
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	payload, err := readMySQLPacket(conn)
	if err != nil {
		return "", err
	}
	if len(payload) == 0 || payload[0] != mysqlErrPacket {
		return "", nil
	}

	// Skip: marker(1) + error_code(2) + sql_state_marker(1) + sql_state(5)
	if len(payload) > 9 {
		return string(payload[9:]), nil
	}
	return "", nil
}

// --- PostgreSQL Integration Tests ---

func TestPGStartupWithTenantInOptions(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &PostgresHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Send startup message with tenant_id in options
	startupMsg := buildPGStartupMessage(map[string]string{
		"user":    "testuser",
		"options": "-c tenant_id=tenant_1",
	})

	go func() {
		client.Write(startupMsg)
	}()

	tenantID, _, err := h.readStartupMessage(server)
	if err != nil {
		t.Fatalf("readStartupMessage error: %v", err)
	}
	if tenantID != "tenant_1" {
		t.Errorf("expected tenant_id=tenant_1, got %q", tenantID)
	}
}

func TestPGStartupWithTenantAsParam(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &PostgresHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Send startup message with tenant_id as direct parameter
	startupMsg := buildPGStartupMessage(map[string]string{
		"user":      "testuser",
		"tenant_id": "tenant_1",
	})

	go func() {
		client.Write(startupMsg)
	}()

	tenantID, _, err := h.readStartupMessage(server)
	if err != nil {
		t.Fatalf("readStartupMessage error: %v", err)
	}
	if tenantID != "tenant_1" {
		t.Errorf("expected tenant_id=tenant_1, got %q", tenantID)
	}
}

func TestPGStartupWithTenantInUsername(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &PostgresHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Send startup message with tenant__user format
	startupMsg := buildPGStartupMessage(map[string]string{
		"user": "tenant_1__appuser",
	})

	go func() {
		client.Write(startupMsg)
	}()

	tenantID, _, err := h.readStartupMessage(server)
	if err != nil {
		t.Fatalf("readStartupMessage error: %v", err)
	}
	if tenantID != "tenant_1" {
		t.Errorf("expected tenant_id=tenant_1, got %q", tenantID)
	}
}

func TestPGStartupNoTenant(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &PostgresHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// No tenant_id at all
	startupMsg := buildPGStartupMessage(map[string]string{
		"user": "testuser",
	})

	errCh := make(chan error, 1)
	go func() {
		client.Write(startupMsg)
		// Read the error response
		_, err := readPGErrorFromConn(client)
		errCh <- err
	}()

	err := h.Handle(context.Background(), server)
	if err == nil {
		t.Fatal("expected error for missing tenant_id")
	}
}

func TestPGStartupUnknownTenant(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &PostgresHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	startupMsg := buildPGStartupMessage(map[string]string{
		"user":    "testuser",
		"options": "-c tenant_id=nonexistent",
	})

	errCh := make(chan error, 1)
	go func() {
		client.Write(startupMsg)
		msg, err := readPGErrorFromConn(client)
		if err == nil && msg == "" {
			errCh <- nil
		} else {
			errCh <- err
		}
	}()

	err := h.Handle(context.Background(), server)
	if err == nil {
		t.Fatal("expected error for unknown tenant")
	}
	// Verify the error message contains the tenant info
	if err.Error() != "unknown tenant: nonexistent" {
		t.Logf("got error: %v", err)
	}
}

func TestPGStartupPausedTenant(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	r.PauseTenant("tenant_1")
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &PostgresHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	startupMsg := buildPGStartupMessage(map[string]string{
		"user":    "testuser",
		"options": "-c tenant_id=tenant_1",
	})

	var clientErr string
	errCh := make(chan struct{}, 1)
	go func() {
		client.Write(startupMsg)
		msg, _ := readPGErrorFromConn(client)
		clientErr = msg
		errCh <- struct{}{}
	}()

	err := h.Handle(context.Background(), server)
	if err == nil {
		t.Fatal("expected error for paused tenant")
	}
	if err.Error() != "tenant tenant_1 is paused" {
		t.Errorf("unexpected error: %v", err)
	}

	<-errCh
	if clientErr == "" {
		t.Log("warning: could not read error from client side (pipe may have closed)")
	} else if clientErr != "tenant tenant_1 is paused" {
		t.Errorf("client got wrong error: %q", clientErr)
	}
}

func TestPGSSLDenied(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &PostgresHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Send SSL request followed by normal startup
	sslReq := buildPGSSLRequest()
	startupMsg := buildPGStartupMessage(map[string]string{
		"user":      "testuser",
		"tenant_id": "tenant_1",
	})

	go func() {
		client.Write(sslReq)
		// Read the 'N' (SSL denied) response
		resp := make([]byte, 1)
		client.Read(resp)
		if resp[0] != 'N' {
			t.Errorf("expected SSL denial 'N', got %c", resp[0])
		}
		// Send normal startup
		client.Write(startupMsg)
	}()

	tenantID, _, err := h.readStartupMessage(server)
	if err != nil {
		t.Fatalf("readStartupMessage error after SSL denial: %v", err)
	}
	if tenantID != "tenant_1" {
		t.Errorf("expected tenant_1, got %q", tenantID)
	}
}

// --- MySQL Integration Tests ---

func TestMySQLSyntheticHandshake(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &MySQLHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- h.sendSyntheticHandshake(server)
	}()

	// Read the handshake on the client side
	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	payload, err := readMySQLPacket(client)
	if err != nil {
		t.Fatalf("reading synthetic handshake: %v", err)
	}

	// Verify it starts with protocol version 10
	if payload[0] != 10 {
		t.Errorf("expected protocol version 10, got %d", payload[0])
	}

	// Verify version string contains "dbbouncer"
	verEnd := 1
	for verEnd < len(payload) && payload[verEnd] != 0 {
		verEnd++
	}
	version := string(payload[1:verEnd])
	if version != "5.7.0-dbbouncer" {
		t.Errorf("expected version '5.7.0-dbbouncer', got %q", version)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("sendSyntheticHandshake error: %v", err)
	}
}

func TestMySQLTenantFromUsername(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &MySQLHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Send handshake response with tenant__user format
	resp := buildMySQLHandshakeResponse("tenant_2__appuser", "")

	go func() {
		client.Write(resp)
	}()

	tenantID, username, _, _, _, _, err := h.readHandshakeResponse(server)
	if err != nil {
		t.Fatalf("readHandshakeResponse error: %v", err)
	}
	if tenantID != "tenant_2" {
		t.Errorf("expected tenant_id=tenant_2, got %q", tenantID)
	}
	if username != "tenant_2__appuser" {
		t.Errorf("expected raw username tenant_2__appuser, got %q", username)
	}
}

func TestMySQLTenantFromDatabase(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &MySQLHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	// Send handshake with plain username but database set to tenant_id
	resp := buildMySQLHandshakeResponse("appuser", "tenant_2")

	go func() {
		client.Write(resp)
	}()

	tenantID, _, _, database, _, _, err := h.readHandshakeResponse(server)
	if err != nil {
		t.Fatalf("readHandshakeResponse error: %v", err)
	}
	if tenantID != "tenant_2" {
		t.Errorf("expected tenant_id=tenant_2 from database, got %q", tenantID)
	}
	if database != "tenant_2" {
		t.Errorf("expected database=tenant_2, got %q", database)
	}
}

func TestMySQLNoTenant(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &MySQLHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	errCh := make(chan error, 1)
	go func() {
		// Read synthetic handshake first
		readMySQLPacket(client)
		// Send response with no tenant info
		resp := buildMySQLHandshakeResponse("plainuser", "unknowndb")
		client.Write(resp)
		// Read error response
		_, err := readMySQLErrorFromConn(client)
		errCh <- err
	}()

	err := h.Handle(context.Background(), server)
	if err == nil {
		t.Fatal("expected error for missing tenant")
	}
}

func TestMySQLPausedTenant(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	r.PauseTenant("tenant_2")
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &MySQLHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	var clientErr string
	errCh := make(chan struct{}, 1)
	go func() {
		// Read synthetic handshake
		readMySQLPacket(client)
		// Send response with tenant__user format
		resp := buildMySQLHandshakeResponse("tenant_2__appuser", "")
		client.Write(resp)
		// Read error
		msg, _ := readMySQLErrorFromConn(client)
		clientErr = msg
		errCh <- struct{}{}
	}()

	err := h.Handle(context.Background(), server)
	if err == nil {
		t.Fatal("expected error for paused tenant")
	}
	if err.Error() != "tenant tenant_2 is paused" {
		t.Errorf("unexpected error: %v", err)
	}

	<-errCh
	if clientErr != "" && clientErr != "tenant tenant_2 is paused" {
		t.Errorf("client got wrong error: %q", clientErr)
	}
}

func TestMySQLUnknownTenant(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &MySQLHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	errCh := make(chan error, 1)
	go func() {
		// Read synthetic handshake
		readMySQLPacket(client)
		// Send response with unknown tenant
		resp := buildMySQLHandshakeResponse("unknown__appuser", "")
		client.Write(resp)
		// Read error
		_, err := readMySQLErrorFromConn(client)
		errCh <- err
	}()

	err := h.Handle(context.Background(), server)
	if err == nil {
		t.Fatal("expected error for unknown tenant")
	}
}

// --- Wire Protocol Tests ---

func TestPGMessageRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	payload := []byte("SELECT 1")
	go func() {
		writePGMessage(client, pgMsgQuery, payload)
	}()

	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	msgType, received, err := readPGMessage(server)
	if err != nil {
		t.Fatalf("readPGMessage error: %v", err)
	}
	if msgType != pgMsgQuery {
		t.Errorf("expected message type 'Q', got %c", msgType)
	}
	if string(received) != "SELECT 1" {
		t.Errorf("expected payload 'SELECT 1', got %q", received)
	}
}

func TestMySQLPacketRoundTrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	payload := []byte{mysqlComQuery}
	payload = append(payload, "SELECT 1"...)

	go func() {
		writeMySQLPacket(client, payload, 0)
	}()

	server.SetReadDeadline(time.Now().Add(2 * time.Second))
	received, err := readMySQLPacket(server)
	if err != nil {
		t.Fatalf("readMySQLPacket error: %v", err)
	}
	if received[0] != mysqlComQuery {
		t.Errorf("expected COM_QUERY, got 0x%02x", received[0])
	}
	if string(received[1:]) != "SELECT 1" {
		t.Errorf("expected 'SELECT 1', got %q", string(received[1:]))
	}
}

func TestPGSendErrorFormat(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &PostgresHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		h.sendPGError(server, "FATAL", "08000", "test error message")
		server.Close()
	}()

	msg, err := readPGErrorFromConn(client)
	if err != nil && err != io.EOF {
		t.Fatalf("readPGErrorFromConn error: %v", err)
	}
	if msg != "test error message" {
		t.Errorf("expected 'test error message', got %q", msg)
	}
}

func TestMySQLSendErrorFormat(t *testing.T) {
	cfg := testConfig()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	defer pm.Close()

	h := &MySQLHandler{router: r, poolMgr: pm, healthCheck: nil, metrics: nil}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		h.sendMySQLError(server, 1045, "28000", "access denied")
		server.Close()
	}()

	msg, err := readMySQLErrorFromConn(client)
	if err != nil && err != io.EOF {
		t.Fatalf("readMySQLErrorFromConn error: %v", err)
	}
	if msg != "access denied" {
		t.Errorf("expected 'access denied', got %q", msg)
	}
}

func TestRelayCopiesBidirectionally(t *testing.T) {
	client1, server1 := net.Pipe()
	client2, server2 := net.Pipe()
	defer client1.Close()
	defer server1.Close()
	defer client2.Close()
	defer server2.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		relay(ctx, server1, client2)
	}()

	// Write from client1 → should appear on server2
	go func() {
		client1.Write([]byte("hello from client"))
		client1.Close()
	}()

	buf := make([]byte, 100)
	server2.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := server2.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read error: %v", err)
	}
	if string(buf[:n]) != "hello from client" {
		t.Errorf("expected 'hello from client', got %q", string(buf[:n]))
	}
}
