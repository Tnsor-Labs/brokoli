package store

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
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
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN sla_deadline TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN sla_timezone TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN depends_on TEXT NOT NULL DEFAULT '[]'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN dependency_rules TEXT NOT NULL DEFAULT '[]'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN webhook_token TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN pipeline_id TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN source TEXT NOT NULL DEFAULT 'ui'`)
	s.db.Exec(`ALTER TABLE pipelines ADD COLUMN org_id TEXT NOT NULL DEFAULT ''`)
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
	// Migrate legacy encrypted values: copy password_enc → password_ref
	// with encrypted:// prefix so the resolver chain handles them.
	s.db.Exec(`UPDATE connections SET password_ref = 'encrypted://' || password_enc WHERE password_enc != '' AND password_ref = ''`)
	s.db.Exec(`UPDATE connections SET extra_ref = 'encrypted://' || extra_enc WHERE extra_enc != '' AND extra_ref = ''`)

	// Tracing & observability columns
	s.db.Exec(`ALTER TABLE runs ADD COLUMN trace_id TEXT NOT NULL DEFAULT ''`)
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
	nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON, depRulesJSON []byte
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
	return &pipelineFields{nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON, depRulesJSON}, nil
}

func (s *SQLiteStore) CreatePipeline(p *models.Pipeline) error {
	f, err := marshalPipelineJSON(p)
	if err != nil {
		return wrapStoreErr("CreatePipeline", p.ID, err)
	}
	_, err = s.db.Exec(
		`INSERT INTO pipelines (id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, string(f.nodesJSON), string(f.edgesJSON),
		p.Schedule, p.ScheduleTimezone, p.WebhookURL, string(f.paramsJSON), string(f.tagsJSON), p.SLADeadline, p.SLATimezone, string(f.depsJSON), string(f.depRulesJSON), p.WebhookToken, boolToInt(p.Enabled), p.CreatedAt.UTC().Format(timeFormat), p.UpdatedAt.UTC().Format(timeFormat), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID,
	)
	return wrapStoreErr("CreatePipeline", p.ID, err)
}

func (s *SQLiteStore) GetPipeline(id string) (*models.Pipeline, error) {
	row := s.db.QueryRow(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
			`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
			 FROM pipelines WHERE org_id = ? ORDER BY id DESC LIMIT ?`, orgID, fetchN)
	} else {
		rows, err = s.db.Query(
			`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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
		`UPDATE pipelines SET name=?, description=?, nodes=?, edges=?, schedule=?, schedule_timezone=?, webhook_url=?, params=?, tags=?, sla_deadline=?, sla_timezone=?, depends_on=?, dependency_rules=?, webhook_token=?, enabled=?, updated_at=?, pipeline_id=?, source=?, workspace_id=?, org_id=?
		 WHERE id=?`,
		p.Name, p.Description, string(f.nodesJSON), string(f.edgesJSON),
		p.Schedule, p.ScheduleTimezone, p.WebhookURL, string(f.paramsJSON), string(f.tagsJSON), p.SLADeadline, p.SLATimezone, string(f.depsJSON), string(f.depRulesJSON), p.WebhookToken, boolToInt(p.Enabled), p.UpdatedAt.UTC().Format(timeFormat), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID, p.ID,
	)
	if err != nil {
		return wrapStoreErr("UpdatePipeline", p.ID, err)
	}
	return wrapStoreErr("UpdatePipeline", p.ID, checkRowsAffected(result, "pipeline", p.ID))
}

func (s *SQLiteStore) GetPipelineByPipelineID(pipelineID string) (*models.Pipeline, error) {
	row := s.db.QueryRow(
		`SELECT id, name, description, nodes, edges, schedule, schedule_timezone, webhook_url, params, tags, sla_deadline, sla_timezone, depends_on, dependency_rules, webhook_token, enabled, created_at, updated_at, pipeline_id, source, workspace_id, org_id
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

	// Per-pipeline latest run via a correlated subquery — avoids loading every run.
	query := `
		SELECT id, pipeline_id, status, started_at, finished_at, trace_id
		FROM runs r
		WHERE pipeline_id IN (` + strings.Join(placeholders, ",") + `)
		  AND started_at = (
		      SELECT MAX(started_at) FROM runs WHERE pipeline_id = r.pipeline_id
		  )
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
		`UPDATE pipelines SET name=?, description=?, nodes=?, edges=?, schedule=?, schedule_timezone=?, webhook_url=?, params=?, tags=?, sla_deadline=?, sla_timezone=?, depends_on=?, dependency_rules=?, webhook_token=?, enabled=?, updated_at=?, pipeline_id=?, source=?, workspace_id=?, org_id=?
		 WHERE id=?`,
		p.Name, p.Description, string(f.nodesJSON), string(f.edgesJSON),
		p.Schedule, p.ScheduleTimezone, p.WebhookURL, string(f.paramsJSON), string(f.tagsJSON), p.SLADeadline, p.SLATimezone, string(f.depsJSON), string(f.depRulesJSON), p.WebhookToken, boolToInt(p.Enabled), p.UpdatedAt.UTC().Format(timeFormat), p.PipelineID, p.Source, p.WorkspaceID, p.OrgID, p.ID,
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
	_, err := s.db.Exec(
		`INSERT INTO runs (id, pipeline_id, status, started_at, finished_at, trace_id) VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.PipelineID, string(r.Status), formatTimePtr(r.StartedAt), formatTimePtr(r.FinishedAt), r.TraceID,
	)
	return wrapStoreErr("CreateRun", r.ID, err)
}

func (s *SQLiteStore) GetRun(id string) (*models.Run, error) {
	row := s.db.QueryRow(
		`SELECT id, pipeline_id, status, started_at, finished_at, trace_id FROM runs WHERE id = ?`, id,
	)
	r, err := scanRun(row)
	if err != nil {
		return nil, wrapStoreErr("GetRun", id, err)
	}

	nodeRuns, err := s.ListNodeRunsByRun(id)
	if err != nil {
		return nil, wrapStoreErr("GetRun", id, err)
	}
	r.NodeRuns = nodeRuns
	return r, nil
}

func (s *SQLiteStore) ListRunsByPipeline(pipelineID string, limit int) ([]models.Run, error) {
	rows, err := s.db.Query(
		`SELECT id, pipeline_id, status, started_at, finished_at, trace_id
		 FROM runs WHERE pipeline_id = ? ORDER BY started_at DESC LIMIT ?`,
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

func (s *SQLiteStore) UpdateRun(r *models.Run) error {
	result, err := s.db.Exec(
		`UPDATE runs SET status=?, started_at=?, finished_at=?, trace_id=? WHERE id=?`,
		string(r.Status), formatTimePtr(r.StartedAt), formatTimePtr(r.FinishedAt), r.TraceID, r.ID,
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

func (s *SQLiteStore) SavePipelineVersion(pipelineID string, snapshot string, message string) (int, error) {
	// Get next version number
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
	return nextVer, err
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
	var nodesJSON, edgesJSON, paramsJSON, tagsJSON, depsJSON, depRulesJSON, createdAt, updatedAt string
	var enabled int

	if err := sc.Scan(&p.ID, &p.Name, &p.Description, &nodesJSON, &edgesJSON, &p.Schedule, &p.ScheduleTimezone, &p.WebhookURL, &paramsJSON, &tagsJSON, &p.SLADeadline, &p.SLATimezone, &depsJSON, &depRulesJSON, &p.WebhookToken, &enabled, &createdAt, &updatedAt, &p.PipelineID, &p.Source, &p.WorkspaceID, &p.OrgID); err != nil {
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

	if err := sc.Scan(&r.ID, &r.PipelineID, &status, &startedAt, &finishedAt, &r.TraceID); err != nil {
		return nil, err
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
		`INSERT INTO connections (id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, workspace_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ConnID, c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		passEnc, extraEnc, passRef, extraRef, wsID,
		c.CreatedAt.UTC().Format(timeFormat), c.UpdatedAt.UTC().Format(timeFormat),
	)
	return err
}

func (s *SQLiteStore) GetConnection(connID string) (*models.Connection, error) {
	row := s.db.QueryRow(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, created_at, updated_at
		 FROM connections WHERE conn_id = ?`, connID,
	)
	return scanConnection(row)
}

func (s *SQLiteStore) ListConnections() ([]models.Connection, error) {
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
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login,
			&c.Password, &c.Extra, &c.PasswordRef, &c.ExtraRef, &createdAt, &updatedAt); err != nil {
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
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, password_ref, extra_ref, created_at, updated_at
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
			&c.Password, &c.Extra, &c.PasswordRef, &c.ExtraRef, &createdAt, &updatedAt); err != nil {
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
		`UPDATE connections SET type=?, description=?, host=?, port=?, schema_name=?, login=?, password_enc=?, extra_enc=?, password_ref=?, extra_ref=?, updated_at=?
		 WHERE conn_id = ?`,
		c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		passEnc, extraEnc, passRef, extraRef,
		c.UpdatedAt.UTC().Format(timeFormat), c.ConnID,
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
		&c.Password, &c.Extra, &c.PasswordRef, &c.ExtraRef, &createdAt, &updatedAt); err != nil {
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
