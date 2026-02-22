package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	yaml := `
listen:
  postgres_port: 6432
  mysql_port: 3307
  api_port: 8080

defaults:
  min_connections: 2
  max_connections: 20
  idle_timeout: 5m
  max_lifetime: 30m
  acquire_timeout: 10s

tenants:
  test_tenant:
    db_type: postgres
    host: localhost
    port: 5432
    dbname: testdb
    username: testuser
    password: testpass
`
	path := writeTemp(t, yaml)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Listen.PostgresPort != 6432 {
		t.Errorf("expected postgres port 6432, got %d", cfg.Listen.PostgresPort)
	}
	if cfg.Listen.MySQLPort != 3307 {
		t.Errorf("expected mysql port 3307, got %d", cfg.Listen.MySQLPort)
	}
	if cfg.Defaults.MaxConnections != 20 {
		t.Errorf("expected max connections 20, got %d", cfg.Defaults.MaxConnections)
	}
	if cfg.Defaults.IdleTimeout != 5*time.Minute {
		t.Errorf("expected idle timeout 5m, got %v", cfg.Defaults.IdleTimeout)
	}

	tc, ok := cfg.Tenants["test_tenant"]
	if !ok {
		t.Fatal("test_tenant not found")
	}
	if tc.DBType != "postgres" {
		t.Errorf("expected db_type postgres, got %s", tc.DBType)
	}
	if tc.Host != "localhost" {
		t.Errorf("expected host localhost, got %s", tc.Host)
	}
}

func TestLoadEnvSubstitution(t *testing.T) {
	os.Setenv("TEST_DB_PASSWORD", "secret123")
	defer os.Unsetenv("TEST_DB_PASSWORD")

	yaml := `
tenants:
  test:
    db_type: postgres
    host: localhost
    port: 5432
    dbname: testdb
    username: user
    password: ${TEST_DB_PASSWORD}
`
	path := writeTemp(t, yaml)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	tc := cfg.Tenants["test"]
	if tc.Password != "secret123" {
		t.Errorf("expected password secret123, got %s", tc.Password)
	}
}

func TestLoadValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{
			name: "invalid db_type",
			yaml: `
tenants:
  t1:
    db_type: sqlite
    host: localhost
    port: 5432
    dbname: db
    username: user
`,
		},
		{
			name: "missing host",
			yaml: `
tenants:
  t1:
    db_type: postgres
    port: 5432
    dbname: db
    username: user
`,
		},
		{
			name: "missing port",
			yaml: `
tenants:
  t1:
    db_type: postgres
    host: localhost
    dbname: db
    username: user
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTemp(t, tt.yaml)
			_, err := Load(path)
			if err == nil {
				t.Error("expected validation error, got nil")
			}
		})
	}
}

func TestApplyDefaults(t *testing.T) {
	yaml := `
tenants: {}
`
	path := writeTemp(t, yaml)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg.Listen.PostgresPort != 6432 {
		t.Errorf("expected default postgres port 6432, got %d", cfg.Listen.PostgresPort)
	}
	if cfg.Listen.MySQLPort != 3307 {
		t.Errorf("expected default mysql port 3307, got %d", cfg.Listen.MySQLPort)
	}
	if cfg.Listen.APIPort != 8080 {
		t.Errorf("expected default api port 8080, got %d", cfg.Listen.APIPort)
	}
	if cfg.Defaults.MinConnections != 2 {
		t.Errorf("expected default min connections 2, got %d", cfg.Defaults.MinConnections)
	}
}

func TestTenantConfigEffectiveValues(t *testing.T) {
	defaults := PoolDefaults{
		MinConnections: 2,
		MaxConnections: 20,
		IdleTimeout:    5 * time.Minute,
		MaxLifetime:    30 * time.Minute,
		AcquireTimeout: 10 * time.Second,
		DialTimeout:    5 * time.Second,
	}

	maxConn := 50
	tc := TenantConfig{
		MaxConnections: &maxConn,
	}

	if tc.EffectiveMinConnections(defaults) != 2 {
		t.Error("expected default min connections")
	}
	if tc.EffectiveMaxConnections(defaults) != 50 {
		t.Error("expected overridden max connections of 50")
	}
	if tc.EffectiveIdleTimeout(defaults) != 5*time.Minute {
		t.Error("expected default idle timeout")
	}
	if tc.EffectiveDialTimeout(defaults) != 5*time.Second {
		t.Error("expected default dial timeout of 5s")
	}

	// Override dial timeout
	dt := 3 * time.Second
	tc.DialTimeout = &dt
	if tc.EffectiveDialTimeout(defaults) != 3*time.Second {
		t.Error("expected overridden dial timeout of 3s")
	}
}

// --- Phase 4 validation tests ---

func TestValidateMinGtMaxConns(t *testing.T) {
	yaml := `
defaults:
  min_connections: 30
  max_connections: 10
tenants: {}
`
	path := writeTemp(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error when min_connections > max_connections")
	}
}

func TestValidateInvalidPort(t *testing.T) {
	yaml := `
listen:
  postgres_port: 99999
tenants: {}
`
	path := writeTemp(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid listen port")
	}
}

func TestValidateTenantInvalidPort(t *testing.T) {
	yaml := `
tenants:
  t1:
    db_type: postgres
    host: localhost
    port: 70000
    dbname: db
    username: user
`
	path := writeTemp(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid tenant port")
	}
}

func TestValidateInvalidTenantID(t *testing.T) {
	yaml := `
tenants:
  "invalid tenant!":
    db_type: postgres
    host: localhost
    port: 5432
    dbname: db
    username: user
`
	path := writeTemp(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid tenant ID")
	}
}

func TestValidateTenantMinGtMax(t *testing.T) {
	yaml := `
tenants:
  t1:
    db_type: postgres
    host: localhost
    port: 5432
    dbname: db
    username: user
    min_connections: 20
    max_connections: 5
`
	path := writeTemp(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error when tenant min_connections > max_connections")
	}
}

func TestValidateHostWithPort(t *testing.T) {
	yaml := `
tenants:
  t1:
    db_type: postgres
    host: "localhost:5432"
    port: 5432
    dbname: db
    username: user
`
	path := writeTemp(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for host containing port")
	}
}

func TestValidateTenantID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"valid-tenant", false},
		{"tenant_123", false},
		{"a", false},
		{"", true},
		{"-starts-with-dash", true},
		{"_starts-with-underscore", true},
		{"has spaces", true},
		{"has.dots", true},
		{"UPPERCASE_OK", false},
	}
	for _, tt := range tests {
		err := ValidateTenantID(tt.id)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateTenantID(%q) err=%v, wantErr=%v", tt.id, err, tt.wantErr)
		}
	}
}

func TestDialTimeoutDefault(t *testing.T) {
	yaml := `
tenants: {}
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Defaults.DialTimeout != 5*time.Second {
		t.Errorf("expected default dial timeout 5s, got %v", cfg.Defaults.DialTimeout)
	}
}

func TestMaxProxyConnectionsDefault(t *testing.T) {
	yaml := `
tenants: {}
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.Listen.MaxProxyConnections != 10000 {
		t.Errorf("expected default max_proxy_connections 10000, got %d", cfg.Listen.MaxProxyConnections)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}
