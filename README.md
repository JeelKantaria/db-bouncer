# DBBouncer

Multi-tenant database connection pooler for Kubernetes. DBBouncer sits between your application pods and tenant databases, providing centralized connection pooling, routing, and observability.

## Problem

Multi-tenant apps using a database-per-tenant pattern across multiple Kubernetes pods cause **connection explosion**:

```
50 tenants × 10 pods × 5 connections = 2,500+ connections
```

DBBouncer reduces this to a shared, managed pool per tenant.

## Features

- **Multi-tenant routing** - Routes connections to the correct tenant database based on tenant ID
- **Connection pooling** - Per-tenant pool with min/max/idle management and auto-scaling
- **Transaction-level pooling** - Returns PostgreSQL backend connections to the pool at transaction boundaries, allowing N clients to share M connections (M << N)
- **PostgreSQL + MySQL** - Supports both protocols from day 1
- **REST API** - Runtime tenant CRUD, pool stats, and drain operations
- **Health checking** - Background health checks with configurable failure thresholds
- **Prometheus metrics** - Active/idle/waiting connections, query duration, tenant health
- **Hot-reload config** - File watcher reloads configuration without restart
- **Kubernetes-native** - Helm chart, readiness/liveness probes, ServiceMonitor
- **Secure by Default** - Non-root container, TLS backend support, and authenticated management API
- **Production Grade** - Race-free concurrency, structured logging (`log/slog`), and graceful degradation during connection spikes

## Quick Start

### Build

```bash
make build
```

### Configure

Edit `configs/dbbouncer.yaml`:

```yaml
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
  pool_mode: session         # "session" (default) or "transaction"

tenants:
  tenant_1:
    db_type: postgres
    host: pg-host.example.com
    port: 5432
    dbname: tenant_1_db
    username: app_user
    password: ${TENANT_1_PG_PASSWORD}
    # pool_mode: transaction # Override default pool_mode per tenant
```

### Run

```bash
./bin/dbbouncer -config configs/dbbouncer.yaml
```

### Connect

**PostgreSQL** (pass tenant ID via options):
```bash
psql "host=localhost port=6432 user=myuser options='-c tenant_id=tenant_1'"
```

**MySQL** (pass tenant ID via username format):
```bash
mysql -h localhost -P 3307 -u tenant_1__appuser -p
```

## REST API

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | /tenants | List all tenants with pool stats |
| POST | /tenants | Register new tenant |
| GET | /tenants/{id} | Get tenant details |
| PUT | /tenants/{id} | Update tenant config |
| DELETE | /tenants/{id} | Remove tenant (drains connections) |
| GET | /tenants/{id}/stats | Per-tenant connection stats |
| POST | /tenants/{id}/drain | Drain all connections |
| GET | /health | Overall health status |
| GET | /ready | Readiness probe |
| GET | /metrics | Prometheus metrics |

### Register a tenant at runtime

```bash
curl -X POST localhost:8080/tenants -H 'Content-Type: application/json' -d '{
  "id": "new_tenant",
  "db_type": "postgres",
  "host": "pg-host.example.com",
  "port": 5432,
  "dbname": "new_tenant_db",
  "username": "app_user",
  "password": "secret"
}'
```

## Metrics

Prometheus metrics exposed at `/metrics`:

| Metric | Labels | Description |
|--------|--------|-------------|
| `dbbouncer_connections_active` | tenant, db_type | Active connections |
| `dbbouncer_connections_idle` | tenant, db_type | Idle connections |
| `dbbouncer_connections_total` | tenant, db_type | Total connections |
| `dbbouncer_connections_waiting` | tenant, db_type | Goroutines waiting for a connection |
| `dbbouncer_query_duration_seconds` | tenant, db_type | Session duration histogram |
| `dbbouncer_tenant_health` | tenant | Health status (1=healthy, 0=unhealthy) |
| `dbbouncer_pool_exhausted_total` | tenant | Pool exhaustion events |

## Security

- **API Authentication:** Secure the REST API by setting `api_key` in the configuration. The API expects a `Bearer` token.
- **TLS Support:** Enable end-to-end TLS for backend proxying by providing `tls_cert` and `tls_key` in the configuration.
- **Hardened Container:** Runs as a non-root user (`dbbouncer`) in a minimal Alpine image.

## Kubernetes Deployment

DBBouncer is designed to run natively in Kubernetes.

### Docker

```bash
make docker-build
```

### Helm

The included Helm chart (`deploy/helm/dbbouncer`) provides:
- **Deployment** & **Service**
- **ConfigMap** with hot-reloading (no pod restarts required for config changes)
- **PodDisruptionBudget (PDB)** for high availability during node drains
- **ServiceMonitor** for automatic Prometheus scraping

```bash
helm install dbbouncer deploy/helm/dbbouncer \
  --set config.tenants.my_tenant.db_type=postgres \
  --set config.tenants.my_tenant.host=pg-host \
  --set config.tenants.my_tenant.port=5432 \
  --set config.tenants.my_tenant.dbname=mydb \
  --set config.tenants.my_tenant.username=appuser
```

## Architecture

```
┌─────────────────┐     ┌─────────────────────────────────┐     ┌──────────────┐
│   App Pod 1     │────▶│         DBBouncer               │────▶│ Tenant 1 DB  │
│   App Pod 2     │────▶│                                 │────▶│ Tenant 2 DB  │
│   App Pod N     │────▶│  ┌────────┐  ┌──────────────┐  │────▶│ Tenant N DB  │
│                 │     │  │ Router │──│ Pool Manager │  │     │              │
└─────────────────┘     │  └────────┘  └──────────────┘  │     └──────────────┘
                        │  ┌────────┐  ┌──────────────┐  │
                        │  │ Health │  │   Metrics    │  │
                        │  └────────┘  └──────────────┘  │
                        └─────────────────────────────────┘
```

## Pool Modes

DBBouncer supports two pool modes for PostgreSQL tenants, controlled by the `pool_mode` setting:

### Session Mode (default)

```yaml
pool_mode: session
```

Each client session gets a dedicated backend connection for its entire lifetime. The connection is closed when the client disconnects. This is the safest mode and works with all PostgreSQL features.

### Transaction Mode

```yaml
pool_mode: transaction
```

Backend connections are returned to the pool at transaction boundaries (when PostgreSQL reports `ReadyForQuery` with idle status). This allows N clients to share M backend connections where M << N, dramatically reducing the number of connections to the database.

**How it works:**
1. The pool pre-authenticates backend connections during startup
2. When a client connects, it receives a synthetic auth-ok with cached server parameters
3. A backend connection is acquired from the pool when the client sends a query
4. After the transaction completes (COMMIT/ROLLBACK or auto-commit), `DISCARD ALL` is sent to reset session state, and the backend is returned to the pool
5. The next query may use a different backend connection

**Session pinning:** Certain operations require holding the backend for the rest of the session. DBBouncer automatically detects these and falls back to session-level behavior:
- `LISTEN` / `NOTIFY` commands
- Named prepared statements (extended query protocol with non-empty statement name)

**Dirty disconnect handling:** If a client disconnects mid-transaction, DBBouncer sends `ROLLBACK` followed by `DISCARD ALL` to clean up the backend before returning it to the pool.

**Limitations:**
- Transaction mode is currently **PostgreSQL only**. MySQL tenants always use session mode.
- Session-level state (SET variables, temporary tables, unnamed cursors) does not persist between transactions.
- Applications using `LISTEN`/`NOTIFY` or named prepared statements will be pinned to a single backend (no multiplexing benefit for those sessions).

**Example: 50 tenants with transaction mode**
```
Session mode:  50 tenants x 10 pods x 5 conns = 2,500 backend connections
Transaction mode: 50 tenants x 20 pool conns     =  1,000 backend connections
```

## Testing

```bash
make test
make test-cover
```

## License

MIT
