package store

import (
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hc12r/broked/models"
	"github.com/hc12r/brokolisql-go/pkg/common"
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
	_, err = s.db.Exec(string(migration))
	return err
}

func (s *PostgresStore) Close() error  { return s.db.Close() }
func (s *PostgresStore) RawDB() interface{} { return s.db }

// --- Pipelines ---

func (s *PostgresStore) CreatePipeline(p *models.Pipeline) error {
	nodesJSON, _ := json.Marshal(p.Nodes)
	edgesJSON, _ := json.Marshal(p.Edges)
	paramsJSON, _ := json.Marshal(p.Params)
	_, err := s.db.Exec(
		`INSERT INTO pipelines (id, name, description, nodes, edges, schedule, webhook_url, params, enabled, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		p.ID, p.Name, p.Description, nodesJSON, edgesJSON,
		p.Schedule, p.WebhookURL, paramsJSON, p.Enabled, p.CreatedAt, p.UpdatedAt,
	)
	return err
}

func (s *PostgresStore) GetPipeline(id string) (*models.Pipeline, error) {
	var p models.Pipeline
	var nodesJSON, edgesJSON, paramsJSON []byte
	err := s.db.QueryRow(
		`SELECT id, name, description, nodes, edges, schedule, webhook_url, params, enabled, created_at, updated_at
		 FROM pipelines WHERE id = $1`, id,
	).Scan(&p.ID, &p.Name, &p.Description, &nodesJSON, &edgesJSON,
		&p.Schedule, &p.WebhookURL, &paramsJSON, &p.Enabled, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	json.Unmarshal(nodesJSON, &p.Nodes)
	json.Unmarshal(edgesJSON, &p.Edges)
	json.Unmarshal(paramsJSON, &p.Params)
	return &p, nil
}

func (s *PostgresStore) ListPipelines() ([]models.Pipeline, error) {
	rows, err := s.db.Query(
		`SELECT id, name, description, nodes, edges, schedule, webhook_url, params, enabled, created_at, updated_at
		 FROM pipelines ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var pipelines []models.Pipeline
	for rows.Next() {
		var p models.Pipeline
		var nodesJSON, edgesJSON, paramsJSON []byte
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &nodesJSON, &edgesJSON,
			&p.Schedule, &p.WebhookURL, &paramsJSON, &p.Enabled, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal(nodesJSON, &p.Nodes)
		json.Unmarshal(edgesJSON, &p.Edges)
		json.Unmarshal(paramsJSON, &p.Params)
		pipelines = append(pipelines, p)
	}
	return pipelines, rows.Err()
}

func (s *PostgresStore) UpdatePipeline(p *models.Pipeline) error {
	nodesJSON, _ := json.Marshal(p.Nodes)
	edgesJSON, _ := json.Marshal(p.Edges)
	paramsJSON, _ := json.Marshal(p.Params)
	result, err := s.db.Exec(
		`UPDATE pipelines SET name=$1, description=$2, nodes=$3, edges=$4, schedule=$5,
		 webhook_url=$6, params=$7, enabled=$8, updated_at=$9 WHERE id=$10`,
		p.Name, p.Description, nodesJSON, edgesJSON, p.Schedule,
		p.WebhookURL, paramsJSON, p.Enabled, p.UpdatedAt, p.ID,
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

// ── Connections (same implementation as SQLite, Postgres-compatible) ──

func (s *PostgresStore) CreateConnection(c *models.Connection) error {
	_, err := s.db.Exec(
		`INSERT INTO connections (id, conn_id, type, description, host, port, schema_name, login, password_enc, extra_enc, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		c.ID, c.ConnID, c.Type, c.Description, c.Host, c.Port, c.Schema, c.Login,
		c.Password, c.Extra, c.CreatedAt, c.UpdatedAt,
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
	_, err := s.db.Exec(
		`INSERT INTO variables (key, value, type, description, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value, type=EXCLUDED.type, description=EXCLUDED.description, updated_at=EXCLUDED.updated_at`,
		v.Key, v.Value, v.Type, v.Description, v.CreatedAt, v.UpdatedAt,
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
