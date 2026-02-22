package main

import (
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dbbouncer/dbbouncer/internal/api"
	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/health"
	"github.com/dbbouncer/dbbouncer/internal/metrics"
	"github.com/dbbouncer/dbbouncer/internal/pool"
	"github.com/dbbouncer/dbbouncer/internal/proxy"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

const shutdownTimeout = 60 * time.Second

func main() {
	configPath := flag.String("config", "configs/dbbouncer.yaml", "path to configuration file")
	flag.Parse()

	slog.Info("DBBouncer starting...")

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed to load config", "err", err)
		os.Exit(1)
	}
	slog.Info("configuration loaded", "path", *configPath, "tenants", len(cfg.Tenants))

	// Initialize components
	m := metrics.New()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	hc := health.NewChecker(r, m, cfg.HealthCheck)

	// Wire up pool exhaustion metric
	pm.SetOnPoolExhausted(func(tenantID string) {
		m.PoolExhausted(tenantID)
	})

	// Start periodic pool stats reporting to Prometheus
	pm.StartStatsLoop(5*time.Second, func(s pool.Stats) {
		m.UpdatePoolStats(s.TenantID, s.DBType, s.Active, s.Idle, s.Total, s.Waiting)
	})

	// Start health checker
	hc.Start()

	// Start proxy server
	proxyServer := proxy.NewServer(r, pm, hc, m, cfg.Listen)

	if err := proxyServer.ListenPostgres(cfg.Listen.PostgresPort); err != nil {
		slog.Error("failed to start PostgreSQL proxy", "err", err)
		os.Exit(1)
	}

	if err := proxyServer.ListenMySQL(cfg.Listen.MySQLPort); err != nil {
		slog.Error("failed to start MySQL proxy", "err", err)
		os.Exit(1)
	}

	// Start REST API
	apiServer := api.NewServer(r, pm, hc, m, cfg.Listen)
	if err := apiServer.Start(cfg.Listen.APIPort); err != nil {
		slog.Error("failed to start API server", "err", err)
		os.Exit(1)
	}

	// Set up config hot-reload
	configWatcher, err := config.NewWatcher(*configPath, func(newCfg *config.Config) {
		slog.Info("reloading configuration...")
		r.Reload(newCfg)
		pm.UpdateDefaults(newCfg.Defaults)
	})
	if err != nil {
		slog.Warn("config hot-reload not available", "err", err)
	}

	slog.Info("DBBouncer ready",
		"pg_port", cfg.Listen.PostgresPort,
		"mysql_port", cfg.Listen.MySQLPort,
		"api_port", cfg.Listen.APIPort)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	slog.Info("received signal, shutting down...", "signal", sig)

	// Graceful shutdown with timeout
	done := make(chan struct{})
	go func() {
		if configWatcher != nil {
			configWatcher.Stop()
		}
		apiServer.Stop()
		proxyServer.Stop()
		hc.Stop()
		pm.Close()
		close(done)
	}()

	select {
	case <-done:
		slog.Info("DBBouncer stopped")
	case <-time.After(shutdownTimeout):
		slog.Error("shutdown timed out, forcing exit", "timeout", shutdownTimeout)
		os.Exit(1)
	}
}
