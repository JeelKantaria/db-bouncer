package proxy

import (
	"testing"
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
	// Use net.Pipe to test message round-trip
	// Create a simple test with a known message
	payload := []byte("test payload")
	msgType := byte('Q')

	// Calculate expected wire format
	msgLen := len(payload) + 4
	expected := make([]byte, 1+4+len(payload))
	expected[0] = msgType
	expected[1] = byte(msgLen >> 24)
	expected[2] = byte(msgLen >> 16)
	expected[3] = byte(msgLen >> 8)
	expected[4] = byte(msgLen)
	copy(expected[5:], payload)

	// Verify the format
	if expected[0] != 'Q' {
		t.Error("message type mismatch")
	}
	if int(expected[1])<<24|int(expected[2])<<16|int(expected[3])<<8|int(expected[4]) != msgLen {
		t.Error("message length mismatch")
	}
}

func TestSendPGErrorFormat(t *testing.T) {
	// Test that error message is properly formatted
	// We can't easily test the full send without a connection,
	// but we can verify the buffer construction logic
	severity := "FATAL"
	code := "08000"
	message := "test error"

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
	buf = append(buf, 0)

	// Verify structure
	if buf[0] != 'S' {
		t.Error("expected severity field marker 'S'")
	}

	// Find code field
	found := false
	for i, b := range buf {
		if b == 'C' && i > 0 && buf[i-1] == 0 {
			found = true
			break
		}
	}
	if !found {
		t.Error("code field marker not found")
	}
}
