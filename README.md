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
  <a href="https://brokoli.orkestri.site">Website</a> ·
  <a href="https://docs.brokoli.orkestri.site">Documentation</a> ·
  <a href="#quick-start">Quick Start</a> ·
  <a href="#features">Features</a> ·
  <a href="#brokoli-cloud">Brokoli Cloud</a>
</p>

---

Brokoli is a data pipeline orchestrator that runs as a single ~30MB binary. Visual drag-and-drop editor, parallel DAG execution, built-in data quality and profiling, real-time monitoring, and a Python SDK — no infrastructure required beyond the binary itself.

---

## Quick Start

### Option A: Install Script

```bash
curl -fsSL https://raw.githubusercontent.com/Tnsor-Labs/brokoli/main/install.sh | sh
brokoli serve
```

Open `http://localhost:8080`, create your admin account, and build your first pipeline.

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
```

### From Source

```bash
cd ui && npm install && npm run build && cd ..
go build -o brokoli . && ./brokoli serve
```

---

## Features

**Single binary, no infrastructure.** Runs on any Linux/macOS machine with no external dependencies. SQLite by default; swap to PostgreSQL with a connection string.

**Visual pipeline editor.** Drag-and-drop canvas with 15 node types, zoom/pan, inline node config, dry-run preview, and YAML import/export. No code required for common ETL patterns.

**Built-in data quality.** 10 quality rules (not_null, unique, min/max, range, regex, row_count, type_check, freshness, no_blank), auto-profiling on every node output, and schema drift detection across runs.

**Parallel DAG execution.** Wave-based Kahn's algorithm, per-node retry with exponential backoff, resume from failure, cross-pipeline dependencies with trigger-mode chaining, condition branching.

**Real-time monitoring.** Live run status and log streaming over WebSocket, interactive Gantt timeline, Slack alerts, SLA deadlines, run calendar heatmap, and cross-pipeline lineage graph.

**Secrets and connections.** AES-256-GCM encryption at rest, 7 connection types (Postgres, MySQL, SQLite, HTTP, SFTP, S3, Generic), typed variables with `${var.key}` resolution in any config field.

**Authentication.** httpOnly cookie sessions, JWT auth, API key support, role-based access (admin/editor/viewer).

**Pipeline versioning.** Auto-snapshot on every save, full version history, one-click rollback.

**Webhook triggers.** Per-pipeline tokens — integrate with GitHub Actions, dbt, Kafka, or any HTTP source.

---

## Brokoli Cloud

Brokoli Cloud builds on the open-source core with team and governance features — SSO/OIDC, audit logging, workspaces with RBAC, Git Sync, data contracts, column-level lineage, PII detection, OpenLineage export, and Kubernetes execution. Free tier available.

[Try it now](https://brokoli.orkestri.site)

---

## Configuration

| Flag | Default | Description |
|---|---|---|
| `--port` / `-p` | 8080 | HTTP server port |
| `--db` | ./brokoli.db | SQLite path or `postgres://...` URI |
| `--api-key` | — | Enable API key authentication |

| Environment variable | Description |
|---|---|
| `BROKOLI_APP_URL` | Base URL for Slack deep links |
| `BROKOLI_JWT_SECRET` | Persistent JWT signing secret |
| `BROKOLI_CORS_ORIGINS` | Allowed CORS origins (comma-separated) |
| `BROKOLI_MAX_CONCURRENT_RUNS` | Max parallel pipeline runs (default: 4) |

---

## License

Apache 2.0 — see [LICENSE](LICENSE).

Enterprise features are available under a separate commercial license. [Contact us](https://brokoli.orkestri.site) for pricing.
