package router

import (
	"testing"

	"github.com/dbbouncer/dbbouncer/internal/config"
)

func newTestConfig() *config.Config {
	return &config.Config{
		Defaults: config.PoolDefaults{
			MinConnections: 2,
			MaxConnections: 20,
		},
		Tenants: map[string]config.TenantConfig{
			"tenant_1": {
				DBType:   "postgres",
				Host:     "pg-host",
				Port:     5432,
				DBName:   "db1",
				Username: "user1",
			},
			"tenant_2": {
				DBType:   "mysql",
				Host:     "mysql-host",
				Port:     3306,
				DBName:   "db2",
				Username: "user2",
			},
		},
	}
}

func TestResolve(t *testing.T) {
	r := New(newTestConfig())

	tc, err := r.Resolve("tenant_1")
	if err != nil {
		t.Fatalf("Resolve tenant_1 failed: %v", err)
	}
	if tc.DBType != "postgres" {
		t.Errorf("expected postgres, got %s", tc.DBType)
	}
	if tc.Host != "pg-host" {
		t.Errorf("expected pg-host, got %s", tc.Host)
	}
}

func TestResolveUnknown(t *testing.T) {
	r := New(newTestConfig())

	_, err := r.Resolve("nonexistent")
	if err == nil {
		t.Error("expected error for unknown tenant")
	}
}

func TestAddAndRemoveTenant(t *testing.T) {
	r := New(newTestConfig())

	tc := config.TenantConfig{
		DBType:   "postgres",
		Host:     "new-host",
		Port:     5432,
		DBName:   "newdb",
		Username: "newuser",
	}

	r.AddTenant("tenant_3", tc)

	resolved, err := r.Resolve("tenant_3")
	if err != nil {
		t.Fatalf("Resolve tenant_3 failed: %v", err)
	}
	if resolved.Host != "new-host" {
		t.Errorf("expected new-host, got %s", resolved.Host)
	}

	if !r.RemoveTenant("tenant_3") {
		t.Error("RemoveTenant should return true")
	}

	_, err = r.Resolve("tenant_3")
	if err == nil {
		t.Error("expected error after removal")
	}
}

func TestRemoveNonexistent(t *testing.T) {
	r := New(newTestConfig())

	if r.RemoveTenant("nonexistent") {
		t.Error("RemoveTenant should return false for nonexistent tenant")
	}
}

func TestListTenants(t *testing.T) {
	r := New(newTestConfig())

	tenants := r.ListTenants()
	if len(tenants) != 2 {
		t.Errorf("expected 2 tenants, got %d", len(tenants))
	}
}

func TestReload(t *testing.T) {
	r := New(newTestConfig())

	newCfg := &config.Config{
		Defaults: config.PoolDefaults{
			MinConnections: 5,
			MaxConnections: 50,
		},
		Tenants: map[string]config.TenantConfig{
			"tenant_new": {
				DBType:   "mysql",
				Host:     "new-mysql",
				Port:     3306,
				DBName:   "newdb",
				Username: "newuser",
			},
		},
	}

	r.Reload(newCfg)

	// Old tenants should be gone
	_, err := r.Resolve("tenant_1")
	if err == nil {
		t.Error("expected error for old tenant after reload")
	}

	// New tenant should exist
	tc, err := r.Resolve("tenant_new")
	if err != nil {
		t.Fatalf("Resolve tenant_new failed: %v", err)
	}
	if tc.DBType != "mysql" {
		t.Errorf("expected mysql, got %s", tc.DBType)
	}

	// Defaults should be updated
	defaults := r.Defaults()
	if defaults.MaxConnections != 50 {
		t.Errorf("expected max connections 50, got %d", defaults.MaxConnections)
	}
}

func TestExtractTenantFromUsername(t *testing.T) {
	tests := []struct {
		username   string
		wantTenant string
		wantUser   string
		wantOk     bool
	}{
		{"tenant_1__appuser", "tenant_1", "appuser", true},
		{"mycompany..admin", "mycompany", "admin", true},
		{"plainuser", "", "plainuser", false},
		{"no_double_sep", "", "no_double_sep", false},
	}

	for _, tt := range tests {
		t.Run(tt.username, func(t *testing.T) {
			tenant, user, ok := ExtractTenantFromUsername(tt.username)
			if tenant != tt.wantTenant || user != tt.wantUser || ok != tt.wantOk {
				t.Errorf("ExtractTenantFromUsername(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.username, tenant, user, ok, tt.wantTenant, tt.wantUser, tt.wantOk)
			}
		})
	}
}

func TestPauseResumeTenant(t *testing.T) {
	r := New(newTestConfig())

	// Initially not paused
	if r.IsPaused("tenant_1") {
		t.Error("tenant_1 should not be paused initially")
	}

	// Pause
	if !r.PauseTenant("tenant_1") {
		t.Error("PauseTenant should return true for existing tenant")
	}
	if !r.IsPaused("tenant_1") {
		t.Error("tenant_1 should be paused")
	}

	// Other tenant unaffected
	if r.IsPaused("tenant_2") {
		t.Error("tenant_2 should not be paused")
	}

	// Resume
	if !r.ResumeTenant("tenant_1") {
		t.Error("ResumeTenant should return true for existing tenant")
	}
	if r.IsPaused("tenant_1") {
		t.Error("tenant_1 should not be paused after resume")
	}

	// Pause nonexistent
	if r.PauseTenant("nonexistent") {
		t.Error("PauseTenant should return false for nonexistent tenant")
	}
	if r.ResumeTenant("nonexistent") {
		t.Error("ResumeTenant should return false for nonexistent tenant")
	}

	// Pause then remove — paused state should be cleaned up
	r.PauseTenant("tenant_1")
	r.RemoveTenant("tenant_1")
	if r.IsPaused("tenant_1") {
		t.Error("paused state should be cleaned up after removal")
	}
}
