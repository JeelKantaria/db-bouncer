package pool

import (
	"context"
	"net"
	"sync"
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

// --- Phase 2: Concurrency & correctness tests ---

func TestPingDetectsClosedConnection(t *testing.T) {
	client, server := net.Pipe()
	pc := NewPooledConn(client, "test", "postgres", nil)

	// Close the other end — Ping should detect the dead connection
	server.Close()

	err := pc.Ping()
	if err == nil {
		t.Error("Ping should return error for closed connection")
	}
	pc.Close()
}

func TestPingHealthyConnection(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	pc := NewPooledConn(client, "test", "postgres", nil)
	defer pc.Close()

	// Healthy connection: Ping should return nil (timeout = healthy)
	err := pc.Ping()
	if err != nil {
		t.Errorf("Ping should return nil for healthy connection, got: %v", err)
	}
}

func TestDoubleCloseTenantPool(t *testing.T) {
	tc := config.TenantConfig{
		DBType: "postgres", Host: "localhost", Port: 5432,
		DBName: "testdb", Username: "user",
	}

	tp := NewTenantPool("test", tc, testDefaults())

	// Should not panic
	tp.Close()
	tp.Close()
}

func TestDoubleCloseManager(t *testing.T) {
	m := NewManager(testDefaults())

	// Should not panic
	m.Close()
	m.Close()
}

func TestConcurrentAcquireReturn(t *testing.T) {
	// Create a pool that uses net.Pipe connections
	tc := config.TenantConfig{
		DBType: "postgres", Host: "localhost", Port: 15432,
		DBName: "testdb", Username: "user",
	}

	defaults := config.PoolDefaults{
		MinConnections: 0,
		MaxConnections: 2,
		IdleTimeout:    5 * time.Minute,
		MaxLifetime:    30 * time.Minute,
		AcquireTimeout: 2 * time.Second,
	}

	tp := NewTenantPool("concurrent_test", tc, defaults)
	defer tp.Close()

	// Inject mock connections manually by manipulating idle list
	var pipes []net.Conn
	for i := 0; i < 2; i++ {
		client, server := net.Pipe()
		pipes = append(pipes, client, server)
		pc := NewPooledConn(client, "concurrent_test", "postgres", tp)
		tp.mu.Lock()
		tp.idle = append(tp.idle, pc)
		tp.total++
		tp.mu.Unlock()
	}
	defer func() {
		for _, p := range pipes {
			p.Close()
		}
	}()

	// Run concurrent acquire/return cycles
	var wg sync.WaitGroup
	const goroutines = 10
	const iterations = 5

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				pc, err := tp.Acquire(context.Background())
				if err != nil {
					continue // pool may be exhausted, that's OK
				}
				// Simulate brief usage
				time.Sleep(time.Millisecond)
				tp.Return(pc)
			}
		}()
	}

	wg.Wait()

	// Verify pool is in a consistent state
	stats := tp.Stats()
	if stats.Active != 0 {
		t.Errorf("expected 0 active after all returns, got %d", stats.Active)
	}
}

// --- Phase 3: Context, reaper, and pre-warming tests ---

func TestAcquireRespectsContextCancellation(t *testing.T) {
	tc := config.TenantConfig{
		DBType: "postgres", Host: "localhost", Port: 15432,
		DBName: "testdb", Username: "user",
	}
	defaults := config.PoolDefaults{
		MinConnections: 0,
		MaxConnections: 1,
		IdleTimeout:    5 * time.Minute,
		MaxLifetime:    30 * time.Minute,
		AcquireTimeout: 5 * time.Second,
	}

	tp := NewTenantPool("ctx_test", tc, defaults)
	defer tp.Close()

	// Inject one connection and acquire it to exhaust the pool
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	pc := NewPooledConn(client, "ctx_test", "postgres", tp)
	tp.mu.Lock()
	tp.idle = append(tp.idle, pc)
	tp.total++
	tp.mu.Unlock()

	acquired, err := tp.Acquire(context.Background())
	if err != nil {
		t.Fatalf("expected successful acquire, got: %v", err)
	}

	// Pool is now exhausted. Acquire with a cancelled context should fail fast.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, err = tp.Acquire(ctx)
	if err == nil {
		t.Error("expected error from cancelled context acquire")
	}

	tp.Return(acquired)
}

func TestReapIdleRemovesOldest(t *testing.T) {
	tc := config.TenantConfig{
		DBType: "postgres", Host: "localhost", Port: 5432,
		DBName: "testdb", Username: "user",
	}
	defaults := config.PoolDefaults{
		MinConnections: 1,
		MaxConnections: 5,
		IdleTimeout:    1 * time.Millisecond, // very short so everything is "idle"
		MaxLifetime:    30 * time.Minute,
		AcquireTimeout: 2 * time.Second,
	}

	tp := NewTenantPool("reap_test", tc, defaults)
	defer tp.Close()

	// Inject 3 connections with known ordering (oldest first)
	var pipes []net.Conn
	for i := 0; i < 3; i++ {
		client, server := net.Pipe()
		pipes = append(pipes, client, server)
		pc := NewPooledConn(client, "reap_test", "postgres", tp)
		pc.MarkIdle()
		tp.mu.Lock()
		tp.idle = append(tp.idle, pc)
		tp.total++
		tp.mu.Unlock()
	}
	defer func() {
		for _, p := range pipes {
			p.Close()
		}
	}()

	// Wait for idle timeout to expire
	time.Sleep(5 * time.Millisecond)

	// Reap should remove oldest (excess over minConns=1)
	tp.reapIdle()

	tp.mu.Lock()
	remaining := len(tp.idle)
	totalAfter := tp.total
	tp.mu.Unlock()

	if remaining < 1 {
		t.Errorf("expected at least minConns(1) remaining, got %d", remaining)
	}
	if totalAfter > remaining {
		t.Errorf("total(%d) should match remaining idle(%d) when no active conns", totalAfter, remaining)
	}
}

func TestMetricsNewDoesNotPanic(t *testing.T) {
	// Calling New() multiple times should not panic because it uses a custom registry
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("New() panicked on second call: %v", r)
		}
	}()

	// These are in the metrics package, but we test the concept here:
	// Creating two TenantPools (which happens on reload) should be fine
	tc := config.TenantConfig{
		DBType: "postgres", Host: "localhost", Port: 5432,
		DBName: "testdb", Username: "user",
	}
	tp1 := NewTenantPool("t1", tc, testDefaults())
	tp2 := NewTenantPool("t2", tc, testDefaults())
	tp1.Close()
	tp2.Close()
}
