package proxy

import (
	"net"
	"testing"
	"time"
)

func TestParseTenantFromOptions(t *testing.T) {
	tests := []struct {
		options string
		want    string
	}{
		{"-c tenant_id=acme_corp", "acme_corp"},
		{"-c tenant_id=test123", "test123"},
		{"tenant_id=direct", "direct"},
		{"-c something_else=foo", ""},
		{"", ""},
		{"-c tenant_id=abc -c other=xyz", "abc"},
	}

	for _, tt := range tests {
		t.Run(tt.options, func(t *testing.T) {
			got := parseTenantFromOptions(tt.options)
			if got != tt.want {
				t.Errorf("parseTenantFromOptions(%q) = %q, want %q", tt.options, got, tt.want)
			}
		})
	}
}

func TestWriteReadPGMessage(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	payload := []byte("SELECT 1")
	go func() {
		writePGMessage(server, pgMsgQuery, payload)
	}()

	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	msgType, received, err := readPGMessage(client)
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

func TestWriteReadMySQLPacket(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	payload := []byte{mysqlComQuery}
	payload = append(payload, "SELECT 1"...)

	go func() {
		writeMySQLPacket(server, payload, 0)
	}()

	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	received, seqNum, err := readMySQLPacket(client)
	if err != nil {
		t.Fatalf("readMySQLPacket error: %v", err)
	}
	if seqNum != 0 {
		t.Errorf("expected seq 0, got %d", seqNum)
	}
	if received[0] != mysqlComQuery {
		t.Errorf("expected COM_QUERY (0x03), got 0x%02x", received[0])
	}
	if string(received[1:]) != "SELECT 1" {
		t.Errorf("expected 'SELECT 1', got %q", received[1:])
	}
}

func TestSendPGErrorFormat(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	h := &PostgresHandler{}

	go func() {
		h.sendPGError(server, "FATAL", "08000", "test error message")
		server.Close()
	}()

	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	msgType, payload, err := readPGMessage(client)
	if err != nil {
		t.Fatalf("readPGMessage error: %v", err)
	}
	if msgType != pgMsgErrorResponse {
		t.Errorf("expected ErrorResponse message type, got %c", msgType)
	}

	// Parse the error fields from the payload
	var severity, code, message string
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
		switch fieldType {
		case 'S':
			severity = string(payload[i:end])
		case 'C':
			code = string(payload[i:end])
		case 'M':
			message = string(payload[i:end])
		}
		i = end
	}

	if severity != "FATAL" {
		t.Errorf("expected severity FATAL, got %q", severity)
	}
	if code != "08000" {
		t.Errorf("expected code 08000, got %q", code)
	}
	if message != "test error message" {
		t.Errorf("expected message 'test error message', got %q", message)
	}
}
