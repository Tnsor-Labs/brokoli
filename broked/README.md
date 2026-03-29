# Broked

**Data orchestration for teams who want to see their pipelines, not just run them.**

A single binary that replaces Apache Airflow for teams that need visual pipeline building, data preview at every node, and zero-config deployment.

## Why Broked?

| | Airflow | Broked |
|---|---|---|
| **Setup** | Docker Compose + Redis + Celery + Postgres | `./broked serve` |
| **Binary size** | ~500MB with deps | ~25MB |
| **Pipeline creation** | Write Python DAGs | Visual drag-and-drop editor |
| **Config** | airflow.cfg + env vars + connections UI | Zero config (SQLite default) |
| **Data preview** | None (need external tools) | Built-in at every node |
| **First pipeline** | ~30 minutes | ~2 minutes |

## Features

### Core Engine
- **11 node types**: File/API/DB sources, transforms, Python code, joins, quality checks, SQL generation, file/DB sinks
- **Parallel execution**: wave-based DAG scheduling with configurable concurrency
- **Retry + resume**: configurable retries per node, resume from failure point
- **Variable resolution**: `${env.*}`, `${param.*}`, `${var.*}`, `${secret.*}`, `${run.*}`
- **Cron scheduling**: standard cron expressions with catch-up for missed runs

### Visual Pipeline Editor
- SVG canvas with drag-and-drop nodes from palette
- Port-to-port connections with snap targeting
- Undo/redo (Ctrl+Z / Ctrl+Shift+Z)
- Auto-layout, JSON view toggle
- Dry-run preview with data at every node
- Node config validation with inline warnings

### Data Quality
- 7 built-in checks: not_null, unique, min, max, range, regex, row_count
- Block or warn policies per check
- Visual quality gate in pipeline editor

### Connections & Secrets
- Airflow-style connection management (conn_id)
- 7 connection types: Postgres, MySQL, SQLite, HTTP, SFTP, S3, Generic
- AES-256-GCM encryption for passwords at rest
- Real connection testing (actually connects, not just ping)
- Variables with typed values (string, number, JSON, secret)

### Monitoring
- Run history with Gantt-style timeline
- Real-time log streaming via WebSocket
- Cross-pipeline data lineage (interactive, draggable graph)
- Run calendar with daily status heatmap
- Scheduler status with next-run times

### Security
- JWT authentication with bcrypt passwords
- 3 roles: admin, editor, viewer
- Rate limiting (100 req/s per IP)
- CORS headers
- Encrypted secrets at rest

## Quick Start

```bash
# Build
cd broked && go build -o broked .

# Run (starts on port 8080 with SQLite)
./broked serve

# Custom port + database
./broked serve --port 9900 --db /data/broked.db
```

Open `http://localhost:8080` in your browser. On first visit, create an admin account.

### Build the UI

```bash
cd broked-ui
npm install
npm run build    # outputs to ../broked/ui/dist/
```

The UI is embedded in the Go binary via `go:embed`.

## API Reference

All endpoints require JWT authentication (except `/health` and `/api/auth/setup`).

### Pipelines
```
GET    /api/pipelines              List all pipelines
POST   /api/pipelines              Create pipeline
GET    /api/pipelines/:id          Get pipeline
PUT    /api/pipelines/:id          Update pipeline
DELETE /api/pipelines/:id          Delete pipeline
POST   /api/pipelines/:id/run      Trigger run
POST   /api/pipelines/:id/dry-run  Preview with 10 rows
POST   /api/pipelines/:id/clone    Duplicate pipeline
POST   /api/pipelines/:id/backfill Backfill date range
GET    /api/pipelines/:id/export   Export as YAML
POST   /api/pipelines/import       Import YAML
POST   /api/pipelines/bulk         Bulk enable/disable/delete
```

### Runs
```
GET    /api/pipelines/:id/runs         List runs
GET    /api/runs/:id                   Get run detail
POST   /api/runs/:id/resume            Resume from failure
GET    /api/runs/:id/logs              Get run logs
GET    /api/runs/:id/logs/export       Download logs as text
GET    /api/runs/:id/nodes/:nid/preview Node data preview
GET    /api/runs/calendar              Daily run status aggregation
```

### Connections
```
GET    /api/connections                List connections
POST   /api/connections                Create connection
GET    /api/connections/:id            Get connection
PUT    /api/connections/:id            Update connection
DELETE /api/connections/:id            Delete connection
POST   /api/connections/:id/test       Test connection
GET    /api/connection-types           Available connection types
```

### Variables
```
GET    /api/variables                  List variables
POST   /api/variables                  Create/update variable
GET    /api/variables/:key             Get variable
DELETE /api/variables/:key             Delete variable
```

### System
```
GET    /health                         Health check
GET    /metrics                        Prometheus metrics
GET    /api/scheduler/status           Active schedules
GET    /api/system/info                System info
GET    /api/lineage                    Cross-pipeline lineage
```

## Node Types

| Type | Description | Required Config |
|---|---|---|
| `source_file` | Read CSV, JSON, XML, Excel | `path` |
| `source_api` | Fetch from REST API | `url` |
| `source_db` | Query Postgres/MySQL/SQLite | `uri` + `query` (or `conn_id`) |
| `transform` | Rename, filter, sort, aggregate, deduplicate | `rules` array |
| `code` | Execute Python script | `script` |
| `join` | Inner/left/right/full join two inputs | `join_type` + keys |
| `quality_check` | Validate data quality | `rules` array |
| `sql_generate` | Generate CREATE TABLE + INSERT SQL | `table` + `dialect` |
| `sink_file` | Write CSV, JSON, or SQL to file | `path` |
| `sink_db` | Execute SQL against database | `uri` (or `conn_id`) |

## Architecture

```
broked/
  cmd/          CLI (cobra) — serve, generate-key
  engine/       Pipeline execution, transforms, SQL gen, scheduler
  api/          HTTP handlers, auth, WebSocket, middleware
  store/        SQLite + Postgres persistence
  crypto/       AES-256-GCM encryption
  quality/      Data quality checks
  models/       Pipeline, Node, Edge, Connection, Variable
  ui/           Embedded Svelte frontend (go:embed)

broked-ui/
  src/
    pages/      Dashboard, Pipelines, Editor, Runs, Lineage, Calendar,
                Connections, Variables, Settings, Login
    components/ Canvas, NodeCard, Sidebar, ConfigPanel, CodeEditor, etc.
    lib/        API client, stores, auth, theme, icons, WebSocket
```

## Configuration

| Flag | Default | Description |
|---|---|---|
| `--port` | 8080 | HTTP server port |
| `--db` | ./broked.db | SQLite database path (or Postgres URI) |
| `--api-key` | (none) | Enable API key authentication |

Environment variables:
- `BROKED_SECRET_*` — resolved via `${secret.name}` in node configs
- Standard env vars — resolved via `${env.NAME}`

## Tests

```bash
go test ./broked/... -v
# 123 tests across engine, crypto, quality, store
```

## License

See repository root for license information.
