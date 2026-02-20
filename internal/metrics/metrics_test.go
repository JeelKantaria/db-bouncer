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
	reg := prometheus.NewRegistry()

	c := &Collector{
		connectionsActive: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "test_connections_active", Help: "h"},
			[]string{"tenant", "db_type"},
		),
		connectionsIdle: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "test_connections_idle", Help: "h"},
			[]string{"tenant", "db_type"},
		),
		connectionsTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "test_connections_total", Help: "h"},
			[]string{"tenant", "db_type"},
		),
		connectionsWaiting: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "test_connections_waiting", Help: "h"},
			[]string{"tenant", "db_type"},
		),
		queryDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "test_query_duration_seconds", Help: "h", Buckets: prometheus.DefBuckets},
			[]string{"tenant", "db_type"},
		),
		tenantHealth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "test_tenant_health", Help: "h"},
			[]string{"tenant"},
		),
		poolExhausted: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "test_pool_exhausted_total", Help: "h"},
			[]string{"tenant"},
		),
	}

	reg.MustRegister(
		c.connectionsActive, c.connectionsIdle, c.connectionsTotal,
		c.connectionsWaiting, c.queryDuration, c.tenantHealth, c.poolExhausted,
	)

	return c, reg
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

func TestConnectionOpenedClosed(t *testing.T) {
	c, _ := newTestCollector(t)

	c.ConnectionOpened("tenant1", "postgres")
	c.ConnectionOpened("tenant1", "postgres")
	c.ConnectionOpened("tenant1", "postgres")

	val := getGaugeValue(c.connectionsActive.WithLabelValues("tenant1", "postgres"))
	if val != 3 {
		t.Errorf("expected active=3, got %v", val)
	}

	c.ConnectionClosed("tenant1", "postgres")
	val = getGaugeValue(c.connectionsActive.WithLabelValues("tenant1", "postgres"))
	if val != 2 {
		t.Errorf("expected active=2 after close, got %v", val)
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
		if f.GetName() == "test_query_duration_seconds" {
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
	c.ConnectionOpened("tenant1", "postgres")
	c.SetTenantHealth("tenant1", true)
	c.PoolExhausted("tenant1")
	c.UpdatePoolStats("tenant1", "postgres", 1, 2, 3, 0)

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

	c.ConnectionOpened("t1", "postgres")
	c.ConnectionOpened("t2", "mysql")
	c.ConnectionOpened("t2", "mysql")

	v1 := getGaugeValue(c.connectionsActive.WithLabelValues("t1", "postgres"))
	v2 := getGaugeValue(c.connectionsActive.WithLabelValues("t2", "mysql"))

	if v1 != 1 {
		t.Errorf("expected t1 active=1, got %v", v1)
	}
	if v2 != 2 {
		t.Errorf("expected t2 active=2, got %v", v2)
	}
}
