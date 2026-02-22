# DBBouncer Performance Optimization Plan

Branch: `feature/performance-optimizations`
Created: 2026-02-23

---

## Phase 0 — Critical Performance (Low-risk, High-impact)

### 0.1 TCP Keepalives on pool dial + accept loop
- **File:** `internal/pool/pool.go` → `dial()`
- **File:** `internal/proxy/server.go` → `acceptLoop()`
- **Change:** Add `KeepAlive: 30 * time.Second` to `net.Dialer`; call `SetKeepAlive(true)` + `SetKeepAlivePeriod(30s)` on accepted client connections
- **Status:** [x] Done

### 0.2 Eliminate Thundering Herd — `Broadcast()` → `Signal()`
- **File:** `internal/pool/pool.go` → `Return()`
- **Change:** Replace `tp.cond.Broadcast()` with `tp.cond.Signal()` when returning a single connection. Keep `Broadcast()` in `Close()` and in the timeout timer.
- **Status:** [x] Done

### 0.3 Buffer Pooling for `io.Copy` relay
- **File:** `internal/proxy/handler.go` → `relay()`
- **Change:** Create a global `sync.Pool` of `[]byte` (32KB buffers). Use `io.CopyBuffer(dst, src, buf)` instead of `io.Copy`. Return buffers via `defer`.
- **Status:** [x] Done

---

## Phase 1 — Operational Hardening (Moderate complexity)

### 1.1 Lock-free Routing via `atomic.Value`
- **File:** `internal/router/router.go`
- **Change:** Replace `sync.RWMutex` + direct map access with `atomic.Value` storing immutable map snapshots. Mutations create a new map copy, store atomically.
- **Status:** [x] Done

### 1.2 Graceful Shutdown Timeout
- **File:** `cmd/dbbouncer/main.go`
- **Change:** Add a 60s timeout on the shutdown sequence. If pool drain exceeds the timeout, force-exit.
- **Status:** [x] Done

### 1.3 Configurable Health Check Interval & Threshold
- **File:** `internal/config/config.go` — added `HealthCheckConfig` struct
- **File:** `internal/health/checker.go` — `NewChecker()` now accepts `config.HealthCheckConfig`
- **File:** `configs/dbbouncer.yaml` — added `health_check` section
- **Status:** [x] Done

### 1.4 Validate Tenant ID in API `createTenant`
- **File:** `internal/api/server.go` → `createTenant()`
- **Change:** Call `config.ValidateTenantID()` before inserting tenant via API.
- **Status:** [x] Done

### 1.5 Preserve Pause State on Config Reload
- **File:** `internal/router/router.go` → `Reload()`
- **Change:** Carry over paused state for tenants that still exist in the new config.
- **Status:** [x] Done (implemented within P1.1 router rewrite)

### 1.6 Protocol-level Error on Connection Limit Rejection
- **File:** `internal/proxy/server.go` → `acceptLoop()`
- **Change:** Send PG ErrorResponse (53300) or MySQL ERR_Packet (1040) before closing when connection limit is hit.
- **Status:** [x] Done

---

## Phase 2 — TLS & Code Quality

### 2.1 TLS Certificate Hot-Reload
- **File:** `internal/proxy/server.go`
- **Change:** Added `certLoader` struct with `GetCertificate` callback. Checks file mod time per handshake, reloads on change.
- **Status:** [x] Done

### 2.2 Consistent `slog` Logging in main.go
- **File:** `cmd/dbbouncer/main.go`
- **Change:** Replaced `log.Fatalf`/`log.Printf` with `slog.Error`/`slog.Info`/`slog.Warn`. Removed `log` import.
- **Status:** [x] Done (implemented within P1.2)

---

## Phase 3 — Architectural (Major, separate PR recommended)

### 3.1 Transaction-Level Pooling
- **Files:** `internal/proxy/postgres.go`, `internal/proxy/mysql.go`, `internal/pool/pool.go`
- **Change:** Parse wire protocol in real-time. For PG: watch for `ReadyForQuery` with `Idle` indicator to return backend connection to pool mid-session. For MySQL: intercept query completion. Requires session state tracking.
- **Status:** [ ] Pending (recommend separate branch/PR)

---

## Test Results

All 7 packages pass (`go test ./... -count=1`):
- `internal/api` — PASS
- `internal/config` — PASS
- `internal/health` — PASS
- `internal/metrics` — PASS
- `internal/pool` — PASS
- `internal/proxy` — PASS
- `internal/router` — PASS

## Files Modified

| File | Changes |
|------|---------|
| `cmd/dbbouncer/main.go` | slog migration, shutdown timeout, health check config |
| `internal/pool/pool.go` | TCP keepalive on dial, Signal() instead of Broadcast() |
| `internal/proxy/handler.go` | sync.Pool buffer reuse for io.CopyBuffer |
| `internal/proxy/server.go` | TCP keepalive on accept, connection limit errors, TLS hot-reload, time import |
| `internal/router/router.go` | atomic.Value for lock-free routing, pause state preservation |
| `internal/config/config.go` | HealthCheckConfig struct, defaults |
| `internal/health/checker.go` | Accept HealthCheckConfig in constructor |
| `internal/health/checker_test.go` | Updated NewChecker calls with config |
| `internal/api/server.go` | Tenant ID validation in createTenant |
| `internal/api/server_test.go` | Updated NewChecker calls with config |
| `configs/dbbouncer.yaml` | Added health_check section |
