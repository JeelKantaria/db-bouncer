package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Collector holds all Prometheus metrics for DBBouncer.
type Collector struct {
	connectionsActive  *prometheus.GaugeVec
	connectionsIdle    *prometheus.GaugeVec
	connectionsTotal   *prometheus.GaugeVec
	connectionsWaiting *prometheus.GaugeVec
	queryDuration      *prometheus.HistogramVec
	tenantHealth       *prometheus.GaugeVec
	poolExhausted      *prometheus.CounterVec
}

// New creates and registers all Prometheus metrics.
func New() *Collector {
	c := &Collector{
		connectionsActive: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "dbbouncer_connections_active",
				Help: "Number of active connections per tenant",
			},
			[]string{"tenant", "db_type"},
		),
		connectionsIdle: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "dbbouncer_connections_idle",
				Help: "Number of idle connections per tenant",
			},
			[]string{"tenant", "db_type"},
		),
		connectionsTotal: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "dbbouncer_connections_total",
				Help: "Total number of connections per tenant",
			},
			[]string{"tenant", "db_type"},
		),
		connectionsWaiting: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "dbbouncer_connections_waiting",
				Help: "Number of goroutines waiting for a connection per tenant",
			},
			[]string{"tenant", "db_type"},
		),
		queryDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "dbbouncer_query_duration_seconds",
				Help:    "Duration of proxied sessions in seconds",
				Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
			},
			[]string{"tenant", "db_type"},
		),
		tenantHealth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "dbbouncer_tenant_health",
				Help: "Health status of tenant database (1=healthy, 0=unhealthy)",
			},
			[]string{"tenant"},
		),
		poolExhausted: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "dbbouncer_pool_exhausted_total",
				Help: "Total number of times the pool was exhausted per tenant",
			},
			[]string{"tenant"},
		),
	}

	prometheus.MustRegister(
		c.connectionsActive,
		c.connectionsIdle,
		c.connectionsTotal,
		c.connectionsWaiting,
		c.queryDuration,
		c.tenantHealth,
		c.poolExhausted,
	)

	return c
}

// ConnectionOpened increments the active connection gauge.
func (c *Collector) ConnectionOpened(tenant, dbType string) {
	c.connectionsActive.WithLabelValues(tenant, dbType).Inc()
}

// ConnectionClosed decrements the active connection gauge.
func (c *Collector) ConnectionClosed(tenant, dbType string) {
	c.connectionsActive.WithLabelValues(tenant, dbType).Dec()
}

// QueryDuration observes a session duration.
func (c *Collector) QueryDuration(tenant, dbType string, d time.Duration) {
	c.queryDuration.WithLabelValues(tenant, dbType).Observe(d.Seconds())
}

// SetTenantHealth sets the health gauge for a tenant.
func (c *Collector) SetTenantHealth(tenant string, healthy bool) {
	val := 0.0
	if healthy {
		val = 1.0
	}
	c.tenantHealth.WithLabelValues(tenant).Set(val)
}

// PoolExhausted increments the pool exhausted counter.
func (c *Collector) PoolExhausted(tenant string) {
	c.poolExhausted.WithLabelValues(tenant).Inc()
}

// UpdatePoolStats updates the pool gauge metrics from stats.
func (c *Collector) UpdatePoolStats(tenant, dbType string, active, idle, total, waiting int) {
	c.connectionsActive.WithLabelValues(tenant, dbType).Set(float64(active))
	c.connectionsIdle.WithLabelValues(tenant, dbType).Set(float64(idle))
	c.connectionsTotal.WithLabelValues(tenant, dbType).Set(float64(total))
	c.connectionsWaiting.WithLabelValues(tenant, dbType).Set(float64(waiting))
}

// RemoveTenant removes all metrics for a tenant.
func (c *Collector) RemoveTenant(tenant string) {
	c.connectionsActive.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.connectionsIdle.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.connectionsTotal.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.connectionsWaiting.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.tenantHealth.DeleteLabelValues(tenant)
	c.poolExhausted.DeleteLabelValues(tenant)
}
