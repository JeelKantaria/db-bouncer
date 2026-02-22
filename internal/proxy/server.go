package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"github.com/dbbouncer/dbbouncer/internal/config"
	"github.com/dbbouncer/dbbouncer/internal/health"
	"github.com/dbbouncer/dbbouncer/internal/metrics"
	"github.com/dbbouncer/dbbouncer/internal/pool"
	"github.com/dbbouncer/dbbouncer/internal/router"
)

// Server is the main TCP proxy server.
type Server struct {
	router      *router.Router
	poolMgr     *pool.Manager
	healthCheck *health.Checker
	metrics     *metrics.Collector
	tlsConfig   *tls.Config

	pgListener    net.Listener
	mysqlListener net.Listener

	wg          sync.WaitGroup
	ctx         context.Context
	cancel      context.CancelFunc
	activeConns atomic.Int64
	maxConns    int
}

// NewServer creates a new proxy server.
func NewServer(r *router.Router, pm *pool.Manager, hc *health.Checker, m *metrics.Collector, lc config.ListenConfig) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		router:      r,
		poolMgr:     pm,
		healthCheck: hc,
		metrics:     m,
		ctx:         ctx,
		cancel:      cancel,
		maxConns:    lc.MaxProxyConnections,
	}

	if lc.TLSEnabled() {
		cert, err := tls.LoadX509KeyPair(lc.TLSCert, lc.TLSKey)
		if err != nil {
			slog.Warn("failed to load TLS cert/key — TLS disabled", "err", err)
		} else {
			s.tlsConfig = &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS12,
			}
			slog.Info("TLS enabled", "cert", lc.TLSCert)
		}
	}

	return s
}

// ListenPostgres starts the PostgreSQL proxy listener.
func (s *Server) ListenPostgres(port int) error {
	addr := fmt.Sprintf("0.0.0.0:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s for postgres: %w", addr, err)
	}
	s.pgListener = ln
	slog.Info("PostgreSQL proxy listening", "addr", addr)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.acceptLoop(ln, "postgres")
	}()

	return nil
}

// ListenMySQL starts the MySQL proxy listener.
func (s *Server) ListenMySQL(port int) error {
	addr := fmt.Sprintf("0.0.0.0:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s for mysql: %w", addr, err)
	}
	s.mysqlListener = ln
	slog.Info("MySQL proxy listening", "addr", addr)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.acceptLoop(ln, "mysql")
	}()

	return nil
}

func (s *Server) acceptLoop(ln net.Listener, dbType string) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return
			default:
				slog.Error("accept error", "db_type", dbType, "err", err)
				continue
			}
		}

		// Enforce global connection limit
		if s.maxConns > 0 && s.activeConns.Load() >= int64(s.maxConns) {
			slog.Warn("connection limit reached, rejecting",
				"limit", s.maxConns, "db_type", dbType, "remote_addr", conn.RemoteAddr())
			conn.Close()
			continue
		}

		s.activeConns.Add(1)
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer s.activeConns.Add(-1)
			s.handleConnection(conn, dbType)
		}()
	}
}

func (s *Server) handleConnection(clientConn net.Conn, dbType string) {
	defer clientConn.Close()

	var handler ConnectionHandler
	switch dbType {
	case "postgres":
		handler = &PostgresHandler{
			router:      s.router,
			poolMgr:     s.poolMgr,
			healthCheck: s.healthCheck,
			metrics:     s.metrics,
			tlsConfig:   s.tlsConfig,
		}
	case "mysql":
		handler = &MySQLHandler{
			router:      s.router,
			poolMgr:     s.poolMgr,
			healthCheck: s.healthCheck,
			metrics:     s.metrics,
		}
	default:
		slog.Error("unknown db type", "db_type", dbType)
		return
	}

	if err := handler.Handle(s.ctx, clientConn); err != nil {
		slog.Error("connection error", "db_type", dbType, "err", err)
	}
}

// Stop gracefully shuts down the server.
func (s *Server) Stop() {
	s.cancel()

	if s.pgListener != nil {
		s.pgListener.Close()
	}
	if s.mysqlListener != nil {
		s.mysqlListener.Close()
	}

	s.wg.Wait()
	slog.Info("proxy server stopped")
}
