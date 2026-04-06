package store

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
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
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_org ON runs(org_id)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_runs_pipeline_status ON runs(pipeline_id, status, started_at DESC)`)
	s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_node_runs_run_status ON node_runs(run_id, status)`)

	// Pipelines tags column
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN IF NOT EXISTS tags JSONB NOT NULL DEFAULT '[]'`)

	// Default workspace seed
	s.db.Exec(`INSERT INTO workspaces (id, name, slug, description, created_at, updated_at)
		VALUES ('default', 'Default', 'default', 'Default workspace', NOW(), NOW())
		ON CONFLICT (id) DO NOTHING`)

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
	_, err := s.db.Exec(
		`INSERT INTO pipelines (id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		p.ID, p.Name, p.Description, nodesJSON, edgesJSON,
		p.Schedule, p.ScheduleTimezone, p.WebhookURL, paramsJSON, tagsJSON, p.SLADeadline, p.SLATimezone, depsJSON, p.WebhookToken, p.Enabled, p.CreatedAt.UTC(), p.UpdatedAt.UTC(), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID,
	)
	return err
}

func (s *PostgresStore) GetPipeline(id string) (*models.Pipeline, error) {
	var p models.Pipeline
	var nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON []byte
	err := s.db.QueryRow(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
		 FROM pipelines WHERE id = $1`, id,
	).Scan(&p.ID, &p.Name, &p.Description, &nodesJSON, &edgesJSON,
		&p.Schedule, &p.ScheduleTimezone, &p.WebhookURL, &paramsJSON, &tagsJSON, &p.SLADeadline, &p.SLATimezone, &depsJSON, &p.WebhookToken, &p.Enabled, &p.CreatedAt, &p.UpdatedAt, &p.PipelineID, &p.Source, &p.WorkspaceID, &p.OrgID)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(nodesJSON, &p.Nodes)
	json.Unmarshal(edgesJSON, &p.Edges)
	json.Unmarshal(paramsJSON, &p.Params)
	json.Unmarshal(tagsJSON, &p.Tags)
	json.Unmarshal(depsJSON, &p.DependsOn)
	if p.Tags == nil {
		p.Tags = []string{}
	}
	return &p, nil
}

func (s *PostgresStore) ListPipelines() ([]models.Pipeline, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
	var nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON []byte
	if err := sc.Scan(&p.ID, &p.Name, &p.Description, &nodesJSON, &edgesJSON,
		&p.Schedule, &p.ScheduleTimezone, &p.WebhookURL, &paramsJSON, &tagsJSON, &p.SLADeadline, &p.SLATimezone, &depsJSON, &p.WebhookToken, &p.Enabled, &p.CreatedAt, &p.UpdatedAt, &p.PipelineID, &p.Source, &p.WorkspaceID, &p.OrgID); err != nil {
		return nil, err
	}
	json.Unmarshal(nodesJSON, &p.Nodes)
	json.Unmarshal(edgesJSON, &p.Edges)
	json.Unmarshal(paramsJSON, &p.Params)
	json.Unmarshal(tagsJSON, &p.Tags)
	json.Unmarshal(depsJSON, &p.DependsOn)
	if p.Tags == nil {
		p.Tags = []string{}
	}
	return &p, nil
}

func (s *PostgresStore) UpdatePipeline(p *models.Pipeline) error {
	nodesJSON, _ := json.Marshal(p.Nodes)
	edgesJSON, _ := json.Marshal(p.Edges)
	paramsJSON, _ := json.Marshal(p.Params)
	tagsJSON, _ := json.Marshal(p.Tags)
	depsJSON, _ := json.Marshal(p.DependsOn)
	result, err := s.db.Exec(
		`UPDATE pipelines SET name=$1, description=$2, nodes=$3, edges=$4, schedule=$5, schedule_timezone=$6,
		 webhook_url=$7, params=$8, tags=$9, sla_deadline=$10, sla_timezone=$11, depends_on=$12, webhook_token=$13, enabled=$14, updated_at=$15, pipeline_id=$16, source=$17, workspace_id=$18, org_id=$19 WHERE id=$20`,
		p.Name, p.Description, nodesJSON, edgesJSON, p.Schedule, p.ScheduleTimezone,
		p.WebhookURL, paramsJSON, tagsJSON, p.SLADeadline, p.SLATimezone, depsJSON, p.WebhookToken, p.Enabled, p.UpdatedAt.UTC(), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID, p.ID,
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
	var nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON []byte
	err := s.db.QueryRow(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
		 FROM pipelines WHERE pipeline_id = $1`, pipelineID,
	).Scan(&p.ID, &p.Name, &p.Description, &nodesJSON, &edgesJSON,
		&p.Schedule, &p.ScheduleTimezone, &p.WebhookURL, &paramsJSON, &tagsJSON, &p.SLADeadline, &p.SLATimezone, &depsJSON, &p.WebhookToken, &p.Enabled, &p.CreatedAt, &p.UpdatedAt, &p.PipelineID, &p.Source, &p.WorkspaceID, &p.OrgID)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(nodesJSON, &p.Nodes)
	json.Unmarshal(edgesJSON, &p.Edges)
	json.Unmarshal(paramsJSON, &p.Params)
	json.Unmarshal(tagsJSON, &p.Tags)
	json.Unmarshal(depsJSON, &p.DependsOn)
	if p.Tags == nil {
		p.Tags = []string{}
	}
	return &p, nil
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
	_, err := s.db.Exec(
		`INSERT INTO runs (id, pipeline_id, status, started_at, finished_at) VALUES ($1,$2,$3,$4,$5)`,
		r.ID, r.PipelineID, string(r.Status), r.StartedAt, r.FinishedAt,
	)
	return err
}

func (s *PostgresStore) GetRun(id string) (*models.Run, error) {
	var r models.Run
	var status string
	err := s.db.QueryRow(
		`SELECT id, pipeline_id, status, started_at, finished_at FROM runs WHERE id = $1`, id,
	).Scan(&r.ID, &r.PipelineID, &status, &r.StartedAt, &r.FinishedAt)
	if err != nil {
		return nil, err
	}
	r.Status = models.RunStatus(status)

	nodeRuns, err := s.ListNodeRunsByRun(id)
	if err != nil {
		return nil, err
	}
	r.NodeRuns = nodeRuns
	return &r, nil
}

func (s *PostgresStore) ListRunsByPipeline(pipelineID string, limit int) ([]models.Run, error) {
	rows, err := s.db.Query(
		`SELECT id, pipeline_id, status, started_at, finished_at
		 FROM runs WHERE pipeline_id = $1 ORDER BY started_at DESC LIMIT $2`,
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
		if err := rows.Scan(&r.ID, &r.PipelineID, &status, &r.StartedAt, &r.FinishedAt); err != nil {
			return nil, err
		}
		r.Status = models.RunStatus(status)
		runs = append(runs, r)
	}
	return runs, rows.Err()
}

func (s *PostgresStore) UpdateRun(r *models.Run) error {
	_, err := s.db.Exec(
		`UPDATE runs SET status=$1, started_at=$2, finished_at=$3 WHERE id=$4`,
		string(r.Status), r.StartedAt, r.FinishedAt, r.ID,
	)
	return err
}

// --- Node Runs ---

func (s *PostgresStore) CreateNodeRun(nr *models.NodeRun) error {
	_, err := s.db.Exec(
		`INSERT INTO node_runs (id, run_id, node_id, status, row_count, started_at, duration_ms, error)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		nr.ID, nr.RunID, nr.NodeID, string(nr.Status), nr.RowCount, nr.StartedAt, nr.DurationMs, nr.Error,
	)
	return err
}

func (s *PostgresStore) UpdateNodeRun(nr *models.NodeRun) error {
	_, err := s.db.Exec(
		`UPDATE node_runs SET status=$1, row_count=$2, started_at=$3, duration_ms=$4, error=$5 WHERE id=$6`,
		string(nr.Status), nr.RowCount, nr.StartedAt, nr.DurationMs, nr.Error, nr.ID,
	)
	return err
}

func (s *PostgresStore) ListNodeRunsByRun(runID string) ([]models.NodeRun, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, node_id, status, row_count, started_at, duration_ms, error
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
		if err := rows.Scan(&nr.ID, &nr.RunID, &nr.NodeID, &status, &nr.RowCount, &nr.StartedAt, &nr.DurationMs, &nr.Error); err != nil {
			return nil, err
		}
		nr.Status = models.RunStatus(status)
		nodeRuns = append(nodeRuns, nr)
	}
	return nodeRuns, rows.Err()
}

// --- Logs ---

func (s *PostgresStore) AppendLog(entry *models.LogEntry) error {
	_, err := s.db.Exec(
		`INSERT INTO logs (run_id, node_id, level, message, timestamp) VALUES ($1,$2,$3,$4,$5)`,
		entry.RunID, entry.NodeID, string(entry.Level), entry.Message, entry.Timestamp,
	)
	return err
}

func (s *PostgresStore) GetLogs(runID string) ([]models.LogEntry, error) {
	rows, err := s.db.Query(
		`SELECT run_id, node_id, level, message, timestamp FROM logs WHERE run_id = $1 ORDER BY timestamp`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []models.LogEntry
	for rows.Next() {
		var entry models.LogEntry
		var level string
		if err := rows.Scan(&entry.RunID, &entry.NodeID, &level, &entry.Message, &entry.Timestamp); err != nil {
			return nil, err
		}
		entry.Level = models.LogLevel(level)
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

func (s *PostgresStore) SavePipelineVersion(pipelineID string, snapshot string, message string) (int, error) {
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
	return nextVer, err
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
	_, err := s.db.Exec(
		`INSERT INTO connections (id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, workspace_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		c.ID, c.ConnID, c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		c.Password, c.Extra, wsID, c.CreatedAt, c.UpdatedAt,
	)
	return err
}

func (s *PostgresStore) GetConnection(connID string) (*models.Connection, error) {
	var c models.Connection
	err := s.db.QueryRow(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, created_at, updated_at
		 FROM connections WHERE conn_id = $1`, connID,
	).Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login,
		&c.Password, &c.Extra, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (s *PostgresStore) ListConnections() ([]models.Connection, error) {
	rows, err := s.db.Query(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, created_at, updated_at
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
			&c.Password, &c.Extra, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, nil
}

func (s *PostgresStore) UpdateConnection(c *models.Connection) error {
	result, err := s.db.Exec(
		`UPDATE connections SET type=$1, description=$2, host=$3, port=$4, schema_name=$5, login=$6, password_enc=$7, extra_enc=$8, updated_at=$9
		 WHERE conn_id = $10`,
		c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		c.Password, c.Extra, c.UpdatedAt, c.ConnID,
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
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
			`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
			 FROM pipelines WHERE org_id = $1 ORDER BY id DESC LIMIT $2`, orgID, fetchN)
	} else {
		rows, err = s.db.Query(
			`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, created_at, updated_at
		 FROM connections WHERE workspace_id = $1 ORDER BY conn_id LIMIT $2 OFFSET $3`, workspaceID, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var conns []models.Connection
	for rows.Next() {
		var c models.Connection
		rows.Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login, &c.Password, &c.Extra, &c.CreatedAt, &c.UpdatedAt)
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
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, created_at, updated_at
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
			&c.Password, &c.Extra, &c.CreatedAt, &c.UpdatedAt); err != nil {
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
