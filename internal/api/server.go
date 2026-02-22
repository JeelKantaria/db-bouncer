package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/health"
	"github.com/dbbouncer/dbbouncer/internal/metrics"
	"github.com/dbbouncer/dbbouncer/internal/pool"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

const maxRequestBodySize = 1 << 20 // 1 MB

// Server is the REST API and metrics server.
type Server struct {
	router      *router.Router
	poolMgr     *pool.Manager
	healthCheck *health.Checker
	metrics     *metrics.Collector
	httpServer  *http.Server
	startTime   time.Time
	listenCfg   config.ListenConfig
	tenantMu    sync.Mutex // protects read-modify-write in updateTenant
}

// NewServer creates a new API server.
func NewServer(r *router.Router, pm *pool.Manager, hc *health.Checker, m *metrics.Collector, lc config.ListenConfig) *Server {
	return &Server{
		router:      r,
		poolMgr:     pm,
		healthCheck: hc,
		metrics:     m,
		startTime:   time.Now(),
		listenCfg:   lc,
	}
}

// authMiddleware returns a middleware that checks for a valid API key.
// Unauthenticated routes (health, ready, metrics) are excluded.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip auth for health/readiness probes and metrics
		path := r.URL.Path
		if path == "/health" || path == "/ready" || path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		apiKey := s.listenCfg.APIKey
		if apiKey == "" {
			// No API key configured — allow all requests
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if auth == "" || !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != apiKey {
			writeError(w, http.StatusUnauthorized, "unauthorized: invalid or missing API key")
			return
		}

		next.ServeHTTP(w, r)
	})
}

// Start starts the HTTP API server.
func (s *Server) Start(port int) error {
	r := mux.NewRouter()

	// Tenant CRUD
	r.HandleFunc("/tenants", s.listTenants).Methods("GET")
	r.HandleFunc("/tenants", s.createTenant).Methods("POST")
	r.HandleFunc("/tenants/{id}", s.getTenant).Methods("GET")
	r.HandleFunc("/tenants/{id}", s.updateTenant).Methods("PUT")
	r.HandleFunc("/tenants/{id}", s.deleteTenant).Methods("DELETE")
	r.HandleFunc("/tenants/{id}/stats", s.tenantStats).Methods("GET")
	r.HandleFunc("/tenants/{id}/drain", s.drainTenant).Methods("POST")

	// Pause/Resume
	r.HandleFunc("/tenants/{id}/pause", s.pauseTenant).Methods("POST")
	r.HandleFunc("/tenants/{id}/resume", s.resumeTenant).Methods("POST")

	// Server status & config
	r.HandleFunc("/status", s.statusHandler).Methods("GET")
	r.HandleFunc("/config", s.configHandler).Methods("GET")

	// Health & readiness
	r.HandleFunc("/health", s.healthHandler).Methods("GET")
	r.HandleFunc("/ready", s.readyHandler).Methods("GET")

	// Prometheus metrics
	if s.metrics != nil && s.metrics.Registry != nil {
		r.Handle("/metrics", promhttp.HandlerFor(s.metrics.Registry, promhttp.HandlerOpts{}))
	} else {
		r.Handle("/metrics", promhttp.Handler())
	}

	// Admin dashboard (must be registered last — catch-all for "/" and "/dashboard")
	r.HandleFunc("/", s.dashboardHandler).Methods("GET")
	r.HandleFunc("/dashboard", s.dashboardHandler).Methods("GET")

	// Wrap with security headers, then auth middleware
	handler := s.securityHeaders(s.authMiddleware(r))

	bind := s.listenCfg.APIBind
	if bind == "" {
		bind = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", bind, port)
	s.httpServer = &http.Server{
		Addr:         addr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	if s.listenCfg.APIKey == "" {
		slog.Warn("API key not configured — management endpoints are unauthenticated")
	}
	slog.Info("REST API listening", "addr", addr)

	go func() {
		if err := s.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("API server error", "err", err)
		}
	}()

	return nil
}

// Stop gracefully shuts down the API server.
func (s *Server) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.httpServer.Shutdown(ctx)
}

// --- Tenant Handlers ---

type tenantRequest struct {
	DBType         string `json:"db_type"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	DBName         string `json:"dbname"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	MinConnections *int   `json:"min_connections,omitempty"`
	MaxConnections *int   `json:"max_connections,omitempty"`
}

type tenantResponse struct {
	ID     string               `json:"id"`
	Config config.TenantConfig  `json:"config"`
	Stats  *pool.Stats          `json:"stats,omitempty"`
	Health *health.TenantHealth `json:"health,omitempty"`
	Paused bool                 `json:"paused"`
}

func (s *Server) listTenants(w http.ResponseWriter, r *http.Request) {
	tenants := s.router.ListTenants()

	var result []tenantResponse
	for id, tc := range tenants {
		tr := tenantResponse{
			ID:     id,
			Config: tc.Redacted(),
			Paused: s.router.IsPaused(id),
		}
		if stats, ok := s.poolMgr.TenantStats(id); ok {
			tr.Stats = &stats
		}
		h := s.healthCheck.GetStatus(id)
		tr.Health = &h
		result = append(result, tr)
	}

	writeJSON(w, http.StatusOK, result)
}

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	var req struct {
		ID string `json:"id"`
		tenantRequest
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "tenant id is required")
		return
	}
	if req.DBType != "postgres" && req.DBType != "mysql" {
		writeError(w, http.StatusBadRequest, "db_type must be postgres or mysql")
		return
	}
	if req.Host == "" || req.Port == 0 || req.DBName == "" || req.Username == "" {
		writeError(w, http.StatusBadRequest, "host, port, dbname, and username are required")
		return
	}

	tc := config.TenantConfig{
		DBType:         req.DBType,
		Host:           req.Host,
		Port:           req.Port,
		DBName:         req.DBName,
		Username:       req.Username,
		Password:       req.Password,
		MinConnections: req.MinConnections,
		MaxConnections: req.MaxConnections,
	}

	s.router.AddTenant(req.ID, tc)
	slog.Info("tenant registered", "tenant", req.ID, "db_type", tc.DBType, "host", tc.Host, "port", tc.Port)

	writeJSON(w, http.StatusCreated, tenantResponse{ID: req.ID, Config: tc.Redacted()})
}

func (s *Server) getTenant(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	tc, err := s.router.Resolve(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	tr := tenantResponse{ID: id, Config: tc.Redacted(), Paused: s.router.IsPaused(id)}
	if stats, ok := s.poolMgr.TenantStats(id); ok {
		tr.Stats = &stats
	}
	h := s.healthCheck.GetStatus(id)
	tr.Health = &h

	writeJSON(w, http.StatusOK, tr)
}

func (s *Server) updateTenant(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)
	id := mux.Vars(r)["id"]

	var req tenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}

	// Hold lock for the entire read-modify-write to prevent TOCTOU races
	s.tenantMu.Lock()
	defer s.tenantMu.Unlock()

	// Verify tenant exists
	existing, err := s.router.Resolve(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	// Merge with existing config
	if req.DBType != "" {
		existing.DBType = req.DBType
	}
	if req.Host != "" {
		existing.Host = req.Host
	}
	if req.Port != 0 {
		existing.Port = req.Port
	}
	if req.DBName != "" {
		existing.DBName = req.DBName
	}
	if req.Username != "" {
		existing.Username = req.Username
	}
	if req.Password != "" {
		existing.Password = req.Password
	}
	if req.MinConnections != nil {
		existing.MinConnections = req.MinConnections
	}
	if req.MaxConnections != nil {
		existing.MaxConnections = req.MaxConnections
	}

	s.router.AddTenant(id, existing)
	slog.Info("tenant updated", "tenant", id)

	writeJSON(w, http.StatusOK, tenantResponse{ID: id, Config: existing.Redacted()})
}

func (s *Server) deleteTenant(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	if !s.router.RemoveTenant(id) {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	// Drain and remove pool
	s.poolMgr.Remove(id)
	if s.healthCheck != nil {
		s.healthCheck.RemoveTenant(id)
	}
	if s.metrics != nil {
		s.metrics.RemoveTenant(id)
	}

	slog.Info("tenant removed", "tenant", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "tenant": id})
}

func (s *Server) tenantStats(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	stats, ok := s.poolMgr.TenantStats(id)
	if !ok {
		// Check if tenant exists but has no pool yet
		if _, err := s.router.Resolve(id); err != nil {
			writeError(w, http.StatusNotFound, "tenant not found")
			return
		}
		stats = pool.Stats{TenantID: id}
	}

	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) drainTenant(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	if !s.poolMgr.DrainTenant(id) {
		writeError(w, http.StatusNotFound, "tenant not found or no active pool")
		return
	}

	slog.Info("tenant drained", "tenant", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "drained", "tenant": id})
}

// --- Health Handlers ---

func (s *Server) healthHandler(w http.ResponseWriter, r *http.Request) {
	statuses := s.healthCheck.GetAllStatuses()
	allHealthy := s.healthCheck.OverallHealthy()

	status := http.StatusOK
	if !allHealthy {
		status = http.StatusServiceUnavailable
	}

	writeJSON(w, status, map[string]interface{}{
		"status":  boolToStatus(allHealthy),
		"tenants": statuses,
	})
}

func (s *Server) readyHandler(w http.ResponseWriter, r *http.Request) {
	// Ready if at least one tenant is healthy or there are no tenants
	tenants := s.router.ListTenants()
	if len(tenants) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
		return
	}

	for id := range tenants {
		if s.healthCheck.IsHealthy(id) {
			writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
			return
		}
	}

	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
}

// --- Status & Config Handlers ---

func (s *Server) statusHandler(w http.ResponseWriter, r *http.Request) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	uptime := time.Since(s.startTime).Seconds()
	tenants := s.router.ListTenants()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uptime_seconds": int(uptime),
		"go_version":     runtime.Version(),
		"goroutines":     runtime.NumGoroutine(),
		"memory_mb":      float64(mem.Alloc) / 1024 / 1024,
		"num_tenants":    len(tenants),
		"listen": map[string]int{
			"postgres_port": s.listenCfg.PostgresPort,
			"mysql_port":    s.listenCfg.MySQLPort,
			"api_port":      s.listenCfg.APIPort,
		},
	})
}

func (s *Server) configHandler(w http.ResponseWriter, r *http.Request) {
	defaults := s.router.Defaults()
	tenants := s.router.ListTenants()

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"listen": map[string]int{
			"postgres_port": s.listenCfg.PostgresPort,
			"mysql_port":    s.listenCfg.MySQLPort,
			"api_port":      s.listenCfg.APIPort,
		},
		"defaults": map[string]interface{}{
			"min_connections": defaults.MinConnections,
			"max_connections": defaults.MaxConnections,
			"idle_timeout":    defaults.IdleTimeout.String(),
			"max_lifetime":    defaults.MaxLifetime.String(),
			"acquire_timeout": defaults.AcquireTimeout.String(),
		},
		"tenant_count": len(tenants),
	})
}

// --- Pause/Resume Handlers ---

func (s *Server) pauseTenant(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	if !s.router.PauseTenant(id) {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	slog.Info("tenant paused", "tenant", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused", "tenant": id})
}

func (s *Server) resumeTenant(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	if !s.router.ResumeTenant(id) {
		writeError(w, http.StatusNotFound, "tenant not found")
		return
	}

	slog.Info("tenant resumed", "tenant", id)
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed", "tenant": id})
}

// securityHeaders adds security-related HTTP headers to all responses.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-XSS-Protection", "1; mode=block")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		next.ServeHTTP(w, r)
	})
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func boolToStatus(b bool) string {
	if b {
		return "healthy"
	}
	return "unhealthy"
}
