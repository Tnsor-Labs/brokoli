package store

import (
	"bytes"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundle"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/pkg/taskruntime"
	"github.com/Tnsor-Labs/brokoli/pkg/templates"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const timeFormat = time.RFC3339Nano

// wrapStoreErr annotates storage errors with the operation name and resource ID.
func wrapStoreErr(op string, id string, err error) error {
	if err == nil {
		return nil
	}
	if id != "" {
		return fmt.Errorf("[%s:%s] %w", op, id, err)
	}
	return fmt.Errorf("[%s] %w", op, err)
}

// SQLiteStore implements Store using an embedded SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

// NewSQLiteStore opens (or creates) a SQLite database and runs migrations.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Connection pool tuning for concurrent access
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	files := []string{"001_initial.sql", "002_connections.sql", "003_variables.sql", "005_workspaces.sql"}
	for _, f := range files {
		migration, err := migrationsFS.ReadFile("migrations/" + f)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", f, err)
		}
		if _, err := s.db.Exec(string(migration)); err != nil {
			return fmt.Errorf("execute migration %s: %w", f, err)
		}
	}

	// Schema additions (safe to re-run, ignores "already exists" errors)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN tags TEXT NOT NULL DEFAULT '[]'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'default'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN schedule_timezone TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN catchup INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS parked_waits (
		run_id TEXT PRIMARY KEY,
		pipeline_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		condition TEXT NOT NULL,
		poll_interval_ms INTEGER NOT NULL,
		next_poll_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_parked_waits_due ON parked_waits(next_poll_at)`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN sla_deadline TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN sla_timezone TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN depends_on TEXT NOT NULL DEFAULT '[]'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN dependency_rules TEXT NOT NULL DEFAULT '[]'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN webhook_token TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN pipeline_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN source TEXT NOT NULL DEFAULT 'ui'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN org_id TEXT NOT NULL DEFAULT ''`)
	if _, err := s.db.Exec(`ALTER TABLE pipelines ADD COLUMN ir_version TEXT NOT NULL DEFAULT ''`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("add pipelines.ir_version: %w", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE pipelines ADD COLUMN hooks TEXT NOT NULL DEFAULT '{}'`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("add pipelines.hooks: %w", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE pipelines ADD COLUMN extensions TEXT NOT NULL DEFAULT '{}'`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("add pipelines.extensions: %w", err)
	}
	// ADR-032 rollout step 4 (#439): typed pipeline parameter declarations,
	// distinct from the legacy untyped params column.
	if _, err := s.db.Exec(`ALTER TABLE pipelines ADD COLUMN parameters TEXT NOT NULL DEFAULT '{}'`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("add pipelines.parameters: %w", err)
	}
	s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_pid ON pipelines(pipeline_id) WHERE pipeline_id != ''`)
	s.db.Exec(`ALTER TABLE connections ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'default'`)
	s.db.Exec(`ALTER TABLE variables ADD COLUMN workspace_id TEXT NOT NULL DEFAULT 'default'`)

	// Node profiles table
	s.db.Exec(`CREATE TABLE IF NOT EXISTS node_profiles (
		run_id TEXT NOT NULL, node_id TEXT NOT NULL,
		profile TEXT NOT NULL DEFAULT '{}', schema_snapshot TEXT NOT NULL DEFAULT '{}',
		drift_alerts TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL,
		PRIMARY KEY (run_id, node_id),
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_node_profiles ON node_profiles(run_id)`)

	// Performance indexes (safe to re-run)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_pipelines_workspace ON pipelines(workspace_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_connections_workspace ON connections(workspace_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_variables_workspace ON variables(workspace_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_pipeline_status ON runs(pipeline_id, status, started_at DESC)`)
	// Backs the keyset walk in ListRunsByPipelineCursor. The status index
	// above cannot serve it: it leads with status, while the walk filters on
	// pipeline_id and ranges over id. Without this, every page is a scan.
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_pipeline_id ON runs(pipeline_id, id DESC)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_node_runs_run_status ON node_runs(run_id, status)`)

	// Settings key-value store
	s.db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '')`)

	// Login attempts tracking (account lockout)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS login_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL,
		ip TEXT NOT NULL DEFAULT '',
		success INTEGER NOT NULL DEFAULT 0,
		attempted_at TEXT NOT NULL)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_login_attempts ON login_attempts(username, attempted_at DESC)`)

	// Roles table
	s.db.Exec(`CREATE TABLE IF NOT EXISTS roles (
		id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT NOT NULL DEFAULT '',
		permissions TEXT NOT NULL DEFAULT '[]', is_system INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL)`)

	// Seed default roles on first run
	var roleCount int
	s.db.QueryRow("SELECT COUNT(*) FROM roles").Scan(&roleCount)
	if roleCount == 0 {
		for _, role := range models.DefaultRoles() {
			permsJSON, _ := json.Marshal(role.Permissions)
			s.db.Exec("INSERT INTO roles (id, name, description, permissions, is_system, created_at) VALUES (?,?,?,?,?,?)",
				role.ID, role.Name, role.Description, string(permsJSON), boolToInt(role.IsSystem), time.Now().UTC().Format(timeFormat))
		}
	}

	// Dead letter queue
	s.db.Exec(`CREATE TABLE IF NOT EXISTS dead_letter_queue (
		id TEXT PRIMARY KEY,
		pipeline_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		error TEXT NOT NULL,
		node_id TEXT NOT NULL DEFAULT '',
		node_name TEXT NOT NULL DEFAULT '',
		payload TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL,
		resolved INTEGER NOT NULL DEFAULT 0,
		resolved_at TEXT,
		FOREIGN KEY (pipeline_id) REFERENCES pipelines(id) ON DELETE CASCADE)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_dlq_pipeline ON dead_letter_queue(pipeline_id, resolved, created_at DESC)`)

	// Credential references — store pointers to external secret stores
	// instead of bare encrypted blobs. See pkg/secrets for resolvers.
	s.db.Exec(`ALTER TABLE connections ADD COLUMN password_ref TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE connections ADD COLUMN extra_ref TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE connections ADD COLUMN max_concurrent INTEGER NOT NULL DEFAULT 0`)
	// Migrate legacy encrypted values: copy password_enc → password_ref
	// with encrypted:// prefix so the resolver chain handles them.
	s.db.Exec(`UPDATE connections SET password_ref = 'encrypted://' || password_enc WHERE password_enc != '' AND password_ref = ''`)
	s.db.Exec(`UPDATE connections SET extra_ref = 'encrypted://' || extra_enc WHERE extra_enc != '' AND extra_ref = ''`)

	// Tracing & observability columns
	s.db.Exec(`ALTER TABLE runs ADD COLUMN trace_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE runs ADD COLUMN error TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE runs ADD COLUMN params TEXT NOT NULL DEFAULT '{}'`)
	// ADR-032 rollout step 4 (#439): the resolved, typed run-parameter
	// snapshot, distinct from the legacy untyped params column above.
	if _, err := s.db.Exec(`ALTER TABLE runs ADD COLUMN parameters TEXT NOT NULL DEFAULT '{}'`); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return fmt.Errorf("add runs.parameters: %w", err)
	}
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN ready_at TEXT`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN queue_ms INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN rows_per_sec REAL NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN trace_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN span_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE logs ADD COLUMN trace_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE logs ADD COLUMN span_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE logs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE logs ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_node_runs_node_pipeline ON node_runs(node_id, run_id)`)

	// Run event log — immutable append-only log of run/node-attempt lifecycle
	// transitions (issue #6). See store/migrations/009_run_events.sql for the
	// documented schema; like 004/006/007/008 it is not in the hardcoded list
	// of files migrate() executes above, so the table is created here instead
	// via idempotent DDL, consistent with how every other addition in this
	// function works.
	s.db.Exec(`CREATE TABLE IF NOT EXISTS run_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		run_id TEXT NOT NULL,
		node_id TEXT,
		attempt INTEGER,
		event_type TEXT NOT NULL,
		payload TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL,
		schema_version INTEGER NOT NULL DEFAULT 1,
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(run_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_run_events_run_node ON run_events(run_id, node_id)`)

	// Execution attempts — durable outbox/intent + claim/lease record for one
	// (run_id, node_id, instance_key, attempt) unit of dispatchable work
	// (issue #7; instance_key added for ADR-017, worker protocol v2).
	// node_id/instance_key/attempt default to ''/''/0 for pipeline-level
	// (not yet node-level) outbox rows, mirroring run_events' nullable
	// node_id. Applied as idempotent inline DDL here, consistent with
	// run_events above and every other addition in this function, rather
	// than a tracked migration file.
	s.db.Exec(`CREATE TABLE IF NOT EXISTS execution_attempts (
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL DEFAULT '',
		instance_key TEXT NOT NULL DEFAULT '',
		attempt INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'queued',
		claimed_by TEXT NOT NULL DEFAULT '',
		lease_expires_at TEXT,
		fencing_generation INTEGER NOT NULL DEFAULT 0,
		idempotency_key TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (run_id, node_id, instance_key, attempt),
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_execution_attempts_lease ON execution_attempts(status, lease_expires_at)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_execution_attempts_idempotency ON execution_attempts(idempotency_key)`)

	// A database created before ADR-017 already has execution_attempts with
	// the narrower (run_id, node_id, attempt) primary key — the CREATE TABLE
	// IF NOT EXISTS above is a no-op against it, and SQLite cannot ALTER a
	// PRIMARY KEY in place. Widen it by rebuilding the table exactly once,
	// guarded on instance_key's absence so this is a safe no-op on every
	// later startup (including a freshly created database, which already
	// has the new schema from the CREATE TABLE above and so already has the
	// column).
	if hasInstanceKey, err := s.hasColumn("execution_attempts", "instance_key"); err != nil {
		return fmt.Errorf("check execution_attempts schema: %w", err)
	} else if !hasInstanceKey {
		if err := s.widenExecutionAttemptsPrimaryKey(); err != nil {
			return fmt.Errorf("widen execution_attempts primary key for ADR-017: %w", err)
		}
	}

	// pipeline_versions gets a unique (pipeline_id, version) index so
	// SavePipelineVersion's read-then-insert next-version computation can
	// detect (and retry past) a concurrent writer racing it for the same
	// pipeline — issue #8's resolveRunPipelineVersion made this path far
	// hotter than before (every run trigger for a pipeline with no saved
	// version yet, not just manual edits/rollbacks), so a race that was
	// previously rare enough to ignore now needs a real guard. Best-effort
	// on an existing database that already has duplicate rows from before
	// this index existed: like every other CREATE INDEX in this function,
	// a failure here (with pre-existing duplicates) is silently ignored,
	// consistent with this migration style, rather than blocking startup.
	s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_versions_unique ON pipeline_versions(pipeline_id, version)`)

	// Run definition snapshot + resume lineage (issue #8). pipeline_version
	// pins a run to a store.PipelineVersion snapshot (see SavePipelineVersion
	// / GetPipelineVersion above) instead of the live, mutable pipeline row;
	// 0 means unset (a run created before this column existed). Set once at
	// creation, never updated — deliberately absent from UpdateRun's SET
	// list below, matching trace_id/params being creation-time-only in
	// practice even though they're technically updatable columns.
	s.db.Exec(`ALTER TABLE runs ADD COLUMN pipeline_version INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE runs ADD COLUMN resumed_from_run_id TEXT NOT NULL DEFAULT ''`)
	// ADR-028: the data interval a scheduled run covers, and what created
	// the run. The partial unique index is the leader-failover guard --
	// two instances firing the same tick race to insert, the second
	// loses, and no duplicate scheduled run exists. Scheduled only:
	// manual runs carry no interval, and a backfill re-running an
	// interval is a deliberate new run.
	s.db.Exec(`ALTER TABLE runs ADD COLUMN trigger_type TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE runs ADD COLUMN data_interval_start TEXT`)
	s.db.Exec(`ALTER TABLE runs ADD COLUMN data_interval_end TEXT`)
	s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_runs_scheduled_interval ON runs(pipeline_id, data_interval_start) WHERE trigger_type = 'scheduled' AND data_interval_start IS NOT NULL`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_resumed_from ON runs(resumed_from_run_id) WHERE resumed_from_run_id != ''`)

	// Durable cancellation intent (see models.Run.CancelRequested and
	// RequestRunCancel below). Set-only; terminal runs stop consulting it.
	s.db.Exec(`ALTER TABLE runs ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0`) // #nosec G104 -- best-effort additive migration, same idiom as every ALTER above (errors on re-run are expected)

	// Expansion instances — durable per-item execution record for a
	// dynamic-expansion `code` node (issue #31). See
	// models.ExpansionInstance's doc comment for why this is a standalone
	// table instead of reusing node_runs/execution_attempts' (node_id,
	// attempt) keying.
	s.db.Exec(`CREATE TABLE IF NOT EXISTS expansion_instances (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		node_attempt INTEGER NOT NULL DEFAULT 0,
		instance_index INTEGER NOT NULL,
		instance_key TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'running',
		row_count INTEGER NOT NULL DEFAULT 0,
		started_at TEXT,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_expansion_instances_run_node ON expansion_instances(run_id, node_id, node_attempt)`)

	// run_physical_plans — the physical execution plan as decided at run
	// time (ADR-015, #90 M2). One row per run, holding the planner's
	// JSON snapshot so recovery and audit see the plan as-planned, not a
	// recomputation that could differ after code/policy changes. Cascades
	// with the run, so retention is automatic.
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS run_physical_plans (
		run_id TEXT PRIMARY KEY,
		plan TEXT NOT NULL,
		created_at TEXT NOT NULL,
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`)

	// physical_instances — the authoritative per-instance record (ADR-015
	// point 3, #90 M3), one row per physical unit a run executed, keyed by
	// (run_id, instance_key). Cascades with the run.
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS physical_instances (
		run_id TEXT NOT NULL,
		instance_key TEXT NOT NULL,
		logical_node_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		idx INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT '',
		row_count INTEGER NOT NULL DEFAULT 0,
		started_at TEXT,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		attempt INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (run_id, instance_key),
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`)
	_, _ = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_physical_instances_run ON physical_instances(run_id)`)

	// runs.org_id — multi-tenant scoping for run-level queries
	// (PurgeRunsOlderThanByOrg, GetRunCalendarByOrg, ListRunIDsOlderThanByOrg).
	// This was missing entirely from the SQLite schema even though every
	// query referencing it already assumed it existed (Postgres has had the
	// equivalent `ALTER TABLE runs ADD COLUMN IF NOT EXISTS org_id ...`
	// since its own org_id migration) — any multi-tenant SQLite deployment
	// calling one of those methods would hit "no such column: org_id"
	// (Tnsor-Labs/brokoli#49, found while testing that issue's retention
	// work, not something that issue's own scope introduced).
	s.db.Exec(`ALTER TABLE runs ADD COLUMN org_id TEXT NOT NULL DEFAULT 'default'`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_org ON runs(org_id)`)

	// Backfill: every run created before this fix landed still has the
	// column's bare 'default' — CreateRun never set it, regardless of
	// which org the owning pipeline actually belonged to (Tnsor-Labs/
	// brokoli#50). Every run created going forward carries a real value
	// from Runner.orgID (== the pipeline's own OrgID at creation time), so
	// this only ever touches pre-fix rows — a run already correctly
	// backfilled (or a fresh one) never matches org_id = 'default' again
	// unless its pipeline's own org_id happens to be the literal string
	// 'default', which no pipeline-creation path in this codebase ever
	// sets (pipelines.org_id's own convention is '' for "no org", not
	// 'default' — see requirePipelineOrg's doc comment). Orphaned runs
	// (pipeline since deleted) keep their existing value: the subquery
	// returns NULL and COALESCE falls back rather than clobbering them
	// with a guess.
	s.db.Exec(`UPDATE runs SET org_id = COALESCE((SELECT p.org_id FROM pipelines p WHERE p.id = runs.pipeline_id), org_id) WHERE org_id = 'default'`)

	// Alerts — persisted, readable notifications. Before this table the
	// only alerting was fire-and-forget outbound (Slack webhooks, the
	// notify node), so nothing could ever be read back. org_id is
	// indexed because every query is org-scoped.
	s.db.Exec(`CREATE TABLE IF NOT EXISTS alerts (
		id TEXT PRIMARY KEY,
		org_id TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL,
		severity TEXT NOT NULL DEFAULT 'info',
		title TEXT NOT NULL,
		body TEXT NOT NULL DEFAULT '',
		pipeline_id TEXT NOT NULL DEFAULT '',
		pipeline_name TEXT NOT NULL DEFAULT '',
		run_id TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		read_at TEXT,
		dismissed_at TEXT)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_alerts_org ON alerts(org_id, created_at DESC)`)

	// Task bundles (ADR-031): tenant-scoped, content-addressed project
	// archives. A bundle's identity IS its digest, so the primary key is
	// (org_id, digest) — no id column, no revision, no overwrite. Uploads
	// that hash to an existing digest are idempotent no-ops; uploads that
	// collide differently are refused by the store method, never silently
	// clobbered. archive is immutable, banned from UPDATE (guarded at the
	// method layer, not just convention), and capped
	// (pkg/taskbundle.MaxArchiveBytes) before it ever lands here.
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS task_bundles (
		org_id TEXT NOT NULL,
		digest TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		archive BLOB NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (org_id, digest))`)

	// Task bundles v2 (ADR-033): same shape and contract as task_bundles
	// above, but a distinct table -- task-bundle/v2 is a separate,
	// additive format from task-bundle/1 (ADR-035 Decision 1), never
	// stored in the same namespace.
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS task_bundles_v2 (
		org_id TEXT NOT NULL,
		digest TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		archive BLOB NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (org_id, digest))`)

	// Resolved execution records (ADR-033 section 4): one pin per
	// (run_id, node_id), immutable once set -- record_json is the
	// pkg/taskruntime.ResolvedExecutionRecord as JSON, not decomposed
	// into columns, since nothing ever queries by its individual fields
	// (only fetched whole, by this table's own key).
	_, _ = s.db.Exec(`CREATE TABLE IF NOT EXISTS resolved_execution_records (
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		record_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (run_id, node_id))`)

	// Pipeline templates — global, admin-curated starter pipelines
	// (GET /api/templates). Used to be hardcoded JS in the frontend;
	// moved to the backend (brokoli#71) and now to the database so
	// they're editable without a redeploy. Seeded from
	// pkg/templates.Builtin on first migrate only — if an admin edits or
	// deletes a seeded row, that's a real, intentional change and must
	// not be silently reverted on every subsequent startup.
	s.db.Exec(`CREATE TABLE IF NOT EXISTS pipeline_templates (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		icon TEXT NOT NULL DEFAULT '',
		nodes TEXT NOT NULL DEFAULT '[]',
		edges TEXT NOT NULL DEFAULT '[]',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL)`)
	if err := s.seedPipelineTemplates(); err != nil {
		return fmt.Errorf("seed pipeline templates: %w", err)
	}

	return nil
}

// hasColumn reports whether table has a column named column, via SQLite's
// PRAGMA table_info — the standard way to check schema shape before an
// ALTER TABLE ADD COLUMN or, as with widenExecutionAttemptsPrimaryKey
// below, before a full table rebuild that must run exactly once.
func (s *SQLiteStore) hasColumn(table, column string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid        int
			name       string
			ctype      string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &defaultVal, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// widenExecutionAttemptsPrimaryKey rebuilds execution_attempts from
// (run_id, node_id, attempt) to (run_id, node_id, instance_key, attempt) —
// see the call site's comment for why (ADR-017: two different physical
// instances of the same node's same attempt number need independent rows,
// which the original primary key made impossible). SQLite has no ALTER
// TABLE for primary keys, so this is the standard rebuild recipe: create
// the new shape under a temporary name, copy every existing row across
// with instance_key=” (preserving every whole-node/whole-pipeline claim's
// identity and state exactly), drop the old table, rename the new one into
// place — all inside one transaction, so a crash mid-rebuild leaves the
// original table untouched rather than half-migrated (the caller's
// hasColumn guard then simply retries the whole thing on next startup).
func (s *SQLiteStore) widenExecutionAttemptsPrimaryKey() (retErr error) {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.Exec(`CREATE TABLE execution_attempts_v2 (
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL DEFAULT '',
		instance_key TEXT NOT NULL DEFAULT '',
		attempt INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'queued',
		claimed_by TEXT NOT NULL DEFAULT '',
		lease_expires_at TEXT,
		fencing_generation INTEGER NOT NULL DEFAULT 0,
		idempotency_key TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY (run_id, node_id, instance_key, attempt),
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`); err != nil {
		return fmt.Errorf("create execution_attempts_v2: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO execution_attempts_v2
		(run_id, node_id, instance_key, attempt, status, claimed_by, lease_expires_at, fencing_generation, idempotency_key, error, created_at, updated_at)
		SELECT run_id, node_id, '', attempt, status, claimed_by, lease_expires_at, fencing_generation, idempotency_key, error, created_at, updated_at
		FROM execution_attempts`); err != nil {
		return fmt.Errorf("copy execution_attempts rows: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE execution_attempts`); err != nil {
		return fmt.Errorf("drop old execution_attempts: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE execution_attempts_v2 RENAME TO execution_attempts`); err != nil {
		return fmt.Errorf("rename execution_attempts_v2: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_execution_attempts_lease ON execution_attempts(status, lease_expires_at)`); err != nil {
		return fmt.Errorf("recreate lease index: %w", err)
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS idx_execution_attempts_idempotency ON execution_attempts(idempotency_key)`); err != nil {
		return fmt.Errorf("recreate idempotency index: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit execution_attempts rebuild: %w", err)
	}
	return nil
}

// seedPipelineTemplates inserts pkg/templates.Builtin's rows only when the
// table is empty (first run against this database) — never overwrites an
// existing row, so an admin's edit or deletion of a seeded template
// persists across restarts instead of being silently reverted.
func (s *SQLiteStore) seedPipelineTemplates() error {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pipeline_templates`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	now := time.Now().UTC()
	for _, t := range templates.Builtin {
		t.CreatedAt, t.UpdatedAt = now, now
		if err := s.CreatePipelineTemplate(&t); err != nil {
			return fmt.Errorf("seed template %q: %w", t.ID, err)
		}
	}
	return nil
}

// --- Settings ---

func (s *SQLiteStore) GetSetting(key string) (string, error) {
	var val string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&val)
	if err != nil {
		return "", nil // not found = empty, not an error
	}
	return val, nil
}

func (s *SQLiteStore) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = ?`,
		key, value, value,
	)
	return err
}

// --- Login Attempts ---

func (s *SQLiteStore) RecordLoginAttempt(username, ip string, success bool) error {
	successInt := 0
	if success {
		successInt = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO login_attempts (username, ip, success, attempted_at) VALUES (?, ?, ?, ?)`,
		username, ip, successInt, time.Now().UTC().Format(timeFormat),
	)
	return err
}

func (s *SQLiteStore) GetRecentFailedAttempts(username string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM login_attempts WHERE username = ? AND success = 0 AND attempted_at > ?`,
		username, since.UTC().Format(timeFormat),
	).Scan(&count)
	return count, err
}

func (s *SQLiteStore) ClearLoginAttempts(username string) error {
	_, err := s.db.Exec(`DELETE FROM login_attempts WHERE username = ?`, username)
	return err
}

// WithTx executes a function within a database transaction.
func (s *SQLiteStore) WithTx(fn func(*sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// --- Dead Letter Queue ---

func (s *SQLiteStore) AddToDLQ(pipelineID, runID, nodeID, nodeName, errMsg, payload string) error {
	id := common.NewID()
	_, err := s.db.Exec(
		`INSERT INTO dead_letter_queue (id, pipeline_id, run_id, error, node_id, node_name, payload, created_at) VALUES (?,?,?,?,?,?,?,?)`,
		id, pipelineID, runID, errMsg, nodeID, nodeName, payload, time.Now().UTC().Format(timeFormat),
	)
	return wrapStoreErr("AddToDLQ", id, err)
}

func (s *SQLiteStore) ListDLQ(pipelineID string, includeResolved bool, limit int) ([]DLQEntry, error) {
	query := `SELECT id, pipeline_id, run_id, error, node_id, node_name, payload, created_at, resolved, COALESCE(resolved_at,'') FROM dead_letter_queue WHERE pipeline_id = ?`
	if !includeResolved {
		query += " AND resolved = 0"
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	rows, err := s.db.Query(query, pipelineID, limit)
	if err != nil {
		return nil, wrapStoreErr("ListDLQ", pipelineID, err)
	}
	defer rows.Close()
	var entries []DLQEntry
	for rows.Next() {
		var e DLQEntry
		var resolved int
		if err := rows.Scan(&e.ID, &e.PipelineID, &e.RunID, &e.Error, &e.NodeID, &e.NodeName, &e.Payload, &e.CreatedAt, &resolved, &e.ResolvedAt); err != nil {
			return nil, err
		}
		e.Resolved = resolved != 0
		entries = append(entries, e)
	}
	return entries, nil
}

func (s *SQLiteStore) ResolveDLQ(id string) error {
	_, err := s.db.Exec(`UPDATE dead_letter_queue SET resolved = 1, resolved_at = ? WHERE id = ?`, time.Now().UTC().Format(timeFormat), id)
	return wrapStoreErr("ResolveDLQ", id, err)
}

func (s *SQLiteStore) Close() error       { return s.db.Close() }
func (s *SQLiteStore) RawDB() interface{} { return s.db }

// --- Pipelines ---

// pipelineFields holds pre-marshaled JSON for pipeline storage operations.
type pipelineFields struct {
	nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON, depRulesJSON, hooksJSON, extensionsJSON, parametersJSON []byte
}

// marshalPipelineJSON marshals the JSON fields of a pipeline for storage.
func marshalPipelineJSON(p *models.Pipeline) (*pipelineFields, error) {
	nodesJSON, err := json.Marshal(p.Nodes)
	if err != nil {
		return nil, fmt.Errorf("marshal nodes: %w", err)
	}
	edgesJSON, err := json.Marshal(p.Edges)
	if err != nil {
		return nil, fmt.Errorf("marshal edges: %w", err)
	}
	paramsJSON, _ := json.Marshal(p.Params)
	tagsJSON, _ := json.Marshal(p.Tags)
	if tagsJSON == nil {
		tagsJSON = []byte("[]")
	}
	depsJSON, _ := json.Marshal(p.DependsOn)
	if depsJSON == nil {
		depsJSON = []byte("[]")
	}
	depRulesJSON, _ := json.Marshal(p.DependencyRules)
	if depRulesJSON == nil {
		depRulesJSON = []byte("[]")
	}
	hooksJSON, _ := json.Marshal(p.Hooks)
	if hooksJSON == nil || string(hooksJSON) == "null" {
		hooksJSON = []byte("{}")
	}
	extensionsJSON, _ := json.Marshal(p.Extensions)
	if extensionsJSON == nil || string(extensionsJSON) == "null" {
		extensionsJSON = []byte("{}")
	}
	parametersJSON, _ := json.Marshal(p.Parameters)
	if parametersJSON == nil || string(parametersJSON) == "null" {
		parametersJSON = []byte("{}")
	}
	return &pipelineFields{nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON, depRulesJSON, hooksJSON, extensionsJSON, parametersJSON}, nil
}

func (s *SQLiteStore) CreatePipeline(p *models.Pipeline) error {
	f, err := marshalPipelineJSON(p)
	if err != nil {
		return wrapStoreErr("CreatePipeline", p.ID, err)
	}
	_, err = s.db.Exec(
		`INSERT INTO pipelines (id, ir_version, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id, hooks, extensions, catchup, parameters)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.IRVersion, p.Name, p.Description, string(f.nodesJSON), string(f.edgesJSON),
		p.Schedule, p.ScheduleTimezone, p.WebhookURL, string(f.paramsJSON), string(f.tagsJSON), p.SLADeadline, p.SLATimezone, string(f.depsJSON), string(f.depRulesJSON), p.WebhookToken, boolToInt(p.Enabled), p.CreatedAt.UTC().Format(timeFormat), p.UpdatedAt.UTC().Format(timeFormat), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID, string(f.hooksJSON), string(f.extensionsJSON), boolToInt(p.Catchup), string(f.parametersJSON),
	)
	return wrapStoreErr("CreatePipeline", p.ID, err)
}

func (s *SQLiteStore) GetPipeline(id string) (*models.Pipeline, error) {
	row := s.db.QueryRow(
		`SELECT id, ir_version, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id, hooks, extensions, catchup, parameters
		 FROM pipelines WHERE id = ?`, id,
	)
	p, err := scanPipeline(row)
	if err != nil {
		return nil, wrapStoreErr("GetPipeline", id, err)
	}
	return p, nil
}

func (s *SQLiteStore) ListPipelines() ([]models.Pipeline, error) {
	rows, err := s.db.Query(
		`SELECT id, ir_version, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id, hooks, extensions, catchup, parameters
		 FROM pipelines ORDER BY created_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pipelines []models.Pipeline
	for rows.Next() {
		p, err := scanPipelineRows(rows)
		if err != nil {
			return nil, err
		}
		pipelines = append(pipelines, *p)
	}
	return pipelines, rows.Err()
}

func (s *SQLiteStore) ListPipelinesByWorkspace(workspaceID string) ([]models.Pipeline, error) {
	rows, err := s.db.Query(
		`SELECT id, ir_version, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id, hooks, extensions, catchup, parameters
		 FROM pipelines WHERE workspace_id = ? ORDER BY created_at DESC`, workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pipelines []models.Pipeline
	for rows.Next() {
		p, err := scanPipelineRows(rows)
		if err != nil {
			return nil, err
		}
		pipelines = append(pipelines, *p)
	}
	return pipelines, rows.Err()
}

func (s *SQLiteStore) ListPipelinesByOrg(orgID string) ([]models.Pipeline, error) {
	rows, err := s.db.Query(
		`SELECT id, ir_version, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id, hooks, extensions, catchup, parameters
		 FROM pipelines WHERE org_id = ? ORDER BY created_at DESC`, orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pipelines []models.Pipeline
	for rows.Next() {
		p, err := scanPipelineRows(rows)
		if err != nil {
			return nil, err
		}
		pipelines = append(pipelines, *p)
	}
	return pipelines, rows.Err()
}

func (s *SQLiteStore) ListPipelinesByOrgPaged(orgID string, limit, offset int) ([]models.Pipeline, int, error) {
	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE org_id = ?`, orgID).Scan(&total)
	rows, err := s.db.Query(
		`SELECT id, ir_version, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id, hooks, extensions, catchup, parameters
		 FROM pipelines WHERE org_id = ? ORDER BY created_at DESC LIMIT ? OFFSET ?`, orgID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var pipelines []models.Pipeline
	for rows.Next() {
		p, err := scanPipelineRows(rows)
		if err != nil {
			return nil, 0, err
		}
		pipelines = append(pipelines, *p)
	}
	return pipelines, total, rows.Err()
}

func (s *SQLiteStore) ListPipelinesByOrgCursor(orgID string, afterID string, limit int) ([]models.Pipeline, bool, error) {
	fetchN := limit + 1
	var rows *sql.Rows
	var err error
	if afterID == "" {
		rows, err = s.db.Query(
			`SELECT id, ir_version, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id, hooks, extensions, catchup, parameters
			 FROM pipelines WHERE org_id = ? ORDER BY id DESC LIMIT ?`, orgID, fetchN)
	} else {
		rows, err = s.db.Query(
			`SELECT id, ir_version, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id, hooks, extensions, catchup, parameters
			 FROM pipelines WHERE org_id = ? AND id < ? ORDER BY id DESC LIMIT ?`, orgID, afterID, fetchN)
	}
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var pipelines []models.Pipeline
	for rows.Next() {
		p, err := scanPipelineRows(rows)
		if err != nil {
			return nil, false, err
		}
		pipelines = append(pipelines, *p)
	}
	hasNext := len(pipelines) > limit
	if hasNext {
		pipelines = pipelines[:limit]
	}
	return pipelines, hasNext, rows.Err()
}

func (s *SQLiteStore) ListConnectionsByWorkspacePaged(wsID string, limit, offset int) ([]models.Connection, int, error) {
	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM connections WHERE workspace_id = ?`, wsID).Scan(&total)
	conns, err := s.ListConnectionsByWorkspace(wsID)
	if err != nil {
		return nil, 0, err
	}
	end := offset + limit
	if end > len(conns) {
		end = len(conns)
	}
	if offset > len(conns) {
		offset = len(conns)
	}
	return conns[offset:end], total, nil
}

func (s *SQLiteStore) ListVariablesByWorkspacePaged(wsID string, limit, offset int) ([]models.Variable, int, error) {
	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM variables WHERE workspace_id = ?`, wsID).Scan(&total)
	vars, err := s.ListVariablesByWorkspace(wsID)
	if err != nil {
		return nil, 0, err
	}
	end := offset + limit
	if end > len(vars) {
		end = len(vars)
	}
	if offset > len(vars) {
		offset = len(vars)
	}
	return vars[offset:end], total, nil
}

func (s *SQLiteStore) UpdatePipeline(p *models.Pipeline) error {
	f, err := marshalPipelineJSON(p)
	if err != nil {
		return wrapStoreErr("UpdatePipeline", p.ID, err)
	}

	result, err := s.db.Exec(
		`UPDATE pipelines SET ir_version=?, name=?, description=?, nodes=?, edges=?, schedule=?, schedule_timezone=?, webhook_url=?, params=?, tags=?, sla_deadline=?, sla_timezone=?, depends_on=?, dependency_rules=?, webhook_token=?, enabled=?, updated_at=?, pipeline_id=?, source=?, workspace_id=?, org_id=?, hooks=?, extensions=?, catchup=?, parameters=?
		 WHERE id=?`,
		p.IRVersion, p.Name, p.Description, string(f.nodesJSON), string(f.edgesJSON),
		p.Schedule, p.ScheduleTimezone, p.WebhookURL, string(f.paramsJSON), string(f.tagsJSON), p.SLADeadline, p.SLATimezone, string(f.depsJSON), string(f.depRulesJSON), p.WebhookToken, boolToInt(p.Enabled), p.UpdatedAt.UTC().Format(timeFormat), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID, string(f.hooksJSON), string(f.extensionsJSON), boolToInt(p.Catchup), string(f.parametersJSON), p.ID,
	)
	if err != nil {
		return wrapStoreErr("UpdatePipeline", p.ID, err)
	}
	return wrapStoreErr("UpdatePipeline", p.ID, checkRowsAffected(result, "pipeline", p.ID))
}

func (s *SQLiteStore) GetPipelineByPipelineID(pipelineID string) (*models.Pipeline, error) {
	row := s.db.QueryRow(
		`SELECT id, ir_version, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id, hooks, extensions, catchup, parameters
		 FROM pipelines WHERE pipeline_id = ?`, pipelineID,
	)
	p, err := scanPipeline(row)
	if err != nil {
		return nil, wrapStoreErr("GetPipelineByPipelineID", pipelineID, err)
	}
	return p, nil
}

// ListPipelineDepsByOrg returns a projection that only reads the dep columns (no nodes/edges).
func (s *SQLiteStore) ListPipelineDepsByOrg(orgID string) ([]models.PipelineDepSummary, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if orgID != "" {
		rows, err = s.db.Query(
			`SELECT id, name, org_id, depends_on, dependency_rules FROM pipelines WHERE org_id = ?`,
			orgID,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, name, org_id, depends_on, dependency_rules FROM pipelines`,
		)
	}
	if err != nil {
		return nil, wrapStoreErr("ListPipelineDepsByOrg", orgID, err)
	}
	defer rows.Close()

	out := make([]models.PipelineDepSummary, 0, 64)
	for rows.Next() {
		var (
			s            models.PipelineDepSummary
			depsJSON     string
			depRulesJSON string
		)
		if err := rows.Scan(&s.ID, &s.Name, &s.OrgID, &depsJSON, &depRulesJSON); err != nil {
			return nil, wrapStoreErr("ListPipelineDepsByOrg", orgID, err)
		}
		if depsJSON != "" && depsJSON != "null" {
			json.Unmarshal([]byte(depsJSON), &s.DependsOn)
		}
		if depRulesJSON != "" && depRulesJSON != "null" {
			json.Unmarshal([]byte(depRulesJSON), &s.DependencyRules)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetLatestRunsByPipelineIDs returns a map of pipelineID → most recent run in one query.
func (s *SQLiteStore) GetLatestRunsByPipelineIDs(ids []string) (map[string]*models.Run, error) {
	out := make(map[string]*models.Run, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

	// Dedupe IDs and build placeholders for WHERE IN (...)
	uniq := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out, nil
	}

	placeholders := make([]string, len(uniq))
	args := make([]interface{}, len(uniq))
	for i, id := range uniq {
		placeholders[i] = "?"
		args[i] = id
	}

	// Rank pending runs first, then started runs newest-first, consistently with Postgres.
	query := `
		SELECT id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id, cancel_requested, trigger_type, data_interval_start, data_interval_end
		FROM (
			SELECT id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id, cancel_requested, trigger_type, data_interval_start, data_interval_end,
			       ROW_NUMBER() OVER (
			           PARTITION BY pipeline_id
			           ORDER BY (started_at IS NULL) DESC, started_at DESC, id DESC
			       ) AS rank
			FROM runs
			WHERE pipeline_id IN (` + strings.Join(placeholders, ",") + `)
		)
		WHERE rank = 1
	`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, wrapStoreErr("GetLatestRunsByPipelineIDs", "", err)
	}
	defer rows.Close()

	for rows.Next() {
		run, err := scanRunRows(rows)
		if err != nil {
			return nil, wrapStoreErr("GetLatestRunsByPipelineIDs", "", err)
		}
		// In case of ties on started_at, keep whichever sorts first — harmless for dep checks.
		if _, exists := out[run.PipelineID]; !exists {
			out[run.PipelineID] = run
		}
	}
	return out, rows.Err()
}

// DeletePipelineTx deletes a pipeline inside an active transaction.
func (s *SQLiteStore) DeletePipelineTx(tx *sql.Tx, id string) error {
	result, err := tx.Exec(`DELETE FROM pipelines WHERE id=?`, id)
	if err != nil {
		return wrapStoreErr("DeletePipelineTx", id, err)
	}
	return wrapStoreErr("DeletePipelineTx", id, checkRowsAffected(result, "pipeline", id))
}

// UpdatePipelineTx updates a pipeline inside an active transaction.
func (s *SQLiteStore) UpdatePipelineTx(tx *sql.Tx, p *models.Pipeline) error {
	f, err := marshalPipelineJSON(p)
	if err != nil {
		return wrapStoreErr("UpdatePipelineTx", p.ID, err)
	}
	result, err := tx.Exec(
		`UPDATE pipelines SET ir_version=?, name=?, description=?, nodes=?, edges=?, schedule=?, schedule_timezone=?, webhook_url=?, params=?, tags=?, sla_deadline=?, sla_timezone=?, depends_on=?, dependency_rules=?, webhook_token=?, enabled=?, updated_at=?, pipeline_id=?, source=?, workspace_id=?, org_id=?, hooks=?, extensions=?, catchup=?, parameters=?
		 WHERE id=?`,
		p.IRVersion, p.Name, p.Description, string(f.nodesJSON), string(f.edgesJSON),
		p.Schedule, p.ScheduleTimezone, p.WebhookURL, string(f.paramsJSON), string(f.tagsJSON), p.SLADeadline, p.SLATimezone, string(f.depsJSON), string(f.depRulesJSON), p.WebhookToken, boolToInt(p.Enabled), p.UpdatedAt.UTC().Format(timeFormat), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID, string(f.hooksJSON), string(f.extensionsJSON), boolToInt(p.Catchup), string(f.parametersJSON), p.ID,
	)
	if err != nil {
		return wrapStoreErr("UpdatePipelineTx", p.ID, err)
	}
	return wrapStoreErr("UpdatePipelineTx", p.ID, checkRowsAffected(result, "pipeline", p.ID))
}

func (s *SQLiteStore) PipelinesDependingOn(pipelineID string) ([]models.Pipeline, error) {
	all, err := s.ListPipelines()
	if err != nil {
		return nil, err
	}
	out := make([]models.Pipeline, 0)
	for _, p := range all {
		for _, dep := range p.EffectiveDependencies() {
			if dep.PipelineID == pipelineID {
				out = append(out, p)
				break
			}
		}
	}
	return out, nil
}

func (s *SQLiteStore) DeletePipeline(id string) error {
	result, err := s.db.Exec(`DELETE FROM pipelines WHERE id=?`, id)
	if err != nil {
		return wrapStoreErr("DeletePipeline", id, err)
	}
	return wrapStoreErr("DeletePipeline", id, checkRowsAffected(result, "pipeline", id))
}

// --- Runs ---

func (s *SQLiteStore) CreateRun(r *models.Run) error {
	return sqliteCreateRun(s.db, r)
}

// CreateRunTx creates a run inside an existing transaction, so it can commit
// atomically alongside AppendEventTx and CreateExecutionAttemptTx — see
// Engine.RunPipelineAsync's dispatch outbox (Tnsor-Labs/brokoli#7).
func (s *SQLiteStore) CreateRunTx(tx *sql.Tx, r *models.Run) error {
	return sqliteCreateRun(tx, r)
}

func sqliteCreateRun(x sqlExecer, r *models.Run) error {
	paramsJSON, err := json.Marshal(r.Params)
	if err != nil {
		return wrapStoreErr("CreateRun", r.ID, fmt.Errorf("marshal params: %w", err))
	}
	parametersJSON, err := json.Marshal(r.Parameters)
	if err != nil {
		return wrapStoreErr("CreateRun", r.ID, fmt.Errorf("marshal parameters: %w", err))
	}
	_, err = x.Exec(
		`INSERT INTO runs (id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id, cancel_requested, trigger_type, data_interval_start, data_interval_end, parameters) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.PipelineID, string(r.Status), formatTimePtr(r.StartedAt), formatTimePtr(r.FinishedAt), r.TraceID, r.Error, string(paramsJSON), r.PipelineVersion, r.ResumedFromRunID, r.OrgID, boolToInt(r.CancelRequested), r.Trigger, formatTimePtr(r.DataIntervalStart), formatTimePtr(r.DataIntervalEnd), string(parametersJSON),
	)
	if isScheduledIntervalConflict(err) {
		return ErrDuplicateScheduledRun
	}
	return wrapStoreErr("CreateRun", r.ID, err)
}

// ── Parked waits (#399) ─────────────────────────────────────

func (s *SQLiteStore) CreateParkedWait(w *models.ParkedWait) error {
	_, err := s.db.Exec(
		`INSERT INTO parked_waits (run_id, pipeline_id, node_id, condition, poll_interval_ms, next_poll_at, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		w.RunID, w.PipelineID, w.NodeID, w.Condition, w.PollInterval,
		w.NextPollAt.UTC().Format(timeFormat), w.ExpiresAt.UTC().Format(timeFormat), w.CreatedAt.UTC().Format(timeFormat),
	)
	return wrapStoreErr("CreateParkedWait", w.RunID, err)
}

func (s *SQLiteStore) DeleteParkedWait(runID string) (bool, error) {
	result, err := s.db.Exec(`DELETE FROM parked_waits WHERE run_id = ?`, runID)
	if err != nil {
		return false, wrapStoreErr("DeleteParkedWait", runID, err)
	}
	n, err := result.RowsAffected()
	return n == 1, wrapStoreErr("DeleteParkedWait", runID, err)
}

func (s *SQLiteStore) ListDueParkedWaits(now time.Time, limit int) ([]models.ParkedWait, error) {
	rows, err := s.db.Query(
		`SELECT run_id, pipeline_id, node_id, condition, poll_interval_ms, next_poll_at, expires_at, created_at
		 FROM parked_waits WHERE next_poll_at <= ? OR expires_at <= ? ORDER BY next_poll_at ASC LIMIT ?`,
		now.UTC().Format(timeFormat), now.UTC().Format(timeFormat), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []models.ParkedWait
	for rows.Next() {
		var w models.ParkedWait
		var next, exp, created string
		if err := rows.Scan(&w.RunID, &w.PipelineID, &w.NodeID, &w.Condition, &w.PollInterval, &next, &exp, &created); err != nil {
			return nil, err
		}
		w.NextPollAt, _ = time.Parse(timeFormat, next)
		w.ExpiresAt, _ = time.Parse(timeFormat, exp)
		w.CreatedAt, _ = time.Parse(timeFormat, created)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) BumpParkedWait(runID string, nextPollAt time.Time) error {
	_, err := s.db.Exec(`UPDATE parked_waits SET next_poll_at = ? WHERE run_id = ?`,
		nextPollAt.UTC().Format(timeFormat), runID)
	return wrapStoreErr("BumpParkedWait", runID, err)
}

func (s *SQLiteStore) CountParkedWaits() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM parked_waits`).Scan(&n)
	return n, err
}

func (s *SQLiteStore) ClaimWaitingRun(runID string) (bool, error) {
	result, err := s.db.Exec(
		`UPDATE runs SET status=? WHERE id=? AND status=?`,
		string(models.RunStatusRunning), runID, string(models.RunStatusWaiting),
	)
	if err != nil {
		return false, wrapStoreErr("ClaimWaitingRun", runID, err)
	}
	n, err := result.RowsAffected()
	return n == 1, wrapStoreErr("ClaimWaitingRun", runID, err)
}

func (s *SQLiteStore) FailWaitingRun(runID string, finishedAt time.Time, errMsg string) (bool, error) {
	result, err := s.db.Exec(
		`UPDATE runs SET status=?, finished_at=?, error=? WHERE id=? AND status=?`,
		string(models.RunStatusFailed), formatTimePtr(&finishedAt), errMsg, runID, string(models.RunStatusWaiting),
	)
	if err != nil {
		return false, wrapStoreErr("FailWaitingRun", runID, err)
	}
	n, err := result.RowsAffected()
	return n == 1, wrapStoreErr("FailWaitingRun", runID, err)
}

func (s *SQLiteStore) ClaimPendingRun(runID, pipelineID string, startedAt time.Time, traceID string) (bool, error) {
	result, err := s.db.Exec(
		`UPDATE runs SET status=?, started_at=?, trace_id=? WHERE id=? AND pipeline_id=? AND status=?`,
		string(models.RunStatusRunning), formatTimePtr(&startedAt), traceID, runID, pipelineID, string(models.RunStatusPending),
	)
	if err != nil {
		return false, wrapStoreErr("ClaimPendingRun", runID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, wrapStoreErr("ClaimPendingRun", runID, err)
	}
	return n == 1, nil
}

func (s *SQLiteStore) RequestRunCancel(runID string) (bool, error) {
	result, err := s.db.Exec(
		`UPDATE runs SET cancel_requested=1 WHERE id=? AND status IN (?, ?)`,
		runID, string(models.RunStatusPending), string(models.RunStatusRunning),
	)
	if err != nil {
		return false, wrapStoreErr("RequestRunCancel", runID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, wrapStoreErr("RequestRunCancel", runID, err)
	}
	return n == 1, nil
}

func (s *SQLiteStore) CancelPendingRun(runID string, finishedAt time.Time) (bool, error) {
	result, err := s.db.Exec(
		`UPDATE runs SET status=?, finished_at=?, error=? WHERE id=? AND status=?`,
		string(models.RunStatusCancelled), formatTimePtr(&finishedAt), "cancelled by user", runID, string(models.RunStatusPending),
	)
	if err != nil {
		return false, wrapStoreErr("CancelPendingRun", runID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, wrapStoreErr("CancelPendingRun", runID, err)
	}
	return n == 1, nil
}

// GetRun does its own scan rather than the shared scanRun/runColumns path
// (unlike List* below) so that adding the ADR-032 'parameters' column
// (issue #439) here does not require touching runColumns and every List*
// query that constant serves -- GetRun is the single-run detail fetch;
// list views deliberately don't carry the resolved-parameter snapshot,
// the same "skip the expensive blob for list shapes" precedent
// ListPipelineDepsByOrg already established for pipelines.
func (s *SQLiteStore) GetRun(id string) (*models.Run, error) {
	var r models.Run
	var status string
	var startedAt, finishedAt, intervalStart, intervalEnd sql.NullString
	var paramsJSON, parametersJSON string
	err := s.db.QueryRow(
		`SELECT id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id, cancel_requested, trigger_type, data_interval_start, data_interval_end, parameters FROM runs WHERE id = ?`, id,
	).Scan(&r.ID, &r.PipelineID, &status, &startedAt, &finishedAt, &r.TraceID, &r.Error, &paramsJSON, &r.PipelineVersion, &r.ResumedFromRunID, &r.OrgID, &r.CancelRequested, &r.Trigger, &intervalStart, &intervalEnd, &parametersJSON)
	if err != nil {
		return nil, wrapStoreErr("GetRun", id, err)
	}
	r.Status = models.RunStatus(status)
	r.StartedAt = parseTimePtr(startedAt)
	r.FinishedAt = parseTimePtr(finishedAt)
	r.DataIntervalStart = parseTimePtr(intervalStart)
	r.DataIntervalEnd = parseTimePtr(intervalEnd)
	if err := json.Unmarshal([]byte(paramsJSON), &r.Params); err != nil {
		return nil, wrapStoreErr("GetRun", id, fmt.Errorf("decode params: %w", err))
	}
	if parametersJSON != "" && parametersJSON != "null" && parametersJSON != "{}" {
		if err := json.Unmarshal([]byte(parametersJSON), &r.Parameters); err != nil {
			return nil, wrapStoreErr("GetRun", id, fmt.Errorf("decode parameters: %w", err))
		}
	}

	nodeRuns, err := s.ListNodeRunsByRun(id)
	if err != nil {
		return nil, wrapStoreErr("GetRun", id, err)
	}
	r.NodeRuns = nodeRuns
	return &r, nil
}

func (s *SQLiteStore) ListRunsByPipeline(pipelineID string, limit int) ([]models.Run, error) {
	rows, err := s.db.Query(
		`SELECT id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id, cancel_requested, trigger_type, data_interval_start, data_interval_end
		 FROM runs WHERE pipeline_id = ? ORDER BY (started_at IS NULL) DESC, started_at DESC, id DESC LIMIT ?`,
		pipelineID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []models.Run
	for rows.Next() {
		r, err := scanRunRows(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *r)
	}
	return runs, rows.Err()
}

const runColumns = `id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id, cancel_requested, trigger_type, data_interval_start, data_interval_end`

func (s *SQLiteStore) ListRunsByPipelineCursor(pipelineID, afterID string, limit int) ([]models.Run, bool, error) {
	// Fetch one more than asked for: if it comes back, there is another
	// page. That is what lets hasNext be exact without a COUNT.
	fetchN := limit + 1
	var rows *sql.Rows
	var err error
	if afterID == "" {
		rows, err = s.db.Query(
			`SELECT `+runColumns+` FROM runs WHERE pipeline_id = ? ORDER BY id DESC LIMIT ?`,
			pipelineID, fetchN)
	} else {
		rows, err = s.db.Query(
			`SELECT `+runColumns+` FROM runs WHERE pipeline_id = ? AND id < ? ORDER BY id DESC LIMIT ?`,
			pipelineID, afterID, fetchN)
	}
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var runs []models.Run
	for rows.Next() {
		r, err := scanRunRows(rows)
		if err != nil {
			return nil, false, err
		}
		runs = append(runs, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasNext := len(runs) > limit
	if hasNext {
		runs = runs[:limit]
	}
	return runs, hasNext, nil
}

func (s *SQLiteStore) ListRunsByPipelinePaged(pipelineID string, limit, offset int) ([]models.Run, int, error) {
	total, err := s.CountRunsByPipeline(pipelineID)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(
		`SELECT `+runColumns+` FROM runs WHERE pipeline_id = ? ORDER BY id DESC LIMIT ? OFFSET ?`,
		pipelineID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var runs []models.Run
	for rows.Next() {
		r, err := scanRunRows(rows)
		if err != nil {
			return nil, 0, err
		}
		runs = append(runs, *r)
	}
	return runs, total, rows.Err()
}

// ListNonTerminalRuns returns a keyset-paginated page of runs left in a
// non-terminal status — see store.Store.ListNonTerminalRuns and
// Tnsor-Labs/brokoli#9. Ordered ascending by id (a sortable UUIDv7, see
// pkg/common.NewID), consistent with ListPipelinesByOrgCursor's cursor
// convention elsewhere in this file.
func (s *SQLiteStore) ListNonTerminalRuns(afterID string, limit int) ([]models.Run, bool, error) {
	fetchN := limit + 1
	var rows *sql.Rows
	var err error
	if afterID == "" {
		rows, err = s.db.Query(
			`SELECT id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id, cancel_requested, trigger_type, data_interval_start, data_interval_end
			 FROM runs WHERE `+nonTerminalRunStatusFilter+` ORDER BY id ASC LIMIT ?`,
			fetchN,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id, cancel_requested, trigger_type, data_interval_start, data_interval_end
			 FROM runs WHERE `+nonTerminalRunStatusFilter+` AND id > ? ORDER BY id ASC LIMIT ?`,
			afterID, fetchN,
		)
	}
	if err != nil {
		return nil, false, wrapStoreErr("ListNonTerminalRuns", afterID, err)
	}
	defer rows.Close()

	var runs []models.Run
	for rows.Next() {
		r, err := scanRunRows(rows)
		if err != nil {
			return nil, false, wrapStoreErr("ListNonTerminalRuns", afterID, err)
		}
		runs = append(runs, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, false, wrapStoreErr("ListNonTerminalRuns", afterID, err)
	}
	hasNext := len(runs) > limit
	if hasNext {
		runs = runs[:limit]
	}
	return runs, hasNext, nil
}

func (s *SQLiteStore) UpdateRun(r *models.Run) error {
	paramsJSON, err := json.Marshal(r.Params)
	if err != nil {
		return wrapStoreErr("UpdateRun", r.ID, fmt.Errorf("marshal params: %w", err))
	}
	result, err := s.db.Exec(
		`UPDATE runs SET status=?, started_at=?, finished_at=?, trace_id=?, error=?, params=? WHERE id=?`,
		string(r.Status), formatTimePtr(r.StartedAt), formatTimePtr(r.FinishedAt), r.TraceID, r.Error, string(paramsJSON), r.ID,
	)
	if err != nil {
		return wrapStoreErr("UpdateRun", r.ID, err)
	}
	return wrapStoreErr("UpdateRun", r.ID, checkRowsAffected(result, "run", r.ID))
}

// --- Node Runs ---

func (s *SQLiteStore) CreateNodeRun(nr *models.NodeRun) error {
	_, err := s.db.Exec(
		`INSERT INTO node_runs (id, run_id, node_id, status, row_count, started_at, duration_ms, error, attempt, ready_at, queue_ms, rows_per_sec, trace_id, span_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nr.ID, nr.RunID, nr.NodeID, string(nr.Status), nr.RowCount,
		formatTimePtr(nr.StartedAt), nr.DurationMs, nr.Error,
		nr.Attempt, formatTimePtr(nr.ReadyAt), nr.QueueMs, nr.RowsPerSec, nr.TraceID, nr.SpanID,
	)
	return err
}

func (s *SQLiteStore) CreateNodeRunTx(tx *sql.Tx, nr *models.NodeRun) error {
	_, err := tx.Exec(
		`INSERT INTO node_runs (id, run_id, node_id, status, row_count, started_at, duration_ms, error, attempt, ready_at, queue_ms, rows_per_sec, trace_id, span_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		nr.ID, nr.RunID, nr.NodeID, string(nr.Status), nr.RowCount,
		formatTimePtr(nr.StartedAt), nr.DurationMs, nr.Error,
		nr.Attempt, formatTimePtr(nr.ReadyAt), nr.QueueMs, nr.RowsPerSec, nr.TraceID, nr.SpanID,
	)
	return err
}

func (s *SQLiteStore) UpdateNodeRun(nr *models.NodeRun) error {
	result, err := s.db.Exec(
		`UPDATE node_runs SET status=?, row_count=?, started_at=?, duration_ms=?, error=?, attempt=?, ready_at=?, queue_ms=?, rows_per_sec=?, trace_id=?, span_id=? WHERE id=?`,
		string(nr.Status), nr.RowCount, formatTimePtr(nr.StartedAt), nr.DurationMs, nr.Error,
		nr.Attempt, formatTimePtr(nr.ReadyAt), nr.QueueMs, nr.RowsPerSec, nr.TraceID, nr.SpanID, nr.ID,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(result, "node_run", nr.ID)
}

func (s *SQLiteStore) UpdateNodeRunTx(tx *sql.Tx, nr *models.NodeRun) error {
	result, err := tx.Exec(
		`UPDATE node_runs SET status=?, row_count=?, started_at=?, duration_ms=?, error=?, attempt=?, ready_at=?, queue_ms=?, rows_per_sec=?, trace_id=?, span_id=? WHERE id=?`,
		string(nr.Status), nr.RowCount, formatTimePtr(nr.StartedAt), nr.DurationMs, nr.Error,
		nr.Attempt, formatTimePtr(nr.ReadyAt), nr.QueueMs, nr.RowsPerSec, nr.TraceID, nr.SpanID, nr.ID,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(result, "node_run", nr.ID)
}

// GridNodeRuns batch-fetches the latest attempt's node-run per (run, node)
// for the given runs -- the grid view's cells (#400). One query, the
// ROW_NUMBER-per-partition idiom this file already uses twice.
func (s *SQLiteStore) GridNodeRuns(runIDs []string) (map[string][]models.NodeRun, error) {
	out := make(map[string][]models.NodeRun, len(runIDs))
	if len(runIDs) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(runIDs))
	args := make([]interface{}, len(runIDs))
	for i, id := range runIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := s.db.Query(`
		SELECT id, run_id, node_id, status, row_count, started_at, duration_ms, error, attempt, queue_ms
		FROM (
			SELECT id, run_id, node_id, status, row_count, started_at, duration_ms, error, attempt, queue_ms,
			       ROW_NUMBER() OVER (PARTITION BY run_id, node_id ORDER BY attempt DESC) AS rn
			FROM node_runs WHERE run_id IN (`+strings.Join(placeholders, ",")+`)
		) WHERE rn = 1`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var nr models.NodeRun
		var status string
		var startedAt sql.NullString
		if err := rows.Scan(&nr.ID, &nr.RunID, &nr.NodeID, &status, &nr.RowCount,
			&startedAt, &nr.DurationMs, &nr.Error, &nr.Attempt, &nr.QueueMs); err != nil {
			return nil, err
		}
		nr.Status = models.RunStatus(status)
		nr.StartedAt = parseTimePtr(startedAt)
		out[nr.RunID] = append(out[nr.RunID], nr)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) ListNodeRunsByRun(runID string) ([]models.NodeRun, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, node_id, status, row_count, started_at, duration_ms, error, attempt, ready_at, queue_ms, rows_per_sec, trace_id, span_id
		 FROM node_runs WHERE run_id = ?`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodeRuns []models.NodeRun
	for rows.Next() {
		var nr models.NodeRun
		var status string
		var startedAt, readyAt sql.NullString
		if err := rows.Scan(&nr.ID, &nr.RunID, &nr.NodeID, &status, &nr.RowCount, &startedAt, &nr.DurationMs, &nr.Error,
			&nr.Attempt, &readyAt, &nr.QueueMs, &nr.RowsPerSec, &nr.TraceID, &nr.SpanID); err != nil {
			return nil, err
		}
		nr.Status = models.RunStatus(status)
		nr.StartedAt = parseTimePtr(startedAt)
		nr.ReadyAt = parseTimePtr(readyAt)
		nodeRuns = append(nodeRuns, nr)
	}
	return nodeRuns, rows.Err()
}

// --- Expansion Instances ---
//
// See store.ExpansionInstanceStore and models.ExpansionInstance for the
// contract these implement — the durable per-item execution record for a
// dynamic-expansion `code` node (issue #31).

func (s *SQLiteStore) CreateExpansionInstance(ei *models.ExpansionInstance) error {
	_, err := s.db.Exec(
		`INSERT INTO expansion_instances (id, run_id, node_id, node_attempt, instance_index, instance_key, status, row_count, started_at, duration_ms, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		ei.ID, ei.RunID, ei.NodeID, ei.NodeAttempt, ei.InstanceIndex, ei.InstanceKey,
		string(ei.Status), ei.RowCount, formatTimePtr(ei.StartedAt), ei.DurationMs, ei.Error,
	)
	return wrapStoreErr("CreateExpansionInstance", ei.RunID, err)
}

func (s *SQLiteStore) UpdateExpansionInstance(ei *models.ExpansionInstance) error {
	result, err := s.db.Exec(
		`UPDATE expansion_instances SET status=?, row_count=?, started_at=?, duration_ms=?, error=? WHERE id=?`,
		string(ei.Status), ei.RowCount, formatTimePtr(ei.StartedAt), ei.DurationMs, ei.Error, ei.ID,
	)
	if err != nil {
		return wrapStoreErr("UpdateExpansionInstance", ei.RunID, err)
	}
	return wrapStoreErr("UpdateExpansionInstance", ei.RunID, checkRowsAffected(result, "expansion_instance", ei.ID))
}

func (s *SQLiteStore) ListExpansionInstancesByRun(runID string) ([]models.ExpansionInstance, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, node_id, node_attempt, instance_index, instance_key, status, row_count, started_at, duration_ms, error
		 FROM expansion_instances WHERE run_id = ? ORDER BY node_id ASC, node_attempt ASC, instance_index ASC`,
		runID,
	)
	if err != nil {
		return nil, wrapStoreErr("ListExpansionInstancesByRun", runID, err)
	}
	defer rows.Close()

	var instances []models.ExpansionInstance
	for rows.Next() {
		var ei models.ExpansionInstance
		var status string
		var startedAt sql.NullString
		if err := rows.Scan(&ei.ID, &ei.RunID, &ei.NodeID, &ei.NodeAttempt, &ei.InstanceIndex, &ei.InstanceKey,
			&status, &ei.RowCount, &startedAt, &ei.DurationMs, &ei.Error); err != nil {
			return nil, wrapStoreErr("ListExpansionInstancesByRun", runID, err)
		}
		ei.Status = models.RunStatus(status)
		ei.StartedAt = parseTimePtr(startedAt)
		instances = append(instances, ei)
	}
	return instances, rows.Err()
}

// --- Run Events ---

// sqlExecer is satisfied by both *sql.DB and *sql.Tx, letting AppendEvent
// and AppendEventTx share one implementation.
type sqlExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

func (s *SQLiteStore) AppendEvent(e *models.RunEvent) error {
	return sqliteAppendEvent(s.db, e)
}

// AppendEventTx appends an event using an existing transaction, so it can be
// committed atomically alongside other writes via WithTx.
func (s *SQLiteStore) AppendEventTx(tx *sql.Tx, e *models.RunEvent) error {
	return sqliteAppendEvent(tx, e)
}

func sqliteAppendEvent(x sqlExecer, e *models.RunEvent) error {
	payloadJSON, err := json.Marshal(e.Payload)
	if err != nil {
		return wrapStoreErr("AppendEvent", e.RunID, fmt.Errorf("marshal payload: %w", err))
	}
	schemaVersion := e.SchemaVersion
	if schemaVersion == 0 {
		schemaVersion = 1
	}
	e.CreatedAt = time.Now().UTC()

	var nodeID interface{}
	if e.NodeID != "" {
		nodeID = e.NodeID
	}
	var attempt interface{}
	if e.Attempt != nil {
		attempt = *e.Attempt
	}

	result, err := x.Exec(
		`INSERT INTO run_events (run_id, node_id, attempt, event_type, payload, created_at, schema_version) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.RunID, nodeID, attempt, string(e.EventType), string(payloadJSON), e.CreatedAt.Format(timeFormat), schemaVersion,
	)
	if err != nil {
		return wrapStoreErr("AppendEvent", e.RunID, err)
	}
	e.SchemaVersion = schemaVersion
	if id, idErr := result.LastInsertId(); idErr == nil {
		e.ID = id
	}
	return nil
}

func (s *SQLiteStore) ListEventsByRun(runID string) ([]models.RunEvent, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, node_id, attempt, event_type, payload, created_at, schema_version
		 FROM run_events WHERE run_id = ? ORDER BY id ASC`, runID,
	)
	if err != nil {
		return nil, wrapStoreErr("ListEventsByRun", runID, err)
	}
	defer rows.Close()

	var events []models.RunEvent
	for rows.Next() {
		ev, err := scanRunEvent(rows)
		if err != nil {
			return nil, wrapStoreErr("ListEventsByRun", runID, err)
		}
		events = append(events, *ev)
	}
	return events, rows.Err()
}

func scanRunEvent(rows *sql.Rows) (*models.RunEvent, error) {
	var ev models.RunEvent
	var nodeID sql.NullString
	var attempt sql.NullInt64
	var eventType, payloadJSON, createdAt string
	if err := rows.Scan(&ev.ID, &ev.RunID, &nodeID, &attempt, &eventType, &payloadJSON, &createdAt, &ev.SchemaVersion); err != nil {
		return nil, err
	}
	ev.NodeID = nodeID.String
	if attempt.Valid {
		a := int(attempt.Int64)
		ev.Attempt = &a
	}
	ev.EventType = models.RunEventType(eventType)
	if err := json.Unmarshal([]byte(payloadJSON), &ev.Payload); err != nil {
		return nil, fmt.Errorf("decode event payload: %w", err)
	}
	createdAtTime, err := time.Parse(timeFormat, createdAt)
	if err != nil {
		return nil, fmt.Errorf("decode event created_at: %w", err)
	}
	ev.CreatedAt = createdAtTime
	return &ev, nil
}

// --- Execution Attempts ---
//
// See store.ExecutionAttemptStore and models.ExecutionAttempt for the
// contract these implement: a durable outbox/intent record plus a
// compare-and-swap claim/lease, extending PendingRunClaimer's run-level CAS
// (ClaimPendingRun above) to (run_id, node_id, attempt) granularity.

func (s *SQLiteStore) CreateExecutionAttemptTx(tx *sql.Tx, a *models.ExecutionAttempt) error {
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	status := a.Status
	if status == "" {
		status = models.AttemptStatusQueued
	}
	_, err := tx.Exec(
		`INSERT INTO execution_attempts (run_id, node_id, instance_key, attempt, status, claimed_by, lease_expires_at, fencing_generation, idempotency_key, error, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(run_id, node_id, instance_key, attempt) DO NOTHING`,
		a.RunID, a.NodeID, a.InstanceKey, a.Attempt, string(status), a.ClaimedBy, formatTimePtr(a.LeaseExpiresAt),
		a.FencingGeneration, a.IdempotencyKey, a.Error, a.CreatedAt.Format(timeFormat), a.UpdatedAt.Format(timeFormat),
	)
	return wrapStoreErr("CreateExecutionAttempt", a.RunID, err)
}

func (s *SQLiteStore) ClaimAttempt(runID, nodeID, instanceKey string, attempt int, claimedBy string, leaseDuration time.Duration) (int64, bool, error) {
	now := time.Now().UTC()
	leaseExpires := now.Add(leaseDuration)
	result, err := s.db.Exec(
		`UPDATE execution_attempts
		 SET status=?, claimed_by=?, lease_expires_at=?, fencing_generation=fencing_generation+1, updated_at=?
		 WHERE run_id=? AND node_id=? AND instance_key=? AND attempt=?
		   AND status NOT IN (?, ?)
		   AND (status=? OR lease_expires_at IS NULL OR lease_expires_at<?)`,
		string(models.AttemptStatusClaimed), claimedBy, formatTimePtr(&leaseExpires), now.Format(timeFormat),
		runID, nodeID, instanceKey, attempt,
		string(models.AttemptStatusCompleted), string(models.AttemptStatusFailed),
		string(models.AttemptStatusQueued), now.Format(timeFormat),
	)
	if err != nil {
		return 0, false, wrapStoreErr("ClaimAttempt", runID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return 0, false, wrapStoreErr("ClaimAttempt", runID, err)
	}
	if n != 1 {
		return 0, false, nil
	}
	existing, err := sqliteGetExecutionAttempt(s.db, runID, nodeID, instanceKey, attempt)
	if err != nil {
		return 0, false, wrapStoreErr("ClaimAttempt", runID, err)
	}
	return existing.FencingGeneration, true, nil
}

func (s *SQLiteStore) RenewLease(runID, nodeID, instanceKey string, attempt int, claimedBy string, fencingGeneration int64, leaseDuration time.Duration) (bool, error) {
	now := time.Now().UTC()
	leaseExpires := now.Add(leaseDuration)
	result, err := s.db.Exec(
		`UPDATE execution_attempts SET lease_expires_at=?, updated_at=?
		 WHERE run_id=? AND node_id=? AND instance_key=? AND attempt=? AND claimed_by=? AND fencing_generation=? AND status IN (?, ?)`,
		formatTimePtr(&leaseExpires), now.Format(timeFormat),
		runID, nodeID, instanceKey, attempt, claimedBy, fencingGeneration,
		string(models.AttemptStatusClaimed), string(models.AttemptStatusStarted),
	)
	if err != nil {
		return false, wrapStoreErr("RenewLease", runID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, wrapStoreErr("RenewLease", runID, err)
	}
	return n == 1, nil
}

func (s *SQLiteStore) AckAttempt(runID, nodeID, instanceKey string, attempt int, claimedBy string, fencingGeneration int64) error {
	now := time.Now().UTC()
	result, err := s.db.Exec(
		`UPDATE execution_attempts SET status=?, updated_at=?
		 WHERE run_id=? AND node_id=? AND instance_key=? AND attempt=? AND claimed_by=? AND fencing_generation=? AND status IN (?, ?)`,
		string(models.AttemptStatusStarted), now.Format(timeFormat),
		runID, nodeID, instanceKey, attempt, claimedBy, fencingGeneration,
		string(models.AttemptStatusClaimed), string(models.AttemptStatusStarted),
	)
	if err != nil {
		return wrapStoreErr("AckAttempt", runID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return wrapStoreErr("AckAttempt", runID, err)
	}
	if n == 1 {
		return nil
	}
	existing, getErr := sqliteGetExecutionAttempt(s.db, runID, nodeID, instanceKey, attempt)
	if getErr != nil {
		return wrapStoreErr("AckAttempt", runID, getErr)
	}
	if existing.Status == models.AttemptStatusStarted && existing.ClaimedBy == claimedBy && existing.FencingGeneration == fencingGeneration {
		return nil // idempotent re-ack
	}
	return wrapStoreErr("AckAttempt", runID, fmt.Errorf("fencing generation mismatch or attempt not claimed by %q (have generation %d, status %s)", claimedBy, existing.FencingGeneration, existing.Status))
}

func (s *SQLiteStore) CompleteAttempt(runID, nodeID, instanceKey string, attempt int, fencingGeneration int64) error {
	return sqliteSettleAttempt(s.db, runID, nodeID, instanceKey, attempt, fencingGeneration, models.AttemptStatusCompleted, "")
}

func (s *SQLiteStore) FailAttempt(runID, nodeID, instanceKey string, attempt int, fencingGeneration int64, errMsg string) error {
	return sqliteSettleAttempt(s.db, runID, nodeID, instanceKey, attempt, fencingGeneration, models.AttemptStatusFailed, errMsg)
}

// sqliteSettleAttempt performs the fencing-checked transition into a
// terminal status (completed/failed). Settling an attempt already in that
// same terminal status is a no-op success — the documented duplicate-
// delivery contract; any other mismatch (wrong fencing generation, or
// already terminal in the *other* status) is an error.
func sqliteSettleAttempt(db *sql.DB, runID, nodeID, instanceKey string, attempt int, fencingGeneration int64, toStatus models.AttemptStatus, errMsg string) error {
	now := time.Now().UTC()
	result, err := db.Exec(
		`UPDATE execution_attempts SET status=?, error=?, updated_at=?
		 WHERE run_id=? AND node_id=? AND instance_key=? AND attempt=? AND fencing_generation=? AND status NOT IN (?, ?)`,
		string(toStatus), errMsg, now.Format(timeFormat),
		runID, nodeID, instanceKey, attempt, fencingGeneration,
		string(models.AttemptStatusCompleted), string(models.AttemptStatusFailed),
	)
	if err != nil {
		return wrapStoreErr("SettleAttempt", runID, err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return wrapStoreErr("SettleAttempt", runID, err)
	}
	if n == 1 {
		return nil
	}
	existing, getErr := sqliteGetExecutionAttempt(db, runID, nodeID, instanceKey, attempt)
	if getErr != nil {
		return wrapStoreErr("SettleAttempt", runID, getErr)
	}
	if existing.Status == toStatus {
		return nil // idempotent duplicate settle
	}
	return wrapStoreErr("SettleAttempt", runID, fmt.Errorf("fencing generation mismatch settling attempt to %s (have generation %d, status %s)", toStatus, existing.FencingGeneration, existing.Status))
}

func (s *SQLiteStore) GetExecutionAttempt(runID, nodeID, instanceKey string, attempt int) (*models.ExecutionAttempt, error) {
	a, err := sqliteGetExecutionAttempt(s.db, runID, nodeID, instanceKey, attempt)
	if err != nil {
		return nil, wrapStoreErr("GetExecutionAttempt", runID, err)
	}
	return a, nil
}

func sqliteGetExecutionAttempt(db *sql.DB, runID, nodeID, instanceKey string, attempt int) (*models.ExecutionAttempt, error) {
	row := db.QueryRow(
		`SELECT run_id, node_id, instance_key, attempt, status, claimed_by, lease_expires_at, fencing_generation, idempotency_key, error, created_at, updated_at
		 FROM execution_attempts WHERE run_id=? AND node_id=? AND instance_key=? AND attempt=?`,
		runID, nodeID, instanceKey, attempt,
	)
	return scanExecutionAttempt(row)
}

// ListExecutionAttemptsByRun returns every execution_attempts row for a run
// — see store.ExecutionAttemptStore.ListExecutionAttemptsByRun and
// Tnsor-Labs/brokoli#9, which needs this enumeration to check every
// attempt's lease state during startup recovery.
func (s *SQLiteStore) ListExecutionAttemptsByRun(runID string) ([]models.ExecutionAttempt, error) {
	rows, err := s.db.Query(
		`SELECT run_id, node_id, instance_key, attempt, status, claimed_by, lease_expires_at, fencing_generation, idempotency_key, error, created_at, updated_at
		 FROM execution_attempts WHERE run_id = ? ORDER BY node_id ASC, instance_key ASC, attempt ASC`,
		runID,
	)
	if err != nil {
		return nil, wrapStoreErr("ListExecutionAttemptsByRun", runID, err)
	}
	defer rows.Close()

	var attempts []models.ExecutionAttempt
	for rows.Next() {
		a, err := scanExecutionAttempt(rows)
		if err != nil {
			return nil, wrapStoreErr("ListExecutionAttemptsByRun", runID, err)
		}
		attempts = append(attempts, *a)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapStoreErr("ListExecutionAttemptsByRun", runID, err)
	}
	return attempts, nil
}

// scanExecutionAttempt scans one execution_attempts row from either a
// *sql.Row (GetExecutionAttempt) or *sql.Rows (ListExecutionAttemptsByRun) —
// both satisfy the shared scanner interface (see scanRunFromScanner above
// for the same pattern applied to runs).
func scanExecutionAttempt(row scanner) (*models.ExecutionAttempt, error) {
	var a models.ExecutionAttempt
	var status string
	var leaseExpiresAt sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(&a.RunID, &a.NodeID, &a.InstanceKey, &a.Attempt, &status, &a.ClaimedBy, &leaseExpiresAt, &a.FencingGeneration, &a.IdempotencyKey, &a.Error, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	a.Status = models.AttemptStatus(status)
	a.LeaseExpiresAt = parseTimePtr(leaseExpiresAt)
	createdAtTime, err := time.Parse(timeFormat, createdAt)
	if err != nil {
		return nil, fmt.Errorf("decode created_at: %w", err)
	}
	a.CreatedAt = createdAtTime
	updatedAtTime, err := time.Parse(timeFormat, updatedAt)
	if err != nil {
		return nil, fmt.Errorf("decode updated_at: %w", err)
	}
	a.UpdatedAt = updatedAtTime
	return &a, nil
}

// --- Logs ---

func (s *SQLiteStore) AppendLog(entry *models.LogEntry) error {
	metadata := "{}"
	if entry.Metadata != nil {
		metaBytes, err := json.Marshal(entry.Metadata)
		if err != nil {
			return fmt.Errorf("marshal log metadata: %w", err)
		}
		metadata = string(metaBytes)
	}
	_, err := s.db.Exec(
		`INSERT INTO logs (run_id, node_id, level, message, timestamp, trace_id, span_id, attempt, metadata) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.RunID, entry.NodeID, string(entry.Level), entry.Message, entry.Timestamp.UTC().Format(timeFormat),
		entry.TraceID, entry.SpanID, entry.Attempt, metadata,
	)
	return err
}

func (s *SQLiteStore) GetLogs(runID string) ([]models.LogEntry, error) {
	rows, err := s.db.Query(
		`SELECT run_id, node_id, level, message, timestamp, trace_id, span_id, attempt, metadata FROM logs WHERE run_id = ? ORDER BY timestamp`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []models.LogEntry
	for rows.Next() {
		var entry models.LogEntry
		var level, ts, metadataStr string
		if err := rows.Scan(&entry.RunID, &entry.NodeID, &level, &entry.Message, &ts,
			&entry.TraceID, &entry.SpanID, &entry.Attempt, &metadataStr); err != nil {
			return nil, err
		}
		entry.Level = models.LogLevel(level)
		entry.Timestamp, _ = time.Parse(timeFormat, ts)
		if metadataStr != "" {
			_ = json.Unmarshal([]byte(metadataStr), &entry.Metadata)
		}
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

// --- Node Previews ---

func (s *SQLiteStore) SaveNodePreview(runID, nodeID string, columns []string, rows []common.DataRow) error {
	colJSON, err := json.Marshal(columns)
	if err != nil {
		return fmt.Errorf("marshal columns: %w", err)
	}
	// Limit to 50 rows
	if len(rows) > 50 {
		rows = rows[:50]
	}
	rowJSON, err := json.Marshal(rows)
	if err != nil {
		return fmt.Errorf("marshal rows: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO node_previews (run_id, node_id, columns, rows) VALUES (?, ?, ?, ?)`,
		runID, nodeID, string(colJSON), string(rowJSON),
	)
	return err
}

func (s *SQLiteStore) GetNodePreview(runID, nodeID string) ([]string, []common.DataRow, error) {
	row := s.db.QueryRow(
		`SELECT columns, rows FROM node_previews WHERE run_id = ? AND node_id = ?`, runID, nodeID,
	)
	var colJSON, rowJSON string
	if err := row.Scan(&colJSON, &rowJSON); err != nil {
		return nil, nil, err
	}
	var columns []string
	if err := json.Unmarshal([]byte(colJSON), &columns); err != nil {
		return nil, nil, fmt.Errorf("unmarshal columns: %w", err)
	}
	var rows []common.DataRow
	if err := json.Unmarshal([]byte(rowJSON), &rows); err != nil {
		return nil, nil, fmt.Errorf("unmarshal rows: %w", err)
	}
	return columns, rows, nil
}

// --- Versioning ---

// SavePipelineVersion computes the next version number and inserts a
// snapshot row. The compute-then-insert is not atomic, so two concurrent
// callers for the same pipeline_id (a real possibility now that
// engine.resolveRunPipelineVersion calls this on every run trigger for a
// pipeline with no saved version yet, not just manual edits/rollbacks) can
// both read the same MAX(version) and race to insert it. The unique
// (pipeline_id, version) index added in migrate() turns that race into a
// constraint-violation error instead of two silently-ambiguous rows sharing
// one version number, and this retries past it with a fresh read — the
// loser of the race simply becomes the next version instead.
func (s *SQLiteStore) SavePipelineVersion(pipelineID string, snapshot string, message string) (int, error) {
	const maxAttempts = 100
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var maxVer sql.NullInt64
		s.db.QueryRow(`SELECT MAX(version) FROM pipeline_versions WHERE pipeline_id = ?`, pipelineID).Scan(&maxVer)
		nextVer := 1
		if maxVer.Valid {
			nextVer = int(maxVer.Int64) + 1
		}

		_, err := s.db.Exec(
			`INSERT INTO pipeline_versions (pipeline_id, version, snapshot, message, created_at) VALUES (?, ?, ?, ?, ?)`,
			pipelineID, nextVer, snapshot, message, time.Now().UTC().Format(timeFormat),
		)
		if err == nil {
			return nextVer, nil
		}
		lastErr = err
	}
	return 0, wrapStoreErr("SavePipelineVersion", pipelineID, fmt.Errorf("after %d attempts (likely concurrent version creation): %w", maxAttempts, lastErr))
}

func (s *SQLiteStore) ListPipelineVersions(pipelineID string) ([]PipelineVersion, error) {
	rows, err := s.db.Query(
		`SELECT version, message, created_at FROM pipeline_versions WHERE pipeline_id = ? ORDER BY version DESC LIMIT 50`,
		pipelineID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []PipelineVersion
	for rows.Next() {
		var v PipelineVersion
		if err := rows.Scan(&v.Version, &v.Message, &v.CreatedAt); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

func (s *SQLiteStore) GetPipelineVersion(pipelineID string, version int) (string, error) {
	var snapshot string
	err := s.db.QueryRow(
		`SELECT snapshot FROM pipeline_versions WHERE pipeline_id = ? AND version = ?`,
		pipelineID, version,
	).Scan(&snapshot)
	return snapshot, err
}

// --- Node Profiles ---

func (s *SQLiteStore) SaveNodeProfile(runID, nodeID, profileJSON, schemaJSON, driftJSON string) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO node_profiles (run_id, node_id, profile, schema_snapshot, drift_alerts, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		runID, nodeID, profileJSON, schemaJSON, driftJSON, time.Now().UTC().Format(timeFormat),
	)
	return err
}

func (s *SQLiteStore) GetNodeProfile(runID, nodeID string) (string, string, string, error) {
	var profile, schema, drift string
	err := s.db.QueryRow(
		`SELECT profile, schema_snapshot, drift_alerts FROM node_profiles WHERE run_id = ? AND node_id = ?`,
		runID, nodeID,
	).Scan(&profile, &schema, &drift)
	return profile, schema, drift, err
}

func (s *SQLiteStore) GetLatestNodeProfile(pipelineID, nodeID string) (string, string, error) {
	var profile, schema string
	err := s.db.QueryRow(
		`SELECT np.profile, np.schema_snapshot FROM node_profiles np
		 JOIN runs r ON r.id = np.run_id
		 WHERE r.pipeline_id = ? AND np.node_id = ?
		 ORDER BY np.created_at DESC LIMIT 1`, pipelineID, nodeID,
	).Scan(&profile, &schema)
	return profile, schema, err
}

// GetLatestNodeProfilesForPipelines batch-fetches the latest profile for
// every node of every given pipeline in one query, the same
// ROW_NUMBER-per-partition shape GetLatestRunsByPipelineIDs already uses for
// an analogous "latest per group" problem.
func (s *SQLiteStore) GetLatestNodeProfilesForPipelines(pipelineIDs []string) (map[string]NodeProfileRecord, error) {
	out := make(map[string]NodeProfileRecord, len(pipelineIDs))
	uniq := make([]string, 0, len(pipelineIDs))
	seen := make(map[string]bool, len(pipelineIDs))
	for _, id := range pipelineIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		uniq = append(uniq, id)
	}
	if len(uniq) == 0 {
		return out, nil
	}

	placeholders := make([]string, len(uniq))
	args := make([]interface{}, len(uniq))
	for i, id := range uniq {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `
		SELECT pipeline_id, node_id, profile, schema_snapshot, run_id, created_at
		FROM (
			SELECT r.pipeline_id AS pipeline_id, np.node_id AS node_id, np.profile AS profile,
			       np.schema_snapshot AS schema_snapshot, np.run_id AS run_id, np.created_at AS created_at,
			       ROW_NUMBER() OVER (
			           PARTITION BY r.pipeline_id, np.node_id
			           ORDER BY np.created_at DESC
			       ) AS rank
			FROM node_profiles np
			JOIN runs r ON r.id = np.run_id
			WHERE r.pipeline_id IN (` + strings.Join(placeholders, ",") + `)
		)
		WHERE rank = 1
	`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, wrapStoreErr("GetLatestNodeProfilesForPipelines", "", err)
	}
	defer rows.Close()

	for rows.Next() {
		var pipelineID, nodeID, profile, schema, runID, createdAt string
		if err := rows.Scan(&pipelineID, &nodeID, &profile, &schema, &runID, &createdAt); err != nil {
			return nil, wrapStoreErr("GetLatestNodeProfilesForPipelines", "", err)
		}
		observedAt, _ := time.Parse(timeFormat, createdAt)
		out[pipelineID+":"+nodeID] = NodeProfileRecord{
			ProfileJSON: profile, SchemaJSON: schema, RunID: runID, ObservedAt: observedAt,
		}
	}
	return out, rows.Err()
}

// --- Pagination Counts ---

func (s *SQLiteStore) CountPipelines(workspaceID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM pipelines WHERE workspace_id = ?", workspaceID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) CountConnections(workspaceID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM connections WHERE workspace_id = ?", workspaceID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) CountVariables(workspaceID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM variables WHERE workspace_id = ?", workspaceID).Scan(&count)
	return count, err
}

func (s *SQLiteStore) CountRunsByPipeline(pipelineID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM runs WHERE pipeline_id = ?", pipelineID).Scan(&count)
	return count, err
}

// CountRunsByStatus totals runs per status in one pass, for /metrics.
func (s *SQLiteStore) CountRunsByStatus() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT status, COUNT(*) FROM runs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make(map[string]int)
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		counts[status] = n
	}
	return counts, rows.Err()
}

// --- Maintenance ---

func (s *SQLiteStore) PurgeRunsOlderThan(days int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(timeFormat)
	result, err := s.db.Exec(`DELETE FROM runs WHERE started_at < ? AND started_at IS NOT NULL`, cutoff)
	if err != nil {
		return 0, err
	}
	// VACUUM to reclaim space
	s.db.Exec("VACUUM")
	return result.RowsAffected()
}

func (s *SQLiteStore) PurgeRunsOlderThanByOrg(days int, orgID string) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(timeFormat)
	result, err := s.db.Exec(`DELETE FROM runs WHERE started_at < ? AND started_at IS NOT NULL AND org_id = ?`, cutoff, orgID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ListRunIDsOlderThan mirrors PurgeRunsOlderThan's WHERE clause exactly —
// call before it to know which run IDs are about to be purged.
func (s *SQLiteStore) ListRunIDsOlderThan(days int) ([]string, error) {
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(timeFormat)
	return queryRunIDs(s.db, `SELECT id FROM runs WHERE started_at < ? AND started_at IS NOT NULL`, cutoff)
}

// ListRunIDsOlderThanByOrg mirrors PurgeRunsOlderThanByOrg's WHERE clause.
func (s *SQLiteStore) ListRunIDsOlderThanByOrg(days int, orgID string) ([]string, error) {
	cutoff := time.Now().AddDate(0, 0, -days).UTC().Format(timeFormat)
	return queryRunIDs(s.db, `SELECT id FROM runs WHERE started_at < ? AND started_at IS NOT NULL AND org_id = ?`, cutoff, orgID)
}

// queryRunIDs runs a "SELECT id FROM runs WHERE ..." query and collects the
// results into a slice — shared by both SQLiteStore's and PostgresStore's
// ListRunIDsOlderThan(By/Org), since both hold a *sql.DB via database/sql.
func queryRunIDs(db *sql.DB, query string, args ...interface{}) ([]string, error) {
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *SQLiteStore) GetDBSize() (int64, error) {
	var pageCount, pageSize int64
	s.db.QueryRow("PRAGMA page_count").Scan(&pageCount)
	s.db.QueryRow("PRAGMA page_size").Scan(&pageSize)
	return pageCount * pageSize, nil
}

// --- Helpers ---

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanPipelineFromScanner(sc scanner) (*models.Pipeline, error) {
	var p models.Pipeline
	var nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON, depRulesJSON, hooksJSON, extensionsJSON, parametersJSON, createdAt, updatedAt string
	var enabled, catchup int

	if err := sc.Scan(&p.ID, &p.IRVersion, &p.Name, &p.Description, &nodesJSON, &edgesJSON, &p.Schedule, &p.ScheduleTimezone, &p.WebhookURL, &paramsJSON, &tagsJSON, &p.SLADeadline, &p.SLATimezone, &depsJSON, &depRulesJSON, &p.WebhookToken, &enabled, &createdAt, &updatedAt, &p.PipelineID, &p.Source, &p.WorkspaceID, &p.OrgID, &hooksJSON, &extensionsJSON, &catchup, &parametersJSON); err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(nodesJSON), &p.Nodes); err != nil {
		return nil, fmt.Errorf("unmarshal nodes: %w", err)
	}
	if err := json.Unmarshal([]byte(edgesJSON), &p.Edges); err != nil {
		return nil, fmt.Errorf("unmarshal edges: %w", err)
	}
	if paramsJSON != "" && paramsJSON != "null" {
		json.Unmarshal([]byte(paramsJSON), &p.Params)
	}
	if tagsJSON != "" && tagsJSON != "null" {
		json.Unmarshal([]byte(tagsJSON), &p.Tags)
	}
	if depsJSON != "" && depsJSON != "null" {
		json.Unmarshal([]byte(depsJSON), &p.DependsOn)
	}
	if depRulesJSON != "" && depRulesJSON != "null" {
		json.Unmarshal([]byte(depRulesJSON), &p.DependencyRules)
	}
	if hooksJSON != "" && hooksJSON != "null" && hooksJSON != "{}" {
		if err := json.Unmarshal([]byte(hooksJSON), &p.Hooks); err != nil {
			return nil, fmt.Errorf("unmarshal hooks: %w", err)
		}
	}
	if extensionsJSON != "" && extensionsJSON != "null" && extensionsJSON != "{}" {
		if err := json.Unmarshal([]byte(extensionsJSON), &p.Extensions); err != nil {
			return nil, fmt.Errorf("unmarshal extensions: %w", err)
		}
	}
	if parametersJSON != "" && parametersJSON != "null" && parametersJSON != "{}" {
		if err := json.Unmarshal([]byte(parametersJSON), &p.Parameters); err != nil {
			return nil, fmt.Errorf("unmarshal parameters: %w", err)
		}
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if p.DependsOn == nil {
		p.DependsOn = []string{}
	}
	if p.DependencyRules == nil {
		p.DependencyRules = []models.DependencyRule{}
	}
	p.Enabled = enabled != 0
	p.Catchup = catchup != 0
	p.CreatedAt, _ = time.Parse(timeFormat, createdAt)
	p.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
	return &p, nil
}

func scanPipeline(row *sql.Row) (*models.Pipeline, error) {
	return scanPipelineFromScanner(row)
}

func scanPipelineRows(rows *sql.Rows) (*models.Pipeline, error) {
	return scanPipelineFromScanner(rows)
}

func scanRunFromScanner(sc scanner) (*models.Run, error) {
	var r models.Run
	var status string
	var startedAt, finishedAt sql.NullString
	var paramsJSON string

	var intervalStart, intervalEnd sql.NullString
	if err := sc.Scan(&r.ID, &r.PipelineID, &status, &startedAt, &finishedAt, &r.TraceID, &r.Error, &paramsJSON, &r.PipelineVersion, &r.ResumedFromRunID, &r.OrgID, &r.CancelRequested, &r.Trigger, &intervalStart, &intervalEnd); err != nil {
		return nil, err
	}
	r.DataIntervalStart = parseTimePtr(intervalStart)
	r.DataIntervalEnd = parseTimePtr(intervalEnd)
	if err := json.Unmarshal([]byte(paramsJSON), &r.Params); err != nil {
		return nil, fmt.Errorf("decode run params: %w", err)
	}
	r.Status = models.RunStatus(status)
	r.StartedAt = parseTimePtr(startedAt)
	r.FinishedAt = parseTimePtr(finishedAt)
	return &r, nil
}

func scanRun(row *sql.Row) (*models.Run, error) {
	return scanRunFromScanner(row)
}

func scanRunRows(rows *sql.Rows) (*models.Run, error) {
	return scanRunFromScanner(rows)
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func formatTimePtr(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(timeFormat), Valid: true}
}

func parseTimePtr(ns sql.NullString) *time.Time {
	if !ns.Valid {
		return nil
	}
	t, err := time.Parse(timeFormat, ns.String)
	if err != nil {
		return nil
	}
	return &t
}

func checkRowsAffected(result sql.Result, entity, id string) error {
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%s not found: %s", entity, id)
	}
	return nil
}

// ── Connections ──────────────────────────────────────────────

func (s *SQLiteStore) CreateConnection(c *models.Connection) error {
	wsID := c.WorkspaceID
	if wsID == "" {
		wsID = "default"
	}
	// Write password_ref/extra_ref (new) and password_enc/extra_enc (legacy compat).
	// If a ref is provided, store it in the ref column and also in the enc column
	// (so older code that only reads enc still works during rolling upgrades).
	passRef := c.PasswordRef
	passEnc := c.Password
	if passRef == "" && passEnc != "" {
		passRef = "encrypted://" + passEnc
	}
	extraRef := c.ExtraRef
	extraEnc := c.Extra
	if extraRef == "" && extraEnc != "" {
		extraRef = "encrypted://" + extraEnc
	}
	_, err := s.db.Exec(
		`INSERT INTO connections (id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, workspace_id, created_at, updated_at, max_concurrent)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ConnID, c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		passEnc, extraEnc, passRef, extraRef, wsID,
		c.CreatedAt.UTC().Format(timeFormat), c.UpdatedAt.UTC().Format(timeFormat), c.MaxConcurrent,
	)
	return err
}

func (s *SQLiteStore) GetConnection(connID string) (*models.Connection, error) {
	row := s.db.QueryRow(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, created_at, updated_at, max_concurrent
		 FROM connections WHERE conn_id = ?`, connID,
	)
	return scanConnection(row)
}

func (s *SQLiteStore) ListConnections() ([]models.Connection, error) {
	rows, err := s.db.Query(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, created_at, updated_at, max_concurrent
		 FROM connections ORDER BY conn_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conns []models.Connection
	for rows.Next() {
		var c models.Connection
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login,
			&c.Password, &c.Extra, &c.PasswordRef, &c.ExtraRef, &createdAt, &updatedAt, &c.MaxConcurrent); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = time.Parse(timeFormat, createdAt)
		c.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
		conns = append(conns, c)
	}
	return conns, nil
}

func (s *SQLiteStore) ListConnectionsByWorkspace(workspaceID string) ([]models.Connection, error) {
	rows, err := s.db.Query(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, created_at, updated_at, max_concurrent
		 FROM connections WHERE workspace_id = ? ORDER BY conn_id`, workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conns []models.Connection
	for rows.Next() {
		var c models.Connection
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login,
			&c.Password, &c.Extra, &c.PasswordRef, &c.ExtraRef, &createdAt, &updatedAt, &c.MaxConcurrent); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = time.Parse(timeFormat, createdAt)
		c.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
		conns = append(conns, c)
	}
	return conns, nil
}

func (s *SQLiteStore) UpdateConnection(c *models.Connection) error {
	passRef := c.PasswordRef
	passEnc := c.Password
	if passRef == "" && passEnc != "" {
		passRef = "encrypted://" + passEnc
	}
	extraRef := c.ExtraRef
	extraEnc := c.Extra
	if extraRef == "" && extraEnc != "" {
		extraRef = "encrypted://" + extraEnc
	}
	result, err := s.db.Exec(
		`UPDATE connections SET type=?, description=?, host=?, port=?, schema_name=?, login=?, password_enc=?, extra_enc=?, password_ref=?, extra_ref=?, updated_at=?, max_concurrent=?
		 WHERE conn_id = ?`,
		c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		passEnc, extraEnc, passRef, extraRef,
		c.UpdatedAt.UTC().Format(timeFormat), c.MaxConcurrent, c.ConnID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("connection not found: %s", c.ConnID)
	}
	return nil
}

func (s *SQLiteStore) DeleteConnection(connID string) error {
	result, err := s.db.Exec("DELETE FROM connections WHERE conn_id = ?", connID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("connection not found: %s", connID)
	}
	return nil
}

func scanConnection(row *sql.Row) (*models.Connection, error) {
	var c models.Connection
	var createdAt, updatedAt string
	if err := row.Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login,
		&c.Password, &c.Extra, &c.PasswordRef, &c.ExtraRef, &createdAt, &updatedAt, &c.MaxConcurrent); err != nil {
		return nil, err
	}
	c.CreatedAt, _ = time.Parse(timeFormat, createdAt)
	c.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
	return &c, nil
}

// ── Workspaces ───────────────────────────────────────────────

func (s *SQLiteStore) CreateWorkspace(w *models.Workspace) error {
	_, err := s.db.Exec(
		`INSERT INTO workspaces (id, name, slug, description, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		w.ID, w.Name, w.Slug, w.Description, w.CreatedAt.UTC().Format(timeFormat), w.UpdatedAt.UTC().Format(timeFormat),
	)
	return err
}

func (s *SQLiteStore) GetWorkspace(id string) (*models.Workspace, error) {
	var w models.Workspace
	var createdAt, updatedAt string
	err := s.db.QueryRow(`SELECT id, name, slug, description, created_at, updated_at FROM workspaces WHERE id = ?`, id).
		Scan(&w.ID, &w.Name, &w.Slug, &w.Description, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	w.CreatedAt, _ = time.Parse(timeFormat, createdAt)
	w.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
	return &w, nil
}

func (s *SQLiteStore) ListWorkspaces() ([]models.Workspace, error) {
	rows, err := s.db.Query(`SELECT id, name, slug, description, created_at, updated_at FROM workspaces ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ws []models.Workspace
	for rows.Next() {
		var w models.Workspace
		var createdAt, updatedAt string
		rows.Scan(&w.ID, &w.Name, &w.Slug, &w.Description, &createdAt, &updatedAt)
		w.CreatedAt, _ = time.Parse(timeFormat, createdAt)
		w.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
		ws = append(ws, w)
	}
	return ws, nil
}

func (s *SQLiteStore) DeleteWorkspace(id string) error {
	if id == models.DefaultWorkspaceID {
		return fmt.Errorf("cannot delete default workspace")
	}
	result, err := s.db.Exec(`DELETE FROM workspaces WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("workspace not found")
	}
	return nil
}

func (s *SQLiteStore) AddWorkspaceMember(m *models.WorkspaceMember) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO workspace_members (workspace_id, user_id, username, role, joined_at) VALUES (?, ?, ?, ?, ?)`,
		m.WorkspaceID, m.UserID, m.Username, m.Role, m.JoinedAt.UTC().Format(timeFormat),
	)
	return err
}

func (s *SQLiteStore) RemoveWorkspaceMember(workspaceID, userID string) error {
	_, err := s.db.Exec(`DELETE FROM workspace_members WHERE workspace_id = ? AND user_id = ?`, workspaceID, userID)
	return err
}

func (s *SQLiteStore) ListWorkspaceMembers(workspaceID string) ([]models.WorkspaceMember, error) {
	rows, err := s.db.Query(
		`SELECT workspace_id, user_id, username, role, joined_at FROM workspace_members WHERE workspace_id = ? ORDER BY username`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var members []models.WorkspaceMember
	for rows.Next() {
		var m models.WorkspaceMember
		var joinedAt string
		rows.Scan(&m.WorkspaceID, &m.UserID, &m.Username, &m.Role, &joinedAt)
		m.JoinedAt, _ = time.Parse(timeFormat, joinedAt)
		members = append(members, m)
	}
	return members, nil
}

func (s *SQLiteStore) GetUserWorkspaces(userID string) ([]models.Workspace, error) {
	rows, err := s.db.Query(
		`SELECT w.id, w.name, w.slug, w.description, w.created_at, w.updated_at
		 FROM workspaces w
		 JOIN workspace_members wm ON w.id = wm.workspace_id
		 WHERE wm.user_id = ?
		 ORDER BY w.name`, userID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ws []models.Workspace
	for rows.Next() {
		var w models.Workspace
		var createdAt, updatedAt string
		rows.Scan(&w.ID, &w.Name, &w.Slug, &w.Description, &createdAt, &updatedAt)
		w.CreatedAt, _ = time.Parse(timeFormat, createdAt)
		w.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
		ws = append(ws, w)
	}
	return ws, nil
}

// ── API Tokens ───────────────────────────────────────────────

func (s *SQLiteStore) CreateAPIToken(t *models.APIToken) error {
	_, err := s.db.Exec(
		`INSERT INTO api_tokens (id, name, token_hash, workspace_id, user_id, role, expires_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.TokenHash, t.WorkspaceID, t.UserID, t.Role,
		t.ExpiresAt.UTC().Format(timeFormat), t.CreatedAt.UTC().Format(timeFormat),
	)
	return err
}

func (s *SQLiteStore) GetAPITokenByHash(hash string) (*models.APIToken, error) {
	var t models.APIToken
	var expiresAt, createdAt, lastUsed string
	err := s.db.QueryRow(
		`SELECT id, name, token_hash, workspace_id, user_id, role, expires_at, created_at, last_used_at FROM api_tokens WHERE token_hash = ?`, hash,
	).Scan(&t.ID, &t.Name, &t.TokenHash, &t.WorkspaceID, &t.UserID, &t.Role, &expiresAt, &createdAt, &lastUsed)
	if err != nil {
		return nil, err
	}
	t.ExpiresAt, _ = time.Parse(timeFormat, expiresAt)
	t.CreatedAt, _ = time.Parse(timeFormat, createdAt)
	if lastUsed != "" {
		t.LastUsedAt, _ = time.Parse(timeFormat, lastUsed)
	}
	return &t, nil
}

func (s *SQLiteStore) ListAPITokens(workspaceID string) ([]models.APIToken, error) {
	rows, err := s.db.Query(
		`SELECT id, name, workspace_id, user_id, role, expires_at, created_at, last_used_at FROM api_tokens WHERE workspace_id = ? ORDER BY created_at DESC`,
		workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tokens []models.APIToken
	for rows.Next() {
		var t models.APIToken
		var expiresAt, createdAt, lastUsed string
		rows.Scan(&t.ID, &t.Name, &t.WorkspaceID, &t.UserID, &t.Role, &expiresAt, &createdAt, &lastUsed)
		t.ExpiresAt, _ = time.Parse(timeFormat, expiresAt)
		t.CreatedAt, _ = time.Parse(timeFormat, createdAt)
		if lastUsed != "" {
			t.LastUsedAt, _ = time.Parse(timeFormat, lastUsed)
		}
		tokens = append(tokens, t)
	}
	return tokens, nil
}

func (s *SQLiteStore) DeleteAPIToken(id string) error {
	_, err := s.db.Exec(`DELETE FROM api_tokens WHERE id = ?`, id)
	return err
}

// ── Calendar / Aggregation ────────────────────────────────────

func (s *SQLiteStore) GetRunCalendar(days int) ([]CalendarDay, error) {
	rows, err := s.db.Query(
		`SELECT substr(started_at, 1, 10) as day,
		        COUNT(*) as total,
		        SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
		        SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) as failed,
		        SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END) as running
		 FROM runs
		 WHERE started_at >= date('now', ?)
		 GROUP BY day ORDER BY day`,
		fmt.Sprintf("-%d days", days),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []CalendarDay
	for rows.Next() {
		var d CalendarDay
		if err := rows.Scan(&d.Date, &d.Total, &d.Success, &d.Failed, &d.Running); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, nil
}

func (s *SQLiteStore) GetRunCalendarByOrg(days int, orgID string) ([]CalendarDay, error) {
	query := `SELECT substr(started_at, 1, 10) as day,
		COUNT(*) as total,
		SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
		SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) as failed,
		SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END) as running
	 FROM runs WHERE started_at >= date('now', ?)`
	args := []interface{}{fmt.Sprintf("-%d days", days)}
	if orgID != "" {
		query += ` AND org_id = ?`
		args = append(args, orgID)
	}
	query += ` GROUP BY day ORDER BY day`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []CalendarDay
	for rows.Next() {
		var d CalendarDay
		if err := rows.Scan(&d.Date, &d.Total, &d.Success, &d.Failed, &d.Running); err != nil {
			return nil, err
		}
		result = append(result, d)
	}
	return result, nil
}

// ── Variables ────────────────────────────────────────────────

func (s *SQLiteStore) SetVariable(v *models.Variable) error {
	wsID := v.WorkspaceID
	if wsID == "" {
		wsID = "default"
	}
	_, err := s.db.Exec(
		`INSERT INTO variables (key, value, type, description, workspace_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, type=excluded.type, description=excluded.description, updated_at=excluded.updated_at`,
		v.Key, v.Value, v.Type, v.Description, wsID,
		v.CreatedAt.UTC().Format(timeFormat), v.UpdatedAt.UTC().Format(timeFormat),
	)
	return err
}

func (s *SQLiteStore) GetVariable(key string) (*models.Variable, error) {
	var v models.Variable
	var createdAt, updatedAt string
	err := s.db.QueryRow(
		`SELECT key, value, type, description, created_at, updated_at FROM variables WHERE key = ?`, key,
	).Scan(&v.Key, &v.Value, &v.Type, &v.Description, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	v.CreatedAt, _ = time.Parse(timeFormat, createdAt)
	v.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
	return &v, nil
}

func (s *SQLiteStore) ListVariables() ([]models.Variable, error) {
	rows, err := s.db.Query(
		`SELECT key, value, type, description, created_at, updated_at FROM variables ORDER BY key`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var vars []models.Variable
	for rows.Next() {
		var v models.Variable
		var createdAt, updatedAt string
		if err := rows.Scan(&v.Key, &v.Value, &v.Type, &v.Description, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(timeFormat, createdAt)
		v.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
		vars = append(vars, v)
	}
	return vars, nil
}

func (s *SQLiteStore) ListVariablesByWorkspace(workspaceID string) ([]models.Variable, error) {
	rows, err := s.db.Query(
		`SELECT key, value, type, description, created_at, updated_at FROM variables WHERE workspace_id = ? ORDER BY key`, workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vars []models.Variable
	for rows.Next() {
		var v models.Variable
		var createdAt, updatedAt string
		if err := rows.Scan(&v.Key, &v.Value, &v.Type, &v.Description, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(timeFormat, createdAt)
		v.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
		vars = append(vars, v)
	}
	return vars, nil
}

func (s *SQLiteStore) DeleteVariable(key string) error {
	result, err := s.db.Exec("DELETE FROM variables WHERE key = ?", key)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("variable not found: %s", key)
	}
	return nil
}

// --- Alerts ---

func (s *SQLiteStore) CreateAlert(a *models.Alert) error {
	_, err := s.db.Exec(
		`INSERT INTO alerts (id, org_id, kind, severity, title, body, pipeline_id, pipeline_name, run_id, created_at, read_at, dismissed_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.OrgID, a.Kind, a.Severity, a.Title, a.Body,
		a.PipelineID, a.PipelineName, a.RunID,
		a.CreatedAt.UTC().Format(timeFormat),
		nullableTime(a.ReadAt), nullableTime(a.DismissedAt),
	)
	return wrapStoreErr("CreateAlert", a.ID, err)
}

// --- Task bundles (ADR-031) ---

// PutTaskBundle stores an archive content-addressed under digest.
// Callers pass a digest the bytes actually hash to; a mismatch is a
// caller bug and is refused here rather than papered over. The insert is
// race-safe on both the SQLite and Postgres backends: INSERT ... ON
// CONFLICT DO NOTHING, then read back — a concurrent identical upload
// reports created=false, a concurrent colliding one reports
// ErrTaskBundleCollision. Never an update; the archive is immutable.
func (s *SQLiteStore) PutTaskBundle(orgID, digest string, archive []byte) (bool, error) {
	if len(archive) > taskbundle.MaxArchiveBytes {
		return false, fmt.Errorf("task bundle archive %d bytes exceeds the %d-byte cap", len(archive), int64(taskbundle.MaxArchiveBytes))
	}
	if taskbundle.DigestOf(archive) != digest {
		return false, fmt.Errorf("task bundle digest %q does not match the archive's actual digest %q", digest, taskbundle.DigestOf(archive))
	}
	now := time.Now().UTC().Format(timeFormat)
	res, err := s.db.Exec(
		`INSERT INTO task_bundles (org_id, digest, size_bytes, archive, created_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (org_id, digest) DO NOTHING`,
		orgID, digest, len(archive), archive, now,
	)
	if err != nil {
		return false, wrapStoreErr("PutTaskBundle", digest, err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return true, nil
	}
	existing, err := s.GetTaskBundle(orgID, digest)
	if err != nil {
		return false, wrapStoreErr("PutTaskBundle", digest, err)
	}
	if !bytes.Equal(existing, archive) {
		return false, ErrTaskBundleCollision
	}
	return false, nil
}

// GetTaskBundle returns the stored archive for orgID under digest.
func (s *SQLiteStore) GetTaskBundle(orgID, digest string) ([]byte, error) {
	var archive []byte
	err := s.db.QueryRow(
		`SELECT archive FROM task_bundles WHERE org_id = ? AND digest = ?`, orgID, digest,
	).Scan(&archive)
	if err == sql.ErrNoRows {
		return nil, ErrTaskBundleNotFound
	}
	if err != nil {
		return nil, wrapStoreErr("GetTaskBundle", digest, err)
	}
	return archive, nil
}

// --- Task bundles v2 (ADR-033) ---
// Same shape and race-safety as the task_bundles methods above, against
// the separate task_bundles_v2 table.

// PutTaskBundleV2 stores an archive content-addressed under digest.
func (s *SQLiteStore) PutTaskBundleV2(orgID, digest string, archive []byte) (bool, error) {
	if len(archive) > taskbundlev2.MaxArchiveBytes {
		return false, fmt.Errorf("task bundle archive %d bytes exceeds the %d-byte cap", len(archive), int64(taskbundlev2.MaxArchiveBytes))
	}
	if taskbundlev2.DigestOf(archive) != digest {
		return false, fmt.Errorf("task bundle digest %q does not match the archive's actual digest %q", digest, taskbundlev2.DigestOf(archive))
	}
	now := time.Now().UTC().Format(timeFormat)
	res, err := s.db.Exec(
		`INSERT INTO task_bundles_v2 (org_id, digest, size_bytes, archive, created_at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT (org_id, digest) DO NOTHING`,
		orgID, digest, len(archive), archive, now,
	)
	if err != nil {
		return false, wrapStoreErr("PutTaskBundleV2", digest, err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return true, nil
	}
	existing, err := s.GetTaskBundleV2(orgID, digest)
	if err != nil {
		return false, wrapStoreErr("PutTaskBundleV2", digest, err)
	}
	if !bytes.Equal(existing, archive) {
		return false, ErrTaskBundleV2Collision
	}
	return false, nil
}

// GetTaskBundleV2 returns the stored archive for orgID under digest.
func (s *SQLiteStore) GetTaskBundleV2(orgID, digest string) ([]byte, error) {
	var archive []byte
	err := s.db.QueryRow(
		`SELECT archive FROM task_bundles_v2 WHERE org_id = ? AND digest = ?`, orgID, digest,
	).Scan(&archive)
	if err == sql.ErrNoRows {
		return nil, ErrTaskBundleV2NotFound
	}
	if err != nil {
		return nil, wrapStoreErr("GetTaskBundleV2", digest, err)
	}
	return archive, nil
}

// --- Resolved execution records (ADR-033 section 4) ---

// PutResolvedExecutionRecord pins record for (runID, nodeID), the first
// time only. Race-safe the same way PutTaskBundleV2 is: INSERT ... ON
// CONFLICT DO NOTHING, then read back — a concurrent identical
// resolution reports created=false, a concurrent DIFFERENT one reports
// ErrResolvedExecutionRecordConflict.
func (s *SQLiteStore) PutResolvedExecutionRecord(runID, nodeID string, record *taskruntime.ResolvedExecutionRecord) (bool, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return false, fmt.Errorf("marshal resolved execution record: %w", err)
	}
	now := time.Now().UTC().Format(timeFormat)
	res, err := s.db.Exec(
		`INSERT INTO resolved_execution_records (run_id, node_id, record_json, created_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT (run_id, node_id) DO NOTHING`,
		runID, nodeID, string(raw), now,
	)
	if err != nil {
		return false, wrapStoreErr("PutResolvedExecutionRecord", nodeID, err)
	}
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		return true, nil
	}
	existing, err := s.GetResolvedExecutionRecord(runID, nodeID)
	if err != nil {
		return false, wrapStoreErr("PutResolvedExecutionRecord", nodeID, err)
	}
	if !reflect.DeepEqual(existing, record) {
		return false, ErrResolvedExecutionRecordConflict
	}
	return false, nil
}

// GetResolvedExecutionRecord returns the pinned record for (runID, nodeID).
func (s *SQLiteStore) GetResolvedExecutionRecord(runID, nodeID string) (*taskruntime.ResolvedExecutionRecord, error) {
	var raw string
	err := s.db.QueryRow(
		`SELECT record_json FROM resolved_execution_records WHERE run_id = ? AND node_id = ?`, runID, nodeID,
	).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, ErrResolvedExecutionRecordNotFound
	}
	if err != nil {
		return nil, wrapStoreErr("GetResolvedExecutionRecord", nodeID, err)
	}
	var record taskruntime.ResolvedExecutionRecord
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		return nil, fmt.Errorf("unmarshal resolved execution record for node %s: %w", nodeID, err)
	}
	return &record, nil
}

// ListAlerts returns an org's alerts newest-first. Dismissed alerts are
// always excluded — dismissal is the user saying "stop showing me this" —
// but the rows are retained rather than deleted so a dismissal remains
// auditable.
func (s *SQLiteStore) ListAlerts(orgID string, unreadOnly bool, limit int) ([]models.Alert, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, org_id, kind, severity, title, body, pipeline_id, pipeline_name, run_id, created_at, read_at, dismissed_at
		FROM alerts WHERE org_id = ? AND dismissed_at IS NULL`
	if unreadOnly {
		query += " AND read_at IS NULL"
	}
	query += " ORDER BY created_at DESC LIMIT ?"

	rows, err := s.db.Query(query, orgID, limit)
	if err != nil {
		return nil, wrapStoreErr("ListAlerts", orgID, err)
	}
	defer rows.Close()

	var out []models.Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, wrapStoreErr("ListAlerts", orgID, err)
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) CountUnreadAlerts(orgID string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM alerts WHERE org_id = ? AND read_at IS NULL AND dismissed_at IS NULL`, orgID,
	).Scan(&n)
	return n, wrapStoreErr("CountUnreadAlerts", orgID, err)
}

// MarkAlertRead is a no-op on an already-read alert, but reports an error
// when the alert doesn't exist *for this org* — the org predicate is what
// stops one tenant marking another tenant's alerts read.
func (s *SQLiteStore) MarkAlertRead(orgID, id string) error {
	result, err := s.db.Exec(
		`UPDATE alerts SET read_at = ? WHERE id = ? AND org_id = ? AND read_at IS NULL`,
		time.Now().UTC().Format(timeFormat), id, orgID,
	)
	if err != nil {
		return wrapStoreErr("MarkAlertRead", id, err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		// Either already read (benign) or not ours (must not 200). Tell
		// them apart with an existence check scoped to the same org.
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE id = ? AND org_id = ?`, id, orgID).Scan(&exists); err != nil {
			return wrapStoreErr("MarkAlertRead", id, err)
		}
		if exists == 0 {
			return wrapStoreErr("MarkAlertRead", id, fmt.Errorf("alert not found"))
		}
	}
	return nil
}

func (s *SQLiteStore) MarkAllAlertsRead(orgID string) error {
	_, err := s.db.Exec(
		`UPDATE alerts SET read_at = ? WHERE org_id = ? AND read_at IS NULL`,
		time.Now().UTC().Format(timeFormat), orgID,
	)
	return wrapStoreErr("MarkAllAlertsRead", orgID, err)
}

func (s *SQLiteStore) DismissAlert(orgID, id string) error {
	result, err := s.db.Exec(
		`UPDATE alerts SET dismissed_at = ? WHERE id = ? AND org_id = ? AND dismissed_at IS NULL`,
		time.Now().UTC().Format(timeFormat), id, orgID,
	)
	if err != nil {
		return wrapStoreErr("DismissAlert", id, err)
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return wrapStoreErr("DismissAlert", id, fmt.Errorf("alert not found"))
	}
	return nil
}

func scanAlert(sc scanner) (*models.Alert, error) {
	var a models.Alert
	var createdAt string
	var readAt, dismissedAt sql.NullString
	if err := sc.Scan(&a.ID, &a.OrgID, &a.Kind, &a.Severity, &a.Title, &a.Body,
		&a.PipelineID, &a.PipelineName, &a.RunID, &createdAt, &readAt, &dismissedAt); err != nil {
		return nil, err
	}
	a.CreatedAt, _ = time.Parse(timeFormat, createdAt)
	if readAt.Valid && readAt.String != "" {
		if t, err := time.Parse(timeFormat, readAt.String); err == nil {
			a.ReadAt = &t
		}
	}
	if dismissedAt.Valid && dismissedAt.String != "" {
		if t, err := time.Parse(timeFormat, dismissedAt.String); err == nil {
			a.DismissedAt = &t
		}
	}
	return &a, nil
}

// nullableTime renders an optional timestamp for a nullable TEXT column.
func nullableTime(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return t.UTC().Format(timeFormat)
}

// ListDLQByOrg is the org-wide counterpart to ListDLQ. Entries carry no
// org_id of their own, so scope is derived by joining through the owning
// pipeline — which also lets us return the pipeline name for display.
func (s *SQLiteStore) ListDLQByOrg(orgID string, includeResolved bool, limit int) ([]DLQEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT d.id, d.pipeline_id, d.run_id, d.error, d.node_id, d.node_name, d.payload,
		d.created_at, d.resolved, COALESCE(d.resolved_at,''), COALESCE(p.name,'')
		FROM dead_letter_queue d JOIN pipelines p ON p.id = d.pipeline_id
		WHERE p.org_id = ?`
	if !includeResolved {
		query += " AND d.resolved = 0"
	}
	query += " ORDER BY d.created_at DESC LIMIT ?"

	rows, err := s.db.Query(query, orgID, limit)
	if err != nil {
		return nil, wrapStoreErr("ListDLQByOrg", orgID, err)
	}
	defer rows.Close()

	var entries []DLQEntry
	for rows.Next() {
		var e DLQEntry
		var resolved int
		if err := rows.Scan(&e.ID, &e.PipelineID, &e.RunID, &e.Error, &e.NodeID, &e.NodeName,
			&e.Payload, &e.CreatedAt, &resolved, &e.ResolvedAt, &e.PipelineName); err != nil {
			return nil, wrapStoreErr("ListDLQByOrg", orgID, err)
		}
		e.Resolved = resolved != 0
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- Pipeline Templates ---

func (s *SQLiteStore) CreatePipelineTemplate(t *models.PipelineTemplate) error {
	nodesJSON, err := json.Marshal(t.Nodes)
	if err != nil {
		return wrapStoreErr("CreatePipelineTemplate", t.ID, fmt.Errorf("marshal nodes: %w", err))
	}
	edgesJSON, err := json.Marshal(t.Edges)
	if err != nil {
		return wrapStoreErr("CreatePipelineTemplate", t.ID, fmt.Errorf("marshal edges: %w", err))
	}
	_, err = s.db.Exec(
		`INSERT INTO pipeline_templates (id, name, description, icon, nodes, edges, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.Name, t.Description, t.Icon, string(nodesJSON), string(edgesJSON),
		t.CreatedAt.UTC().Format(timeFormat), t.UpdatedAt.UTC().Format(timeFormat),
	)
	return wrapStoreErr("CreatePipelineTemplate", t.ID, err)
}

func (s *SQLiteStore) GetPipelineTemplate(id string) (*models.PipelineTemplate, error) {
	row := s.db.QueryRow(
		`SELECT id, name, description, icon, nodes, edges, created_at, updated_at FROM pipeline_templates WHERE id = ?`, id,
	)
	t, err := scanPipelineTemplate(row)
	if err != nil {
		return nil, wrapStoreErr("GetPipelineTemplate", id, err)
	}
	return t, nil
}

func (s *SQLiteStore) ListPipelineTemplates() ([]models.PipelineTemplate, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, icon, nodes, edges, created_at, updated_at FROM pipeline_templates ORDER BY created_at`,
	)
	if err != nil {
		return nil, wrapStoreErr("ListPipelineTemplates", "", err)
	}
	defer rows.Close()

	var out []models.PipelineTemplate
	for rows.Next() {
		t, err := scanPipelineTemplate(rows)
		if err != nil {
			return nil, wrapStoreErr("ListPipelineTemplates", "", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpdatePipelineTemplate(t *models.PipelineTemplate) error {
	nodesJSON, err := json.Marshal(t.Nodes)
	if err != nil {
		return wrapStoreErr("UpdatePipelineTemplate", t.ID, fmt.Errorf("marshal nodes: %w", err))
	}
	edgesJSON, err := json.Marshal(t.Edges)
	if err != nil {
		return wrapStoreErr("UpdatePipelineTemplate", t.ID, fmt.Errorf("marshal edges: %w", err))
	}
	result, err := s.db.Exec(
		`UPDATE pipeline_templates SET name = ?, description = ?, icon = ?, nodes = ?, edges = ?, updated_at = ? WHERE id = ?`,
		t.Name, t.Description, t.Icon, string(nodesJSON), string(edgesJSON), t.UpdatedAt.UTC().Format(timeFormat), t.ID,
	)
	if err != nil {
		return wrapStoreErr("UpdatePipelineTemplate", t.ID, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return wrapStoreErr("UpdatePipelineTemplate", t.ID, fmt.Errorf("template not found"))
	}
	return nil
}

func (s *SQLiteStore) DeletePipelineTemplate(id string) error {
	result, err := s.db.Exec(`DELETE FROM pipeline_templates WHERE id = ?`, id)
	if err != nil {
		return wrapStoreErr("DeletePipelineTemplate", id, err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return wrapStoreErr("DeletePipelineTemplate", id, fmt.Errorf("template not found"))
	}
	return nil
}

func scanPipelineTemplate(sc scanner) (*models.PipelineTemplate, error) {
	var t models.PipelineTemplate
	var nodesJSON, edgesJSON, createdAt, updatedAt string
	if err := sc.Scan(&t.ID, &t.Name, &t.Description, &t.Icon, &nodesJSON, &edgesJSON, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(nodesJSON), &t.Nodes); err != nil {
		return nil, fmt.Errorf("decode nodes: %w", err)
	}
	if err := json.Unmarshal([]byte(edgesJSON), &t.Edges); err != nil {
		return nil, fmt.Errorf("decode edges: %w", err)
	}
	t.CreatedAt, _ = time.Parse(timeFormat, createdAt)
	t.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
	return &t, nil
}

// --- Roles ---

func (s *SQLiteStore) CreateRole(r *models.Role) error {
	permsJSON, err := json.Marshal(r.Permissions)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO roles (id, name, description, permissions, is_system, created_at) VALUES (?,?,?,?,?,?)`,
		r.ID, r.Name, r.Description, string(permsJSON), boolToInt(r.IsSystem), r.CreatedAt,
	)
	return err
}

func (s *SQLiteStore) GetRole(id string) (*models.Role, error) {
	var r models.Role
	var permsJSON string
	var isSystem int
	err := s.db.QueryRow(
		`SELECT id, name, description, permissions, is_system, created_at FROM roles WHERE id = ?`, id,
	).Scan(&r.ID, &r.Name, &r.Description, &permsJSON, &isSystem, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	r.IsSystem = isSystem != 0
	if err := json.Unmarshal([]byte(permsJSON), &r.Permissions); err != nil {
		return nil, fmt.Errorf("unmarshal permissions: %w", err)
	}
	return &r, nil
}

func (s *SQLiteStore) ListRoles() ([]models.Role, error) {
	rows, err := s.db.Query(`SELECT id, name, description, permissions, is_system, created_at FROM roles ORDER BY is_system DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []models.Role
	for rows.Next() {
		var r models.Role
		var permsJSON string
		var isSystem int
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &permsJSON, &isSystem, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.IsSystem = isSystem != 0
		json.Unmarshal([]byte(permsJSON), &r.Permissions)
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

func (s *SQLiteStore) UpdateRole(r *models.Role) error {
	permsJSON, err := json.Marshal(r.Permissions)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}
	result, err := s.db.Exec(
		`UPDATE roles SET name=?, description=?, permissions=? WHERE id=? AND is_system=0`,
		r.Name, r.Description, string(permsJSON), r.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		// Check if it exists at all
		var exists int
		s.db.QueryRow("SELECT COUNT(*) FROM roles WHERE id=?", r.ID).Scan(&exists)
		if exists > 0 {
			return fmt.Errorf("cannot modify system role")
		}
		return fmt.Errorf("role not found: %s", r.ID)
	}
	return nil
}

func (s *SQLiteStore) DeleteRole(id string) error {
	result, err := s.db.Exec("DELETE FROM roles WHERE id = ? AND is_system = 0", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		var exists int
		s.db.QueryRow("SELECT COUNT(*) FROM roles WHERE id=?", id).Scan(&exists)
		if exists > 0 {
			return fmt.Errorf("cannot delete system role")
		}
		return fmt.Errorf("role not found: %s", id)
	}
	return nil
}

// --- Physical plans (ADR-015, #90 M2) ---

func (s *SQLiteStore) SaveRunPlan(runID, planJSON string) error {
	_, err := s.db.Exec(
		`INSERT INTO run_physical_plans (run_id, plan, created_at) VALUES (?, ?, ?)
		 ON CONFLICT(run_id) DO UPDATE SET plan = excluded.plan`,
		runID, planJSON, time.Now().UTC().Format(timeFormat),
	)
	return wrapStoreErr("SaveRunPlan", runID, err)
}

func (s *SQLiteStore) GetRunPlan(runID string) (string, error) {
	var plan string
	err := s.db.QueryRow(`SELECT plan FROM run_physical_plans WHERE run_id = ?`, runID).Scan(&plan)
	if err != nil {
		return "", wrapStoreErr("GetRunPlan", runID, err)
	}
	return plan, nil
}

func (s *SQLiteStore) SavePhysicalInstances(runID string, instances []models.PhysicalInstance) error {
	if len(instances) == 0 {
		return nil
	}
	return s.WithTx(func(tx *sql.Tx) error {
		for _, in := range instances {
			var startedAt interface{}
			if in.StartedAt != nil {
				startedAt = in.StartedAt.UTC().Format(timeFormat)
			}
			_, err := tx.Exec(
				`INSERT INTO physical_instances
				 (run_id, instance_key, logical_node_id, kind, idx, status, row_count, started_at, duration_ms, error, attempt)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT(run_id, instance_key) DO UPDATE SET
				   logical_node_id=excluded.logical_node_id, kind=excluded.kind, idx=excluded.idx,
				   status=excluded.status, row_count=excluded.row_count, started_at=excluded.started_at,
				   duration_ms=excluded.duration_ms, error=excluded.error, attempt=excluded.attempt`,
				runID, in.InstanceKey, in.LogicalNodeID, string(in.Kind), in.Index, string(in.Status),
				in.RowCount, startedAt, in.DurationMs, in.Error, in.Attempt,
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *SQLiteStore) ListPhysicalInstances(runID string) ([]models.PhysicalInstance, error) {
	rows, err := s.db.Query(
		`SELECT logical_node_id, kind, instance_key, idx, status, row_count, started_at, duration_ms, error, attempt
		 FROM physical_instances WHERE run_id = ? ORDER BY logical_node_id, idx`, runID)
	if err != nil {
		return nil, wrapStoreErr("ListPhysicalInstances", runID, err)
	}
	defer rows.Close()
	var out []models.PhysicalInstance
	for rows.Next() {
		var in models.PhysicalInstance
		var kind, status string
		var startedAt sql.NullString
		if err := rows.Scan(&in.LogicalNodeID, &kind, &in.InstanceKey, &in.Index, &status,
			&in.RowCount, &startedAt, &in.DurationMs, &in.Error, &in.Attempt); err != nil {
			return nil, wrapStoreErr("ListPhysicalInstances", runID, err)
		}
		in.Kind = models.WorkUnitKind(kind)
		in.Status = models.RunStatus(status)
		in.StartedAt = parseTimePtr(startedAt)
		out = append(out, in)
	}
	return out, nil
}
