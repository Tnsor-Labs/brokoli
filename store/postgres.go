package store

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/templates"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/001_initial_pg.sql
var pgMigrationsFS embed.FS

// PostgresStore implements Store using PostgreSQL.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore connects to Postgres and runs migrations.
func NewPostgresStore(uri string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", uri)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	s := &PostgresStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *PostgresStore) migrate() error {
	migration, err := pgMigrationsFS.ReadFile("migrations/001_initial_pg.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	if _, err := s.db.Exec(string(migration)); err != nil {
		return err
	}

	// Login attempts tracking (account lockout)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS login_attempts (
		id SERIAL PRIMARY KEY,
		username TEXT NOT NULL,
		ip TEXT NOT NULL DEFAULT '',
		success BOOLEAN NOT NULL DEFAULT FALSE,
		attempted_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_login_attempts ON login_attempts(username, attempted_at DESC)`)

	// Roles table
	s.db.Exec(`CREATE TABLE IF NOT EXISTS roles (
		id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT NOT NULL DEFAULT '',
		permissions JSONB NOT NULL DEFAULT '[]', is_system BOOLEAN NOT NULL DEFAULT FALSE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`)

	// Seed default roles on first run
	var roleCount int
	s.db.QueryRow("SELECT COUNT(*) FROM roles").Scan(&roleCount)
	if roleCount == 0 {
		for _, role := range models.DefaultRoles() {
			permsJSON, _ := json.Marshal(role.Permissions)
			s.db.Exec("INSERT INTO roles (id, name, description, permissions, is_system, created_at) VALUES ($1,$2,$3,$4,$5,NOW())",
				role.ID, role.Name, role.Description, string(permsJSON), role.IsSystem)
		}
	}

	// Pipeline schema additions (safe to re-run — errors ignored for existing columns)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS schedule_timezone TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS sla_deadline TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS sla_timezone TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS depends_on JSONB NOT NULL DEFAULT '[]'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS dependency_rules JSONB NOT NULL DEFAULT '[]'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS webhook_token TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS pipeline_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'ui'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT 'default'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS org_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_pid ON pipelines(pipeline_id) WHERE pipeline_id != ''`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_pipelines_workspace ON pipelines(workspace_id)`)

	// Dead letter queue
	s.db.Exec(`CREATE TABLE IF NOT EXISTS dead_letter_queue (
		id TEXT PRIMARY KEY,
		pipeline_id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		error TEXT NOT NULL,
		node_id TEXT NOT NULL DEFAULT '',
		node_name TEXT NOT NULL DEFAULT '',
		payload TEXT NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		resolved BOOLEAN NOT NULL DEFAULT FALSE,
		resolved_at TIMESTAMPTZ,
		FOREIGN KEY (pipeline_id) REFERENCES pipelines(id) ON DELETE CASCADE)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_dlq_pipeline ON dead_letter_queue(pipeline_id, resolved, created_at DESC)`)

	// Connections table
	s.db.Exec(`CREATE TABLE IF NOT EXISTS connections (
		id TEXT PRIMARY KEY,
		conn_id TEXT NOT NULL UNIQUE,
		type TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		host TEXT NOT NULL DEFAULT '',
		port INTEGER NOT NULL DEFAULT 0,
		schema_name TEXT NOT NULL DEFAULT '',
		login TEXT NOT NULL DEFAULT '',
		password_enc TEXT NOT NULL DEFAULT '',
		extra_enc TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT 'default',
		org_id TEXT NOT NULL DEFAULT 'default'
	)`)
	s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_connections_conn_id ON connections(conn_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_connections_workspace ON connections(workspace_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_connections_org ON connections(org_id)`)
	s.db.Exec(`ALTER TABLE connections ADD COLUMN IF NOT EXISTS workspace_id TEXT NOT NULL DEFAULT 'default'`)
	s.db.Exec(`ALTER TABLE connections ADD COLUMN IF NOT EXISTS org_id TEXT NOT NULL DEFAULT 'default'`)
	// Credential references — external secret store pointers
	s.db.Exec(`ALTER TABLE connections ADD COLUMN IF NOT EXISTS password_ref TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE connections ADD COLUMN IF NOT EXISTS extra_ref TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`UPDATE connections SET password_ref = 'encrypted://' || password_enc WHERE password_enc != '' AND password_ref = ''`)
	s.db.Exec(`UPDATE connections SET extra_ref = 'encrypted://' || extra_enc WHERE extra_enc != '' AND extra_ref = ''`)

	// Variables table
	s.db.Exec(`CREATE TABLE IF NOT EXISTS variables (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT '',
		type TEXT NOT NULL DEFAULT 'string',
		description TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL,
		workspace_id TEXT NOT NULL DEFAULT 'default'
	)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_variables_workspace ON variables(workspace_id)`)

	// Workspaces + related tables
	s.db.Exec(`CREATE TABLE IF NOT EXISTS workspaces (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		slug TEXT NOT NULL UNIQUE,
		description TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS workspace_members (
		workspace_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		username TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL DEFAULT 'viewer',
		joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (workspace_id, user_id),
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
	)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS permissions (
		id TEXT PRIMARY KEY,
		workspace_id TEXT NOT NULL,
		user_id TEXT NOT NULL,
		resource TEXT NOT NULL DEFAULT '*',
		resource_id TEXT NOT NULL DEFAULT '*',
		action TEXT NOT NULL DEFAULT '*',
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
	)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS api_tokens (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		token_hash TEXT NOT NULL UNIQUE,
		workspace_id TEXT NOT NULL DEFAULT 'default',
		user_id TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'editor',
		expires_at TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_used_at TIMESTAMPTZ,
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
	)`)
	s.db.Exec(`CREATE TABLE IF NOT EXISTS oidc_group_mappings (
		oidc_group TEXT NOT NULL,
		workspace_id TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT 'viewer',
		PRIMARY KEY (oidc_group, workspace_id),
		FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE
	)`)

	// Settings key-value store
	s.db.Exec(`CREATE TABLE IF NOT EXISTS settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL DEFAULT ''
	)`)

	// Node profiles table
	s.db.Exec(`CREATE TABLE IF NOT EXISTS node_profiles (
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		profile JSONB NOT NULL DEFAULT '{}',
		schema_snapshot JSONB NOT NULL DEFAULT '{}',
		drift_alerts JSONB NOT NULL DEFAULT '[]',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (run_id, node_id),
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
	)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_node_profiles ON node_profiles(run_id)`)

	// Runs table additions
	s.db.Exec(`ALTER TABLE runs ADD COLUMN IF NOT EXISTS org_id TEXT NOT NULL DEFAULT 'default'`)
	s.db.Exec(`ALTER TABLE runs ADD COLUMN IF NOT EXISTS error TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE runs ADD COLUMN IF NOT EXISTS params JSONB NOT NULL DEFAULT '{}'`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_org ON runs(org_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_pipeline_status ON runs(pipeline_id, status, started_at DESC)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_node_runs_run_status ON node_runs(run_id, status)`)

	// Pipelines tags column
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS tags JSONB NOT NULL DEFAULT '[]'`)

	// Default workspace seed
	s.db.Exec(`INSERT INTO workspaces (id, name, slug, description, created_at, updated_at)
		VALUES ('default', 'Default', 'default', 'Default workspace', NOW(), NOW())
		ON CONFLICT (id) DO NOTHING`)

	// Tracing & observability columns
	s.db.Exec(`ALTER TABLE runs ADD COLUMN IF NOT EXISTS trace_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN IF NOT EXISTS attempt INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN IF NOT EXISTS ready_at TIMESTAMPTZ`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN IF NOT EXISTS queue_ms BIGINT NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN IF NOT EXISTS rows_per_sec REAL NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN IF NOT EXISTS trace_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE node_runs ADD COLUMN IF NOT EXISTS span_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE logs ADD COLUMN IF NOT EXISTS trace_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE logs ADD COLUMN IF NOT EXISTS span_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE logs ADD COLUMN IF NOT EXISTS attempt INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE logs ADD COLUMN IF NOT EXISTS metadata TEXT NOT NULL DEFAULT '{}'`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_node_runs_node_pipeline ON node_runs(node_id, run_id)`)

	// Run event log — immutable append-only log of run/node-attempt lifecycle
	// transitions (issue #6). See store/migrations/009_run_events_pg.sql for
	// the documented schema; like the rest of this function's DDL it is
	// applied idempotently here rather than through a tracked migration file.
	s.db.Exec(`CREATE TABLE IF NOT EXISTS run_events (
		id BIGSERIAL PRIMARY KEY,
		run_id TEXT NOT NULL,
		node_id TEXT,
		attempt INTEGER,
		event_type TEXT NOT NULL,
		payload JSONB NOT NULL DEFAULT '{}',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		schema_version INTEGER NOT NULL DEFAULT 1,
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_run_events_run ON run_events(run_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_run_events_run_node ON run_events(run_id, node_id)`)

	// Execution attempts — durable outbox/intent + claim/lease record for one
	// (run_id, node_id, attempt) unit of dispatchable work (issue #7). See
	// the matching table in store/sqlite.go for the full doc comment; kept
	// in sync column-for-column between backends.
	s.db.Exec(`CREATE TABLE IF NOT EXISTS execution_attempts (
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL DEFAULT '',
		attempt INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'queued',
		claimed_by TEXT NOT NULL DEFAULT '',
		lease_expires_at TIMESTAMPTZ,
		fencing_generation BIGINT NOT NULL DEFAULT 0,
		idempotency_key TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (run_id, node_id, attempt),
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_execution_attempts_lease ON execution_attempts(status, lease_expires_at)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_execution_attempts_idempotency ON execution_attempts(idempotency_key)`)

	// pipeline_versions unique index — see the matching comment in
	// store/sqlite.go for why issue #8 makes this race worth guarding now.
	s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_pipeline_versions_unique ON pipeline_versions(pipeline_id, version)`)

	// Run definition snapshot + resume lineage (issue #8). See the matching
	// columns in store/sqlite.go for the full doc comment; kept in sync
	// column-for-column between backends.
	s.db.Exec(`ALTER TABLE runs ADD COLUMN IF NOT EXISTS pipeline_version INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE runs ADD COLUMN IF NOT EXISTS resumed_from_run_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_resumed_from ON runs(resumed_from_run_id) WHERE resumed_from_run_id != ''`)

	// Scheduler leadership — single-row lease + fencing-generation table
	// backing PostgresLeaderElector (Tnsor-Labs/brokoli#10). See the design
	// note atop store/postgres_leader.go for why this is a leased row
	// rather than a session-scoped pg_advisory_lock. Postgres-only: SQLite
	// is documented as single-instance-only (cmd/serve.go warns at
	// startup), so SQLiteStore never creates or touches this table. See
	// store/migrations/010_scheduler_leader_pg.sql for the documented
	// schema (not itself read at runtime, matching the run_events/
	// execution_attempts precedent above).
	s.db.Exec(`CREATE TABLE IF NOT EXISTS scheduler_leader (
		id TEXT PRIMARY KEY,
		holder TEXT NOT NULL DEFAULT '',
		fencing_generation BIGINT NOT NULL DEFAULT 0,
		lease_expires_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
		acquired_at TIMESTAMPTZ,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`)

	// Expansion instances — durable per-item execution record for a
	// dynamic-expansion `code` node (issue #31). See the matching table in
	// store/sqlite.go for the full doc comment; kept in sync column-for-
	// column between backends.
	s.db.Exec(`CREATE TABLE IF NOT EXISTS expansion_instances (
		id TEXT PRIMARY KEY,
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		node_attempt INTEGER NOT NULL DEFAULT 0,
		instance_index INTEGER NOT NULL,
		instance_key TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'running',
		row_count INTEGER NOT NULL DEFAULT 0,
		started_at TIMESTAMPTZ,
		duration_ms BIGINT NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_expansion_instances_run_node ON expansion_instances(run_id, node_id, node_attempt)`)

	// Backfill: runs.org_id has existed in Postgres since its own org_id
	// migration above, but CreateRun never actually set it until this fix
	// (Tnsor-Labs/brokoli#50) — every run ever created here still has the
	// column's bare 'default', regardless of which org the owning
	// pipeline actually belonged to. Every run created going forward
	// carries a real value from Runner.orgID (== the pipeline's own
	// OrgID at creation time), so this only ever touches pre-fix rows.
	// Orphaned runs (pipeline since deleted) keep their existing value:
	// the subquery returns NULL and COALESCE falls back rather than
	// clobbering them with a guess.
	s.db.Exec(`UPDATE runs SET org_id = COALESCE((SELECT p.org_id FROM pipelines p WHERE p.id = runs.pipeline_id), org_id) WHERE org_id = 'default'`)

	// Alerts — see the matching table in store/sqlite.go for the full doc
	// comment; kept in sync column-for-column between backends
	// (TIMESTAMPTZ here instead of TEXT, matching every other table's
	// dialect split).
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
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		read_at TIMESTAMPTZ,
		dismissed_at TIMESTAMPTZ)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_alerts_org ON alerts(org_id, created_at DESC)`)

	// Pipeline templates — see the matching table in store/sqlite.go for
	// the full doc comment; kept in sync column-for-column between
	// backends (JSONB here instead of TEXT, matching pipelines.nodes/
	// edges' own dialect split).
	s.db.Exec(`CREATE TABLE IF NOT EXISTS pipeline_templates (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		icon TEXT NOT NULL DEFAULT '',
		nodes JSONB NOT NULL DEFAULT '[]',
		edges JSONB NOT NULL DEFAULT '[]',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`)
	if err := s.seedPipelineTemplates(); err != nil {
		return fmt.Errorf("seed pipeline templates: %w", err)
	}

	return nil
}

// seedPipelineTemplates — see the matching SQLite method for why this
// only ever inserts into an empty table, never overwrites.
func (s *PostgresStore) seedPipelineTemplates() error {
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

// --- Login Attempts ---

func (s *PostgresStore) RecordLoginAttempt(username, ip string, success bool) error {
	_, err := s.db.Exec(
		`INSERT INTO login_attempts (username, ip, success, attempted_at) VALUES ($1, $2, $3, NOW())`,
		username, ip, success,
	)
	return err
}

func (s *PostgresStore) GetRecentFailedAttempts(username string, since time.Time) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM login_attempts WHERE username = $1 AND success = false AND attempted_at > $2`,
		username, since,
	).Scan(&count)
	return count, err
}

func (s *PostgresStore) ClearLoginAttempts(username string) error {
	_, err := s.db.Exec(`DELETE FROM login_attempts WHERE username = $1`, username)
	return err
}

// WithTx executes a function within a database transaction.
func (s *PostgresStore) WithTx(fn func(*sql.Tx) error) error {
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

func (s *PostgresStore) AddToDLQ(pipelineID, runID, nodeID, nodeName, errMsg, payload string) error {
	id := common.NewID()
	_, err := s.db.Exec(
		`INSERT INTO dead_letter_queue (id, pipeline_id, run_id, error, node_id, node_name, payload, created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		id, pipelineID, runID, errMsg, nodeID, nodeName, payload, time.Now(),
	)
	return err
}

func (s *PostgresStore) ListDLQ(pipelineID string, includeResolved bool, limit int) ([]DLQEntry, error) {
	query := `SELECT id, pipeline_id, run_id, error, node_id, node_name, payload, created_at, resolved, COALESCE(resolved_at::text,'') FROM dead_letter_queue WHERE pipeline_id = $1`
	if !includeResolved {
		query += " AND resolved = false"
	}
	query += " ORDER BY created_at DESC LIMIT $2"
	rows, err := s.db.Query(query, pipelineID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []DLQEntry
	for rows.Next() {
		var e DLQEntry
		if err := rows.Scan(&e.ID, &e.PipelineID, &e.RunID, &e.Error, &e.NodeID, &e.NodeName, &e.Payload, &e.CreatedAt, &e.Resolved, &e.ResolvedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (s *PostgresStore) ResolveDLQ(id string) error {
	_, err := s.db.Exec(`UPDATE dead_letter_queue SET resolved = true, resolved_at = NOW() WHERE id = $1`, id)
	return err
}

func (s *PostgresStore) Close() error       { return s.db.Close() }
func (s *PostgresStore) RawDB() interface{} { return s.db }

// --- Pipelines ---

func (s *PostgresStore) CreatePipeline(p *models.Pipeline) error {
	nodesJSON, _ := json.Marshal(p.Nodes)
	edgesJSON, _ := json.Marshal(p.Edges)
	paramsJSON, _ := json.Marshal(p.Params)
	tagsJSON, _ := json.Marshal(p.Tags)
	depsJSON, _ := json.Marshal(p.DependsOn)
	depRulesJSON, _ := json.Marshal(p.DependencyRules)
	if depRulesJSON == nil {
		depRulesJSON = []byte("[]")
	}
	_, err := s.db.Exec(
		`INSERT INTO pipelines (id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		p.ID, p.Name, p.Description, nodesJSON, edgesJSON,
		p.Schedule, p.ScheduleTimezone, p.WebhookURL, paramsJSON, tagsJSON, p.SLADeadline, p.SLATimezone, depsJSON, depRulesJSON, p.WebhookToken, p.Enabled, p.CreatedAt.UTC(), p.UpdatedAt.UTC(), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID,
	)
	return err
}

func (s *PostgresStore) GetPipeline(id string) (*models.Pipeline, error) {
	var p models.Pipeline
	var nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON, depRulesJSON []byte
	err := s.db.QueryRow(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
		 FROM pipelines WHERE id = $1`, id,
	).Scan(&p.ID, &p.Name, &p.Description, &nodesJSON, &edgesJSON,
		&p.Schedule, &p.ScheduleTimezone, &p.WebhookURL, &paramsJSON, &tagsJSON, &p.SLADeadline, &p.SLATimezone, &depsJSON, &depRulesJSON, &p.WebhookToken, &p.Enabled, &p.CreatedAt, &p.UpdatedAt, &p.PipelineID, &p.Source, &p.WorkspaceID, &p.OrgID)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(nodesJSON, &p.Nodes)
	json.Unmarshal(edgesJSON, &p.Edges)
	json.Unmarshal(paramsJSON, &p.Params)
	json.Unmarshal(tagsJSON, &p.Tags)
	json.Unmarshal(depsJSON, &p.DependsOn)
	json.Unmarshal(depRulesJSON, &p.DependencyRules)
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if p.DependencyRules == nil {
		p.DependencyRules = []models.DependencyRule{}
	}
	return &p, nil
}

func (s *PostgresStore) ListPipelines() ([]models.Pipeline, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
		 FROM pipelines ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pipelines []models.Pipeline
	for rows.Next() {
		p, err := s.scanPipelineRow(rows)
		if err != nil {
			return nil, err
		}
		pipelines = append(pipelines, *p)
	}
	return pipelines, rows.Err()
}

// scanPipelineRow scans a pipeline row from any scanner (Row or Rows).
func (s *PostgresStore) scanPipelineRow(sc interface{ Scan(...interface{}) error }) (*models.Pipeline, error) {
	var p models.Pipeline
	var nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON, depRulesJSON []byte
	if err := sc.Scan(&p.ID, &p.Name, &p.Description, &nodesJSON, &edgesJSON,
		&p.Schedule, &p.ScheduleTimezone, &p.WebhookURL, &paramsJSON, &tagsJSON, &p.SLADeadline, &p.SLATimezone, &depsJSON, &depRulesJSON, &p.WebhookToken, &p.Enabled, &p.CreatedAt, &p.UpdatedAt, &p.PipelineID, &p.Source, &p.WorkspaceID, &p.OrgID); err != nil {
		return nil, err
	}
	json.Unmarshal(nodesJSON, &p.Nodes)
	json.Unmarshal(edgesJSON, &p.Edges)
	json.Unmarshal(paramsJSON, &p.Params)
	json.Unmarshal(tagsJSON, &p.Tags)
	json.Unmarshal(depsJSON, &p.DependsOn)
	json.Unmarshal(depRulesJSON, &p.DependencyRules)
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if p.DependencyRules == nil {
		p.DependencyRules = []models.DependencyRule{}
	}
	return &p, nil
}

func (s *PostgresStore) UpdatePipeline(p *models.Pipeline) error {
	nodesJSON, _ := json.Marshal(p.Nodes)
	edgesJSON, _ := json.Marshal(p.Edges)
	paramsJSON, _ := json.Marshal(p.Params)
	tagsJSON, _ := json.Marshal(p.Tags)
	depsJSON, _ := json.Marshal(p.DependsOn)
	depRulesJSON, _ := json.Marshal(p.DependencyRules)
	if depRulesJSON == nil {
		depRulesJSON = []byte("[]")
	}
	result, err := s.db.Exec(
		`UPDATE pipelines SET name=$1, description=$2, nodes=$3, edges=$4, schedule=$5, schedule_timezone=$6,
		 webhook_url=$7, params=$8, tags=$9, sla_deadline=$10, sla_timezone=$11, depends_on=$12, dependency_rules=$13, webhook_token=$14, enabled=$15, updated_at=$16, pipeline_id=$17, source=$18, workspace_id=$19, org_id=$20 WHERE id=$21`,
		p.Name, p.Description, nodesJSON, edgesJSON, p.Schedule, p.ScheduleTimezone,
		p.WebhookURL, paramsJSON, tagsJSON, p.SLADeadline, p.SLATimezone, depsJSON, depRulesJSON, p.WebhookToken, p.Enabled, p.UpdatedAt.UTC(), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID, p.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("pipeline not found: %s", p.ID)
	}
	return nil
}

func (s *PostgresStore) GetPipelineByPipelineID(pipelineID string) (*models.Pipeline, error) {
	var p models.Pipeline
	var nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON, depRulesJSON []byte
	err := s.db.QueryRow(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
		 FROM pipelines WHERE pipeline_id = $1`, pipelineID,
	).Scan(&p.ID, &p.Name, &p.Description, &nodesJSON, &edgesJSON,
		&p.Schedule, &p.ScheduleTimezone, &p.WebhookURL, &paramsJSON, &tagsJSON, &p.SLADeadline, &p.SLATimezone, &depsJSON, &depRulesJSON, &p.WebhookToken, &p.Enabled, &p.CreatedAt, &p.UpdatedAt, &p.PipelineID, &p.Source, &p.WorkspaceID, &p.OrgID)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(nodesJSON, &p.Nodes)
	json.Unmarshal(edgesJSON, &p.Edges)
	json.Unmarshal(paramsJSON, &p.Params)
	json.Unmarshal(tagsJSON, &p.Tags)
	json.Unmarshal(depsJSON, &p.DependsOn)
	json.Unmarshal(depRulesJSON, &p.DependencyRules)
	if p.Tags == nil {
		p.Tags = []string{}
	}
	if p.DependencyRules == nil {
		p.DependencyRules = []models.DependencyRule{}
	}
	return &p, nil
}

// ListPipelineDepsByOrg returns only the dep columns, avoiding expensive JSONB blob loads.
func (s *PostgresStore) ListPipelineDepsByOrg(orgID string) ([]models.PipelineDepSummary, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if orgID != "" {
		rows, err = s.db.Query(
			`SELECT id, name, org_id, depends_on, dependency_rules FROM pipelines WHERE org_id = $1`,
			orgID,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, name, org_id, depends_on, dependency_rules FROM pipelines`,
		)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]models.PipelineDepSummary, 0, 64)
	for rows.Next() {
		var (
			summary      models.PipelineDepSummary
			depsJSON     []byte
			depRulesJSON []byte
		)
		if err := rows.Scan(&summary.ID, &summary.Name, &summary.OrgID, &depsJSON, &depRulesJSON); err != nil {
			return nil, err
		}
		if len(depsJSON) > 0 {
			json.Unmarshal(depsJSON, &summary.DependsOn)
		}
		if len(depRulesJSON) > 0 {
			json.Unmarshal(depRulesJSON, &summary.DependencyRules)
		}
		out = append(out, summary)
	}
	return out, rows.Err()
}

// GetLatestRunsByPipelineIDs returns the most recent run per pipeline id in a single query
// using DISTINCT ON (postgres-specific, index-friendly).
func (s *PostgresStore) GetLatestRunsByPipelineIDs(ids []string) (map[string]*models.Run, error) {
	out := make(map[string]*models.Run, len(ids))
	if len(ids) == 0 {
		return out, nil
	}

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
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = id
	}

	query := `
		SELECT DISTINCT ON (pipeline_id)
		    id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id
		FROM runs
		WHERE pipeline_id IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY pipeline_id, started_at DESC NULLS FIRST, id DESC
	`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			r                     models.Run
			status                string
			startedAt, finishedAt sql.NullTime
			paramsJSON            []byte
		)
		if err := rows.Scan(&r.ID, &r.PipelineID, &status, &startedAt, &finishedAt, &r.TraceID, &r.Error, &paramsJSON, &r.PipelineVersion, &r.ResumedFromRunID, &r.OrgID); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(paramsJSON, &r.Params); err != nil {
			return nil, fmt.Errorf("decode run params: %w", err)
		}
		r.Status = models.RunStatus(status)
		if startedAt.Valid {
			t := startedAt.Time
			r.StartedAt = &t
		}
		if finishedAt.Valid {
			t := finishedAt.Time
			r.FinishedAt = &t
		}
		out[r.PipelineID] = &r
	}
	return out, rows.Err()
}

// DeletePipelineTx deletes a pipeline inside an active transaction.
func (s *PostgresStore) DeletePipelineTx(tx *sql.Tx, id string) error {
	result, err := tx.Exec(`DELETE FROM pipelines WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("pipeline not found: %s", id)
	}
	return nil
}

// UpdatePipelineTx updates a pipeline inside an active transaction.
func (s *PostgresStore) UpdatePipelineTx(tx *sql.Tx, p *models.Pipeline) error {
	nodesJSON, _ := json.Marshal(p.Nodes)
	edgesJSON, _ := json.Marshal(p.Edges)
	paramsJSON, _ := json.Marshal(p.Params)
	tagsJSON, _ := json.Marshal(p.Tags)
	depsJSON, _ := json.Marshal(p.DependsOn)
	depRulesJSON, _ := json.Marshal(p.DependencyRules)
	if depRulesJSON == nil {
		depRulesJSON = []byte("[]")
	}
	result, err := tx.Exec(
		`UPDATE pipelines SET name=$1, description=$2, nodes=$3, edges=$4, schedule=$5, schedule_timezone=$6,
		 webhook_url=$7, params=$8, tags=$9, sla_deadline=$10, sla_timezone=$11, depends_on=$12, dependency_rules=$13, webhook_token=$14, enabled=$15, updated_at=$16, pipeline_id=$17, source=$18, workspace_id=$19, org_id=$20 WHERE id=$21`,
		p.Name, p.Description, nodesJSON, edgesJSON, p.Schedule, p.ScheduleTimezone,
		p.WebhookURL, paramsJSON, tagsJSON, p.SLADeadline, p.SLATimezone, depsJSON, depRulesJSON, p.WebhookToken, p.Enabled, p.UpdatedAt.UTC(), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID, p.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("pipeline not found: %s", p.ID)
	}
	return nil
}

func (s *PostgresStore) PipelinesDependingOn(pipelineID string) ([]models.Pipeline, error) {
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

func (s *PostgresStore) DeletePipeline(id string) error {
	result, err := s.db.Exec(`DELETE FROM pipelines WHERE id=$1`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("pipeline not found: %s", id)
	}
	return nil
}

// --- Runs ---

func (s *PostgresStore) CreateRun(r *models.Run) error {
	return pgCreateRun(s.db, r)
}

// CreateRunTx creates a run inside an existing transaction, so it can commit
// atomically alongside AppendEventTx and CreateExecutionAttemptTx — see
// Engine.RunPipelineAsync's dispatch outbox (Tnsor-Labs/brokoli#7).
func (s *PostgresStore) CreateRunTx(tx *sql.Tx, r *models.Run) error {
	return pgCreateRun(tx, r)
}

func pgCreateRun(x sqlExecerPg, r *models.Run) error {
	paramsJSON, err := json.Marshal(r.Params)
	if err != nil {
		return fmt.Errorf("marshal run params: %w", err)
	}
	_, err = x.Exec(
		`INSERT INTO runs (id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		r.ID, r.PipelineID, string(r.Status), r.StartedAt, r.FinishedAt, r.TraceID, r.Error, paramsJSON, r.PipelineVersion, r.ResumedFromRunID, r.OrgID,
	)
	return err
}

// sqlExecerPg is satisfied by both *sql.DB and *sql.Tx, letting CreateRun
// and CreateRunTx share one implementation. Named distinctly from
// store/sqlite.go's identical sqlExecer since this file otherwise defines
// its own helpers rather than sharing them with sqlite.go.
type sqlExecerPg interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

func (s *PostgresStore) ClaimPendingRun(runID, pipelineID string, startedAt time.Time, traceID string) (bool, error) {
	result, err := s.db.Exec(
		`UPDATE runs SET status=$1, started_at=$2, trace_id=$3 WHERE id=$4 AND pipeline_id=$5 AND status=$6`,
		string(models.RunStatusRunning), startedAt, traceID, runID, pipelineID, string(models.RunStatusPending),
	)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (s *PostgresStore) GetRun(id string) (*models.Run, error) {
	var r models.Run
	var status string
	var paramsJSON []byte
	err := s.db.QueryRow(
		`SELECT id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id FROM runs WHERE id = $1`, id,
	).Scan(&r.ID, &r.PipelineID, &status, &r.StartedAt, &r.FinishedAt, &r.TraceID, &r.Error, &paramsJSON, &r.PipelineVersion, &r.ResumedFromRunID, &r.OrgID)
	if err != nil {
		return nil, err
	}
	r.Status = models.RunStatus(status)
	if err := json.Unmarshal(paramsJSON, &r.Params); err != nil {
		return nil, fmt.Errorf("decode run params: %w", err)
	}

	nodeRuns, err := s.ListNodeRunsByRun(id)
	if err != nil {
		return nil, err
	}
	r.NodeRuns = nodeRuns
	return &r, nil
}

func (s *PostgresStore) ListRunsByPipeline(pipelineID string, limit int) ([]models.Run, error) {
	rows, err := s.db.Query(
		`SELECT id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id
		 FROM runs WHERE pipeline_id = $1 ORDER BY started_at DESC NULLS FIRST, id DESC LIMIT $2`,
		pipelineID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []models.Run
	for rows.Next() {
		var r models.Run
		var status string
		var paramsJSON []byte
		if err := rows.Scan(&r.ID, &r.PipelineID, &status, &r.StartedAt, &r.FinishedAt, &r.TraceID, &r.Error, &paramsJSON, &r.PipelineVersion, &r.ResumedFromRunID, &r.OrgID); err != nil {
			return nil, err
		}
		r.Status = models.RunStatus(status)
		if err := json.Unmarshal(paramsJSON, &r.Params); err != nil {
			return nil, fmt.Errorf("decode run params: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

// ListNonTerminalRuns returns a keyset-paginated page of runs left in a
// non-terminal status — see store.Store.ListNonTerminalRuns and
// Tnsor-Labs/brokoli#9. Ordered ascending by id (a sortable UUIDv7, see
// pkg/common.NewID).
func (s *PostgresStore) ListNonTerminalRuns(afterID string, limit int) ([]models.Run, bool, error) {
	fetchN := limit + 1
	var rows *sql.Rows
	var err error
	if afterID == "" {
		rows, err = s.db.Query(
			`SELECT id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id
			 FROM runs WHERE `+nonTerminalRunStatusFilter+` ORDER BY id ASC LIMIT $1`,
			fetchN,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT id, pipeline_id, status, started_at, finished_at, trace_id, error, params, pipeline_version, resumed_from_run_id, org_id
			 FROM runs WHERE `+nonTerminalRunStatusFilter+` AND id > $1 ORDER BY id ASC LIMIT $2`,
			afterID, fetchN,
		)
	}
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()

	var runs []models.Run
	for rows.Next() {
		var r models.Run
		var status string
		var paramsJSON []byte
		if err := rows.Scan(&r.ID, &r.PipelineID, &status, &r.StartedAt, &r.FinishedAt, &r.TraceID, &r.Error, &paramsJSON, &r.PipelineVersion, &r.ResumedFromRunID, &r.OrgID); err != nil {
			return nil, false, err
		}
		r.Status = models.RunStatus(status)
		if err := json.Unmarshal(paramsJSON, &r.Params); err != nil {
			return nil, false, fmt.Errorf("decode run params: %w", err)
		}
		runs = append(runs, r)
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

func (s *PostgresStore) UpdateRun(r *models.Run) error {
	paramsJSON, err := json.Marshal(r.Params)
	if err != nil {
		return fmt.Errorf("marshal run params: %w", err)
	}
	result, err := s.db.Exec(
		`UPDATE runs SET status=$1, started_at=$2, finished_at=$3, trace_id=$4, error=$5, params=$6 WHERE id=$7`,
		string(r.Status), r.StartedAt, r.FinishedAt, r.TraceID, r.Error, paramsJSON, r.ID,
	)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("run not found: %s", r.ID)
	}
	return nil
}

// --- Node Runs ---

func (s *PostgresStore) CreateNodeRun(nr *models.NodeRun) error {
	_, err := s.db.Exec(
		`INSERT INTO node_runs (id, run_id, node_id, status, row_count, started_at, duration_ms, error, attempt, ready_at, queue_ms, rows_per_sec, trace_id, span_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
		nr.ID, nr.RunID, nr.NodeID, string(nr.Status), nr.RowCount, nr.StartedAt, nr.DurationMs, nr.Error,
		nr.Attempt, nr.ReadyAt, nr.QueueMs, nr.RowsPerSec, nr.TraceID, nr.SpanID,
	)
	return err
}

func (s *PostgresStore) UpdateNodeRun(nr *models.NodeRun) error {
	_, err := s.db.Exec(
		`UPDATE node_runs SET status=$1, row_count=$2, started_at=$3, duration_ms=$4, error=$5, attempt=$6, ready_at=$7, queue_ms=$8, rows_per_sec=$9, trace_id=$10, span_id=$11 WHERE id=$12`,
		string(nr.Status), nr.RowCount, nr.StartedAt, nr.DurationMs, nr.Error,
		nr.Attempt, nr.ReadyAt, nr.QueueMs, nr.RowsPerSec, nr.TraceID, nr.SpanID, nr.ID,
	)
	return err
}

func (s *PostgresStore) ListNodeRunsByRun(runID string) ([]models.NodeRun, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, node_id, status, row_count, started_at, duration_ms, error, attempt, ready_at, queue_ms, rows_per_sec, trace_id, span_id
		 FROM node_runs WHERE run_id = $1`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var nodeRuns []models.NodeRun
	for rows.Next() {
		var nr models.NodeRun
		var status string
		if err := rows.Scan(&nr.ID, &nr.RunID, &nr.NodeID, &status, &nr.RowCount, &nr.StartedAt, &nr.DurationMs, &nr.Error,
			&nr.Attempt, &nr.ReadyAt, &nr.QueueMs, &nr.RowsPerSec, &nr.TraceID, &nr.SpanID); err != nil {
			return nil, err
		}
		nr.Status = models.RunStatus(status)
		nodeRuns = append(nodeRuns, nr)
	}
	return nodeRuns, rows.Err()
}

// --- Expansion Instances ---
//
// See store.ExpansionInstanceStore and models.ExpansionInstance for the
// contract these implement — the durable per-item execution record for a
// dynamic-expansion `code` node (issue #31).

func (s *PostgresStore) CreateExpansionInstance(ei *models.ExpansionInstance) error {
	_, err := s.db.Exec(
		`INSERT INTO expansion_instances (id, run_id, node_id, node_attempt, instance_index, instance_key, status, row_count, started_at, duration_ms, error)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		ei.ID, ei.RunID, ei.NodeID, ei.NodeAttempt, ei.InstanceIndex, ei.InstanceKey,
		string(ei.Status), ei.RowCount, ei.StartedAt, ei.DurationMs, ei.Error,
	)
	if err != nil {
		return fmt.Errorf("create expansion instance: %w", err)
	}
	return nil
}

func (s *PostgresStore) UpdateExpansionInstance(ei *models.ExpansionInstance) error {
	result, err := s.db.Exec(
		`UPDATE expansion_instances SET status=$1, row_count=$2, started_at=$3, duration_ms=$4, error=$5 WHERE id=$6`,
		string(ei.Status), ei.RowCount, ei.StartedAt, ei.DurationMs, ei.Error, ei.ID,
	)
	if err != nil {
		return fmt.Errorf("update expansion instance: %w", err)
	}
	return checkRowsAffected(result, "expansion_instance", ei.ID)
}

func (s *PostgresStore) ListExpansionInstancesByRun(runID string) ([]models.ExpansionInstance, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, node_id, node_attempt, instance_index, instance_key, status, row_count, started_at, duration_ms, error
		 FROM expansion_instances WHERE run_id = $1 ORDER BY node_id ASC, node_attempt ASC, instance_index ASC`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("list expansion instances: %w", err)
	}
	defer rows.Close()

	var instances []models.ExpansionInstance
	for rows.Next() {
		var ei models.ExpansionInstance
		var status string
		if err := rows.Scan(&ei.ID, &ei.RunID, &ei.NodeID, &ei.NodeAttempt, &ei.InstanceIndex, &ei.InstanceKey,
			&status, &ei.RowCount, &ei.StartedAt, &ei.DurationMs, &ei.Error); err != nil {
			return nil, fmt.Errorf("list expansion instances: %w", err)
		}
		ei.Status = models.RunStatus(status)
		instances = append(instances, ei)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list expansion instances: %w", err)
	}
	return instances, nil
}

// --- Run Events ---

// pgQueryRower is satisfied by both *sql.DB and *sql.Tx, letting AppendEvent
// and AppendEventTx share one implementation.
type pgQueryRower interface {
	QueryRow(query string, args ...interface{}) *sql.Row
}

func (s *PostgresStore) AppendEvent(e *models.RunEvent) error {
	return pgAppendEvent(s.db, e)
}

// AppendEventTx appends an event using an existing transaction, so it can be
// committed atomically alongside other writes via WithTx.
func (s *PostgresStore) AppendEventTx(tx *sql.Tx, e *models.RunEvent) error {
	return pgAppendEvent(tx, e)
}

func pgAppendEvent(x pgQueryRower, e *models.RunEvent) error {
	payloadJSON, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("marshal event payload: %w", err)
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

	err = x.QueryRow(
		`INSERT INTO run_events (run_id, node_id, attempt, event_type, payload, created_at, schema_version) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		e.RunID, nodeID, attempt, string(e.EventType), payloadJSON, e.CreatedAt, schemaVersion,
	).Scan(&e.ID)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	e.SchemaVersion = schemaVersion
	return nil
}

func (s *PostgresStore) ListEventsByRun(runID string) ([]models.RunEvent, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, node_id, attempt, event_type, payload, created_at, schema_version
		 FROM run_events WHERE run_id = $1 ORDER BY id ASC`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var events []models.RunEvent
	for rows.Next() {
		var ev models.RunEvent
		var nodeID sql.NullString
		var attempt sql.NullInt64
		var eventType string
		var payloadJSON []byte
		if err := rows.Scan(&ev.ID, &ev.RunID, &nodeID, &attempt, &eventType, &payloadJSON, &ev.CreatedAt, &ev.SchemaVersion); err != nil {
			return nil, err
		}
		ev.NodeID = nodeID.String
		if attempt.Valid {
			a := int(attempt.Int64)
			ev.Attempt = &a
		}
		ev.EventType = models.RunEventType(eventType)
		if err := json.Unmarshal(payloadJSON, &ev.Payload); err != nil {
			return nil, fmt.Errorf("decode event payload: %w", err)
		}
		events = append(events, ev)
	}
	return events, rows.Err()
}

// --- Execution Attempts ---
//
// See store.ExecutionAttemptStore and models.ExecutionAttempt for the
// contract these implement: a durable outbox/intent record plus a
// compare-and-swap claim/lease, extending PendingRunClaimer's run-level CAS
// (ClaimPendingRun above) to (run_id, node_id, attempt) granularity.

func (s *PostgresStore) CreateExecutionAttemptTx(tx *sql.Tx, a *models.ExecutionAttempt) error {
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
		`INSERT INTO execution_attempts (run_id, node_id, attempt, status, claimed_by, lease_expires_at, fencing_generation, idempotency_key, error, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		 ON CONFLICT (run_id, node_id, attempt) DO NOTHING`,
		a.RunID, a.NodeID, a.Attempt, string(status), a.ClaimedBy, a.LeaseExpiresAt,
		a.FencingGeneration, a.IdempotencyKey, a.Error, a.CreatedAt, a.UpdatedAt,
	)
	return err
}

func (s *PostgresStore) ClaimAttempt(runID, nodeID string, attempt int, claimedBy string, leaseDuration time.Duration) (int64, bool, error) {
	now := time.Now().UTC()
	leaseExpires := now.Add(leaseDuration)
	var fencingGeneration int64
	err := s.db.QueryRow(
		`UPDATE execution_attempts
		 SET status=$1, claimed_by=$2, lease_expires_at=$3, fencing_generation=fencing_generation+1, updated_at=$4
		 WHERE run_id=$5 AND node_id=$6 AND attempt=$7
		   AND status NOT IN ($8, $9)
		   AND (status=$10 OR lease_expires_at IS NULL OR lease_expires_at<$4)
		 RETURNING fencing_generation`,
		string(models.AttemptStatusClaimed), claimedBy, leaseExpires, now,
		runID, nodeID, attempt,
		string(models.AttemptStatusCompleted), string(models.AttemptStatusFailed),
		string(models.AttemptStatusQueued),
	).Scan(&fencingGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("claim attempt: %w", err)
	}
	return fencingGeneration, true, nil
}

func (s *PostgresStore) RenewLease(runID, nodeID string, attempt int, claimedBy string, fencingGeneration int64, leaseDuration time.Duration) (bool, error) {
	now := time.Now().UTC()
	leaseExpires := now.Add(leaseDuration)
	result, err := s.db.Exec(
		`UPDATE execution_attempts SET lease_expires_at=$1, updated_at=$2
		 WHERE run_id=$3 AND node_id=$4 AND attempt=$5 AND claimed_by=$6 AND fencing_generation=$7 AND status IN ($8, $9)`,
		leaseExpires, now, runID, nodeID, attempt, claimedBy, fencingGeneration,
		string(models.AttemptStatusClaimed), string(models.AttemptStatusStarted),
	)
	if err != nil {
		return false, fmt.Errorf("renew lease: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("renew lease: %w", err)
	}
	return n == 1, nil
}

func (s *PostgresStore) AckAttempt(runID, nodeID string, attempt int, claimedBy string, fencingGeneration int64) error {
	now := time.Now().UTC()
	result, err := s.db.Exec(
		`UPDATE execution_attempts SET status=$1, updated_at=$2
		 WHERE run_id=$3 AND node_id=$4 AND attempt=$5 AND claimed_by=$6 AND fencing_generation=$7 AND status IN ($8, $9)`,
		string(models.AttemptStatusStarted), now, runID, nodeID, attempt, claimedBy, fencingGeneration,
		string(models.AttemptStatusClaimed), string(models.AttemptStatusStarted),
	)
	if err != nil {
		return fmt.Errorf("ack attempt: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("ack attempt: %w", err)
	}
	if n == 1 {
		return nil
	}
	existing, getErr := pgGetExecutionAttempt(s.db, runID, nodeID, attempt)
	if getErr != nil {
		return fmt.Errorf("ack attempt: %w", getErr)
	}
	if existing.Status == models.AttemptStatusStarted && existing.ClaimedBy == claimedBy && existing.FencingGeneration == fencingGeneration {
		return nil // idempotent re-ack
	}
	return fmt.Errorf("ack attempt: fencing generation mismatch or attempt not claimed by %q (have generation %d, status %s)", claimedBy, existing.FencingGeneration, existing.Status)
}

func (s *PostgresStore) CompleteAttempt(runID, nodeID string, attempt int, fencingGeneration int64) error {
	return pgSettleAttempt(s.db, runID, nodeID, attempt, fencingGeneration, models.AttemptStatusCompleted, "")
}

func (s *PostgresStore) FailAttempt(runID, nodeID string, attempt int, fencingGeneration int64, errMsg string) error {
	return pgSettleAttempt(s.db, runID, nodeID, attempt, fencingGeneration, models.AttemptStatusFailed, errMsg)
}

// pgSettleAttempt performs the fencing-checked transition into a terminal
// status (completed/failed). Settling an attempt already in that same
// terminal status is a no-op success — the documented duplicate-delivery
// contract; any other mismatch (wrong fencing generation, or already
// terminal in the *other* status) is an error.
func pgSettleAttempt(db *sql.DB, runID, nodeID string, attempt int, fencingGeneration int64, toStatus models.AttemptStatus, errMsg string) error {
	now := time.Now().UTC()
	result, err := db.Exec(
		`UPDATE execution_attempts SET status=$1, error=$2, updated_at=$3
		 WHERE run_id=$4 AND node_id=$5 AND attempt=$6 AND fencing_generation=$7 AND status NOT IN ($8, $9)`,
		string(toStatus), errMsg, now, runID, nodeID, attempt, fencingGeneration,
		string(models.AttemptStatusCompleted), string(models.AttemptStatusFailed),
	)
	if err != nil {
		return fmt.Errorf("settle attempt: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("settle attempt: %w", err)
	}
	if n == 1 {
		return nil
	}
	existing, getErr := pgGetExecutionAttempt(db, runID, nodeID, attempt)
	if getErr != nil {
		return fmt.Errorf("settle attempt: %w", getErr)
	}
	if existing.Status == toStatus {
		return nil // idempotent duplicate settle
	}
	return fmt.Errorf("settle attempt: fencing generation mismatch settling attempt to %s (have generation %d, status %s)", toStatus, existing.FencingGeneration, existing.Status)
}

func (s *PostgresStore) GetExecutionAttempt(runID, nodeID string, attempt int) (*models.ExecutionAttempt, error) {
	return pgGetExecutionAttempt(s.db, runID, nodeID, attempt)
}

func pgGetExecutionAttempt(db *sql.DB, runID, nodeID string, attempt int) (*models.ExecutionAttempt, error) {
	var a models.ExecutionAttempt
	var status string
	err := db.QueryRow(
		`SELECT run_id, node_id, attempt, status, claimed_by, lease_expires_at, fencing_generation, idempotency_key, error, created_at, updated_at
		 FROM execution_attempts WHERE run_id=$1 AND node_id=$2 AND attempt=$3`,
		runID, nodeID, attempt,
	).Scan(&a.RunID, &a.NodeID, &a.Attempt, &status, &a.ClaimedBy, &a.LeaseExpiresAt, &a.FencingGeneration, &a.IdempotencyKey, &a.Error, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("get execution attempt: %w", err)
	}
	a.Status = models.AttemptStatus(status)
	return &a, nil
}

// ListExecutionAttemptsByRun returns every execution_attempts row for a run
// — see store.ExecutionAttemptStore.ListExecutionAttemptsByRun and
// Tnsor-Labs/brokoli#9, which needs this enumeration to check every
// attempt's lease state during startup recovery.
func (s *PostgresStore) ListExecutionAttemptsByRun(runID string) ([]models.ExecutionAttempt, error) {
	rows, err := s.db.Query(
		`SELECT run_id, node_id, attempt, status, claimed_by, lease_expires_at, fencing_generation, idempotency_key, error, created_at, updated_at
		 FROM execution_attempts WHERE run_id = $1 ORDER BY node_id ASC, attempt ASC`,
		runID,
	)
	if err != nil {
		return nil, fmt.Errorf("list execution attempts: %w", err)
	}
	defer rows.Close()

	var attempts []models.ExecutionAttempt
	for rows.Next() {
		var a models.ExecutionAttempt
		var status string
		if err := rows.Scan(&a.RunID, &a.NodeID, &a.Attempt, &status, &a.ClaimedBy, &a.LeaseExpiresAt, &a.FencingGeneration, &a.IdempotencyKey, &a.Error, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list execution attempts: %w", err)
		}
		a.Status = models.AttemptStatus(status)
		attempts = append(attempts, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list execution attempts: %w", err)
	}
	return attempts, nil
}

// --- Logs ---

func (s *PostgresStore) AppendLog(entry *models.LogEntry) error {
	metadataJSON, _ := json.Marshal(entry.Metadata)
	if entry.Metadata == nil {
		metadataJSON = []byte("{}")
	}
	_, err := s.db.Exec(
		`INSERT INTO logs (run_id, node_id, level, message, timestamp, trace_id, span_id, attempt, metadata) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		entry.RunID, entry.NodeID, string(entry.Level), entry.Message, entry.Timestamp,
		entry.TraceID, entry.SpanID, entry.Attempt, string(metadataJSON),
	)
	return err
}

func (s *PostgresStore) GetLogs(runID string) ([]models.LogEntry, error) {
	rows, err := s.db.Query(
		`SELECT run_id, node_id, level, message, timestamp, trace_id, span_id, attempt, metadata FROM logs WHERE run_id = $1 ORDER BY timestamp`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []models.LogEntry
	for rows.Next() {
		var entry models.LogEntry
		var level string
		var metadataStr string
		if err := rows.Scan(&entry.RunID, &entry.NodeID, &level, &entry.Message, &entry.Timestamp,
			&entry.TraceID, &entry.SpanID, &entry.Attempt, &metadataStr); err != nil {
			return nil, err
		}
		entry.Level = models.LogLevel(level)
		if metadataStr != "" && metadataStr != "{}" {
			json.Unmarshal([]byte(metadataStr), &entry.Metadata)
		}
		logs = append(logs, entry)
	}
	return logs, rows.Err()
}

// --- Data Preview ---

func (s *PostgresStore) SaveNodePreview(runID, nodeID string, columns []string, rows []common.DataRow) error {
	colJSON, _ := json.Marshal(columns)
	if len(rows) > 50 {
		rows = rows[:50]
	}
	rowJSON, _ := json.Marshal(rows)
	_, err := s.db.Exec(
		`INSERT INTO node_previews (run_id, node_id, columns, rows) VALUES ($1,$2,$3,$4)
		 ON CONFLICT (run_id, node_id) DO UPDATE SET columns=$3, rows=$4`,
		runID, nodeID, colJSON, rowJSON,
	)
	return err
}

func (s *PostgresStore) GetNodePreview(runID, nodeID string) ([]string, []common.DataRow, error) {
	var colJSON, rowJSON []byte
	err := s.db.QueryRow(
		`SELECT columns, rows FROM node_previews WHERE run_id = $1 AND node_id = $2`, runID, nodeID,
	).Scan(&colJSON, &rowJSON)
	if err != nil {
		return nil, nil, err
	}
	var columns []string
	var rows []common.DataRow
	json.Unmarshal(colJSON, &columns)
	json.Unmarshal(rowJSON, &rows)
	return columns, rows, nil
}

// --- Versioning ---

// SavePipelineVersion — see the doc comment on SQLiteStore.SavePipelineVersion
// (store/sqlite.go) for why the retry loop exists: the compute-then-insert
// isn't atomic, and issue #8 made concurrent callers for a pipeline with no
// saved version yet a real possibility, not just a manual-edit edge case.
func (s *PostgresStore) SavePipelineVersion(pipelineID string, snapshot string, message string) (int, error) {
	const maxAttempts = 100
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		var maxVer sql.NullInt64
		s.db.QueryRow(`SELECT MAX(version) FROM pipeline_versions WHERE pipeline_id = $1`, pipelineID).Scan(&maxVer)
		nextVer := 1
		if maxVer.Valid {
			nextVer = int(maxVer.Int64) + 1
		}
		_, err := s.db.Exec(
			`INSERT INTO pipeline_versions (pipeline_id, version, snapshot, message, created_at) VALUES ($1,$2,$3,$4,$5)`,
			pipelineID, nextVer, snapshot, message, time.Now(),
		)
		if err == nil {
			return nextVer, nil
		}
		lastErr = err
	}
	return 0, fmt.Errorf("save pipeline version for %s after %d attempts (likely concurrent version creation): %w", pipelineID, maxAttempts, lastErr)
}

func (s *PostgresStore) ListPipelineVersions(pipelineID string) ([]PipelineVersion, error) {
	rows, err := s.db.Query(
		`SELECT version, message, created_at FROM pipeline_versions WHERE pipeline_id = $1 ORDER BY version DESC LIMIT 50`,
		pipelineID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var versions []PipelineVersion
	for rows.Next() {
		var v PipelineVersion
		var ts time.Time
		if err := rows.Scan(&v.Version, &v.Message, &ts); err != nil {
			return nil, err
		}
		v.CreatedAt = ts.Format(time.RFC3339)
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

func (s *PostgresStore) GetPipelineVersion(pipelineID string, version int) (string, error) {
	var snapshot string
	err := s.db.QueryRow(
		`SELECT snapshot FROM pipeline_versions WHERE pipeline_id = $1 AND version = $2`,
		pipelineID, version,
	).Scan(&snapshot)
	return snapshot, err
}

// --- Pagination Counts ---

func (s *PostgresStore) CountPipelines(workspaceID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM pipelines WHERE workspace_id = $1", workspaceID).Scan(&count)
	return count, err
}

func (s *PostgresStore) CountConnections(workspaceID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM connections WHERE workspace_id = $1", workspaceID).Scan(&count)
	return count, err
}

func (s *PostgresStore) CountVariables(workspaceID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM variables WHERE workspace_id = $1", workspaceID).Scan(&count)
	return count, err
}

func (s *PostgresStore) CountRunsByPipeline(pipelineID string) (int, error) {
	var count int
	err := s.db.QueryRow("SELECT COUNT(*) FROM runs WHERE pipeline_id = $1", pipelineID).Scan(&count)
	return count, err
}

// --- Maintenance ---

func (s *PostgresStore) PurgeRunsOlderThan(days int) (int64, error) {
	result, err := s.db.Exec(
		`DELETE FROM runs WHERE started_at < NOW() - $1 * INTERVAL '1 day' AND started_at IS NOT NULL`,
		days,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *PostgresStore) PurgeRunsOlderThanByOrg(days int, orgID string) (int64, error) {
	result, err := s.db.Exec(
		`DELETE FROM runs WHERE started_at < NOW() - $1 * INTERVAL '1 day' AND started_at IS NOT NULL AND org_id = $2`,
		days, orgID,
	)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ListRunIDsOlderThan mirrors PurgeRunsOlderThan's WHERE clause exactly —
// call before it to know which run IDs are about to be purged.
func (s *PostgresStore) ListRunIDsOlderThan(days int) ([]string, error) {
	return queryRunIDs(s.db, `SELECT id FROM runs WHERE started_at < NOW() - $1 * INTERVAL '1 day' AND started_at IS NOT NULL`, days)
}

// ListRunIDsOlderThanByOrg mirrors PurgeRunsOlderThanByOrg's WHERE clause.
func (s *PostgresStore) ListRunIDsOlderThanByOrg(days int, orgID string) ([]string, error) {
	return queryRunIDs(s.db, `SELECT id FROM runs WHERE started_at < NOW() - $1 * INTERVAL '1 day' AND started_at IS NOT NULL AND org_id = $2`, days, orgID)
}

func (s *PostgresStore) GetDBSize() (int64, error) {
	var size int64
	err := s.db.QueryRow(`SELECT pg_database_size(current_database())`).Scan(&size)
	return size, err
}

// ── Calendar ──

func (s *PostgresStore) GetRunCalendar(days int) ([]CalendarDay, error) {
	rows, err := s.db.Query(
		`SELECT date(started_at) as day,
		        COUNT(*) as total,
		        SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
		        SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) as failed,
		        SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END) as running
		 FROM runs
		 WHERE started_at >= NOW() - INTERVAL '1 day' * $1
		 GROUP BY day ORDER BY day`,
		days,
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

func (s *PostgresStore) GetRunCalendarByOrg(days int, orgID string) ([]CalendarDay, error) {
	query := `SELECT date(started_at) as day,
		COUNT(*) as total,
		SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END) as success,
		SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) as failed,
		SUM(CASE WHEN status = 'running' THEN 1 ELSE 0 END) as running
	 FROM runs WHERE started_at >= NOW() - INTERVAL '1 day' * $1`
	var args []interface{}
	args = append(args, days)
	if orgID != "" {
		query += ` AND org_id = $2`
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

// ── Connections (same implementation as SQLite, Postgres-compatible) ──

func (s *PostgresStore) CreateConnection(c *models.Connection) error {
	wsID := c.WorkspaceID
	if wsID == "" {
		wsID = "default"
	}
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
		`INSERT INTO connections (id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, workspace_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		c.ID, c.ConnID, c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		passEnc, extraEnc, passRef, extraRef, wsID, c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func (s *PostgresStore) GetConnection(connID string) (*models.Connection, error) {
	var c models.Connection
	err := s.db.QueryRow(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, created_at, updated_at
		 FROM connections WHERE conn_id = $1`, connID,
	).Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login,
		&c.Password, &c.Extra, &c.PasswordRef, &c.ExtraRef, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *PostgresStore) ListConnections() ([]models.Connection, error) {
	rows, err := s.db.Query(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, created_at, updated_at
		 FROM connections ORDER BY conn_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conns []models.Connection
	for rows.Next() {
		var c models.Connection
		if err := rows.Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login,
			&c.Password, &c.Extra, &c.PasswordRef, &c.ExtraRef, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, nil
}

func (s *PostgresStore) UpdateConnection(c *models.Connection) error {
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
		`UPDATE connections SET type=$1, description=$2, host=$3, port=$4, schema_name=$5, login=$6, password_enc=$7, extra_enc=$8, password_ref=$9, extra_ref=$10, updated_at=$11
		 WHERE conn_id = $12`,
		c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		passEnc, extraEnc, passRef, extraRef, c.UpdatedAt, c.ConnID,
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

func (s *PostgresStore) DeleteConnection(connID string) error {
	result, err := s.db.Exec(`DELETE FROM connections WHERE conn_id = $1`, connID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("connection not found: %s", connID)
	}
	return nil
}

// ── Variables ──

func (s *PostgresStore) SetVariable(v *models.Variable) error {
	wsID := v.WorkspaceID
	if wsID == "" {
		wsID = "default"
	}
	_, err := s.db.Exec(
		`INSERT INTO variables (key, value, type, description, workspace_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value, type=EXCLUDED.type, description=EXCLUDED.description, updated_at=EXCLUDED.updated_at`,
		v.Key, v.Value, v.Type, v.Description, wsID, v.CreatedAt, v.UpdatedAt,
	)
	return err
}

func (s *PostgresStore) GetVariable(key string) (*models.Variable, error) {
	var v models.Variable
	err := s.db.QueryRow(
		`SELECT key, value, type, description, created_at, updated_at FROM variables WHERE key = $1`, key,
	).Scan(&v.Key, &v.Value, &v.Type, &v.Description, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *PostgresStore) ListVariables() ([]models.Variable, error) {
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
		if err := rows.Scan(&v.Key, &v.Value, &v.Type, &v.Description, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		vars = append(vars, v)
	}
	return vars, nil
}

func (s *PostgresStore) DeleteVariable(key string) error {
	result, err := s.db.Exec(`DELETE FROM variables WHERE key = $1`, key)
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

func (s *PostgresStore) CreateAlert(a *models.Alert) error {
	_, err := s.db.Exec(
		`INSERT INTO alerts (id, org_id, kind, severity, title, body, pipeline_id, pipeline_name, run_id, created_at, read_at, dismissed_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		a.ID, a.OrgID, a.Kind, a.Severity, a.Title, a.Body,
		a.PipelineID, a.PipelineName, a.RunID, a.CreatedAt.UTC(), a.ReadAt, a.DismissedAt,
	)
	return err
}

func (s *PostgresStore) ListAlerts(orgID string, unreadOnly bool, limit int) ([]models.Alert, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `SELECT id, org_id, kind, severity, title, body, pipeline_id, pipeline_name, run_id, created_at, read_at, dismissed_at
		FROM alerts WHERE org_id = $1 AND dismissed_at IS NULL`
	if unreadOnly {
		query += " AND read_at IS NULL"
	}
	query += " ORDER BY created_at DESC LIMIT $2"

	rows, err := s.db.Query(query, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.Alert
	for rows.Next() {
		var a models.Alert
		var readAt, dismissedAt sql.NullTime
		if err := rows.Scan(&a.ID, &a.OrgID, &a.Kind, &a.Severity, &a.Title, &a.Body,
			&a.PipelineID, &a.PipelineName, &a.RunID, &a.CreatedAt, &readAt, &dismissedAt); err != nil {
			return nil, err
		}
		if readAt.Valid {
			t := readAt.Time
			a.ReadAt = &t
		}
		if dismissedAt.Valid {
			t := dismissedAt.Time
			a.DismissedAt = &t
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *PostgresStore) CountUnreadAlerts(orgID string) (int, error) {
	var n int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM alerts WHERE org_id = $1 AND read_at IS NULL AND dismissed_at IS NULL`, orgID,
	).Scan(&n)
	return n, err
}

func (s *PostgresStore) MarkAlertRead(orgID, id string) error {
	result, err := s.db.Exec(
		`UPDATE alerts SET read_at = NOW() WHERE id = $1 AND org_id = $2 AND read_at IS NULL`, id, orgID,
	)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE id = $1 AND org_id = $2`, id, orgID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("alert not found: %s", id)
		}
	}
	return nil
}

func (s *PostgresStore) MarkAllAlertsRead(orgID string) error {
	_, err := s.db.Exec(`UPDATE alerts SET read_at = NOW() WHERE org_id = $1 AND read_at IS NULL`, orgID)
	return err
}

func (s *PostgresStore) DismissAlert(orgID, id string) error {
	result, err := s.db.Exec(
		`UPDATE alerts SET dismissed_at = NOW() WHERE id = $1 AND org_id = $2 AND dismissed_at IS NULL`, id, orgID,
	)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return fmt.Errorf("alert not found: %s", id)
	}
	return nil
}

// ListDLQByOrg — see the matching SQLite method for why scope is derived by
// joining through the owning pipeline.
func (s *PostgresStore) ListDLQByOrg(orgID string, includeResolved bool, limit int) ([]DLQEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	query := `SELECT d.id, d.pipeline_id, d.run_id, d.error, d.node_id, d.node_name, d.payload,
		d.created_at, d.resolved, COALESCE(d.resolved_at::text,''), COALESCE(p.name,'')
		FROM dead_letter_queue d JOIN pipelines p ON p.id = d.pipeline_id
		WHERE p.org_id = $1`
	if !includeResolved {
		query += " AND d.resolved = FALSE"
	}
	query += " ORDER BY d.created_at DESC LIMIT $2"

	rows, err := s.db.Query(query, orgID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []DLQEntry
	for rows.Next() {
		var e DLQEntry
		if err := rows.Scan(&e.ID, &e.PipelineID, &e.RunID, &e.Error, &e.NodeID, &e.NodeName,
			&e.Payload, &e.CreatedAt, &e.Resolved, &e.ResolvedAt, &e.PipelineName); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// --- Pipeline Templates ---

func (s *PostgresStore) CreatePipelineTemplate(t *models.PipelineTemplate) error {
	nodesJSON, err := json.Marshal(t.Nodes)
	if err != nil {
		return fmt.Errorf("marshal nodes: %w", err)
	}
	edgesJSON, err := json.Marshal(t.Edges)
	if err != nil {
		return fmt.Errorf("marshal edges: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO pipeline_templates (id, name, description, icon, nodes, edges, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		t.ID, t.Name, t.Description, t.Icon, nodesJSON, edgesJSON, t.CreatedAt.UTC(), t.UpdatedAt.UTC(),
	)
	return err
}

func (s *PostgresStore) GetPipelineTemplate(id string) (*models.PipelineTemplate, error) {
	var t models.PipelineTemplate
	var nodesJSON, edgesJSON []byte
	err := s.db.QueryRow(
		`SELECT id, name, description, icon, nodes, edges, created_at, updated_at FROM pipeline_templates WHERE id = $1`, id,
	).Scan(&t.ID, &t.Name, &t.Description, &t.Icon, &nodesJSON, &edgesJSON, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(nodesJSON, &t.Nodes); err != nil {
		return nil, fmt.Errorf("decode nodes: %w", err)
	}
	if err := json.Unmarshal(edgesJSON, &t.Edges); err != nil {
		return nil, fmt.Errorf("decode edges: %w", err)
	}
	return &t, nil
}

func (s *PostgresStore) ListPipelineTemplates() ([]models.PipelineTemplate, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, icon, nodes, edges, created_at, updated_at FROM pipeline_templates ORDER BY created_at`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []models.PipelineTemplate
	for rows.Next() {
		var t models.PipelineTemplate
		var nodesJSON, edgesJSON []byte
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Icon, &nodesJSON, &edgesJSON, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(nodesJSON, &t.Nodes); err != nil {
			return nil, fmt.Errorf("decode nodes: %w", err)
		}
		if err := json.Unmarshal(edgesJSON, &t.Edges); err != nil {
			return nil, fmt.Errorf("decode edges: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpdatePipelineTemplate(t *models.PipelineTemplate) error {
	nodesJSON, err := json.Marshal(t.Nodes)
	if err != nil {
		return fmt.Errorf("marshal nodes: %w", err)
	}
	edgesJSON, err := json.Marshal(t.Edges)
	if err != nil {
		return fmt.Errorf("marshal edges: %w", err)
	}
	result, err := s.db.Exec(
		`UPDATE pipeline_templates SET name = $1, description = $2, icon = $3, nodes = $4, edges = $5, updated_at = $6 WHERE id = $7`,
		t.Name, t.Description, t.Icon, nodesJSON, edgesJSON, t.UpdatedAt.UTC(), t.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("template not found: %s", t.ID)
	}
	return nil
}

func (s *PostgresStore) DeletePipelineTemplate(id string) error {
	result, err := s.db.Exec(`DELETE FROM pipeline_templates WHERE id = $1`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return fmt.Errorf("template not found: %s", id)
	}
	return nil
}

// ── Workspaces (Postgres) ──

func (s *PostgresStore) CreateWorkspace(w *models.Workspace) error {
	_, err := s.db.Exec(`INSERT INTO workspaces (id, name, slug, description, created_at, updated_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		w.ID, w.Name, w.Slug, w.Description, w.CreatedAt, w.UpdatedAt)
	return err
}
func (s *PostgresStore) GetWorkspace(id string) (*models.Workspace, error) {
	var w models.Workspace
	err := s.db.QueryRow(`SELECT id,name,slug,description,created_at,updated_at FROM workspaces WHERE id=$1`, id).
		Scan(&w.ID, &w.Name, &w.Slug, &w.Description, &w.CreatedAt, &w.UpdatedAt)
	return &w, err
}
func (s *PostgresStore) ListWorkspaces() ([]models.Workspace, error) {
	rows, err := s.db.Query(`SELECT id,name,slug,description,created_at,updated_at FROM workspaces ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ws []models.Workspace
	for rows.Next() {
		var w models.Workspace
		rows.Scan(&w.ID, &w.Name, &w.Slug, &w.Description, &w.CreatedAt, &w.UpdatedAt)
		ws = append(ws, w)
	}
	return ws, nil
}
func (s *PostgresStore) DeleteWorkspace(id string) error {
	_, err := s.db.Exec(`DELETE FROM workspaces WHERE id=$1`, id)
	return err
}
func (s *PostgresStore) AddWorkspaceMember(m *models.WorkspaceMember) error {
	_, err := s.db.Exec(`INSERT INTO workspace_members (workspace_id,user_id,username,role,joined_at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT(workspace_id,user_id) DO UPDATE SET role=EXCLUDED.role`,
		m.WorkspaceID, m.UserID, m.Username, m.Role, m.JoinedAt)
	return err
}
func (s *PostgresStore) RemoveWorkspaceMember(workspaceID, userID string) error {
	_, err := s.db.Exec(`DELETE FROM workspace_members WHERE workspace_id=$1 AND user_id=$2`, workspaceID, userID)
	return err
}
func (s *PostgresStore) ListWorkspaceMembers(workspaceID string) ([]models.WorkspaceMember, error) {
	rows, err := s.db.Query(`SELECT workspace_id,user_id,username,role,joined_at FROM workspace_members WHERE workspace_id=$1`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ms []models.WorkspaceMember
	for rows.Next() {
		var m models.WorkspaceMember
		rows.Scan(&m.WorkspaceID, &m.UserID, &m.Username, &m.Role, &m.JoinedAt)
		ms = append(ms, m)
	}
	return ms, nil
}
func (s *PostgresStore) GetUserWorkspaces(userID string) ([]models.Workspace, error) {
	rows, err := s.db.Query(`SELECT w.id,w.name,w.slug,w.description,w.created_at,w.updated_at FROM workspaces w JOIN workspace_members wm ON w.id=wm.workspace_id WHERE wm.user_id=$1`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ws []models.Workspace
	for rows.Next() {
		var w models.Workspace
		rows.Scan(&w.ID, &w.Name, &w.Slug, &w.Description, &w.CreatedAt, &w.UpdatedAt)
		ws = append(ws, w)
	}
	return ws, nil
}
func (s *PostgresStore) CreateAPIToken(t *models.APIToken) error {
	_, err := s.db.Exec(`INSERT INTO api_tokens (id,name,token_hash,workspace_id,user_id,role,expires_at,created_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		t.ID, t.Name, t.TokenHash, t.WorkspaceID, t.UserID, t.Role, t.ExpiresAt, t.CreatedAt)
	return err
}
func (s *PostgresStore) GetAPITokenByHash(hash string) (*models.APIToken, error) {
	var t models.APIToken
	err := s.db.QueryRow(`SELECT id,name,token_hash,workspace_id,user_id,role,expires_at,created_at,last_used_at FROM api_tokens WHERE token_hash=$1`, hash).
		Scan(&t.ID, &t.Name, &t.TokenHash, &t.WorkspaceID, &t.UserID, &t.Role, &t.ExpiresAt, &t.CreatedAt, &t.LastUsedAt)
	return &t, err
}
func (s *PostgresStore) ListAPITokens(workspaceID string) ([]models.APIToken, error) {
	rows, err := s.db.Query(`SELECT id,name,workspace_id,user_id,role,expires_at,created_at FROM api_tokens WHERE workspace_id=$1 ORDER BY created_at DESC`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ts []models.APIToken
	for rows.Next() {
		var t models.APIToken
		rows.Scan(&t.ID, &t.Name, &t.WorkspaceID, &t.UserID, &t.Role, &t.ExpiresAt, &t.CreatedAt)
		ts = append(ts, t)
	}
	return ts, nil
}
func (s *PostgresStore) DeleteAPIToken(id string) error {
	_, err := s.db.Exec(`DELETE FROM api_tokens WHERE id=$1`, id)
	return err
}

func (s *PostgresStore) ListPipelinesByWorkspace(workspaceID string) ([]models.Pipeline, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
		 FROM pipelines WHERE workspace_id = $1 ORDER BY created_at DESC`, workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pipelines []models.Pipeline
	for rows.Next() {
		p, err := s.scanPipelineRow(rows)
		if err != nil {
			return nil, err
		}
		pipelines = append(pipelines, *p)
	}
	return pipelines, rows.Err()
}

func (s *PostgresStore) ListPipelinesByOrg(orgID string) ([]models.Pipeline, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
		 FROM pipelines WHERE org_id = $1 ORDER BY created_at DESC`, orgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var pipelines []models.Pipeline
	for rows.Next() {
		p, err := s.scanPipelineRow(rows)
		if err != nil {
			return nil, err
		}
		pipelines = append(pipelines, *p)
	}
	return pipelines, rows.Err()
}

func (s *PostgresStore) ListPipelinesByOrgPaged(orgID string, limit, offset int) ([]models.Pipeline, int, error) {
	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM pipelines WHERE org_id = $1`, orgID).Scan(&total)
	rows, err := s.db.Query(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
		 FROM pipelines WHERE org_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, orgID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var pipelines []models.Pipeline
	for rows.Next() {
		p, err := s.scanPipelineRow(rows)
		if err != nil {
			return nil, 0, err
		}
		pipelines = append(pipelines, *p)
	}
	return pipelines, total, rows.Err()
}

func (s *PostgresStore) ListPipelinesByOrgCursor(orgID string, afterID string, limit int) ([]models.Pipeline, bool, error) {
	var rows *sql.Rows
	var err error
	fetchN := limit + 1 // fetch one extra to detect has_next
	if afterID == "" {
		rows, err = s.db.Query(
			`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
			 FROM pipelines WHERE org_id = $1 ORDER BY id DESC LIMIT $2`, orgID, fetchN)
	} else {
		rows, err = s.db.Query(
			`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
			 FROM pipelines WHERE org_id = $1 AND id < $2 ORDER BY id DESC LIMIT $3`, orgID, afterID, fetchN)
	}
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var pipelines []models.Pipeline
	for rows.Next() {
		p, err := s.scanPipelineRow(rows)
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

func (s *PostgresStore) ListConnectionsByWorkspacePaged(workspaceID string, limit, offset int) ([]models.Connection, int, error) {
	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM connections WHERE workspace_id = $1`, workspaceID).Scan(&total)
	rows, err := s.db.Query(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, created_at, updated_at
		 FROM connections WHERE workspace_id = $1 ORDER BY conn_id LIMIT $2 OFFSET $3`, workspaceID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var conns []models.Connection
	for rows.Next() {
		var c models.Connection
		rows.Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login, &c.Password, &c.Extra, &c.PasswordRef, &c.ExtraRef, &c.CreatedAt, &c.UpdatedAt)
		conns = append(conns, c)
	}
	return conns, total, rows.Err()
}

func (s *PostgresStore) ListVariablesByWorkspacePaged(workspaceID string, limit, offset int) ([]models.Variable, int, error) {
	var total int
	s.db.QueryRow(`SELECT COUNT(*) FROM variables WHERE workspace_id = $1`, workspaceID).Scan(&total)
	rows, err := s.db.Query(
		`SELECT key, value, type, description, created_at, updated_at FROM variables WHERE workspace_id = $1 ORDER BY key LIMIT $2 OFFSET $3`, workspaceID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var vars []models.Variable
	for rows.Next() {
		var v models.Variable
		rows.Scan(&v.Key, &v.Value, &v.Type, &v.Description, &v.CreatedAt, &v.UpdatedAt)
		vars = append(vars, v)
	}
	return vars, total, rows.Err()
}

func (s *PostgresStore) ListConnectionsByWorkspace(workspaceID string) ([]models.Connection, error) {
	rows, err := s.db.Query(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, created_at, updated_at
		 FROM connections WHERE workspace_id = $1 ORDER BY conn_id`, workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conns []models.Connection
	for rows.Next() {
		var c models.Connection
		if err := rows.Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login,
			&c.Password, &c.Extra, &c.PasswordRef, &c.ExtraRef, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, rows.Err()
}

func (s *PostgresStore) ListVariablesByWorkspace(workspaceID string) ([]models.Variable, error) {
	rows, err := s.db.Query(
		`SELECT key, value, type, description, created_at, updated_at FROM variables WHERE workspace_id = $1 ORDER BY key`, workspaceID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var vars []models.Variable
	for rows.Next() {
		var v models.Variable
		if err := rows.Scan(&v.Key, &v.Value, &v.Type, &v.Description, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		vars = append(vars, v)
	}
	return vars, rows.Err()
}

// --- Node Profiles ---

func (s *PostgresStore) SaveNodeProfile(runID, nodeID, profileJSON, schemaJSON, driftJSON string) error {
	_, err := s.db.Exec(
		`INSERT INTO node_profiles (run_id, node_id, profile, schema_snapshot, drift_alerts, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6)
		 ON CONFLICT (run_id, node_id) DO UPDATE SET profile=$3, schema_snapshot=$4, drift_alerts=$5`,
		runID, nodeID, profileJSON, schemaJSON, driftJSON, time.Now(),
	)
	return err
}

func (s *PostgresStore) GetNodeProfile(runID, nodeID string) (string, string, string, error) {
	var profile, schema, drift string
	err := s.db.QueryRow(
		`SELECT profile, schema_snapshot, drift_alerts FROM node_profiles WHERE run_id=$1 AND node_id=$2`,
		runID, nodeID,
	).Scan(&profile, &schema, &drift)
	return profile, schema, drift, err
}

func (s *PostgresStore) GetLatestNodeProfile(pipelineID, nodeID string) (string, string, error) {
	var profile, schema string
	err := s.db.QueryRow(
		`SELECT np.profile, np.schema_snapshot FROM node_profiles np
		 JOIN runs r ON r.id = np.run_id
		 WHERE r.pipeline_id=$1 AND np.node_id=$2
		 ORDER BY np.created_at DESC LIMIT 1`, pipelineID, nodeID,
	).Scan(&profile, &schema)
	return profile, schema, err
}

// --- Settings ---

func (s *PostgresStore) GetSetting(key string) (string, error) {
	var val string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key=$1`, key).Scan(&val)
	if err != nil {
		return "", nil
	}
	return val, nil
}

func (s *PostgresStore) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES ($1,$2) ON CONFLICT(key) DO UPDATE SET value=$2`,
		key, value,
	)
	return err
}

// --- Roles ---

func (s *PostgresStore) CreateRole(r *models.Role) error {
	permsJSON, err := json.Marshal(r.Permissions)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO roles (id, name, description, permissions, is_system, created_at) VALUES ($1,$2,$3,$4,$5,$6)`,
		r.ID, r.Name, r.Description, string(permsJSON), r.IsSystem, r.CreatedAt,
	)
	return err
}

func (s *PostgresStore) GetRole(id string) (*models.Role, error) {
	var r models.Role
	var permsJSON string
	err := s.db.QueryRow(
		`SELECT id, name, description, permissions, is_system, created_at FROM roles WHERE id=$1`, id,
	).Scan(&r.ID, &r.Name, &r.Description, &permsJSON, &r.IsSystem, &r.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(permsJSON), &r.Permissions); err != nil {
		return nil, fmt.Errorf("unmarshal permissions: %w", err)
	}
	return &r, nil
}

func (s *PostgresStore) ListRoles() ([]models.Role, error) {
	rows, err := s.db.Query(`SELECT id, name, description, permissions, is_system, created_at FROM roles ORDER BY is_system DESC, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []models.Role
	for rows.Next() {
		var r models.Role
		var permsJSON string
		if err := rows.Scan(&r.ID, &r.Name, &r.Description, &permsJSON, &r.IsSystem, &r.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(permsJSON), &r.Permissions)
		roles = append(roles, r)
	}
	return roles, rows.Err()
}

func (s *PostgresStore) UpdateRole(r *models.Role) error {
	permsJSON, err := json.Marshal(r.Permissions)
	if err != nil {
		return fmt.Errorf("marshal permissions: %w", err)
	}
	result, err := s.db.Exec(
		`UPDATE roles SET name=$1, description=$2, permissions=$3 WHERE id=$4 AND is_system=false`,
		r.Name, r.Description, string(permsJSON), r.ID,
	)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		var exists int
		s.db.QueryRow("SELECT COUNT(*) FROM roles WHERE id=$1", r.ID).Scan(&exists)
		if exists > 0 {
			return fmt.Errorf("cannot modify system role")
		}
		return fmt.Errorf("role not found: %s", r.ID)
	}
	return nil
}

func (s *PostgresStore) DeleteRole(id string) error {
	result, err := s.db.Exec("DELETE FROM roles WHERE id=$1 AND is_system=false", id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		var exists int
		s.db.QueryRow("SELECT COUNT(*) FROM roles WHERE id=$1", id).Scan(&exists)
		if exists > 0 {
			return fmt.Errorf("cannot delete system role")
		}
		return fmt.Errorf("role not found: %s", id)
	}
	return nil
}
