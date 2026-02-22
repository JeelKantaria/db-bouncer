package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"os"
	"sync/atomic"
	"time"

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
		// Verify the cert/key can be loaded initially
		_, err := tls.LoadX509KeyPair(lc.TLSCert, lc.TLSKey)
		if err != nil {
			slog.Warn("failed to load TLS cert/key — TLS disabled", "err", err)
		} else {
			loader := newCertLoader(lc.TLSCert, lc.TLSKey)
			s.tlsConfig = &tls.Config{
				GetCertificate: loader.getCertificate,
				MinVersion:     tls.VersionTLS12,
			}
			slog.Info("TLS enabled with hot-reload", "cert", lc.TLSCert)
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

		// Enable TCP keepalives on client connections
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(30 * time.Second)
		}

		// Enforce global connection limit
		if s.maxConns > 0 && s.activeConns.Load() >= int64(s.maxConns) {
			slog.Warn("connection limit reached, rejecting",
				"limit", s.maxConns, "db_type", dbType, "remote_addr", conn.RemoteAddr())
			sendConnectionLimitError(conn, dbType)
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

// certLoader provides TLS certificate hot-reload by checking file modification
// times and reloading the cert/key pair when they change.
type certLoader struct {
	certPath string
	keyPath  string
	mu       sync.Mutex
	cert     *tls.Certificate
	modTime  time.Time
}

func newCertLoader(certPath, keyPath string) *certLoader {
	cl := &certLoader{certPath: certPath, keyPath: keyPath}
	// Load initial certificate
	cl.loadLocked()
	return cl
}

func (cl *certLoader) loadLocked() {
	cert, err := tls.LoadX509KeyPair(cl.certPath, cl.keyPath)
	if err != nil {
		slog.Error("failed to reload TLS certificate", "err", err)
		return
	}
	cl.cert = &cert
	if info, err := os.Stat(cl.certPath); err == nil {
		cl.modTime = info.ModTime()
	}
	slog.Info("TLS certificate loaded", "cert", cl.certPath)
}

func (cl *certLoader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()

	// Check if cert file has been modified
	if info, err := os.Stat(cl.certPath); err == nil {
		if info.ModTime().After(cl.modTime) {
			cl.loadLocked()
		}
	}

	return cl.cert, nil
}

// sendConnectionLimitError sends a protocol-appropriate error to the client
// before closing the connection when the global limit is reached.
func sendConnectionLimitError(conn net.Conn, dbType string) {
	conn.SetWriteDeadline(time.Now().Add(1 * time.Second))
	switch dbType {
	case "postgres":
		// PG ErrorResponse: severity=FATAL, code=53300 (too_many_connections), message
		var buf []byte
		buf = append(buf, 'S')
		buf = append(buf, "FATAL"...)
		buf = append(buf, 0)
		buf = append(buf, 'C')
		buf = append(buf, "53300"...)
		buf = append(buf, 0)
		buf = append(buf, 'M')
		buf = append(buf, "too many connections"...)
		buf = append(buf, 0)
		buf = append(buf, 0) // terminator
		writePGMessage(conn, pgMsgErrorResponse, buf)
	case "mysql":
		// MySQL ERR_Packet at sequence 0 (before handshake)
		var buf []byte
		buf = append(buf, mysqlErrPacket)
		errCode := uint16(1040) // ER_CON_COUNT_ERROR
		buf = append(buf, byte(errCode), byte(errCode>>8))
		buf = append(buf, '#')
		buf = append(buf, "08004"...)
		buf = append(buf, "Too many connections"...)
		writeMySQLPacket(conn, buf, 0)
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
