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
- **PostgreSQL + MySQL** - Supports both protocols from day 1
- **REST API** - Runtime tenant CRUD, pool stats, and drain operations
- **Health checking** - Background health checks with configurable failure thresholds
- **Prometheus metrics** - Active/idle/waiting connections, query duration, tenant health
- **Hot-reload config** - File watcher reloads configuration without restart
- **Kubernetes-native** - Helm chart, readiness/liveness probes, ServiceMonitor

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

tenants:
  tenant_1:
    db_type: postgres
    host: pg-host.example.com
    port: 5432
    dbname: tenant_1_db
    username: app_user
    password: ${TENANT_1_PG_PASSWORD}
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

## Kubernetes Deployment

### Docker

```bash
make docker-build
```

### Helm

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

## Testing

```bash
make test
make test-cover
```

## License

MIT
