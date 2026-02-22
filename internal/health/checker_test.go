package health

import (
	"testing"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

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
	c := NewChecker(newTestRouter(), nil)

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
	c := NewChecker(newTestRouter(), nil)

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
	c := NewChecker(newTestRouter(), nil)

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
	c := NewChecker(newTestRouter(), nil)

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
	c := NewChecker(newTestRouter(), nil)

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
	c := NewChecker(newTestRouter(), nil)

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
	c := NewChecker(newTestRouter(), nil)
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
	c := NewChecker(r, nil)

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
	c := NewChecker(r, nil)

	tc, _ := r.Resolve("pg")
	if c.pingTenant("pg", tc) {
		t.Error("expected postgres ping to fail on closed port")
	}

	tc, _ = r.Resolve("my")
	if c.pingTenant("my", tc) {
		t.Error("expected mysql ping to fail on closed port")
	}
}
