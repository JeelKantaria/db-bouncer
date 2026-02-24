package pool

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"testing"

	"golang.org/x/crypto/pbkdf2"
)

// mockSCRAMBackend simulates a PG backend that uses SCRAM-SHA-256 auth.
// It reads the startup message, then performs the full SCRAM exchange.
func mockSCRAMBackend(t *testing.T, conn net.Conn, user, password string) {
	t.Helper()

	// Read startup message
	lenBuf := make([]byte, 4)
	conn.Read(lenBuf)
	msgLen := int(binary.BigEndian.Uint32(lenBuf))
	body := make([]byte, msgLen-4)
	conn.Read(body)

	// Send AuthenticationSASL (type 10) with SCRAM-SHA-256
	var saslPayload []byte
	authType := make([]byte, 4)
	binary.BigEndian.PutUint32(authType, 10)
	saslPayload = append(saslPayload, authType...)
	saslPayload = append(saslPayload, "SCRAM-SHA-256"...)
	saslPayload = append(saslPayload, 0)
	saslPayload = append(saslPayload, 0) // terminator
	writePGTestMsg(conn, 'R', saslPayload)

	// Read SASLInitialResponse (password message 'p')
	typeBuf := make([]byte, 1)
	conn.Read(typeBuf)
	if typeBuf[0] != 'p' {
		t.Errorf("expected password message 'p', got %c", typeBuf[0])
		return
	}
	pLenBuf := make([]byte, 4)
	conn.Read(pLenBuf)
	pLen := int(binary.BigEndian.Uint32(pLenBuf)) - 4
	pPayload := make([]byte, pLen)
	conn.Read(pPayload)

	// Parse: mechanism\0 + int32(len) + client-first-message
	mechEnd := 0
	for mechEnd < len(pPayload) && pPayload[mechEnd] != 0 {
		mechEnd++
	}
	mechanism := string(pPayload[:mechEnd])
	if mechanism != "SCRAM-SHA-256" {
		t.Errorf("expected mechanism SCRAM-SHA-256, got %q", mechanism)
		return
	}

	cfmLenBytes := pPayload[mechEnd+1 : mechEnd+5]
	cfmLen := int(binary.BigEndian.Uint32(cfmLenBytes))
	clientFirstMsg := string(pPayload[mechEnd+5 : mechEnd+5+cfmLen])

	// Parse client-first-message: "n,,n=<user>,r=<nonce>"
	// Strip gs2-header "n,,"
	clientFirstBare := clientFirstMsg[3:]
	var clientNonce string
	for _, part := range strings.Split(clientFirstBare, ",") {
		if strings.HasPrefix(part, "r=") {
			clientNonce = part[2:]
		}
	}

	// Server generates its challenge
	serverNonce := clientNonce + "servernonce123"
	salt := []byte("randomsaltvalue!")
	iterations := 4096
	saltB64 := base64.StdEncoding.EncodeToString(salt)
	serverFirstMsg := fmt.Sprintf("r=%s,s=%s,i=%d", serverNonce, saltB64, iterations)

	// Send AuthenticationSASLContinue (type 11)
	var continuePayload []byte
	authType11 := make([]byte, 4)
	binary.BigEndian.PutUint32(authType11, 11)
	continuePayload = append(continuePayload, authType11...)
	continuePayload = append(continuePayload, serverFirstMsg...)
	writePGTestMsg(conn, 'R', continuePayload)

	// Read SASLResponse
	conn.Read(typeBuf)
	if typeBuf[0] != 'p' {
		t.Errorf("expected password message 'p' for SASL response, got %c", typeBuf[0])
		return
	}
	conn.Read(pLenBuf)
	pLen = int(binary.BigEndian.Uint32(pLenBuf)) - 4
	clientFinalMsg := make([]byte, pLen)
	conn.Read(clientFinalMsg)

	// Parse client-final-message: c=<binding>,r=<nonce>,p=<proof>
	clientFinalStr := string(clientFinalMsg)

	// Verify the client proof
	channelBinding := "c=" + base64.StdEncoding.EncodeToString([]byte("n,,"))
	clientFinalWithoutProof := fmt.Sprintf("%s,r=%s", channelBinding, serverNonce)
	authMessage := clientFirstBare + "," + serverFirstMsg + "," + clientFinalWithoutProof

	saltedPassword := pbkdf2.Key([]byte(password), salt, iterations, 32, sha256.New)
	clientKey := hmacSHA256(saltedPassword, []byte("Client Key"))
	storedKey := sha256Sum(clientKey)
	clientSignature := hmacSHA256(storedKey, []byte(authMessage))
	expectedProof := xorBytes(clientKey, clientSignature)
	expectedProofB64 := base64.StdEncoding.EncodeToString(expectedProof)

	if !strings.Contains(clientFinalStr, "p="+expectedProofB64) {
		t.Errorf("client proof mismatch.\ngot:  %s\nwant proof: %s", clientFinalStr, expectedProofB64)
		// Send error
		var errPayload []byte
		errPayload = append(errPayload, 'S')
		errPayload = append(errPayload, "FATAL"...)
		errPayload = append(errPayload, 0)
		errPayload = append(errPayload, 'M')
		errPayload = append(errPayload, "authentication failed"...)
		errPayload = append(errPayload, 0, 0)
		writePGTestMsg(conn, 'E', errPayload)
		return
	}

	// Compute server signature and send AuthenticationSASLFinal (type 12)
	serverKey := hmacSHA256(saltedPassword, []byte("Server Key"))
	serverSig := hmacSHA256(serverKey, []byte(authMessage))
	serverFinal := "v=" + base64.StdEncoding.EncodeToString(serverSig)

	var finalPayload []byte
	authType12 := make([]byte, 4)
	binary.BigEndian.PutUint32(authType12, 12)
	finalPayload = append(finalPayload, authType12...)
	finalPayload = append(finalPayload, serverFinal...)
	writePGTestMsg(conn, 'R', finalPayload)

	// Send AuthenticationOk
	writePGTestMsg(conn, 'R', uint32ToBE(0))

	// Send ParameterStatus + BackendKeyData + ReadyForQuery
	writePGTestMsg(conn, 'S', nullTermPair("server_version", "16.0"))
	bkd := make([]byte, 8)
	binary.BigEndian.PutUint32(bkd[:4], 9999)
	binary.BigEndian.PutUint32(bkd[4:], 8888)
	writePGTestMsg(conn, 'K', bkd)
	writePGTestMsg(conn, 'Z', []byte{'I'})
}

func TestSCRAMSHA256AuthSuccess(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	tp := &TenantPool{
		tenantID: "test",
		dbType:   "postgres",
		poolMode: "transaction",
		username: "scramuser",
		password: "scrampass",
		dbname:   "testdb",
	}

	pc := NewPooledConn(client, "test", "postgres", tp)

	go mockSCRAMBackend(t, server, "scramuser", "scrampass")

	err := tp.authenticatePG(pc)
	if err != nil {
		t.Fatalf("authenticatePG with SCRAM failed: %v", err)
	}

	if !pc.IsAuthenticated() {
		t.Error("expected connection to be authenticated")
	}
	if pc.BackendPID() != 9999 {
		t.Errorf("expected backendPID=9999, got %d", pc.BackendPID())
	}
	if pc.BackendKey() != 8888 {
		t.Errorf("expected backendKey=8888, got %d", pc.BackendKey())
	}
	params := pc.ServerParams()
	if params["server_version"] != "16.0" {
		t.Errorf("expected server_version=16.0, got %q", params["server_version"])
	}
}

func TestSCRAMSHA256WrongPassword(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	tp := &TenantPool{
		tenantID: "test",
		dbType:   "postgres",
		poolMode: "transaction",
		username: "scramuser",
		password: "wrongpass",
		dbname:   "testdb",
	}

	pc := NewPooledConn(client, "test", "postgres", tp)

	// Mock backend that rejects auth after the SASL exchange
	go mockSCRAMBackendReject(t, server)

	err := tp.authenticatePG(pc)
	if err == nil {
		t.Fatal("expected authenticatePG to fail with wrong password")
	}
}

// mockSCRAMBackendReject simulates a PG backend that starts a SCRAM exchange
// but then sends an ErrorResponse instead of SASLFinal (as PG does for wrong password).
func mockSCRAMBackendReject(t *testing.T, conn net.Conn) {
	t.Helper()

	// Read startup message
	lenBuf := make([]byte, 4)
	conn.Read(lenBuf)
	msgLen := int(binary.BigEndian.Uint32(lenBuf))
	body := make([]byte, msgLen-4)
	conn.Read(body)

	// Send AuthenticationSASL (type 10)
	var saslPayload []byte
	authType := make([]byte, 4)
	binary.BigEndian.PutUint32(authType, 10)
	saslPayload = append(saslPayload, authType...)
	saslPayload = append(saslPayload, "SCRAM-SHA-256"...)
	saslPayload = append(saslPayload, 0, 0)
	writePGTestMsg(conn, 'R', saslPayload)

	// Read SASLInitialResponse
	typeBuf := make([]byte, 1)
	conn.Read(typeBuf)
	pLenBuf := make([]byte, 4)
	conn.Read(pLenBuf)
	pLen := int(binary.BigEndian.Uint32(pLenBuf)) - 4
	pPayload := make([]byte, pLen)
	conn.Read(pPayload)

	// Send SASLContinue with a challenge
	salt := base64.StdEncoding.EncodeToString([]byte("salt1234salt5678"))
	serverFirstMsg := fmt.Sprintf("r=fakeclientnonceservernonce,s=%s,i=4096", salt)

	var continuePayload []byte
	authType11 := make([]byte, 4)
	binary.BigEndian.PutUint32(authType11, 11)
	continuePayload = append(continuePayload, authType11...)
	continuePayload = append(continuePayload, serverFirstMsg...)
	writePGTestMsg(conn, 'R', continuePayload)

	// Read SASLResponse (client proof)
	conn.Read(typeBuf)
	conn.Read(pLenBuf)
	pLen = int(binary.BigEndian.Uint32(pLenBuf)) - 4
	resp := make([]byte, pLen)
	conn.Read(resp)

	// Send ErrorResponse (authentication failed)
	var errPayload []byte
	errPayload = append(errPayload, 'S')
	errPayload = append(errPayload, "FATAL"...)
	errPayload = append(errPayload, 0)
	errPayload = append(errPayload, 'M')
	errPayload = append(errPayload, "password authentication failed"...)
	errPayload = append(errPayload, 0, 0)
	writePGTestMsg(conn, 'E', errPayload)
}

func TestParseSASLMechanisms(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want []string
	}{
		{
			name: "single mechanism",
			data: append([]byte("SCRAM-SHA-256"), 0, 0),
			want: []string{"SCRAM-SHA-256"},
		},
		{
			name: "two mechanisms",
			data: append(append([]byte("SCRAM-SHA-256"), 0), append([]byte("SCRAM-SHA-256-PLUS"), 0, 0)...),
			want: []string{"SCRAM-SHA-256", "SCRAM-SHA-256-PLUS"},
		},
		{
			name: "empty",
			data: []byte{0},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseSASLMechanisms(tt.data)
			if len(got) != len(tt.want) {
				t.Errorf("parseSASLMechanisms() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("parseSASLMechanisms()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSASLEscapeUsername(t *testing.T) {
	if got := saslEscapeUsername("user"); got != "user" {
		t.Errorf("expected 'user', got %q", got)
	}
	if got := saslEscapeUsername("us=er"); got != "us=3Der" {
		t.Errorf("expected 'us=3Der', got %q", got)
	}
	if got := saslEscapeUsername("us,er"); got != "us=2Cer" {
		t.Errorf("expected 'us=2Cer', got %q", got)
	}
}

func TestParseServerFirst(t *testing.T) {
	salt := base64.StdEncoding.EncodeToString([]byte("somesalt"))
	msg := fmt.Sprintf("r=clientnonceservernonce,s=%s,i=4096", salt)

	nonce, saltBytes, iterations, err := parseServerFirst(msg)
	if err != nil {
		t.Fatalf("parseServerFirst failed: %v", err)
	}
	if nonce != "clientnonceservernonce" {
		t.Errorf("nonce = %q, want 'clientnonceservernonce'", nonce)
	}
	if string(saltBytes) != "somesalt" {
		t.Errorf("salt = %q, want 'somesalt'", saltBytes)
	}
	if iterations != 4096 {
		t.Errorf("iterations = %d, want 4096", iterations)
	}
}

func TestXorBytes(t *testing.T) {
	a := []byte{0xff, 0x00, 0xaa}
	b := []byte{0x0f, 0xf0, 0x55}
	got := xorBytes(a, b)
	want := []byte{0xf0, 0xf0, 0xff}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("xorBytes[%d] = 0x%02x, want 0x%02x", i, got[i], want[i])
		}
	}
}

func TestHMACSHA256(t *testing.T) {
	key := []byte("key")
	data := []byte("data")
	got := hmacSHA256(key, data)
	// Known HMAC-SHA-256("key", "data")
	h := hmac.New(sha256.New, key)
	h.Write(data)
	want := h.Sum(nil)
	if len(got) != len(want) {
		t.Fatalf("length mismatch: %d vs %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("hmacSHA256[%d] = 0x%02x, want 0x%02x", i, got[i], want[i])
		}
	}
}

// --- MySQL native password hash tests ---

func TestMySQLNativePasswordHash(t *testing.T) {
	challenge := make([]byte, 20)
	for i := range challenge {
		challenge[i] = 0x01
	}
	result := mysqlNativePasswordHash([]byte("password"), challenge)
	if len(result) != 20 {
		t.Fatalf("expected 20 bytes, got %d", len(result))
	}
	// Determinism check
	result2 := mysqlNativePasswordHash([]byte("password"), challenge)
	for i := range result {
		if result[i] != result2[i] {
			t.Error("hash is not deterministic")
		}
	}
}

func TestMySQLNativePasswordEmpty(t *testing.T) {
	result := mysqlNativePasswordHash([]byte(""), []byte("challenge"))
	if len(result) != 0 {
		t.Errorf("expected empty hash for empty password, got %v", result)
	}
}
