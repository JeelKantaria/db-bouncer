package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for DBBouncer.
type Config struct {
	Listen      ListenConfig            `yaml:"listen"`
	Defaults    PoolDefaults            `yaml:"defaults"`
	HealthCheck HealthCheckConfig       `yaml:"health_check"`
	Tenants     map[string]TenantConfig `yaml:"tenants"`
}

// HealthCheckConfig defines health check behavior.
type HealthCheckConfig struct {
	Interval          time.Duration `yaml:"interval"`
	FailureThreshold  int           `yaml:"failure_threshold"`
	ConnectionTimeout time.Duration `yaml:"connection_timeout"`
}

// ListenConfig defines the ports and bind addresses DBBouncer listens on.
type ListenConfig struct {
	PostgresPort        int    `yaml:"postgres_port"`
	MySQLPort           int    `yaml:"mysql_port"`
	APIPort             int    `yaml:"api_port"`
	APIBind             string `yaml:"api_bind"`
	APIKey              string `yaml:"api_key"`
	TLSCert             string `yaml:"tls_cert"`
	TLSKey              string `yaml:"tls_key"`
	MaxProxyConnections int    `yaml:"max_proxy_connections"`
}

// PoolDefaults defines default pool settings applied when tenants don't override.
type PoolDefaults struct {
	MinConnections int           `yaml:"min_connections"`
	MaxConnections int           `yaml:"max_connections"`
	IdleTimeout    time.Duration `yaml:"idle_timeout"`
	MaxLifetime    time.Duration `yaml:"max_lifetime"`
	AcquireTimeout time.Duration `yaml:"acquire_timeout"`
	DialTimeout    time.Duration `yaml:"dial_timeout"`
}

// TenantConfig holds the database configuration for a single tenant.
type TenantConfig struct {
	DBType         string         `yaml:"db_type"`
	Host           string         `yaml:"host"`
	Port           int            `yaml:"port"`
	DBName         string         `yaml:"dbname"`
	Username       string         `yaml:"username"`
	Password       string         `yaml:"password"`
	MinConnections *int           `yaml:"min_connections,omitempty"`
	MaxConnections *int           `yaml:"max_connections,omitempty"`
	IdleTimeout    *time.Duration `yaml:"idle_timeout,omitempty"`
	MaxLifetime    *time.Duration `yaml:"max_lifetime,omitempty"`
	AcquireTimeout *time.Duration `yaml:"acquire_timeout,omitempty"`
	DialTimeout    *time.Duration `yaml:"dial_timeout,omitempty"`
}

// EffectiveMinConnections returns the tenant's min connections or the default.
func (t TenantConfig) EffectiveMinConnections(defaults PoolDefaults) int {
	if t.MinConnections != nil {
		return *t.MinConnections
	}
	return defaults.MinConnections
}

// EffectiveMaxConnections returns the tenant's max connections or the default.
func (t TenantConfig) EffectiveMaxConnections(defaults PoolDefaults) int {
	if t.MaxConnections != nil {
		return *t.MaxConnections
	}
	return defaults.MaxConnections
}

// EffectiveIdleTimeout returns the tenant's idle timeout or the default.
func (t TenantConfig) EffectiveIdleTimeout(defaults PoolDefaults) time.Duration {
	if t.IdleTimeout != nil {
		return *t.IdleTimeout
	}
	return defaults.IdleTimeout
}

// EffectiveMaxLifetime returns the tenant's max lifetime or the default.
func (t TenantConfig) EffectiveMaxLifetime(defaults PoolDefaults) time.Duration {
	if t.MaxLifetime != nil {
		return *t.MaxLifetime
	}
	return defaults.MaxLifetime
}

// EffectiveAcquireTimeout returns the tenant's acquire timeout or the default.
func (t TenantConfig) EffectiveAcquireTimeout(defaults PoolDefaults) time.Duration {
	if t.AcquireTimeout != nil {
		return *t.AcquireTimeout
	}
	return defaults.AcquireTimeout
}

// EffectiveDialTimeout returns the tenant's dial timeout or the default.
func (t TenantConfig) EffectiveDialTimeout(defaults PoolDefaults) time.Duration {
	if t.DialTimeout != nil {
		return *t.DialTimeout
	}
	return defaults.DialTimeout
}

// Redacted returns a copy of the TenantConfig with the password masked.
func (t TenantConfig) Redacted() TenantConfig {
	c := t
	if c.Password != "" {
		c.Password = "***REDACTED***"
	}
	return c
}

// TLSEnabled returns true if both TLS cert and key paths are configured.
func (lc ListenConfig) TLSEnabled() bool {
	return lc.TLSCert != "" && lc.TLSKey != ""
}

// tenantIDPattern restricts tenant IDs to Kubernetes-label-safe values.
var tenantIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

// ValidateTenantID returns an error if the tenant ID is not a valid identifier.
func ValidateTenantID(id string) error {
	if !tenantIDPattern.MatchString(id) {
		return fmt.Errorf("tenant ID %q is invalid: must be 1-63 chars, alphanumeric/hyphens/underscores, starting with alphanumeric", id)
	}
	return nil
}

var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

// substituteEnvVars replaces ${VAR_NAME} patterns with environment variable values.
func substituteEnvVars(data []byte) []byte {
	return envVarPattern.ReplaceAllFunc(data, func(match []byte) []byte {
		varName := envVarPattern.FindSubmatch(match)[1]
		if val, ok := os.LookupEnv(string(varName)); ok {
			return []byte(val)
		}
		return match
	})
}

// Load reads and parses a YAML config file with env var substitution.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	data = substituteEnvVars(data)

	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if err := validate(cfg); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	applyDefaults(cfg)
	return cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Listen.PostgresPort == 0 {
		cfg.Listen.PostgresPort = 6432
	}
	if cfg.Listen.MySQLPort == 0 {
		cfg.Listen.MySQLPort = 3307
	}
	if cfg.Listen.APIPort == 0 {
		cfg.Listen.APIPort = 8080
	}
	if cfg.Listen.APIBind == "" {
		cfg.Listen.APIBind = "127.0.0.1"
	}
	if cfg.Defaults.MinConnections == 0 {
		cfg.Defaults.MinConnections = 2
	}
	if cfg.Defaults.MaxConnections == 0 {
		cfg.Defaults.MaxConnections = 20
	}
	if cfg.Defaults.IdleTimeout == 0 {
		cfg.Defaults.IdleTimeout = 5 * time.Minute
	}
	if cfg.Defaults.MaxLifetime == 0 {
		cfg.Defaults.MaxLifetime = 30 * time.Minute
	}
	if cfg.Defaults.AcquireTimeout == 0 {
		cfg.Defaults.AcquireTimeout = 10 * time.Second
	}
	if cfg.Defaults.DialTimeout == 0 {
		cfg.Defaults.DialTimeout = 5 * time.Second
	}
	if cfg.Listen.MaxProxyConnections == 0 {
		cfg.Listen.MaxProxyConnections = 10000
	}
	if cfg.HealthCheck.Interval == 0 {
		cfg.HealthCheck.Interval = 30 * time.Second
	}
	if cfg.HealthCheck.FailureThreshold == 0 {
		cfg.HealthCheck.FailureThreshold = 3
	}
	if cfg.HealthCheck.ConnectionTimeout == 0 {
		cfg.HealthCheck.ConnectionTimeout = 5 * time.Second
	}
}

func validate(cfg *Config) error {
	// Validate listen port ranges
	for name, port := range map[string]int{
		"postgres_port": cfg.Listen.PostgresPort,
		"mysql_port":    cfg.Listen.MySQLPort,
		"api_port":      cfg.Listen.APIPort,
	} {
		if port != 0 && (port < 1 || port > 65535) {
			return fmt.Errorf("listen.%s: %d is not a valid port (must be 1-65535)", name, port)
		}
	}

	// Validate default pool settings
	if cfg.Defaults.MinConnections > cfg.Defaults.MaxConnections && cfg.Defaults.MaxConnections > 0 {
		return fmt.Errorf("defaults: min_connections (%d) must not exceed max_connections (%d)",
			cfg.Defaults.MinConnections, cfg.Defaults.MaxConnections)
	}

	for id, tenant := range cfg.Tenants {
		// Validate tenant ID format
		if err := ValidateTenantID(id); err != nil {
			return err
		}

		if tenant.DBType != "postgres" && tenant.DBType != "mysql" {
			return fmt.Errorf("tenant %q: unsupported db_type %q (must be postgres or mysql)", id, tenant.DBType)
		}
		if tenant.Host == "" {
			return fmt.Errorf("tenant %q: host is required", id)
		}
		if tenant.Port == 0 {
			return fmt.Errorf("tenant %q: port is required", id)
		}
		if tenant.Port < 1 || tenant.Port > 65535 {
			return fmt.Errorf("tenant %q: port %d is not valid (must be 1-65535)", id, tenant.Port)
		}
		if tenant.DBName == "" {
			return fmt.Errorf("tenant %q: dbname is required", id)
		}
		if tenant.Username == "" {
			return fmt.Errorf("tenant %q: username is required", id)
		}

		// Validate min <= max for per-tenant overrides
		if tenant.MinConnections != nil && tenant.MaxConnections != nil {
			if *tenant.MinConnections > *tenant.MaxConnections {
				return fmt.Errorf("tenant %q: min_connections (%d) must not exceed max_connections (%d)",
					id, *tenant.MinConnections, *tenant.MaxConnections)
			}
		}

		// Warn on unresolved environment variables
		for field, value := range map[string]string{
			"host":     tenant.Host,
			"password": tenant.Password,
			"username": tenant.Username,
		} {
			if envVarPattern.MatchString(value) {
				slog.Warn("unresolved env var in tenant config", "tenant", id, "field", field, "value", value)
			}
		}

		// Validate host is not an IP with port (common misconfiguration)
		if _, _, err := net.SplitHostPort(tenant.Host); err == nil {
			return fmt.Errorf("tenant %q: host %q should not include port (use the port field instead)", id, tenant.Host)
		}
	}
	return nil
}

// Watcher watches a config file for changes and calls the callback with the new config.
type Watcher struct {
	path     string
	callback func(*Config)
	watcher  *fsnotify.Watcher
	mu       sync.Mutex
	stopCh   chan struct{}
	stopOnce sync.Once
}

// NewWatcher creates a new config file watcher.
func NewWatcher(path string, callback func(*Config)) (*Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("creating file watcher: %w", err)
	}

	if err := w.Add(path); err != nil {
		w.Close()
		return nil, fmt.Errorf("watching config file: %w", err)
	}

	cw := &Watcher{
		path:     path,
		callback: callback,
		watcher:  w,
		stopCh:   make(chan struct{}),
	}

	go cw.run()
	return cw, nil
}

func (cw *Watcher) run() {
	// Debounce timer to avoid rapid reloads
	var debounce *time.Timer
	for {
		select {
		case event, ok := <-cw.watcher.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if debounce != nil {
					debounce.Stop()
				}
				debounce = time.AfterFunc(500*time.Millisecond, func() {
					cw.reload()
				})
			}
		case err, ok := <-cw.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("config watcher error", "err", err)
		case <-cw.stopCh:
			return
		}
	}
}

func (cw *Watcher) reload() {
	cw.mu.Lock()
	defer cw.mu.Unlock()

	cfg, err := Load(cw.path)
	if err != nil {
		slog.Error("hot-reload failed", "err", err)
		return
	}

	slog.Info("configuration reloaded", "path", cw.path)
	cw.callback(cfg)
}

// Stop stops the config watcher. Safe to call multiple times.
func (cw *Watcher) Stop() error {
	cw.stopOnce.Do(func() {
		close(cw.stopCh)
	})
	return cw.watcher.Close()
}
