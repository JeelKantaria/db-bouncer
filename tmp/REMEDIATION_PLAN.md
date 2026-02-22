# DBBouncer Code Quality Remediation Plan

> **Generated:** 2026-02-21
> **Status:** Phase 1-2 COMPLETED, Phase 3 NOT STARTED
> **Project:** D:/claude/dbbouncer
> **Total Phases:** 5

---

## How to Use This Plan

Each phase is self-contained. After completing a phase, update the status below and the next agent session can pick up from the next phase. Instruct the agent:

> "Read D:/claude/dbbouncer/tmp/REMEDIATION_PLAN.md and execute the next incomplete phase."

---

## Phase Tracker

| Phase | Focus | Status | Completed Date |
|-------|-------|--------|----------------|
| 1 | Critical Security Fixes | COMPLETED | 2026-02-21 |
| 2 | Critical Concurrency & Protocol Bugs | COMPLETED | 2026-02-22 |
| 3 | High-Priority Improvements | NOT STARTED | - |
| 4 | Medium Issues & Deployment Hardening | NOT STARTED | - |
| 5 | Testing, Code Style & Polish | NOT STARTED | - |

---

## Phase 1: Critical Security Fixes

**Priority:** CRITICAL — Must fix before any deployment
**Estimated scope:** ~6 files modified, ~2 files created

### Task 1.1: API Authentication (api/server.go)

- Add a configurable API key / Bearer token authentication middleware
- Add the `api_key` field to config struct in `internal/config/config.go`
- Create middleware function that checks `Authorization: Bearer <token>` header
- Apply middleware to all routes except `/health`, `/ready`, and `/metrics`
- Return `401 Unauthorized` if token is missing/invalid
- Add config validation: warn if API is enabled without a key set

### Task 1.2: Password Redaction in API Responses (api/server.go)

- In `tenantResponse` and any endpoint returning `TenantConfig`, redact the `Password` field
- Replace password value with `"***REDACTED***"` in JSON output
- Ensure `/config` endpoint also redacts passwords
- File: `internal/api/server.go` — around line 118 and any serialization of TenantConfig

### Task 1.3: Request Body Size Limit (api/server.go)

- Add `http.MaxBytesReader` wrapper to all endpoints that read request bodies
- Set limit to 1MB (configurable if easy, otherwise hardcoded is fine)
- File: `internal/api/server.go` — around line 149 (create/update tenant handlers)

### Task 1.4: API Bind Address (api/server.go)

- Change default bind from `0.0.0.0` to `127.0.0.1`
- Add `api_bind` field to `ListenConfig` so users can override it
- File: `internal/api/server.go` line 76, `internal/config/config.go`

### Task 1.5: Static MySQL Auth Nonce (proxy/mysql.go)

- Replace hardcoded 20-byte auth challenge (lines 160-187) with `crypto/rand.Read()`
- Generate a fresh random nonce for every new client connection
- File: `internal/proxy/mysql.go`

### Task 1.6: TLS/SSL Support — Phase 1 (proxy/postgres.go, proxy/mysql.go)

- Instead of hard-rejecting SSL with 'N', add optional TLS support
- Add TLS config fields to `ListenConfig`: `tls_cert`, `tls_key`, `tls_enabled`
- For PostgreSQL: respond with 'S' and upgrade to TLS when configured, keep 'N' as fallback
- For MySQL: set `CLIENT_SSL` capability flag in handshake when TLS is configured
- Add `tls.Config` loading in config package with cert/key file paths
- Files: `internal/proxy/postgres.go`, `internal/proxy/mysql.go`, `internal/config/config.go`, `internal/proxy/server.go`

### Task 1.7: Tests for Security Fixes

- Test API auth middleware (valid token, missing token, invalid token)
- Test password redaction in tenant list/get/create responses
- Test request body size limit rejection
- Test MySQL random nonce is different per connection

### Verification

```bash
cd D:/claude/dbbouncer && go test ./... && go vet ./...
```

### On Completion

Update Phase 1 status in this file to `COMPLETED` with date.

---

## Phase 2: Critical Concurrency & Protocol Bugs

**Priority:** CRITICAL — Causes goroutine leaks, panics, and broken connections
**Estimated scope:** ~5 files modified

### Task 2.1: Fix Goroutine Leak in relay() (proxy/handler.go)

- File: `internal/proxy/handler.go` lines 50-52
- On error, currently returns without calling `wg.Wait()` or closing connections
- Fix: ensure both connections are closed and `wg.Wait()` is called on all exit paths
- Use `defer` to guarantee cleanup

### Task 2.2: Fix Lost Wakeups in Pool (pool/pool.go)

- File: `internal/pool/pool.go` lines 49, 186-189
- `waitCh` buffer of 1 drops signals when multiple goroutines wait
- Replace channel-based wakeup with `sync.Cond` for reliable broadcast
- Ensure `Acquire()` properly waits and gets woken up when connections are returned

### Task 2.3: Fix Broken Ping() — Zero-Byte Read (pool/conn.go)

- File: `internal/pool/conn.go` lines 120-134
- Read into a 0-length buffer returns `(0, nil)` immediately — health check is a no-op
- Fix: use a 1-byte buffer with a short read deadline, expect `os.ErrDeadlineExceeded` for healthy conn
- Pattern: `SetReadDeadline(now+100ms)`, `Read(buf[0:1])`, check for timeout error (healthy) vs other error (dead)

### Task 2.4: Fix Double-Close Panics (pool/pool.go, health/checker.go, config/config.go)

- Files: `internal/pool/pool.go:459`, `internal/health/checker.go:82`, `internal/config/config.go:250`
- All close channels without `sync.Once` — calling twice panics
- Wrap each channel-close in `sync.Once.Do()` to make idempotent

### Task 2.5: Fix Broken Connection Return After Relay (proxy/postgres.go, proxy/mysql.go)

- Files: `internal/proxy/postgres.go:86`, `internal/proxy/mysql.go:98`
- After `relay()`, backend connection may be half-closed but `defer pc.Return()` puts it back in pool
- Fix: check connection health before returning to pool, or call `pc.Close()` instead of `pc.Return()` after relay
- Better approach: always close after relay — proxied connections should not be reused since their protocol state is unknown

### Task 2.6: Fix Unbounded SSL Recursion (proxy/postgres.go)

- File: `internal/proxy/postgres.go` lines 135-139
- Malicious client can send unlimited SSL request packets causing stack overflow
- Fix: convert recursion to a loop with a max iteration count (e.g., 3 attempts)

### Task 2.7: Fix Hardcoded MySQL Sequence Number (proxy/mysql.go)

- File: `internal/proxy/mysql.go` lines 125, 355
- Sequence number always 2 — diverges from actual sequence under auth switching
- Fix: track sequence number as state, increment properly per the MySQL protocol spec

### Task 2.8: Tests for Concurrency Fixes

- Test `relay()` cleanup: both goroutines exit, no leaks (use `runtime.NumGoroutine()`)
- Test pool `Acquire()`/`Return()` under concurrent access with `-race`
- Test `Ping()` correctly detects dead connections
- Test double-close is safe (no panic)
- Test SSL request loop terminates after max attempts

### Verification

```bash
cd D:/claude/dbbouncer && go test -race ./... && go vet ./...
```

### On Completion

Update Phase 2 status in this file to `COMPLETED` with date.

---

## Phase 3: High-Priority Improvements

**Priority:** HIGH — Significant operational and correctness issues
**Estimated scope:** ~6 files modified

### Task 3.1: Parallel Health Checks (health/checker.go)

- File: `internal/health/checker.go` lines 107-110
- `checkAll()` is sequential — with 100 tenants and downed DBs, it can take 500s
- Fix: run health checks concurrently with a bounded worker pool (e.g., `semaphore` or `errgroup` with limit of 10)
- Ensure results are collected thread-safely

### Task 3.2: Real Health Checks — Not Just TCP (health/checker.go)

- File: `internal/health/checker.go` line 115
- Currently only verifies port is open, not that DB accepts queries
- For PostgreSQL: send a startup message and check for response, or use a simple query
- For MySQL: complete a handshake connection attempt
- Fallback: at minimum, do a TCP dial + read with a short timeout to detect RST

### Task 3.3: Add context.Context to Pool (pool/pool.go)

- File: `internal/pool/pool.go`
- `Acquire()` and internal `dial()` don't accept a context
- Add `context.Context` parameter to `Acquire(ctx)` and `dial(ctx)`
- Use `ctx` for cancellation and deadline propagation
- Update all callers in `proxy/postgres.go`, `proxy/mysql.go`

### Task 3.4: Fix Metrics Conflict (metrics/metrics.go)

- File: `internal/metrics/metrics.go` lines 88-94 vs 117-122
- `ConnectionOpened()` does `Inc()` and `UpdatePoolStats()` does `Set()` on the same gauge
- Fix: remove `Inc()/Dec()` from `ConnectionOpened()/Closed()` — let `UpdatePoolStats()` be the sole authority via periodic `Set()` calls, OR remove `Set()` from `UpdatePoolStats` and only use `Inc()/Dec()`
- Pick one approach and be consistent

### Task 3.5: Fix MustRegister Panics (metrics/metrics.go)

- File: `internal/metrics/metrics.go` line 74
- If `New()` is called twice (tests, config reload), process panics
- Fix: use `prometheus.NewRegistry()` instead of the global default, or use `sync.Once` for registration

### Task 3.6: Fix Idle Reaper Ordering (pool/pool.go)

- File: `internal/pool/pool.go` lines 292-313
- Currently preserves oldest (front) and reaps newest — should be inverted
- Fix: reap from the front (oldest connections first), preserve newest

### Task 3.7: Add Min-Connections Pre-Warming (pool/pool.go)

- File: `internal/pool/pool.go` lines 57-81
- `minConns` is configured but no connections are created on startup
- Add `warmUp()` method that pre-creates `minConns` connections when a pool is first created
- Run in a goroutine so it doesn't block startup

### Task 3.8: Tests for Phase 3

- Test parallel health checks complete faster than sequential
- Test `Acquire(ctx)` respects context cancellation
- Test metrics consistency (no conflicts between Inc/Set)
- Test idle reaper removes oldest connections first
- Test pool pre-warming creates minConns connections on startup

### Verification

```bash
cd D:/claude/dbbouncer && go test -race ./... && go vet ./...
```

### On Completion

Update Phase 3 status in this file to `COMPLETED` with date.

---

## Phase 4: Medium Issues & Deployment Hardening

**Priority:** MEDIUM — Operational robustness and Kubernetes best practices
**Estimated scope:** ~8 files modified, ~4 files created

### Task 4.1: Config Validation Improvements (config/config.go)

- Validate `MinConnections <= MaxConnections`
- Validate port ranges (1-65535)
- Detect and warn on unresolved `${ENV_VAR}` patterns (literal `${...}` in values after substitution)
- Add tenant ID validation: alphanumeric + hyphens + underscores, max 63 chars (Kubernetes label-safe)

### Task 4.2: TOCTOU Race Fix in API (api/server.go)

- `updateTenant` reads then writes with no lock — concurrent PUTs can overwrite each other
- Add locking (mutex) around read-modify-write in update operations
- Or use optimistic concurrency with an ETag/version field

### Task 4.3: Sanitize Error Messages (proxy/postgres.go, proxy/mysql.go)

- Error messages like "tenant X is paused" leak tenant existence and status
- Genericize client-facing errors: "connection refused" or "authentication failed"
- Keep detailed errors in server-side logs only

### Task 4.4: Connection Limits (proxy/server.go)

- `acceptLoop` spawns unbounded goroutines
- Add global and per-tenant connection limit configuration
- Track active connections with atomic counter
- Reject new connections with appropriate error when limit is reached

### Task 4.5: Health Checker Cleanup (health/checker.go)

- When a tenant is deleted, its health entry is never cleaned up
- Add `RemoveTenant(tenantID)` method to health checker
- Call it from tenant deletion in API and router

### Task 4.6: Configurable Dial Timeout (pool/pool.go)

- Line 271 — 5s timeout is hardcoded
- Add `DialTimeout` to `PoolDefaults` and `TenantConfig`
- Default to 5s for backward compatibility

### Task 4.7: Create .dockerignore

- Create `D:/claude/dbbouncer/.dockerignore` with:
  ```
  .git
  bin/
  tmp/
  *.exe
  deploy/
  README.md
  Makefile
  ```

### Task 4.8: Helm Chart Hardening

- **securityContext** in deployment.yaml:
  ```yaml
  securityContext:
    runAsNonRoot: true
    readOnlyRootFilesystem: true
    allowPrivilegeEscalation: false
    capabilities:
      drop: ["ALL"]
  ```
- **PodDisruptionBudget** — new template `pdb.yaml`: `minAvailable: 1`
- **image.tag** — change default from `latest` to `{{ .Chart.AppVersion }}`
- **Secrets template** — new `secret.yaml` for database credentials, reference in deployment
- **startupProbe** — add to deployment.yaml with relaxed thresholds
- **podAntiAffinity** — add soft anti-affinity for HA
- **_helpers.tpl** — create standard label/name helpers
- **NOTES.txt** — create post-install notes

### Task 4.9: Tests for Phase 4

- Test config validation catches MinConns > MaxConns, invalid ports, unresolved env vars
- Test tenant ID validation
- Test connection limits (reject when full)
- Test health entry cleanup on tenant deletion

### Verification

```bash
cd D:/claude/dbbouncer && go test -race ./... && go vet ./...
```

### On Completion

Update Phase 4 status in this file to `COMPLETED` with date.

---

## Phase 5: Testing, Code Style & Polish

**Priority:** MEDIUM-LOW — Production hardening and maintainability
**Estimated scope:** ~10 files modified, ~1 file created

### Task 5.1: Core Pool Tests (pool/pool_test.go)

- Test `Acquire()`/`Return()` basic flow with a real (mock) connection
- Test acquire with pool exhaustion and timeout
- Test concurrent `Acquire()`/`Return()` with multiple goroutines under `-race`
- Test `Drain()` waits for active connections then closes
- Test `Close()` shuts down cleanly
- Test `reapIdle()` removes expired connections

### Task 5.2: Fix Proxy Tests (proxy/proxy_test.go)

- `TestWriteReadPGMessage` manually constructs bytes but never calls `writePGMessage`/`readPGMessage`
- Rewrite to actually call the functions and verify output
- Add tests for `readMySQLPacket`/`writeMySQLPacket`

### Task 5.3: Fix t.Errorf in Goroutine (proxy/integration_test.go)

- Line 421 — `t.Errorf` from a goroutine is a data race
- Replace with channel-based error collection or use `t.Cleanup` with error channel
- Pattern: send errors to a channel, read and assert in main goroutine

### Task 5.4: Add Missing Feature Tests

- Pause/resume tenant flow (end-to-end)
- Config watcher reload (create temp file, modify, verify callback)
- Double-`Stop()` safety on all stoppable components

### Task 5.5: Structured Logging (replace log.Printf)

- Replace `log.Printf` with `log/slog` (stdlib, Go 1.21+)
- Use structured fields: `slog.String("tenant", id)`, `slog.Int("port", port)`
- Replace `[pool]`, `[health]` prefixes with logger groups or attributes
- Add log level support (configurable via config or env var)

### Task 5.6: Compile-Time Interface Assertions

- Add `var _ ConnectionHandler = (*PostgresHandler)(nil)` in `proxy/postgres.go`
- Add `var _ ConnectionHandler = (*MySQLHandler)(nil)` in `proxy/mysql.go`

### Task 5.7: MySQL Named Constants (proxy/mysql.go)

- Replace magic hex numbers (e.g., `0x00200000`) with named constants
- Define all MySQL capability flags, command types, and status flags as `const` block
- Reference: MySQL docs for CLIENT_* flags

### Task 5.8: Extract Dashboard to embed.FS (api/dashboard_html.go)

- Move the HTML content from the Go string constant to `web/static/index.html` (already exists)
- Use `//go:embed web/static/index.html` to embed at compile time
- This makes the dashboard lintable, editable with proper tooling, and testable
- Update `dashboard.go` to serve from `embed.FS`

### Task 5.9: CSRF Protection on Dashboard

- Add CSRF token generation and validation for mutating API calls from the dashboard
- Set `SameSite=Strict` on any cookies
- Add `X-Content-Type-Options: nosniff` and other security headers

### Verification

```bash
cd D:/claude/dbbouncer && go test -race -cover ./... && go vet ./...
```

### On Completion

Update Phase 5 status in this file to `COMPLETED` with date.

---

## Post-Completion Checklist

After all 5 phases:

- [ ] All tests pass with `-race` flag
- [ ] `go vet ./...` clean
- [ ] `golangci-lint run ./...` clean (if linter is installed)
- [ ] Docker build succeeds
- [ ] Helm template renders without errors
- [ ] README updated to document new config fields (TLS, API auth, etc.)
- [ ] Run the binary and verify startup with sample config

---

## Summary of Files Touched Per Phase

| Phase | Files Modified | Files Created |
|-------|---------------|---------------|
| 1 | api/server.go, proxy/mysql.go, proxy/postgres.go, proxy/server.go, config/config.go | - |
| 2 | proxy/handler.go, pool/pool.go, pool/conn.go, proxy/postgres.go, proxy/mysql.go, health/checker.go, config/config.go | - |
| 3 | health/checker.go, pool/pool.go, metrics/metrics.go, proxy/postgres.go, proxy/mysql.go | - |
| 4 | config/config.go, api/server.go, proxy/postgres.go, proxy/mysql.go, proxy/server.go, pool/pool.go, health/checker.go | .dockerignore, helm templates (pdb.yaml, secret.yaml, _helpers.tpl, NOTES.txt) |
| 5 | pool/pool_test.go, proxy/proxy_test.go, proxy/integration_test.go, all packages (logging), proxy/mysql.go, api/dashboard.go, api/dashboard_html.go | - |
