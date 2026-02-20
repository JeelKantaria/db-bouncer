package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/health"
	"github.com/dbbouncer/dbbouncer/internal/pool"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

func newTestServer() (*Server, *mux.Router) {
	cfg := &config.Config{
		Defaults: config.PoolDefaults{
			MinConnections: 2,
			MaxConnections: 20,
		},
		Tenants: map[string]config.TenantConfig{
			"tenant_1": {
				DBType:   "postgres",
				Host:     "localhost",
				Port:     5432,
				DBName:   "db1",
				Username: "user1",
			},
		},
	}

	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	hc := health.NewChecker(r, nil)

	s := NewServer(r, pm, hc, nil, config.ListenConfig{})

	mr := mux.NewRouter()
	mr.HandleFunc("/tenants", s.listTenants).Methods("GET")
	mr.HandleFunc("/tenants", s.createTenant).Methods("POST")
	mr.HandleFunc("/tenants/{id}", s.getTenant).Methods("GET")
	mr.HandleFunc("/tenants/{id}", s.updateTenant).Methods("PUT")
	mr.HandleFunc("/tenants/{id}", s.deleteTenant).Methods("DELETE")
	mr.HandleFunc("/tenants/{id}/stats", s.tenantStats).Methods("GET")
	mr.HandleFunc("/tenants/{id}/drain", s.drainTenant).Methods("POST")
	mr.HandleFunc("/health", s.healthHandler).Methods("GET")
	mr.HandleFunc("/ready", s.readyHandler).Methods("GET")

	return s, mr
}

func TestListTenants(t *testing.T) {
	_, mr := newTestServer()

	req := httptest.NewRequest("GET", "/tenants", nil)
	rr := httptest.NewRecorder()
	mr.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var result []tenantResponse
	if err := json.NewDecoder(rr.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(result) != 1 {
		t.Errorf("expected 1 tenant, got %d", len(result))
	}
}

func TestCreateTenant(t *testing.T) {
	_, mr := newTestServer()

	body := `{
		"id": "tenant_new",
		"db_type": "mysql",
		"host": "mysql-host",
		"port": 3306,
		"dbname": "newdb",
		"username": "newuser",
		"password": "pass"
	}`

	req := httptest.NewRequest("POST", "/tenants", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mr.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var result tenantResponse
	json.NewDecoder(rr.Body).Decode(&result)
	if result.ID != "tenant_new" {
		t.Errorf("expected tenant_new, got %s", result.ID)
	}
}

func TestCreateTenantValidation(t *testing.T) {
	_, mr := newTestServer()

	// Missing required fields
	body := `{"id": "bad", "db_type": "invalid"}`
	req := httptest.NewRequest("POST", "/tenants", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mr.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}

func TestGetTenant(t *testing.T) {
	_, mr := newTestServer()

	req := httptest.NewRequest("GET", "/tenants/tenant_1", nil)
	rr := httptest.NewRecorder()
	mr.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	var result tenantResponse
	json.NewDecoder(rr.Body).Decode(&result)
	if result.ID != "tenant_1" {
		t.Errorf("expected tenant_1, got %s", result.ID)
	}
}

func TestGetTenantNotFound(t *testing.T) {
	_, mr := newTestServer()

	req := httptest.NewRequest("GET", "/tenants/nonexistent", nil)
	rr := httptest.NewRecorder()
	mr.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

func TestUpdateTenant(t *testing.T) {
	_, mr := newTestServer()

	body := `{"host": "updated-host", "port": 5433}`
	req := httptest.NewRequest("PUT", "/tenants/tenant_1", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	mr.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var result tenantResponse
	json.NewDecoder(rr.Body).Decode(&result)
	if result.Config.Host != "updated-host" {
		t.Errorf("expected updated-host, got %s", result.Config.Host)
	}
	if result.Config.Port != 5433 {
		t.Errorf("expected port 5433, got %d", result.Config.Port)
	}
}

func TestDeleteTenant(t *testing.T) {
	_, mr := newTestServer()

	req := httptest.NewRequest("DELETE", "/tenants/tenant_1", nil)
	rr := httptest.NewRecorder()
	mr.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}

	// Should be gone now
	req = httptest.NewRequest("GET", "/tenants/tenant_1", nil)
	rr = httptest.NewRecorder()
	mr.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", rr.Code)
	}
}

func TestHealthEndpoint(t *testing.T) {
	_, mr := newTestServer()

	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()
	mr.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rr.Code)
	}
}

func TestReadyEndpoint(t *testing.T) {
	_, mr := newTestServer()

	req := httptest.NewRequest("GET", "/ready", nil)
	rr := httptest.NewRecorder()
	mr.ServeHTTP(rr, req)

	// With tenants but no health checks yet, all are "unknown" which counts as healthy
	if rr.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
}
