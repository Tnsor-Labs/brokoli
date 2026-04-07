<p align="center">
  <img src="https://raw.githubusercontent.com/Tnsor-Labs/brokoli/main/ui/public/favicon.svg" width="64" height="64" alt="Brokoli" />
</p>

<h1 align="center">Brokoli</h1>

<p align="center">
  <strong>Data orchestration that deploys in 30 seconds, not 30 minutes.</strong><br/>
  Single binary. Visual editor. Built-in data quality. No infrastructure required.
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> ·
  <a href="#features">Features</a> ·
  <a href="#why-brokoli">Why Brokoli</a> ·
  <a href="#enterprise">Enterprise</a> ·
  <a href="#api-reference">API</a>
</p>

---

## Quick Start

### Option A: Install Script

```bash
curl -fsSL https://raw.githubusercontent.com/Tnsor-Labs/brokoli/main/install.sh | sh
brokoli serve
```

Open `http://localhost:8080`. Create your admin account. Build your first pipeline.

### Option B: Download Binary

```bash
# Linux x86_64
curl -L https://github.com/Tnsor-Labs/brokoli/releases/latest/download/brokoli_Linux_x86_64.tar.gz | tar xz
./brokoli serve
```

### Option C: Python SDK

```bash
pip install brokoli
```

```python
from brokoli import Pipeline, task, source_api, quality_check, sink_file

with Pipeline("my_pipeline", schedule="0 6 * * *") as p:

    data = source_api("Fetch", url="https://api.example.com/data", retries=3)

    @task("Transform")
    def clean(raw):
        return [r for r in raw if r.get("status") == "active"]

    cleaned = clean(data)
    quality_check("Validate", cleaned, rules=["not_null(id)", "unique(id)"])
    cleaned >> sink_file("Save", path="/tmp/output.csv")
```

```bash
brokoli deploy my_pipeline.py --server http://localhost:8080
# Pipeline appears in the visual editor instantly
```

### From Source

```bash
cd ui && npm install && npm run build && cd ..
go build -o brokoli . && ./brokoli serve
```

## Why Brokoli

|  | Airflow | Dagster | Prefect | **Brokoli** |
|---|---|---|---|---|
| **Setup** | Docker + Redis + Celery + Postgres | Docker + Dagit + Daemon | Cloud signup or Docker | `./brokoli serve` |
| **Binary size** | ~500MB with deps | ~400MB | Cloud-dependent | **26MB** |
| **First pipeline** | ~30 min (write Python DAG) | ~20 min (write Python assets) | ~15 min (write Python flow) | **~2 min** (drag and drop) |
| **Visual editor** | No | No | No | **Yes** |
| **Data preview** | No | Limited | No | **Every node, every run** |
| **Data quality** | External (Great Expectations) | External | External | **Built-in (10 rules)** |
| **Auto-profiling** | No | No | No | **Yes — null%, unique%, min/max/mean per column** |
| **Schema drift detection** | No | No | No | **Yes — alerts on column changes** |
| **Self-hosted** | Complex | Complex | Limited | **One file** |

## Features

### Visual Pipeline Editor
Drag-and-drop pipeline builder with 13 node types. No code required for common ETL patterns — but full Python support when you need it.

- SVG canvas with zoom, pan, snap-to-port connections
- Undo/redo, auto-layout, node duplication
- Inline node config with connection selector
- Dry-run preview with data at every node
- YAML/JSON import/export for pipeline-as-code

### 13 Node Types

| Sources | Processing | Outputs | Flow Control |
|---|---|---|---|
| File (CSV/JSON/Excel) | Transform (filter, sort, rename, aggregate) | File Output | **If/Else Condition** |
| REST API | Python Code | Database Sink | |
| Database (Postgres/MySQL/SQLite) | Join (inner/left/right/full) | API Sink | |
| | Quality Check (10 rules) | DB Migration | |
| | SQL Generate | | |

### Data Quality — Built In, Not Bolted On
- **10 quality rules**: not_null, unique, min, max, range, regex, row_count, type_check, freshness, no_blank
- **Auto-profiling**: every node output gets profiled — row count, null%, unique%, min/max/mean, type inference, cardinality
- **Schema drift detection**: automatic comparison against previous runs — alerts on column added/removed, type changes, null rate spikes
- **Quality trends**: profile data stored as time series for historical analysis
- Block or warn policies per rule

### Execution Engine
- **Parallel DAG execution** — wave-based Kahn's algorithm with configurable concurrency
- **Distributed tracing** — trace_id per run, span_id per node attempt for correlation across workers
- **Per-attempt tracking** — each retry creates a separate record with its own timing and status
- **Smart retry per node** — exponential backoff, configurable max retries and delay
- **Resume from failure** — skip succeeded nodes, retry from the point of failure
- **Wait time tracking** — measures queue delay between node readiness and execution start
- **Throughput metrics** — rows/sec calculated per node execution
- **Cross-pipeline dependencies** — `depends_on` field, scheduler checks before triggering
- **Condition branching** — if/else nodes evaluate expressions and route data accordingly
- **Cancellation** — cancel running pipelines via API or UI

### Monitoring & Alerts
- **Real-time WebSocket** updates — live run status, node progress, log streaming
- **Gantt timeline** — full-page interactive execution timeline with dependency arrows
- **Node stats** — historical duration tracking with sparklines across runs
- **Slack notifications** — configurable via UI, fires on run success, failure, and SLA breach
- **Schema drift alerts** — critical schema changes trigger Slack notifications with column details
- **SLA deadlines** — per-pipeline "must complete by HH:MM" with timezone support
- **Run calendar** — GitHub-style heatmap of daily run activity
- **Data lineage** — interactive cross-pipeline DAG visualization

### Connections & Secrets
- **7 connection types**: Postgres, MySQL, SQLite, HTTP, SFTP, S3, Generic
- **AES-256-GCM encryption** for all secrets at rest
- **Real connection testing** — actually connects and authenticates, not just ping
- **Variables** with typed values (string, number, JSON, secret) and `${var.key}` resolution in any config field

### Authentication & Security
- **httpOnly cookie sessions** — no tokens in localStorage
- **JWT auth** with persistent secret
- **API key authentication** for automation
- **Role-based access** — admin, editor, viewer

### Pipeline Versioning
- Auto-snapshot on every save
- Version history with timestamps
- One-click rollback to any version

### Webhook Triggers
- Per-pipeline webhook tokens
- Trigger runs via HTTP: `POST /api/pipelines/:id/webhook?token=whk_...`
- Use with GitHub Actions, dbt, Kafka consumers, or any external event

### CLI for CI/CD

```bash
# Trigger a run and wait for completion
brokoli run <pipeline-id> --server http://localhost:8080

# Run with assertions (exits non-zero on failure)
brokoli assert <pipeline-id> -a assertions.yaml
```

**Assertion file format:**

```yaml
assertions:
  - name: "Has data"
    type: min_rows
    value: "1"
  - name: "ID is unique"
    type: unique
    column: id
  - name: "Email not null"
    type: no_nulls
    column: email
```

### Python Integration

Works with any `python3`. For large datasets:

```bash
pip install pyarrow pandas
```

| Dataset size | Method | Speed |
|---|---|---|
| < 10K rows | JSON stdin/stdout | Baseline |
| ≥ 10K rows | CSV temp files | 3–5x faster |
| ≥ 10K rows + pyarrow | Arrow IPC | 5–10x faster |

Auto-detected. No configuration needed.

## Enterprise

Brokoli Enterprise adds governance, compliance, and team features for production deployments. Same single binary — just with a license key.

| Feature | Community | Enterprise |
|---|---|---|
| Visual pipeline editor | ✓ | ✓ |
| 13 node types + data quality | ✓ | ✓ |
| Auto-profiling + schema drift | ✓ | ✓ |
| Slack alerts (via UI config) | ✓ | ✓ |
| SLA monitoring | ✓ | ✓ |
| Webhook triggers + CLI | ✓ | ✓ |
| Pipeline versioning + rollback | ✓ | ✓ |
| Cross-pipeline dependencies | ✓ | ✓ |
| Smart retry per node | ✓ | ✓ |
| Condition branching (if/else) | ✓ | ✓ |
| Distributed tracing | ✓ | ✓ |
| Gantt execution timeline | ✓ | ✓ |
| **SSO/OIDC** (Okta, Azure AD, Google) | | ✓ |
| **Audit logging** with before/after diff | | ✓ |
| **Workspaces** with RBAC (owner/admin/editor/viewer) | | ✓ |
| **API tokens** per workspace | | ✓ |
| **Git Sync** — pipeline-as-code with auto-deploy | | ✓ |
| **Data contracts** — schema enforcement between teams | | ✓ |
| **Column-level lineage** tracking | | ✓ |
| **PII detection** — auto-scan for email, phone, SSN, IP, credit card | | ✓ |
| **OpenLineage** export to DataHub/Marquez/Atlan | | ✓ |
| **Kubernetes executor** — dispatch nodes as K8s Jobs | | ✓ |
| **Work pools** — managed and self-hosted remote workers | | ✓ |

## API Reference

All endpoints require authentication (except `/health` and `/api/auth/setup`).
Authentication via httpOnly session cookie (set on login) or `Authorization: Bearer <token>` header.

### Auth
```
POST   /api/auth/login                 Login (sets session cookie)
POST   /api/auth/logout                Logout (clears session cookie)
GET    /api/auth/me                    Current user info
GET    /api/auth/setup                 Check if setup needed (no auth)
```

### Pipelines
```
GET    /api/pipelines                  List pipelines
POST   /api/pipelines                  Create pipeline
GET    /api/pipelines/:id              Get pipeline (full detail)
PUT    /api/pipelines/:id              Update pipeline
DELETE /api/pipelines/:id              Delete pipeline
POST   /api/pipelines/:id/run          Trigger run
POST   /api/pipelines/:id/dry-run      Preview with sample rows
POST   /api/pipelines/:id/clone        Duplicate pipeline
POST   /api/pipelines/:id/backfill     Backfill date range
GET    /api/pipelines/:id/export       Export as YAML
GET    /api/pipelines/:id/versions     Version history
POST   /api/pipelines/:id/rollback     Restore version
GET    /api/pipelines/:id/node-stats   Historical node durations
POST   /api/pipelines/:id/webhook      Webhook trigger (token auth)
POST   /api/pipelines/import           Import YAML/JSON
```

### Runs
```
GET    /api/pipelines/:id/runs              List runs
GET    /api/runs/:id                        Run detail (with node_runs, trace_id)
POST   /api/runs/:id/resume                 Resume from failure
POST   /api/runs/:id/cancel                 Cancel running pipeline
GET    /api/runs/:id/logs                   Run logs (with trace_id, span_id)
GET    /api/runs/:id/logs/export            Download logs
GET    /api/runs/:id/nodes/:nid/preview     Node data preview
GET    /api/runs/:id/nodes/:nid/profile     Node profiling data + drift alerts
GET    /api/runs/calendar                   Daily run heatmap
```

### Connections & Variables
```
GET    /api/connections                     List connections
POST   /api/connections                     Create connection
POST   /api/connections/:id/test            Test connection
GET    /api/variables                       List variables
POST   /api/variables                       Create/update variable
```

### Settings & Notifications
```
GET    /api/settings/notifications          Slack config (masked)
PUT    /api/settings/notifications          Save Slack config
POST   /api/settings/notifications/test     Send test Slack message
```

### System
```
GET    /health                              Health check (no auth)
GET    /metrics                             Prometheus metrics (no auth)
GET    /api/system/info                     System info
GET    /api/scheduler/status                Active schedules
GET    /api/lineage                         Cross-pipeline lineage graph
```

## Architecture

```
~30MB single binary
├── Go backend (chi router, gorilla/websocket, SQLite/PostgreSQL)
├── Svelte 5 frontend (embedded via go:embed)
├── Execution engine (parallel DAG, tracing, retry, profiling)
└── Extension system (enterprise plugins via interface injection)
```

```
├── cmd/           CLI — serve, run, assert, generate-key
├── engine/        Execution, transforms, profiling, drift, conditions, retry, scheduler
├── api/           HTTP handlers, auth, WebSocket, middleware, rate limiting
├── store/         SQLite + PostgreSQL dual-dialect store with migrations
├── crypto/        AES-256-GCM encryption
├── quality/       10 data quality rules
├── extensions/    Enterprise plugin interfaces
├── models/        Pipeline, Run, NodeRun, Connection, Variable, Workspace
├── pkg/           Shared utilities (common types, fetchers)
├── web/           Embedded Svelte frontend (go:embed)
└── ui/            Svelte 5 source (components, pages, stores)
```

## Tests

```bash
# Build UI first (required for go:embed)
cd ui && npm install && npm run build && cd ..

# Run all tests
go test ./... -v
```

## Configuration

| Flag | Default | Description |
|---|---|---|
| `--port` / `-p` | 8080 | HTTP server port |
| `--db` | ./brokoli.db | SQLite path (or `postgres://...` URI) |
| `--api-key` | — | Enable API key authentication |

| Env Variable | Description |
|---|---|
| `BROKOLI_APP_URL` | Base URL for Slack deep links (default: `http://localhost:8080`) |
| `BROKOLI_JWT_SECRET` | Persistent JWT signing secret |
| `BROKOLI_CORS_ORIGINS` | Allowed CORS origins (comma-separated, default: `*`) |

## License

Apache 2.0 — see [LICENSE](LICENSE).

Enterprise features are available under a separate commercial license. [Contact us](https://brokoli.dev) for pricing.
