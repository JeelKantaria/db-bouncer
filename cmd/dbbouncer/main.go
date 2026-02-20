package main

import (
	"flag"
	"log"
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

func main() {
	configPath := flag.String("config", "configs/dbbouncer.yaml", "path to configuration file")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("DBBouncer starting...")

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("Configuration loaded from %s (%d tenants)", *configPath, len(cfg.Tenants))

	// Initialize components
	m := metrics.New()
	r := router.New(cfg)
	pm := pool.NewManager(cfg.Defaults)
	hc := health.NewChecker(r, m)

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
	proxyServer := proxy.NewServer(r, pm, hc, m)

	if err := proxyServer.ListenPostgres(cfg.Listen.PostgresPort); err != nil {
		log.Fatalf("Failed to start PostgreSQL proxy: %v", err)
	}

	if err := proxyServer.ListenMySQL(cfg.Listen.MySQLPort); err != nil {
		log.Fatalf("Failed to start MySQL proxy: %v", err)
	}

	// Start REST API
	apiServer := api.NewServer(r, pm, hc, m, cfg.Listen)
	if err := apiServer.Start(cfg.Listen.APIPort); err != nil {
		log.Fatalf("Failed to start API server: %v", err)
	}

	// Set up config hot-reload
	configWatcher, err := config.NewWatcher(*configPath, func(newCfg *config.Config) {
		log.Printf("Reloading configuration...")
		r.Reload(newCfg)
		pm.UpdateDefaults(newCfg.Defaults)
	})
	if err != nil {
		log.Printf("Warning: config hot-reload not available: %v", err)
	}

	log.Printf("DBBouncer ready - PG:%d MySQL:%d API:%d",
		cfg.Listen.PostgresPort, cfg.Listen.MySQLPort, cfg.Listen.APIPort)

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received signal %s, shutting down...", sig)

	// Graceful shutdown
	if configWatcher != nil {
		configWatcher.Stop()
	}
	apiServer.Stop()
	proxyServer.Stop()
	hc.Stop()
	pm.Close()

	log.Printf("DBBouncer stopped")
}
