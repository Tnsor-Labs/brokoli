<p align="center">
  <img src="https://raw.githubusercontent.com/hc12r/brokoli/main/broked-ui/public/favicon.svg" width="64" height="64" alt="Brokoli" />
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

### Option A: Binary

```bash
# Download and run — that's it
curl -L https://github.com/hc12r/brokoli/releases/latest/download/brokoli-linux-amd64 -o brokoli
chmod +x brokoli
./brokoli serve
```

Open `http://localhost:8080`. Create your admin account. Build your first pipeline.

### Option B: Python SDK

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

See the [Python SDK documentation](../brokoli-python/README.md) and [7 runnable demo pipelines](../brokoli-python/examples/demo/).

**From source:**

```bash
cd broked-ui && npm install && npm run build && cd ..
cd broked && go build -o brokoli . && ./brokoli serve
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
- YAML import/export for pipeline-as-code

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

### Smart Execution
- **Parallel DAG execution** — wave-based Kahn's algorithm with configurable concurrency
- **Smart retry per node** — exponential/linear/fixed backoff, node-type-aware defaults (DB sources get 3 retries, transforms get 0)
- **Resume from failure** — skip succeeded nodes, retry from the point of failure
- **Cross-pipeline dependencies** — `depends_on` field, scheduler checks before triggering
- **Condition branching** — if/else nodes evaluate expressions (`row_count > 0`, `null_pct("col") < 10`) and route data accordingly
- **Cancellation** — cancel running pipelines via API or UI

### Monitoring & Alerts
- **Real-time WebSocket** updates — live run status, node progress, log streaming
- **Slack notifications** — configurable via UI, fires on run success, failure, and SLA breach
- **Schema drift alerts** — critical schema changes trigger Slack notifications with column details
- **SLA deadlines** — per-pipeline "must complete by HH:MM" with timezone support
- **Run calendar** — GitHub-style heatmap of daily run activity
- **Data lineage** — interactive cross-pipeline DAG visualization
- **Gantt timeline** — node-level execution timing per run

### Connections & Secrets
- **7 connection types**: Postgres, MySQL, SQLite, HTTP, SFTP, S3, Generic
- **AES-256-GCM encryption** for all secrets at rest
- **Real connection testing** — actually connects and authenticates, not just ping
- **Variables** with typed values (string, number, JSON, secret) and `${var.key}` resolution in any config field

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

```bash
BROKOLI_LICENSE_KEY=enterprise-yourco-sso,audit,k8s,gitsync,alerts-2027-01-01-xxx \
  ./brokoli-ee serve --port 9900
```

## API Reference

All endpoints require authentication (except `/health` and `/api/auth/setup`).

### Pipelines
```
GET    /api/pipelines                List pipelines (lean DTO — no nodes/edges)
POST   /api/pipelines                Create pipeline
GET    /api/pipelines/:id            Get pipeline (full detail)
PUT    /api/pipelines/:id            Update pipeline
DELETE /api/pipelines/:id            Delete pipeline
POST   /api/pipelines/:id/run        Trigger run
POST   /api/pipelines/:id/dry-run    Preview with sample rows
POST   /api/pipelines/:id/clone      Duplicate pipeline
POST   /api/pipelines/:id/backfill   Backfill date range
GET    /api/pipelines/:id/export     Export as YAML
GET    /api/pipelines/:id/versions   Version history
POST   /api/pipelines/:id/rollback   Restore version
GET    /api/pipelines/:id/deps       Dependency status
GET    /api/pipelines/:id/impact     Downstream impact analysis
POST   /api/pipelines/:id/webhook    Webhook trigger (token auth)
POST   /api/pipelines/import         Import YAML/JSON
POST   /api/pipelines/bulk           Bulk enable/disable/delete
```

### Runs
```
GET    /api/pipelines/:id/runs            List runs
GET    /api/runs/:id                      Run detail (with error)
POST   /api/runs/:id/resume               Resume from failure
POST   /api/runs/:id/cancel               Cancel running pipeline
GET    /api/runs/:id/logs                  Run logs
GET    /api/runs/:id/logs/export           Download logs
GET    /api/runs/:id/nodes/:nid/preview    Node data preview
GET    /api/runs/:id/nodes/:nid/profile    Node profiling data + drift alerts
GET    /api/runs/calendar                  Daily run heatmap
```

### Connections & Variables
```
GET    /api/connections                    List connections
POST   /api/connections                    Create connection
POST   /api/connections/:id/test           Test connection
GET    /api/variables                      List variables
POST   /api/variables                      Create/update variable
```

### Settings & Notifications
```
GET    /api/settings/notifications         Slack config (masked)
PUT    /api/settings/notifications         Save Slack config
POST   /api/settings/notifications/test    Send test Slack message
```

### System
```
GET    /health                             Health check (no auth)
GET    /metrics                            Prometheus metrics (no auth)
GET    /api/system/info                    System info
GET    /api/scheduler/status               Active schedules
GET    /api/lineage                        Cross-pipeline lineage graph
```

## Architecture

```
26MB single binary
├── Go backend (chi router, gorilla/websocket, SQLite/PostgreSQL)
├── Svelte 5 frontend (embedded via go:embed)
├── Pipeline engine (parallel DAG execution, retry, profiling)
└── Extension system (enterprise plugins via interface injection)
```

```
broked/
  cmd/           CLI — serve, run, assert, generate-key
  engine/        Execution, transforms, profiling, drift, conditions, retry, deps, scheduler, SLA
  api/           HTTP handlers, auth, WebSocket, middleware, rate limiting
  store/         SQLite + PostgreSQL with migrations
  crypto/        AES-256-GCM encryption
  quality/       10 data quality rules
  extensions/    Enterprise plugin interfaces
  models/        Pipeline, Run, Node, Connection, Variable, Workspace
  ui/            Embedded Svelte frontend

broked-ui/
  src/pages/     Dashboard, Pipelines, Editor, Runs, Calendar, Lineage,
                 Connections, Variables, Workspaces, Audit Log, Git Sync, Settings
  src/components/ Canvas, NodeCard, Sidebar, ConfigPanel, CodeEditor, Pagination, etc.
```

## Tests

```bash
go test ./broked/... -v
# 219+ Go tests + 52 Python SDK tests across engine, crypto, quality, store, validation
# Covers: profiling, drift detection, conditions, assertions,
#         retry logic, dependencies, webhooks, contracts, PII, lineage
```

## Configuration

| Flag | Default | Description |
|---|---|---|
| `--port` / `-p` | 8080 | HTTP server port |
| `--db` | ./broked.db | SQLite path (or `postgres://...` URI) |
| `--api-key` | — | Enable API key authentication |

| Env Variable | Description |
|---|---|
| `BROKOLI_APP_URL` | Base URL for Slack deep links (default: `http://localhost:8080`) |
| `BROKOLI_JWT_SECRET` | Persistent JWT signing secret |
| `BROKED_SECRET_*` | Resolved via `${secret.name}` in node configs |

## License

Apache 2.0 — see [LICENSE](../LICENSE).

Enterprise features are available under a separate commercial license. Contact us for pricing.
