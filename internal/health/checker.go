package health

import (
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/metrics"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

// Status represents the health status of a tenant's database.
type Status int

const (
	StatusUnknown   Status = iota
	StatusHealthy
	StatusUnhealthy
)

func (s Status) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// TenantHealth holds health information for a tenant.
type TenantHealth struct {
	Status             Status    `json:"status"`
	LastCheck          time.Time `json:"last_check"`
	ConsecutiveFailures int      `json:"consecutive_failures"`
	LastError          string    `json:"last_error,omitempty"`
}

// Checker performs periodic health checks on tenant databases.
type Checker struct {
	mu      sync.RWMutex
	tenants map[string]*TenantHealth
	router  *router.Router
	metrics *metrics.Collector

	interval          time.Duration
	failureThreshold  int
	connectionTimeout time.Duration

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewChecker creates a new health checker.
func NewChecker(r *router.Router, m *metrics.Collector) *Checker {
	return &Checker{
		tenants:           make(map[string]*TenantHealth),
		router:            r,
		metrics:           m,
		interval:          30 * time.Second,
		failureThreshold:  3,
		connectionTimeout: 5 * time.Second,
		stopCh:            make(chan struct{}),
	}
}

// Start begins periodic health checking.
func (c *Checker) Start() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		c.run()
	}()
	log.Printf("[health] checker started (interval=%s, threshold=%d)", c.interval, c.failureThreshold)
}

// Stop stops the health checker.
func (c *Checker) Stop() {
	close(c.stopCh)
	c.wg.Wait()
	log.Printf("[health] checker stopped")
}

func (c *Checker) run() {
	// Run immediately on start
	c.checkAll()

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.checkAll()
		case <-c.stopCh:
			return
		}
	}
}

func (c *Checker) checkAll() {
	tenants := c.router.ListTenants()

	for id, tc := range tenants {
		healthy := c.pingTenant(id, tc)
		c.updateStatus(id, healthy)
	}
}

func (c *Checker) pingTenant(tenantID string, tc config.TenantConfig) bool {
	addr := net.JoinHostPort(tc.Host, fmt.Sprintf("%d", tc.Port))
	conn, err := net.DialTimeout("tcp", addr, c.connectionTimeout)
	if err != nil {
		c.mu.Lock()
		th := c.getOrCreate(tenantID)
		th.LastError = err.Error()
		c.mu.Unlock()
		return false
	}
	conn.Close()
	return true
}

func (c *Checker) updateStatus(tenantID string, healthy bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	th := c.getOrCreate(tenantID)
	th.LastCheck = time.Now()

	if healthy {
		if th.ConsecutiveFailures > 0 {
			log.Printf("[health] tenant %s recovered after %d failures", tenantID, th.ConsecutiveFailures)
		}
		th.Status = StatusHealthy
		th.ConsecutiveFailures = 0
		th.LastError = ""
	} else {
		th.ConsecutiveFailures++
		if th.ConsecutiveFailures >= c.failureThreshold {
			if th.Status != StatusUnhealthy {
				log.Printf("[health] tenant %s marked unhealthy after %d consecutive failures: %s",
					tenantID, th.ConsecutiveFailures, th.LastError)
			}
			th.Status = StatusUnhealthy
		}
	}

	if c.metrics != nil {
		c.metrics.SetTenantHealth(tenantID, th.Status == StatusHealthy)
	}
}

func (c *Checker) getOrCreate(tenantID string) *TenantHealth {
	th, ok := c.tenants[tenantID]
	if !ok {
		th = &TenantHealth{Status: StatusUnknown}
		c.tenants[tenantID] = th
	}
	return th
}

// IsHealthy returns whether a tenant is healthy (or unknown, which is treated as healthy).
func (c *Checker) IsHealthy(tenantID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	th, ok := c.tenants[tenantID]
	if !ok {
		return true // unknown = allow through
	}
	return th.Status != StatusUnhealthy
}

// GetStatus returns the health status for a tenant.
func (c *Checker) GetStatus(tenantID string) TenantHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()

	th, ok := c.tenants[tenantID]
	if !ok {
		return TenantHealth{Status: StatusUnknown}
	}
	return *th
}

// GetAllStatuses returns health statuses for all known tenants.
func (c *Checker) GetAllStatuses() map[string]TenantHealth {
	c.mu.RLock()
	defer c.mu.RUnlock()

	result := make(map[string]TenantHealth, len(c.tenants))
	for id, th := range c.tenants {
		result[id] = *th
	}
	return result
}

// OverallHealthy returns true if all tenants are healthy.
func (c *Checker) OverallHealthy() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	for _, th := range c.tenants {
		if th.Status == StatusUnhealthy {
			return false
		}
	}
	return true
}
