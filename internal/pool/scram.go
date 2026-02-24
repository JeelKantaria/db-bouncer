package pool

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"

	"golang.org/x/crypto/pbkdf2"
)

// scramSHA256Auth performs the SASL SCRAM-SHA-256 authentication exchange
// with a PostgreSQL backend. It handles:
//   - AuthenticationSASL (type 10) — mechanism selection
//   - AuthenticationSASLContinue (type 11) — server challenge
//   - AuthenticationSASLFinal (type 12) — server signature verification
//
// The conn must already have the startup message sent and the initial
// AuthenticationSASL response read (payload passed as saslPayload).
func scramSHA256Auth(conn net.Conn, user, password string, saslPayload []byte) error {
	// Parse mechanism list from AuthenticationSASL payload
	// Format: mechanism1\0mechanism2\0\0
	mechanisms := parseSASLMechanisms(saslPayload[4:]) // skip auth type (4 bytes)
	if !containsMechanism(mechanisms, "SCRAM-SHA-256") {
		return fmt.Errorf("server does not support SCRAM-SHA-256, offered: %v", mechanisms)
	}

	// Generate client nonce
	nonceBytes := make([]byte, 18)
	if _, err := rand.Read(nonceBytes); err != nil {
		return fmt.Errorf("generating nonce: %w", err)
	}
	clientNonce := base64.StdEncoding.EncodeToString(nonceBytes)

	// Build client-first-message
	// gs2-header = "n,,"  (no channel binding, no authzid)
	// client-first-message-bare = "n=<user>,r=<nonce>"
	gs2Header := "n,,"
	clientFirstBare := fmt.Sprintf("n=%s,r=%s", saslEscapeUsername(user), clientNonce)
	clientFirstMsg := gs2Header + clientFirstBare

	// Send SASLInitialResponse
	if err := sendSASLInitialResponse(conn, "SCRAM-SHA-256", []byte(clientFirstMsg)); err != nil {
		return fmt.Errorf("sending SASL initial response: %w", err)
	}

	// Read AuthenticationSASLContinue (type 11)
	serverFirstMsg, err := readAuthMessage(conn, 11)
	if err != nil {
		return fmt.Errorf("reading server-first-message: %w", err)
	}

	// Parse server-first-message: r=<nonce>,s=<salt>,i=<iterations>
	serverNonce, salt, iterations, err := parseServerFirst(string(serverFirstMsg))
	if err != nil {
		return fmt.Errorf("parsing server-first-message: %w", err)
	}

	// Verify server nonce starts with our client nonce
	if !strings.HasPrefix(serverNonce, clientNonce) {
		return fmt.Errorf("server nonce does not start with client nonce")
	}

	// Compute SCRAM proof
	saltedPassword := pbkdf2.Key([]byte(password), salt, iterations, 32, sha256.New)

	clientKey := hmacSHA256(saltedPassword, []byte("Client Key"))
	storedKey := sha256Sum(clientKey)

	// channel-binding = "c=" + base64(gs2Header)
	channelBinding := "c=" + base64.StdEncoding.EncodeToString([]byte(gs2Header))
	clientFinalWithoutProof := fmt.Sprintf("%s,r=%s", channelBinding, serverNonce)

	// AuthMessage = client-first-message-bare + "," + server-first-message + "," + client-final-without-proof
	authMessage := clientFirstBare + "," + string(serverFirstMsg) + "," + clientFinalWithoutProof

	clientSignature := hmacSHA256(storedKey, []byte(authMessage))
	clientProof := xorBytes(clientKey, clientSignature)

	// Build client-final-message
	clientFinalMsg := clientFinalWithoutProof + ",p=" + base64.StdEncoding.EncodeToString(clientProof)

	// Send SASLResponse
	if err := sendSASLResponse(conn, []byte(clientFinalMsg)); err != nil {
		return fmt.Errorf("sending SASL response: %w", err)
	}

	// Read AuthenticationSASLFinal (type 12)
	serverFinalMsg, err := readAuthMessage(conn, 12)
	if err != nil {
		return fmt.Errorf("reading server-final-message: %w", err)
	}

	// Verify server signature
	serverKey := hmacSHA256(saltedPassword, []byte("Server Key"))
	expectedServerSig := hmacSHA256(serverKey, []byte(authMessage))
	expectedServerFinal := "v=" + base64.StdEncoding.EncodeToString(expectedServerSig)

	if string(serverFinalMsg) != expectedServerFinal {
		return fmt.Errorf("server signature mismatch")
	}

	return nil
}

// parseSASLMechanisms parses a null-terminated list of SASL mechanism names.
func parseSASLMechanisms(data []byte) []string {
	var mechs []string
	for len(data) > 0 {
		idx := 0
		for idx < len(data) && data[idx] != 0 {
			idx++
		}
		if idx > 0 {
			mechs = append(mechs, string(data[:idx]))
		}
		if idx >= len(data) {
			break
		}
		data = data[idx+1:]
	}
	return mechs
}

func containsMechanism(mechs []string, target string) bool {
	for _, m := range mechs {
		if m == target {
			return true
		}
	}
	return false
}

// parseServerFirst parses "r=<nonce>,s=<salt>,i=<iterations>" from the server.
func parseServerFirst(msg string) (nonce string, salt []byte, iterations int, err error) {
	parts := strings.Split(msg, ",")
	for _, part := range parts {
		if strings.HasPrefix(part, "r=") {
			nonce = part[2:]
		} else if strings.HasPrefix(part, "s=") {
			salt, err = base64.StdEncoding.DecodeString(part[2:])
			if err != nil {
				return "", nil, 0, fmt.Errorf("decoding salt: %w", err)
			}
		} else if strings.HasPrefix(part, "i=") {
			fmt.Sscanf(part[2:], "%d", &iterations)
		}
	}
	if nonce == "" || salt == nil || iterations == 0 {
		return "", nil, 0, fmt.Errorf("incomplete server-first-message: %q", msg)
	}
	return nonce, salt, iterations, nil
}

// saslEscapeUsername replaces "=" with "=3D" and "," with "=2C" per RFC 5802.
func saslEscapeUsername(user string) string {
	user = strings.ReplaceAll(user, "=", "=3D")
	user = strings.ReplaceAll(user, ",", "=2C")
	return user
}

// sendSASLInitialResponse sends a password message ('p') containing the
// SASL mechanism name and client-first-message.
func sendSASLInitialResponse(conn net.Conn, mechanism string, clientFirstMsg []byte) error {
	// Format: mechanism\0 + int32(len(clientFirstMsg)) + clientFirstMsg
	var payload []byte
	payload = append(payload, mechanism...)
	payload = append(payload, 0)

	lenBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(lenBuf, uint32(len(clientFirstMsg)))
	payload = append(payload, lenBuf...)
	payload = append(payload, clientFirstMsg...)

	msgLen := len(payload) + 4
	buf := make([]byte, 1+4+len(payload))
	buf[0] = 'p'
	binary.BigEndian.PutUint32(buf[1:5], uint32(msgLen))
	copy(buf[5:], payload)
	_, err := conn.Write(buf)
	return err
}

// sendSASLResponse sends a password message ('p') containing the SASL response.
func sendSASLResponse(conn net.Conn, data []byte) error {
	msgLen := len(data) + 4
	buf := make([]byte, 1+4+len(data))
	buf[0] = 'p'
	binary.BigEndian.PutUint32(buf[1:5], uint32(msgLen))
	copy(buf[5:], data)
	_, err := conn.Write(buf)
	return err
}

// readAuthMessage reads a PG Authentication message and verifies its auth subtype.
// Returns the payload after the 4-byte auth type field.
func readAuthMessage(conn net.Conn, expectedAuthType uint32) ([]byte, error) {
	// Read message type
	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, typeBuf); err != nil {
		return nil, fmt.Errorf("reading message type: %w", err)
	}

	if typeBuf[0] == 'E' {
		// ErrorResponse — read and return the error
		return nil, readAndParseError(conn)
	}

	if typeBuf[0] != 'R' {
		return nil, fmt.Errorf("expected Authentication message ('R'), got '%c'", typeBuf[0])
	}

	// Read length
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return nil, fmt.Errorf("reading message length: %w", err)
	}
	payloadLen := int(binary.BigEndian.Uint32(lenBuf)) - 4
	if payloadLen < 4 {
		return nil, fmt.Errorf("auth message too short: %d", payloadLen)
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return nil, fmt.Errorf("reading auth payload: %w", err)
	}

	authType := binary.BigEndian.Uint32(payload[:4])
	if authType != expectedAuthType {
		return nil, fmt.Errorf("expected auth type %d, got %d", expectedAuthType, authType)
	}

	return payload[4:], nil
}

// readAndParseError reads an ErrorResponse message body and returns an error.
func readAndParseError(conn net.Conn) error {
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return fmt.Errorf("reading error length: %w", err)
	}
	payloadLen := int(binary.BigEndian.Uint32(lenBuf)) - 4
	if payloadLen > 0 {
		payload := make([]byte, payloadLen)
		io.ReadFull(conn, payload)
		return fmt.Errorf("backend error: %s", parseErrorMessage(payload))
	}
	return fmt.Errorf("backend error (empty)")
}

// hmacSHA256 computes HMAC-SHA-256.
func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// sha256Sum computes SHA-256.
func sha256Sum(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

// xorBytes XORs two byte slices of equal length.
func xorBytes(a, b []byte) []byte {
	result := make([]byte, len(a))
	for i := range a {
		result[i] = a[i] ^ b[i]
	}
	return result
}
