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
