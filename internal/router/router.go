package router

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/dbbouncer/dbbouncer/internal/config"
)

// routerSnapshot is an immutable point-in-time view of the routing table.
// Stored in atomic.Value for lock-free reads on the hot path.
type routerSnapshot struct {
	tenants  map[string]config.TenantConfig
	defaults config.PoolDefaults
	paused   map[string]bool
}

// Router resolves tenant IDs to their database configurations.
// Resolve() and IsPaused() are lock-free via atomic.Value.
// Mutations serialize on a write mutex and swap in a new snapshot.
type Router struct {
	snap atomic.Value // holds *routerSnapshot
	wmu  sync.Mutex   // serializes mutations (writes are rare)
}

// New creates a new Router populated from the given config.
func New(cfg *config.Config) *Router {
	snap := &routerSnapshot{
		tenants:  make(map[string]config.TenantConfig, len(cfg.Tenants)),
		defaults: cfg.Defaults,
		paused:   make(map[string]bool),
	}
	for id, tc := range cfg.Tenants {
		snap.tenants[id] = tc
	}

	r := &Router{}
	r.snap.Store(snap)
	return r
}

// load returns the current immutable snapshot (lock-free).
func (r *Router) load() *routerSnapshot {
	return r.snap.Load().(*routerSnapshot)
}

// cloneSnap returns a mutable deep copy of the current snapshot.
// Must be called with wmu held.
func (r *Router) cloneSnap() *routerSnapshot {
	cur := r.load()
	newTenants := make(map[string]config.TenantConfig, len(cur.tenants))
	for id, tc := range cur.tenants {
		newTenants[id] = tc
	}
	newPaused := make(map[string]bool, len(cur.paused))
	for id, v := range cur.paused {
		newPaused[id] = v
	}
	return &routerSnapshot{
		tenants:  newTenants,
		defaults: cur.defaults,
		paused:   newPaused,
	}
}

// Resolve looks up the TenantConfig for the given tenant ID. Lock-free.
func (r *Router) Resolve(tenantID string) (config.TenantConfig, error) {
	snap := r.load()
	tc, ok := snap.tenants[tenantID]
	if !ok {
		return config.TenantConfig{}, fmt.Errorf("unknown tenant: %q", tenantID)
	}
	return tc, nil
}

// AddTenant registers or updates a tenant configuration.
func (r *Router) AddTenant(tenantID string, tc config.TenantConfig) {
	r.wmu.Lock()
	defer r.wmu.Unlock()

	s := r.cloneSnap()
	s.tenants[tenantID] = tc
	r.snap.Store(s)
}

// RemoveTenant removes a tenant from the router.
func (r *Router) RemoveTenant(tenantID string) bool {
	r.wmu.Lock()
	defer r.wmu.Unlock()

	cur := r.load()
	if _, ok := cur.tenants[tenantID]; !ok {
		return false
	}

	s := r.cloneSnap()
	delete(s.tenants, tenantID)
	delete(s.paused, tenantID)
	r.snap.Store(s)
	return true
}

// PauseTenant marks a tenant as paused. Returns false if tenant not found.
func (r *Router) PauseTenant(tenantID string) bool {
	r.wmu.Lock()
	defer r.wmu.Unlock()

	cur := r.load()
	if _, ok := cur.tenants[tenantID]; !ok {
		return false
	}

	s := r.cloneSnap()
	s.paused[tenantID] = true
	r.snap.Store(s)
	return true
}

// ResumeTenant unpauses a tenant. Returns false if tenant not found.
func (r *Router) ResumeTenant(tenantID string) bool {
	r.wmu.Lock()
	defer r.wmu.Unlock()

	cur := r.load()
	if _, ok := cur.tenants[tenantID]; !ok {
		return false
	}

	s := r.cloneSnap()
	delete(s.paused, tenantID)
	r.snap.Store(s)
	return true
}

// IsPaused returns whether a tenant is currently paused. Lock-free.
func (r *Router) IsPaused(tenantID string) bool {
	return r.load().paused[tenantID]
}

// ListTenants returns all tenant IDs and their configs.
func (r *Router) ListTenants() map[string]config.TenantConfig {
	snap := r.load()
	result := make(map[string]config.TenantConfig, len(snap.tenants))
	for id, tc := range snap.tenants {
		result[id] = tc
	}
	return result
}

// Defaults returns the current pool defaults. Lock-free.
func (r *Router) Defaults() config.PoolDefaults {
	return r.load().defaults
}

// Reload replaces the entire routing table from a new config.
// Preserves paused state for tenants that still exist in the new config.
func (r *Router) Reload(cfg *config.Config) {
	r.wmu.Lock()
	defer r.wmu.Unlock()

	cur := r.load()
	newTenants := make(map[string]config.TenantConfig, len(cfg.Tenants))
	for id, tc := range cfg.Tenants {
		newTenants[id] = tc
	}

	// Carry over paused state for tenants that still exist
	newPaused := make(map[string]bool)
	for id, v := range cur.paused {
		if _, exists := newTenants[id]; exists {
			newPaused[id] = v
		}
	}

	r.snap.Store(&routerSnapshot{
		tenants:  newTenants,
		defaults: cfg.Defaults,
		paused:   newPaused,
	})
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
