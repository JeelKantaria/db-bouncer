package pool

import (
	"net"
	"testing"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
)

func testDefaults() config.PoolDefaults {
	return config.PoolDefaults{
		MinConnections: 1,
		MaxConnections: 5,
		IdleTimeout:    1 * time.Minute,
		MaxLifetime:    5 * time.Minute,
		AcquireTimeout: 2 * time.Second,
	}
}

func TestManagerGetOrCreate(t *testing.T) {
	m := NewManager(testDefaults())
	defer m.Close()

	tc := config.TenantConfig{
		DBType:   "postgres",
		Host:     "localhost",
		Port:     5432,
		DBName:   "testdb",
		Username: "user",
	}

	// First call creates pool
	p1 := m.GetOrCreate("tenant_1", tc)
	if p1 == nil {
		t.Fatal("expected non-nil pool")
	}

	// Second call returns same pool
	p2 := m.GetOrCreate("tenant_1", tc)
	if p1 != p2 {
		t.Error("expected same pool instance")
	}
}

func TestManagerRemove(t *testing.T) {
	m := NewManager(testDefaults())
	defer m.Close()

	tc := config.TenantConfig{
		DBType:   "postgres",
		Host:     "localhost",
		Port:     5432,
		DBName:   "testdb",
		Username: "user",
	}

	m.GetOrCreate("tenant_1", tc)

	if !m.Remove("tenant_1") {
		t.Error("Remove should return true for existing pool")
	}

	if m.Remove("tenant_1") {
		t.Error("Remove should return false for already-removed pool")
	}
}

func TestManagerAllStats(t *testing.T) {
	m := NewManager(testDefaults())
	defer m.Close()

	tc := config.TenantConfig{
		DBType:   "postgres",
		Host:     "localhost",
		Port:     5432,
		DBName:   "testdb",
		Username: "user",
	}

	m.GetOrCreate("tenant_1", tc)
	m.GetOrCreate("tenant_2", tc)

	stats := m.AllStats()
	if len(stats) != 2 {
		t.Errorf("expected 2 stats entries, got %d", len(stats))
	}
}

func TestPooledConnStates(t *testing.T) {
	// Create a pipe to simulate a connection
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	pc := NewPooledConn(client, "test_tenant", "postgres", nil)

	if pc.State() != ConnStateIdle {
		t.Error("new connection should be idle")
	}

	pc.MarkActive()
	if pc.State() != ConnStateActive {
		t.Error("should be active after MarkActive")
	}

	pc.MarkIdle()
	if pc.State() != ConnStateIdle {
		t.Error("should be idle after MarkIdle")
	}

	if pc.TenantID() != "test_tenant" {
		t.Errorf("expected tenant_id test_tenant, got %s", pc.TenantID())
	}

	if pc.DBType() != "postgres" {
		t.Errorf("expected db_type postgres, got %s", pc.DBType())
	}
}

func TestPooledConnExpiry(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	pc := NewPooledConn(client, "test", "postgres", nil)

	if pc.IsExpired(5 * time.Minute) {
		t.Error("new connection should not be expired")
	}

	if pc.IsExpired(0) {
		t.Error("zero max lifetime should never expire")
	}

	// Test with very short lifetime - sleep to ensure time has passed
	time.Sleep(2 * time.Millisecond)
	if !pc.IsExpired(1 * time.Millisecond) {
		t.Error("connection should be expired with 1ms lifetime after 2ms sleep")
	}
}

func TestPooledConnIdle(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	pc := NewPooledConn(client, "test", "postgres", nil)
	pc.MarkIdle()

	// Just created, should not be idle yet
	if pc.IsIdle(5 * time.Minute) {
		t.Error("freshly used connection should not be idle")
	}

	// Should be idle with very short timeout
	time.Sleep(2 * time.Millisecond)
	if !pc.IsIdle(1 * time.Millisecond) {
		t.Error("connection should be idle with 1ms timeout")
	}
}

func TestTenantPoolStats(t *testing.T) {
	tc := config.TenantConfig{
		DBType:   "postgres",
		Host:     "localhost",
		Port:     5432,
		DBName:   "testdb",
		Username: "user",
	}

	tp := NewTenantPool("test_tenant", tc, testDefaults())
	defer tp.Close()

	stats := tp.Stats()
	if stats.TenantID != "test_tenant" {
		t.Errorf("expected tenant_id test_tenant, got %s", stats.TenantID)
	}
	if stats.Active != 0 {
		t.Errorf("expected 0 active, got %d", stats.Active)
	}
	if stats.MaxConns != 5 {
		t.Errorf("expected max conns 5, got %d", stats.MaxConns)
	}
}

func TestManagerTenantStats(t *testing.T) {
	m := NewManager(testDefaults())
	defer m.Close()

	// Stats for nonexistent tenant
	_, ok := m.TenantStats("nonexistent")
	if ok {
		t.Error("expected false for nonexistent tenant")
	}

	tc := config.TenantConfig{
		DBType:   "postgres",
		Host:     "localhost",
		Port:     5432,
		DBName:   "testdb",
		Username: "user",
	}
	m.GetOrCreate("tenant_1", tc)

	stats, ok := m.TenantStats("tenant_1")
	if !ok {
		t.Error("expected true for existing tenant")
	}
	if stats.TenantID != "tenant_1" {
		t.Errorf("expected tenant_1, got %s", stats.TenantID)
	}
}
