package router

import (
	"fmt"
	"strings"
	"sync"

	"github.com/dbbouncer/dbbouncer/internal/config"
)

// Router resolves tenant IDs to their database configurations.
type Router struct {
	mu       sync.RWMutex
	tenants  map[string]config.TenantConfig
	defaults config.PoolDefaults
	paused   map[string]bool
}

// New creates a new Router populated from the given config.
func New(cfg *config.Config) *Router {
	r := &Router{
		tenants:  make(map[string]config.TenantConfig, len(cfg.Tenants)),
		defaults: cfg.Defaults,
		paused:   make(map[string]bool),
	}
	for id, tc := range cfg.Tenants {
		r.tenants[id] = tc
	}
	return r
}

// Resolve looks up the TenantConfig for the given tenant ID.
func (r *Router) Resolve(tenantID string) (config.TenantConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	tc, ok := r.tenants[tenantID]
	if !ok {
		return config.TenantConfig{}, fmt.Errorf("unknown tenant: %q", tenantID)
	}
	return tc, nil
}

// AddTenant registers or updates a tenant configuration.
func (r *Router) AddTenant(tenantID string, tc config.TenantConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tenants[tenantID] = tc
}

// RemoveTenant removes a tenant from the router.
func (r *Router) RemoveTenant(tenantID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tenants[tenantID]; !ok {
		return false
	}
	delete(r.tenants, tenantID)
	delete(r.paused, tenantID)
	return true
}

// PauseTenant marks a tenant as paused. Returns false if tenant not found.
func (r *Router) PauseTenant(tenantID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tenants[tenantID]; !ok {
		return false
	}
	r.paused[tenantID] = true
	return true
}

// ResumeTenant unpauses a tenant. Returns false if tenant not found.
func (r *Router) ResumeTenant(tenantID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tenants[tenantID]; !ok {
		return false
	}
	delete(r.paused, tenantID)
	return true
}

// IsPaused returns whether a tenant is currently paused.
func (r *Router) IsPaused(tenantID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.paused[tenantID]
}

// ListTenants returns all tenant IDs and their configs.
func (r *Router) ListTenants() map[string]config.TenantConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]config.TenantConfig, len(r.tenants))
	for id, tc := range r.tenants {
		result[id] = tc
	}
	return result
}

// Defaults returns the current pool defaults.
func (r *Router) Defaults() config.PoolDefaults {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.defaults
}

// Reload replaces the entire routing table from a new config.
func (r *Router) Reload(cfg *config.Config) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.defaults = cfg.Defaults
	newTenants := make(map[string]config.TenantConfig, len(cfg.Tenants))
	for id, tc := range cfg.Tenants {
		newTenants[id] = tc
	}
	r.tenants = newTenants
	r.paused = make(map[string]bool)
}

// ExtractTenantFromUsername parses tenant ID from username formats like "tenant_123_appuser".
// Returns the tenant ID and the real username.
func ExtractTenantFromUsername(username string) (tenantID, realUser string, ok bool) {
	// Format: {tenant_id}_{real_username} where tenant_id can contain underscores
	// We try to match known tenant IDs, but as a heuristic, we split on the last underscore group
	// Convention: username format is "{tenantid}.{realuser}" or "{tenantid}__{realuser}"
	if idx := strings.Index(username, ".."); idx > 0 {
		return username[:idx], username[idx+2:], true
	}
	if idx := strings.Index(username, "__"); idx > 0 {
		return username[:idx], username[idx+2:], true
	}
	return "", username, false
}
