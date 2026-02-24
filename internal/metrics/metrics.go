package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Collector holds all Prometheus metrics for DBBouncer.
type Collector struct {
	Registry           *prometheus.Registry
	connectionsActive  *prometheus.GaugeVec
	connectionsIdle    *prometheus.GaugeVec
	connectionsTotal   *prometheus.GaugeVec
	connectionsWaiting *prometheus.GaugeVec
	queryDuration      *prometheus.HistogramVec
	tenantHealth       *prometheus.GaugeVec
	poolExhausted      *prometheus.CounterVec

	// Health check metrics
	healthCheckDuration *prometheus.HistogramVec
	healthCheckErrors   *prometheus.CounterVec

	// Transaction-mode metrics
	transactionsTotal    *prometheus.CounterVec
	transactionDuration  *prometheus.HistogramVec
	acquireDuration      *prometheus.HistogramVec
	sessionPinsTotal     *prometheus.CounterVec
	backendResetsTotal   *prometheus.CounterVec
	dirtyDisconnects     *prometheus.CounterVec
}

// New creates and registers all Prometheus metrics using a custom registry.
// Safe to call multiple times (e.g., in tests or on config reload) — each call
// creates an independent registry that doesn't conflict with others.
func New() *Collector {
	reg := prometheus.NewRegistry()

	c := &Collector{
		Registry: reg,
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

		healthCheckDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "dbbouncer_health_check_duration_seconds",
				Help:    "Duration of health check probes",
				Buckets: prometheus.ExponentialBuckets(0.001, 2, 12),
			},
			[]string{"tenant", "status"},
		),
		healthCheckErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "dbbouncer_health_check_errors_total",
				Help: "Health check errors by type",
			},
			[]string{"tenant", "error_type"},
		),

		transactionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "dbbouncer_transactions_total",
				Help: "Total completed transactions (transaction-mode pooling)",
			},
			[]string{"tenant", "db_type"},
		),
		transactionDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "dbbouncer_transaction_duration_seconds",
				Help:    "Duration from backend acquire to return per transaction",
				Buckets: prometheus.ExponentialBuckets(0.0005, 2, 16),
			},
			[]string{"tenant", "db_type"},
		),
		acquireDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "dbbouncer_acquire_duration_seconds",
				Help:    "Time waiting for pool.Acquire()",
				Buckets: prometheus.ExponentialBuckets(0.0001, 2, 14),
			},
			[]string{"tenant", "db_type"},
		),
		sessionPinsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "dbbouncer_session_pins_total",
				Help: "Session pin events in transaction-mode pooling",
			},
			[]string{"tenant", "reason"},
		),
		backendResetsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "dbbouncer_backend_resets_total",
				Help: "Backend DISCARD ALL reset results",
			},
			[]string{"tenant", "status"},
		),
		dirtyDisconnects: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "dbbouncer_dirty_disconnects_total",
				Help: "Client disconnects mid-transaction requiring ROLLBACK",
			},
			[]string{"tenant"},
		),
	}

	reg.MustRegister(
		c.connectionsActive,
		c.connectionsIdle,
		c.connectionsTotal,
		c.connectionsWaiting,
		c.queryDuration,
		c.tenantHealth,
		c.poolExhausted,
		c.healthCheckDuration,
		c.healthCheckErrors,
		c.transactionsTotal,
		c.transactionDuration,
		c.acquireDuration,
		c.sessionPinsTotal,
		c.backendResetsTotal,
		c.dirtyDisconnects,
	)

	return c
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

// HealthCheckCompleted records a health check probe duration and result.
func (c *Collector) HealthCheckCompleted(tenant string, d time.Duration, healthy bool) {
	status := "healthy"
	if !healthy {
		status = "unhealthy"
	}
	c.healthCheckDuration.WithLabelValues(tenant, status).Observe(d.Seconds())
}

// HealthCheckError records a health check error by type.
func (c *Collector) HealthCheckError(tenant, errorType string) {
	c.healthCheckErrors.WithLabelValues(tenant, errorType).Inc()
}

// TransactionCompleted records a completed transaction and its duration.
func (c *Collector) TransactionCompleted(tenant, dbType string, d time.Duration) {
	c.transactionsTotal.WithLabelValues(tenant, dbType).Inc()
	c.transactionDuration.WithLabelValues(tenant, dbType).Observe(d.Seconds())
}

// AcquireDuration observes the time spent waiting for a pool connection.
func (c *Collector) AcquireDuration(tenant, dbType string, d time.Duration) {
	c.acquireDuration.WithLabelValues(tenant, dbType).Observe(d.Seconds())
}

// SessionPinned increments the session pin counter with the given reason.
func (c *Collector) SessionPinned(tenant, reason string) {
	c.sessionPinsTotal.WithLabelValues(tenant, reason).Inc()
}

// BackendReset records a DISCARD ALL result (success or failure).
func (c *Collector) BackendReset(tenant string, success bool) {
	status := "success"
	if !success {
		status = "failure"
	}
	c.backendResetsTotal.WithLabelValues(tenant, status).Inc()
}

// DirtyDisconnect increments the dirty disconnect counter.
func (c *Collector) DirtyDisconnect(tenant string) {
	c.dirtyDisconnects.WithLabelValues(tenant).Inc()
}

// RemoveTenant removes all metrics for a tenant.
func (c *Collector) RemoveTenant(tenant string) {
	c.connectionsActive.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.connectionsIdle.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.connectionsTotal.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.connectionsWaiting.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.tenantHealth.DeleteLabelValues(tenant)
	c.poolExhausted.DeleteLabelValues(tenant)
	c.healthCheckDuration.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.healthCheckErrors.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.transactionsTotal.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.transactionDuration.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.acquireDuration.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.sessionPinsTotal.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.backendResetsTotal.DeletePartialMatch(prometheus.Labels{"tenant": tenant})
	c.dirtyDisconnects.DeleteLabelValues(tenant)
}
