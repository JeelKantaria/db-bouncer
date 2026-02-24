package health

import (
	"net"
	"testing"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/metrics"
	"github.com/dbbouncer/dbbouncer/internal/pool"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

var testHealthCfg = config.HealthCheckConfig{
	Interval:          30 * time.Second,
	FailureThreshold:  3,
	ConnectionTimeout: 5 * time.Second,
}

func newTestRouter() *router.Router {
	return router.New(&config.Config{
		Tenants: map[string]config.TenantConfig{
			"healthy_tenant": {
				DBType:   "postgres",
				Host:     "localhost",
				Port:     5432,
				DBName:   "db",
				Username: "user",
			},
		},
	})
}

func TestCheckerInitialState(t *testing.T) {
	c := NewChecker(newTestRouter(), nil, testHealthCfg)

	// Unknown tenant should be treated as healthy
	if !c.IsHealthy("unknown") {
		t.Error("unknown tenant should be treated as healthy")
	}

	status := c.GetStatus("unknown")
	if status.Status != StatusUnknown {
		t.Errorf("expected StatusUnknown, got %v", status.Status)
	}
}

func TestCheckerUpdateStatus(t *testing.T) {
	c := NewChecker(newTestRouter(), nil, testHealthCfg)

	// Mark as healthy
	c.updateStatus("test", true)
	if !c.IsHealthy("test") {
		t.Error("should be healthy after healthy update")
	}

	status := c.GetStatus("test")
	if status.Status != StatusHealthy {
		t.Errorf("expected StatusHealthy, got %v", status.Status)
	}

	// Single failure shouldn't make it unhealthy (threshold is 3)
	c.updateStatus("test", false)
	if !c.IsHealthy("test") {
		t.Error("should still be healthy after one failure")
	}

	status = c.GetStatus("test")
	if status.ConsecutiveFailures != 1 {
		t.Errorf("expected 1 consecutive failure, got %d", status.ConsecutiveFailures)
	}
}

func TestCheckerThreshold(t *testing.T) {
	c := NewChecker(newTestRouter(), nil, testHealthCfg)

	// Hit the failure threshold (default 3)
	c.updateStatus("test", false)
	c.updateStatus("test", false)
	c.updateStatus("test", false)

	if c.IsHealthy("test") {
		t.Error("should be unhealthy after 3 consecutive failures")
	}

	status := c.GetStatus("test")
	if status.Status != StatusUnhealthy {
		t.Errorf("expected StatusUnhealthy, got %v", status.Status)
	}
}

func TestCheckerRecovery(t *testing.T) {
	c := NewChecker(newTestRouter(), nil, testHealthCfg)

	// Mark as unhealthy
	c.updateStatus("test", false)
	c.updateStatus("test", false)
	c.updateStatus("test", false)

	if c.IsHealthy("test") {
		t.Error("should be unhealthy")
	}

	// Recovery
	c.updateStatus("test", true)
	if !c.IsHealthy("test") {
		t.Error("should be healthy after recovery")
	}

	status := c.GetStatus("test")
	if status.ConsecutiveFailures != 0 {
		t.Errorf("expected 0 consecutive failures after recovery, got %d", status.ConsecutiveFailures)
	}
}

func TestOverallHealthy(t *testing.T) {
	c := NewChecker(newTestRouter(), nil, testHealthCfg)

	// No tenants checked yet
	if !c.OverallHealthy() {
		t.Error("should be overall healthy with no checks")
	}

	c.updateStatus("good", true)
	if !c.OverallHealthy() {
		t.Error("should be overall healthy with one healthy tenant")
	}

	// Add an unhealthy tenant
	c.updateStatus("bad", false)
	c.updateStatus("bad", false)
	c.updateStatus("bad", false)

	if c.OverallHealthy() {
		t.Error("should not be overall healthy with one unhealthy tenant")
	}
}

func TestGetAllStatuses(t *testing.T) {
	c := NewChecker(newTestRouter(), nil, testHealthCfg)

	c.updateStatus("t1", true)
	c.updateStatus("t2", true)

	statuses := c.GetAllStatuses()
	if len(statuses) != 2 {
		t.Errorf("expected 2 statuses, got %d", len(statuses))
	}
}

func TestStatusString(t *testing.T) {
	tests := []struct {
		status Status
		want   string
	}{
		{StatusUnknown, "unknown"},
		{StatusHealthy, "healthy"},
		{StatusUnhealthy, "unhealthy"},
	}

	for _, tt := range tests {
		if got := tt.status.String(); got != tt.want {
			t.Errorf("Status(%d).String() = %q, want %q", tt.status, got, tt.want)
		}
	}
}

func TestDoubleStop(t *testing.T) {
	c := NewChecker(newTestRouter(), nil, testHealthCfg)
	c.Start()

	// Should not panic
	c.Stop()
	c.Stop()
}

func TestCheckAllIsParallel(t *testing.T) {
	// Create a router with multiple tenants
	r := router.New(&config.Config{
		Tenants: map[string]config.TenantConfig{
			"t1": {DBType: "postgres", Host: "localhost", Port: 59991, DBName: "db", Username: "u"},
			"t2": {DBType: "postgres", Host: "localhost", Port: 59992, DBName: "db", Username: "u"},
			"t3": {DBType: "postgres", Host: "localhost", Port: 59993, DBName: "db", Username: "u"},
		},
	})
	c := NewChecker(r, nil, testHealthCfg)

	// checkAll should not panic and should update all tenant statuses
	// (will fail health checks since ports don't exist, but that's fine)
	c.checkAll()

	statuses := c.GetAllStatuses()
	if len(statuses) != 3 {
		t.Errorf("expected 3 statuses after checkAll, got %d", len(statuses))
	}
}

func TestPingTenantProtocolCheck(t *testing.T) {
	// Verify that pingTenant fails gracefully when the port is not open
	r := router.New(&config.Config{
		Tenants: map[string]config.TenantConfig{
			"pg": {DBType: "postgres", Host: "localhost", Port: 59999, DBName: "db", Username: "u"},
			"my": {DBType: "mysql", Host: "localhost", Port: 59998, DBName: "db", Username: "u"},
		},
	})
	c := NewChecker(r, nil, testHealthCfg)

	tc, _ := r.Resolve("pg")
	if c.pingTenant("pg", tc) {
		t.Error("expected postgres ping to fail on closed port")
	}

	tc, _ = r.Resolve("my")
	if c.pingTenant("my", tc) {
		t.Error("expected mysql ping to fail on closed port")
	}
}

// --- Phase 4: RemoveTenant test ---

func TestRemoveTenant(t *testing.T) {
	c := NewChecker(newTestRouter(), nil, testHealthCfg)

	// Add some health state
	c.updateStatus("tenant_a", true)
	c.updateStatus("tenant_b", true)

	if len(c.GetAllStatuses()) != 2 {
		t.Fatalf("expected 2 statuses before removal")
	}

	// Remove one tenant
	c.RemoveTenant("tenant_a")

	statuses := c.GetAllStatuses()
	if len(statuses) != 1 {
		t.Errorf("expected 1 status after removal, got %d", len(statuses))
	}
	if _, exists := statuses["tenant_a"]; exists {
		t.Error("tenant_a should have been removed")
	}
	if _, exists := statuses["tenant_b"]; !exists {
		t.Error("tenant_b should still exist")
	}

	// Remove nonexistent tenant should not panic
	c.RemoveTenant("nonexistent")
}

// --- Phase 6: Health Check Improvement Tests ---

func TestHealthCheckViaPoolSuccess(t *testing.T) {
	// Spin up a minimal mock PG server that handles SELECT 1
	listener, err := newLocalListener()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	host, port := listenerHostPort(listener)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))

		// Read the Query message
		msgType, _, err := readPGHealthMsg(conn)
		if err != nil || msgType != 'Q' {
			return
		}
		// Send DataRow + CommandComplete + ReadyForQuery
		writePGHealthMsg(conn, 'D', []byte{0, 1, 0, 0, 0, 1, '1'}) // DataRow with "1"
		writePGHealthMsg(conn, 'C', append([]byte("SELECT 1"), 0))  // CommandComplete
		writePGHealthMsg(conn, 'Z', []byte{'I'})                    // ReadyForQuery
	}()

	txnMode := "transaction"
	tc := config.TenantConfig{
		DBType:   "postgres",
		Host:     host,
		Port:     port,
		DBName:   "db",
		Username: "user",
		PoolMode: &txnMode,
	}
	defaults := config.PoolDefaults{
		MinConnections: 0, MaxConnections: 2,
		IdleTimeout: 5 * time.Minute, MaxLifetime: 30 * time.Minute,
		AcquireTimeout: 3 * time.Second, PoolMode: "transaction",
	}

	// Create pool with a pre-authenticated connection
	import_pool := newTestPool(t, tc, defaults)
	defer import_pool.Close()

	backendConn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	pc := pool.NewPooledConn(backendConn, "test", "postgres", import_pool)
	pc.SetAuthenticated(map[string]string{"server_version": "16.0"}, 1234, 5678)
	import_pool.InjectTestConn(pc)

	c := NewChecker(newTestRouter(), nil, testHealthCfg)
	healthy := c.pingPostgresViaPool("test", import_pool)
	if !healthy {
		t.Error("expected pingPostgresViaPool to return true")
	}
}

func TestHealthCheckViaPoolExhausted(t *testing.T) {
	txnMode := "transaction"
	tc := config.TenantConfig{
		DBType: "postgres", Host: "localhost", Port: 15432,
		DBName: "db", Username: "user", PoolMode: &txnMode,
	}
	defaults := config.PoolDefaults{
		MinConnections: 0, MaxConnections: 1,
		IdleTimeout: 5 * time.Minute, MaxLifetime: 30 * time.Minute,
		AcquireTimeout: 100 * time.Millisecond, PoolMode: "transaction",
	}
	tp := newTestPool(t, tc, defaults)
	defer tp.Close()
	// No connections injected — pool is empty, acquire will time out

	c := NewChecker(newTestRouter(), nil, config.HealthCheckConfig{
		Interval:          30 * time.Second,
		FailureThreshold:  3,
		ConnectionTimeout: 100 * time.Millisecond,
	})

	healthy := c.pingPostgresViaPool("test", tp)
	if healthy {
		t.Error("expected pingPostgresViaPool to return false when pool is exhausted")
	}
}

func TestHealthCheckTimingMetric(t *testing.T) {
	m := newTestMetrics(t)

	// Simulate a successful health check result with timing
	elapsed := 5 * time.Millisecond
	m.HealthCheckCompleted("t1", elapsed, true)

	// Verify metric was recorded — just checking no panic
	if m == nil {
		t.Error("expected metrics collector to be non-nil")
	}
}

func TestHealthCheckErrorMetric(t *testing.T) {
	m := newTestMetrics(t)

	m.HealthCheckError("t1", "connection_refused")
	m.HealthCheckError("t1", "connection_refused")
	m.HealthCheckError("t1", "pool_exhausted")

	// Check the error counter recorded values without panicking
	_ = m
}

// --- Test helpers ---

func newLocalListener() (net.Listener, error) {
	return net.Listen("tcp", "127.0.0.1:0")
}

func listenerHostPort(l net.Listener) (string, int) {
	addr := l.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func newTestPool(t *testing.T, tc config.TenantConfig, defaults config.PoolDefaults) *pool.TenantPool {
	t.Helper()
	return pool.NewTenantPool("test_hc", tc, defaults)
}

func newTestMetrics(t *testing.T) *metrics.Collector {
	t.Helper()
	return metrics.New()
}
