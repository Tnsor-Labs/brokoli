package store

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hc12r/broked/models"
	"github.com/hc12r/brokolisql-go/pkg/common"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

const timeFormat = time.RFC3339Nano

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

	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}
	return s, nil
}

func (s *SQLiteStore) migrate() error {
	files := []string{"001_initial.sql", "002_connections.sql", "003_variables.sql"}
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

	return nil
}

func (s *SQLiteStore) Close() error       { return s.db.Close() }
func (s *SQLiteStore) RawDB() interface{} { return s.db }

// --- Pipelines ---

func (s *SQLiteStore) CreatePipeline(p *models.Pipeline) error {
	nodesJSON, err := json.Marshal(p.Nodes)
	if err != nil {
		return fmt.Errorf("marshal nodes: %w", err)
	}
	edgesJSON, err := json.Marshal(p.Edges)
	if err != nil {
		return fmt.Errorf("marshal edges: %w", err)
	}
	paramsJSON, err := json.Marshal(p.Params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	tagsJSON, _ := json.Marshal(p.Tags)
	if tagsJSON == nil {
		tagsJSON = []byte("[]")
	}

	_, err = s.db.Exec(
		`INSERT INTO pipelines (id, name, description, nodes, edges, schedule, webhook_url, params, tags, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Description, string(nodesJSON), string(edgesJSON),
		p.Schedule, p.WebhookURL, string(paramsJSON), string(tagsJSON), boolToInt(p.Enabled), p.CreatedAt.Format(timeFormat), p.UpdatedAt.Format(timeFormat),
	)
	return err
}

func (s *SQLiteStore) GetPipeline(id string) (*models.Pipeline, error) {
	row := s.db.QueryRow(
		`SELECT id, name, description, nodes, edges, schedule, webhook_url, params, tags, enabled, created_at, updated_at
		 FROM pipelines WHERE id = ?`, id,
	)
	return scanPipeline(row)
}

func (s *SQLiteStore) ListPipelines() ([]models.Pipeline, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, nodes, edges, schedule, webhook_url, params, tags, enabled, created_at, updated_at
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

func (s *SQLiteStore) UpdatePipeline(p *models.Pipeline) error {
	nodesJSON, err := json.Marshal(p.Nodes)
	if err != nil {
		return fmt.Errorf("marshal nodes: %w", err)
	}
	edgesJSON, err := json.Marshal(p.Edges)
	if err != nil {
		return fmt.Errorf("marshal edges: %w", err)
	}
	paramsJSON, _ := json.Marshal(p.Params)
	tagsJSON, _ := json.Marshal(p.Tags)
	if tagsJSON == nil {
		tagsJSON = []byte("[]")
	}

	result, err := s.db.Exec(
		`UPDATE pipelines SET name=?, description=?, nodes=?, edges=?, schedule=?, webhook_url=?, params=?, tags=?, enabled=?, updated_at=?
		 WHERE id=?`,
		p.Name, p.Description, string(nodesJSON), string(edgesJSON),
		p.Schedule, p.WebhookURL, string(paramsJSON), string(tagsJSON), boolToInt(p.Enabled), p.UpdatedAt.Format(timeFormat), p.ID,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(result, "pipeline", p.ID)
}

func (s *SQLiteStore) DeletePipeline(id string) error {
	result, err := s.db.Exec(`DELETE FROM pipelines WHERE id=?`, id)
	if err != nil {
		return err
	}
	return checkRowsAffected(result, "pipeline", id)
}

// --- Runs ---

func (s *SQLiteStore) CreateRun(r *models.Run) error {
	_, err := s.db.Exec(
		`INSERT INTO runs (id, pipeline_id, status, started_at, finished_at) VALUES (?, ?, ?, ?, ?)`,
		r.ID, r.PipelineID, string(r.Status), formatTimePtr(r.StartedAt), formatTimePtr(r.FinishedAt),
	)
	return err
}

func (s *SQLiteStore) GetRun(id string) (*models.Run, error) {
	row := s.db.QueryRow(
		`SELECT id, pipeline_id, status, started_at, finished_at FROM runs WHERE id = ?`, id,
	)
	r, err := scanRun(row)
	if err != nil {
		return nil, err
	}

	nodeRuns, err := s.ListNodeRunsByRun(id)
	if err != nil {
		return nil, err
	}
	r.NodeRuns = nodeRuns
	return r, nil
}

func (s *SQLiteStore) ListRunsByPipeline(pipelineID string, limit int) ([]models.Run, error) {
	rows, err := s.db.Query(
		`SELECT id, pipeline_id, status, started_at, finished_at
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
		`UPDATE runs SET status=?, started_at=?, finished_at=? WHERE id=?`,
		string(r.Status), formatTimePtr(r.StartedAt), formatTimePtr(r.FinishedAt), r.ID,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(result, "run", r.ID)
}

// --- Node Runs ---

func (s *SQLiteStore) CreateNodeRun(nr *models.NodeRun) error {
	_, err := s.db.Exec(
		`INSERT INTO node_runs (id, run_id, node_id, status, row_count, started_at, duration_ms, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		nr.ID, nr.RunID, nr.NodeID, string(nr.Status), nr.RowCount,
		formatTimePtr(nr.StartedAt), nr.DurationMs, nr.Error,
	)
	return err
}

func (s *SQLiteStore) UpdateNodeRun(nr *models.NodeRun) error {
	result, err := s.db.Exec(
		`UPDATE node_runs SET status=?, row_count=?, started_at=?, duration_ms=?, error=? WHERE id=?`,
		string(nr.Status), nr.RowCount, formatTimePtr(nr.StartedAt), nr.DurationMs, nr.Error, nr.ID,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(result, "node_run", nr.ID)
}

func (s *SQLiteStore) ListNodeRunsByRun(runID string) ([]models.NodeRun, error) {
	rows, err := s.db.Query(
		`SELECT id, run_id, node_id, status, row_count, started_at, duration_ms, error
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
		var startedAt sql.NullString
		if err := rows.Scan(&nr.ID, &nr.RunID, &nr.NodeID, &status, &nr.RowCount, &startedAt, &nr.DurationMs, &nr.Error); err != nil {
			return nil, err
		}
		nr.Status = models.RunStatus(status)
		nr.StartedAt = parseTimePtr(startedAt)
		nodeRuns = append(nodeRuns, nr)
	}
	return nodeRuns, rows.Err()
}

// --- Logs ---

func (s *SQLiteStore) AppendLog(entry *models.LogEntry) error {
	_, err := s.db.Exec(
		`INSERT INTO logs (run_id, node_id, level, message, timestamp) VALUES (?, ?, ?, ?, ?)`,
		entry.RunID, entry.NodeID, string(entry.Level), entry.Message, entry.Timestamp.Format(timeFormat),
	)
	return err
}

func (s *SQLiteStore) GetLogs(runID string) ([]models.LogEntry, error) {
	rows, err := s.db.Query(
		`SELECT run_id, node_id, level, message, timestamp FROM logs WHERE run_id = ? ORDER BY timestamp`, runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var logs []models.LogEntry
	for rows.Next() {
		var entry models.LogEntry
		var level, ts string
		if err := rows.Scan(&entry.RunID, &entry.NodeID, &level, &entry.Message, &ts); err != nil {
			return nil, err
		}
		entry.Level = models.LogLevel(level)
		entry.Timestamp, _ = time.Parse(timeFormat, ts)
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
		pipelineID, nextVer, snapshot, message, time.Now().Format(timeFormat),
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

// --- Maintenance ---

func (s *SQLiteStore) PurgeRunsOlderThan(days int) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -days).Format(timeFormat)
	result, err := s.db.Exec(`DELETE FROM runs WHERE started_at < ? AND started_at IS NOT NULL`, cutoff)
	if err != nil {
		return 0, err
	}
	// VACUUM to reclaim space
	s.db.Exec("VACUUM")
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
	var nodesJSON, edgesJSON, paramsJSON, tagsJSON, createdAt, updatedAt string
	var enabled int

	if err := sc.Scan(&p.ID, &p.Name, &p.Description, &nodesJSON, &edgesJSON, &p.Schedule, &p.WebhookURL, &paramsJSON, &tagsJSON, &enabled, &createdAt, &updatedAt); err != nil {
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
	if p.Tags == nil {
		p.Tags = []string{}
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

	if err := sc.Scan(&r.ID, &r.PipelineID, &status, &startedAt, &finishedAt); err != nil {
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
	return sql.NullString{String: t.Format(timeFormat), Valid: true}
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
	_, err := s.db.Exec(
		`INSERT INTO connections (id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.ConnID, c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		c.Password, c.Extra, // caller is responsible for encrypting before calling
		c.CreatedAt.Format(timeFormat), c.UpdatedAt.Format(timeFormat),
	)
	return err
}

func (s *SQLiteStore) GetConnection(connID string) (*models.Connection, error) {
	row := s.db.QueryRow(
		`SELECT id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, created_at, updated_at
		 FROM connections WHERE conn_id = ?`, connID,
	)
	return scanConnection(row)
}

func (s *SQLiteStore) ListConnections() ([]models.Connection, error) {
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
		var createdAt, updatedAt string
		if err := rows.Scan(&c.ID, &c.ConnID, &c.Type, &c.Description, &c.Host, &c.Port, &c.Schema, &c.Login,
			&c.Password, &c.Extra, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = time.Parse(timeFormat, createdAt)
		c.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
		conns = append(conns, c)
	}
	return conns, nil
}

func (s *SQLiteStore) UpdateConnection(c *models.Connection) error {
	result, err := s.db.Exec(
		`UPDATE connections SET type=?, description=?, host=?, port=?, schema_name=?, login=?, password_enc=?, extra_enc=?, updated_at=?
		 WHERE conn_id = ?`,
		c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		c.Password, c.Extra,
		c.UpdatedAt.Format(timeFormat), c.ConnID,
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
		&c.Password, &c.Extra, &createdAt, &updatedAt); err != nil {
		return nil, err
	}
	c.CreatedAt, _ = time.Parse(timeFormat, createdAt)
	c.UpdatedAt, _ = time.Parse(timeFormat, updatedAt)
	return &c, nil
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

// ── Variables ────────────────────────────────────────────────

func (s *SQLiteStore) SetVariable(v *models.Variable) error {
	_, err := s.db.Exec(
		`INSERT INTO variables (key, value, type, description, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value, type=excluded.type, description=excluded.description, updated_at=excluded.updated_at`,
		v.Key, v.Value, v.Type, v.Description,
		v.CreatedAt.Format(timeFormat), v.UpdatedAt.Format(timeFormat),
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
