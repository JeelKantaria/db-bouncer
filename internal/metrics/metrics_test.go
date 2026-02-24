package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// newTestCollector creates a Collector registered with a fresh registry
// so tests don't conflict with each other or with the default registry.
func newTestCollector(t *testing.T) (*Collector, *prometheus.Registry) {
	t.Helper()
	c := New()
	return c, c.Registry
}

func getGaugeValue(g prometheus.Gauge) float64 {
	m := &dto.Metric{}
	g.Write(m)
	return m.GetGauge().GetValue()
}

func getCounterValue(c prometheus.Counter) float64 {
	m := &dto.Metric{}
	c.Write(m)
	return m.GetCounter().GetValue()
}

func TestUpdatePoolStatsAuthority(t *testing.T) {
	c, _ := newTestCollector(t)

	// UpdatePoolStats is the sole authority for connection gauges.
	c.UpdatePoolStats("tenant1", "postgres", 3, 5, 8, 1)

	val := getGaugeValue(c.connectionsActive.WithLabelValues("tenant1", "postgres"))
	if val != 3 {
		t.Errorf("expected active=3, got %v", val)
	}

	// A second call replaces (not increments) the value
	c.UpdatePoolStats("tenant1", "postgres", 2, 4, 6, 0)
	val = getGaugeValue(c.connectionsActive.WithLabelValues("tenant1", "postgres"))
	if val != 2 {
		t.Errorf("expected active=2 after update, got %v", val)
	}
}

func TestQueryDuration(t *testing.T) {
	c, reg := newTestCollector(t)

	c.QueryDuration("tenant1", "postgres", 100*time.Millisecond)
	c.QueryDuration("tenant1", "postgres", 200*time.Millisecond)

	// Verify histogram was observed by gathering metrics
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	var found bool
	for _, f := range families {
		if f.GetName() == "dbbouncer_query_duration_seconds" {
			found = true
			m := f.GetMetric()
			if len(m) == 0 {
				t.Fatal("no metric samples")
			}
			if m[0].GetHistogram().GetSampleCount() != 2 {
				t.Errorf("expected 2 samples, got %d", m[0].GetHistogram().GetSampleCount())
			}
		}
	}
	if !found {
		t.Error("query duration metric not found")
	}
}

func TestSetTenantHealth(t *testing.T) {
	c, _ := newTestCollector(t)

	c.SetTenantHealth("tenant1", true)
	val := getGaugeValue(c.tenantHealth.WithLabelValues("tenant1"))
	if val != 1 {
		t.Errorf("expected health=1 (healthy), got %v", val)
	}

	c.SetTenantHealth("tenant1", false)
	val = getGaugeValue(c.tenantHealth.WithLabelValues("tenant1"))
	if val != 0 {
		t.Errorf("expected health=0 (unhealthy), got %v", val)
	}
}

func TestPoolExhausted(t *testing.T) {
	c, _ := newTestCollector(t)

	c.PoolExhausted("tenant1")
	c.PoolExhausted("tenant1")
	c.PoolExhausted("tenant1")

	val := getCounterValue(c.poolExhausted.WithLabelValues("tenant1"))
	if val != 3 {
		t.Errorf("expected exhausted=3, got %v", val)
	}
}

func TestUpdatePoolStats(t *testing.T) {
	c, _ := newTestCollector(t)

	c.UpdatePoolStats("tenant1", "postgres", 5, 10, 15, 2)

	if v := getGaugeValue(c.connectionsActive.WithLabelValues("tenant1", "postgres")); v != 5 {
		t.Errorf("expected active=5, got %v", v)
	}
	if v := getGaugeValue(c.connectionsIdle.WithLabelValues("tenant1", "postgres")); v != 10 {
		t.Errorf("expected idle=10, got %v", v)
	}
	if v := getGaugeValue(c.connectionsTotal.WithLabelValues("tenant1", "postgres")); v != 15 {
		t.Errorf("expected total=15, got %v", v)
	}
	if v := getGaugeValue(c.connectionsWaiting.WithLabelValues("tenant1", "postgres")); v != 2 {
		t.Errorf("expected waiting=2, got %v", v)
	}
}

func TestRemoveTenant(t *testing.T) {
	c, reg := newTestCollector(t)

	// Set some metrics for tenant
	c.UpdatePoolStats("tenant1", "postgres", 1, 2, 3, 0)
	c.SetTenantHealth("tenant1", true)
	c.PoolExhausted("tenant1")

	// Remove tenant
	c.RemoveTenant("tenant1")

	// Verify metrics are gone by gathering
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range families {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "tenant" && l.GetValue() == "tenant1" {
					t.Errorf("metric %s still has tenant1 label after removal", f.GetName())
				}
			}
		}
	}
}

func TestMultipleTenants(t *testing.T) {
	c, _ := newTestCollector(t)

	c.UpdatePoolStats("t1", "postgres", 1, 0, 1, 0)
	c.UpdatePoolStats("t2", "mysql", 2, 1, 3, 0)

	v1 := getGaugeValue(c.connectionsActive.WithLabelValues("t1", "postgres"))
	v2 := getGaugeValue(c.connectionsActive.WithLabelValues("t2", "mysql"))

	if v1 != 1 {
		t.Errorf("expected t1 active=1, got %v", v1)
	}
	if v2 != 2 {
		t.Errorf("expected t2 active=2, got %v", v2)
	}
}

func TestNewDoesNotPanicOnMultipleCalls(t *testing.T) {
	// Calling New() multiple times should not panic because each creates
	// its own registry instead of using the global default.
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("New() panicked on repeated calls: %v", r)
		}
	}()

	c1 := New()
	c2 := New()

	// Both should work independently
	c1.UpdatePoolStats("t1", "postgres", 1, 0, 1, 0)
	c2.UpdatePoolStats("t1", "postgres", 2, 0, 2, 0)

	v1 := getGaugeValue(c1.connectionsActive.WithLabelValues("t1", "postgres"))
	v2 := getGaugeValue(c2.connectionsActive.WithLabelValues("t1", "postgres"))

	if v1 != 1 {
		t.Errorf("c1 expected active=1, got %v", v1)
	}
	if v2 != 2 {
		t.Errorf("c2 expected active=2, got %v", v2)
	}
}

// --- Transaction-Mode Metrics Tests ---

func TestTransactionCompleted(t *testing.T) {
	c, reg := newTestCollector(t)

	c.TransactionCompleted("t1", "postgres", 50*time.Millisecond)
	c.TransactionCompleted("t1", "postgres", 100*time.Millisecond)

	val := getCounterValue(c.transactionsTotal.WithLabelValues("t1", "postgres"))
	if val != 2 {
		t.Errorf("expected transactionsTotal=2, got %v", val)
	}

	// Check histogram
	families, _ := reg.Gather()
	for _, f := range families {
		if f.GetName() == "dbbouncer_transaction_duration_seconds" {
			m := f.GetMetric()
			if len(m) > 0 && m[0].GetHistogram().GetSampleCount() != 2 {
				t.Errorf("expected 2 duration samples, got %d", m[0].GetHistogram().GetSampleCount())
			}
		}
	}
}

func TestAcquireDuration(t *testing.T) {
	c, reg := newTestCollector(t)

	c.AcquireDuration("t1", "postgres", 5*time.Millisecond)

	families, _ := reg.Gather()
	var found bool
	for _, f := range families {
		if f.GetName() == "dbbouncer_acquire_duration_seconds" {
			found = true
			m := f.GetMetric()
			if len(m) > 0 && m[0].GetHistogram().GetSampleCount() != 1 {
				t.Errorf("expected 1 acquire sample, got %d", m[0].GetHistogram().GetSampleCount())
			}
		}
	}
	if !found {
		t.Error("acquire duration metric not found")
	}
}

func TestSessionPinned(t *testing.T) {
	c, _ := newTestCollector(t)

	c.SessionPinned("t1", "listen command")
	c.SessionPinned("t1", "listen command")
	c.SessionPinned("t1", "named prepared statement")

	val := getCounterValue(c.sessionPinsTotal.WithLabelValues("t1", "listen command"))
	if val != 2 {
		t.Errorf("expected listen pins=2, got %v", val)
	}
	val = getCounterValue(c.sessionPinsTotal.WithLabelValues("t1", "named prepared statement"))
	if val != 1 {
		t.Errorf("expected prepared stmt pins=1, got %v", val)
	}
}

func TestBackendReset(t *testing.T) {
	c, _ := newTestCollector(t)

	c.BackendReset("t1", true)
	c.BackendReset("t1", true)
	c.BackendReset("t1", false)

	successVal := getCounterValue(c.backendResetsTotal.WithLabelValues("t1", "success"))
	if successVal != 2 {
		t.Errorf("expected reset success=2, got %v", successVal)
	}
	failVal := getCounterValue(c.backendResetsTotal.WithLabelValues("t1", "failure"))
	if failVal != 1 {
		t.Errorf("expected reset failure=1, got %v", failVal)
	}
}

func TestDirtyDisconnect(t *testing.T) {
	c, _ := newTestCollector(t)

	c.DirtyDisconnect("t1")
	c.DirtyDisconnect("t1")

	val := getCounterValue(c.dirtyDisconnects.WithLabelValues("t1"))
	if val != 2 {
		t.Errorf("expected dirty disconnects=2, got %v", val)
	}
}
