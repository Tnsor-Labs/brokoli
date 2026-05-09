<p align="center">
  <img src="https://raw.githubusercontent.com/Tnsor-Labs/brokoli/main/ui/public/favicon.svg" width="64" height="64" alt="Brokoli" />
</p>

<h1 align="center">Brokoli</h1>

<p align="center">
  Self-hosted data pipeline orchestration. Single binary, visual editor, built-in data quality.
</p>

<p align="center">
  <a href="https://github.com/Tnsor-Labs/brokoli/releases/latest">
    <img src="https://img.shields.io/github/v/release/Tnsor-Labs/brokoli?color=0066cc&labelColor=0a0a0a" alt="Latest release" />
  </a>
  <a href="https://github.com/Tnsor-Labs/brokoli/stargazers">
    <img src="https://img.shields.io/github/stars/Tnsor-Labs/brokoli?color=0066cc&labelColor=0a0a0a" alt="Stars" />
  </a>
  <a href="https://github.com/Tnsor-Labs/brokoli/blob/main/LICENSE">
    <img src="https://img.shields.io/badge/license-Apache%202.0-0066cc?labelColor=0a0a0a" alt="Apache 2.0" />
  </a>
  <a href="https://github.com/Tnsor-Labs/brokoli/pulse">
    <img src="https://img.shields.io/github/commit-activity/m/Tnsor-Labs/brokoli?color=0066cc&labelColor=0a0a0a" alt="Commit activity" />
  </a>
</p>

<p align="center">
  <a href="#quick-start">Quick Start</a> ·
  <a href="#features">Features</a> ·
  <a href="#why-brokoli">Why Brokoli</a> ·
  <a href="#enterprise">Enterprise</a> ·
  <a href="#api-reference">API Reference</a>
</p>

---

Brokoli is a data pipeline orchestrator that runs as a single ~30MB binary. It ships with a visual drag-and-drop editor, parallel DAG execution, built-in data quality checks, real-time monitoring, and a Python SDK — no infrastructure required beyond the binary itself.

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

---

## Why Brokoli

|  | Airflow | Dagster | Prefect | **Brokoli** |
|---|---|---|---|---|
| **Setup** | Docker + Redis + Celery + Postgres | Docker + Dagit + Daemon | Cloud signup or Docker | `./brokoli serve` |
| **Binary size** | ~500MB with deps | ~400MB | Cloud-dependent | **26MB** |
| **First pipeline** | ~30 min | ~20 min | ~15 min | **~2 min** |
| **Visual editor** | No | No | No | **Yes** |
| **Data preview** | No | Limited | No | **Every node, every run** |
| **Data quality** | External | External | External | **Built-in (10 rules)** |
| **Auto-profiling** | No | No | No | **Yes** |
| **Schema drift detection** | No | No | No | **Yes** |
| **Self-hosted** | Complex | Complex | Limited | **One file** |

---

## Features

### Visual Pipeline Editor

Drag-and-drop pipeline builder with 15 node types. No code required for common ETL patterns — full Python support when you need it.

- SVG canvas with zoom, pan, snap-to-port connections
- Undo/redo, auto-layout, node duplication
- Inline node config with connection selector
- Dry-run preview with data at every node
- YAML/JSON import/export for pipeline-as-code

### Node Types

| Sources | Processing | Outputs | Flow Control |
|---|---|---|---|
| File (CSV/JSON/Excel) | Transform | File | Condition (if/else) |
| REST API | Python Code | Database | |
| Database (Postgres/MySQL/SQLite) | Join (inner/left/right/full) | API | |
| dbt | Quality Check (10 rules) | Migration | |
| | SQL Generate | Notify | |

### Data Quality — Built In, Not Bolted On

- **10 quality rules**: not_null, unique, min, max, range, regex, row_count, type_check, freshness, no_blank
- **Auto-profiling**: every node output gets null%, unique%, min/max/mean, type inference, and cardinality
- **Schema drift detection**: automatic column comparison across runs — alerts on added/removed columns, type changes, null rate spikes
- **Quality trends**: profile data stored as time series for historical analysis
- Block or warn policies per rule

### Execution Engine

- **Parallel DAG execution** — wave-based Kahn's algorithm with configurable concurrency
- **Distributed tracing** — trace_id per run, span_id per node for correlation across workers
- **Per-attempt tracking** — each retry creates a separate record with its own timing and status
- **Smart retry per node** — exponential backoff, configurable max retries and delay
- **Resume from failure** — skip succeeded nodes, retry from the point of failure
- **Throughput metrics** — rows/sec calculated per node execution
- **Cross-pipeline dependencies** — rich `dependency_rules` with state, mode (`gate`/`trigger`), and freshness windows
- **Cycle detection** — DFS prevents dependency cycles on save with human-readable error paths
- **Smart delete resolver** — `DELETE ?resolve=cascade|decouple|abort` with transitive cascade and atomic transactions
- **Condition branching** — if/else nodes evaluate expressions and route data accordingly
- **Cancellation** — cancel running pipelines via API or UI

### Monitoring & Alerts

- **Real-time WebSocket** updates over [SODP](docs/realtime.md) — binary, msgpack-framed, per-key subscriptions with delta fanout
- **Gantt timeline** — full-page interactive execution timeline with dependency arrows
- **Node stats** — historical duration tracking with sparklines across runs
- **Slack notifications** — configurable via UI, fires on run success, failure, and SLA breach
- **Schema drift alerts** — critical schema changes trigger Slack notifications
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
- Works with GitHub Actions, dbt, Kafka consumers, or any external event

### CLI

```bash
brokoli login                                  # authenticate — stored in ~/.brokoli/config.json
brokoli dev ./pipelines --port 8080            # dev server with hot-reload
brokoli run <pipeline-id>                      # trigger a run
brokoli run <pipeline-id> --follow             # stream live logs
brokoli import pipeline.yaml                   # import pipeline from YAML
brokoli export <pipeline-id> -o pipeline.yaml  # export pipeline to YAML
brokoli whoami                                 # verify current session
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
| >= 10K rows | CSV temp files | 3–5x faster |
| >= 10K rows + pyarrow | Arrow IPC | 5–10x faster |

Auto-detected. No configuration needed.

---

## Enterprise

Brokoli Enterprise adds governance, compliance, and team features for production deployments. Same single binary — just with a license key.

| Feature | Community | Enterprise |
|---|---|---|
| Visual pipeline editor | Yes | Yes |
| 15 node types + data quality | Yes | Yes |
| Auto-profiling + schema drift | Yes | Yes |
| Slack alerts | Yes | Yes |
| SLA monitoring | Yes | Yes |
| Webhook triggers + CLI | Yes | Yes |
| Pipeline versioning + rollback | Yes | Yes |
| Cross-pipeline dependencies | Yes | Yes |
| Smart retry per node | Yes | Yes |
| Condition branching | Yes | Yes |
| Distributed tracing | Yes | Yes |
| Gantt execution timeline | Yes | Yes |
| **SSO/OIDC** (Okta, Azure AD, Google) | | Yes |
| **Audit logging** with before/after diff | | Yes |
| **Workspaces** with RBAC | | Yes |
| **Git Sync** — pipeline-as-code with auto-deploy | | Yes |
| **Data contracts** — schema enforcement between teams | | Yes |
| **Column-level lineage** tracking | | Yes |
| **PII detection** — auto-scan for email, phone, SSN, IP, credit card | | Yes |
| **OpenLineage** export to DataHub/Marquez/Atlan | | Yes |
| **Kubernetes executor** — dispatch nodes as K8s Jobs | | Yes |
| **Work pools** — managed and self-hosted remote workers | | Yes |

---

## API Reference

All endpoints require authentication (except `/health` and `/api/auth/setup`).
Authenticate via httpOnly session cookie (set on login) or `Authorization: Bearer <token>` header.

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
GET    /api/pipelines/:id              Get pipeline
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

### Dependencies
```
GET    /api/pipelines/:id/deps              Dependency status
GET    /api/pipelines/:id/dependents        Reverse lookup
GET    /api/pipelines/dependency-graph      Full dependency graph
DELETE /api/pipelines/:id?resolve=abort     409 with dependent list (default)
DELETE /api/pipelines/:id?resolve=cascade   Delete + all transitive dependents
DELETE /api/pipelines/:id?resolve=decouple  Strip references, then delete
```

### Runs
```
GET    /api/pipelines/:id/runs              List runs
GET    /api/runs/:id                        Run detail
POST   /api/runs/:id/resume                 Resume from failure
POST   /api/runs/:id/cancel                 Cancel running pipeline
GET    /api/runs/:id/logs                   Run logs (?node_id=&level= filters)
GET    /api/runs/:id/logs/export            Download logs
GET    /api/runs/:id/nodes/:nid/preview     Node data preview
GET    /api/runs/:id/nodes/:nid/profile     Node profiling + drift alerts
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

---

## Architecture

```
~30MB single binary
├── Go backend (chi router, gorilla/websocket, SQLite/PostgreSQL)
├── Svelte 5 frontend (embedded via go:embed)
├── Execution engine (parallel DAG, tracing, retry, profiling)
├── SODP realtime (binary state-sync over WebSocket)
└── Extension system (enterprise plugins via interface injection)
```

```
├── cmd/           CLI — serve, dev, run, login, import, export
├── engine/        Execution, transforms, profiling, drift, retry, scheduler
├── api/           HTTP handlers, auth, middleware
├── pkg/sodp/      SODP realtime server (state store, fanout, msgpack frames)
├── store/         SQLite + PostgreSQL dual-dialect store with migrations
├── crypto/        AES-256-GCM encryption
├── quality/       10 data quality rules
├── extensions/    Enterprise plugin interfaces
├── models/        Pipeline, Run, NodeRun, Connection, Variable, Workspace
├── web/           Embedded Svelte frontend (go:embed)
├── ui/            Svelte 5 source (components, pages, stores)
└── docs/          Architecture and protocol notes
```

---

## Tests

```bash
# Build UI first (required for go:embed)
cd ui && npm install && npm run build && cd ..

go test ./... -v
```

---

## Configuration

### Server flags

| Flag | Default | Description |
|---|---|---|
| `--port` / `-p` | 8080 | HTTP server port |
| `--db` | ./brokoli.db | SQLite path (or `postgres://...` URI) |
| `--api-key` | — | Enable API key authentication |

### Environment variables

| Variable | Description |
|---|---|
| `BROKOLI_APP_URL` | Base URL for Slack deep links (default: `http://localhost:8080`) |
| `BROKOLI_JWT_SECRET` | Persistent JWT signing secret |
| `BROKOLI_CORS_ORIGINS` | Allowed CORS origins (comma-separated) |
| `BROKOLI_MAX_CONCURRENT_RUNS` | Max parallel pipeline runs (default: 4) |

### CLI credentials

```bash
brokoli login    # interactive — stores to ~/.brokoli/config.json
brokoli whoami   # verify current session
brokoli logout   # clear stored credentials
```

All CLI commands (`run`, `import`, `export`) read credentials from `~/.brokoli/config.json`. Override with `--server` and `--api-key` flags.

---

## License

Apache 2.0 — see [LICENSE](LICENSE).

Enterprise features are available under a separate commercial license. [Contact us](https://brokoli.dev) for pricing.
