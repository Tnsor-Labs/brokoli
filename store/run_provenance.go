package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// Per-run provenance (ADR-039 part 4).
//
// One row per (run, node), holding what that execution consumed and
// produced. The facts are stored as JSON rather than as columns: the
// shape is a list of inputs whose length varies by node, and widening
// this table positionally is the trap run_attribution was created to
// avoid.
//
// ON DELETE CASCADE, like node_profiles and every other per-run table.
// The ADR is explicit that provenance is written for every run and
// deleted with the run: a record that outlives what it describes inflates
// the table and answers questions about something that no longer exists.
// That is not a hypothetical here -- run_attribution shipped without the
// constraint and its rows outlived their runs until #632.

// createRunProvenanceTable is called from both dialects' migrations.
// Written once so a column cannot be added to one path and not the other.
func createRunProvenanceTable(db *sql.DB, dialect string) {
	jsonType, timestamp := "TEXT", "TIMESTAMP"
	if dialect == "postgres" {
		jsonType, timestamp = "JSONB", "TIMESTAMPTZ"
	}
	_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS run_provenance (
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		facts ` + jsonType + ` NOT NULL,
		recorded_at ` + timestamp + ` NOT NULL,
		PRIMARY KEY (run_id, node_id),
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
	)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_run_provenance_run ON run_provenance(run_id)`)
}

// saveNodeProvenance records one node execution, replacing any previous
// record for the same (run, node).
//
// Replacing rather than accumulating: a retried node execution describes
// the same node in the same run, and the last attempt is the one whose
// output downstream nodes actually consumed.
func saveNodeProvenance(db *sql.DB, dialect string, p *models.NodeProvenance) error {
	if p == nil || p.RunID == "" || p.NodeID == "" {
		return nil
	}
	if p.RecordedAt.IsZero() {
		p.RecordedAt = time.Now().UTC()
	}
	facts, err := json.Marshal(struct {
		Inputs []models.DatasetFact `json:"inputs,omitempty"`
		Output *models.DatasetFact  `json:"output,omitempty"`
	}{Inputs: p.Inputs, Output: p.Output})
	if err != nil {
		return fmt.Errorf("save provenance %s/%s: %w", p.RunID, p.NodeID, err)
	}

	query := rewritePlaceholders(dialect,
		`INSERT INTO run_provenance (run_id, node_id, facts, recorded_at) VALUES (?,?,?,?)
		 ON CONFLICT(run_id, node_id) DO UPDATE SET facts=excluded.facts, recorded_at=excluded.recorded_at`)
	if _, err := db.Exec(query, p.RunID, p.NodeID, string(facts), p.RecordedAt); err != nil {
		return fmt.Errorf("save provenance %s/%s: %w", p.RunID, p.NodeID, err)
	}
	return nil
}

// getRunProvenance reads every node's record for one run, in node order.
func getRunProvenance(db *sql.DB, dialect, runID string) ([]models.NodeProvenance, error) {
	if runID == "" {
		return nil, nil
	}
	query := rewritePlaceholders(dialect,
		`SELECT node_id, facts, recorded_at FROM run_provenance WHERE run_id = ? ORDER BY node_id`)
	rows, err := db.Query(query, runID)
	if err != nil {
		return nil, fmt.Errorf("get provenance for %s: %w", runID, err)
	}
	defer rows.Close()

	var out []models.NodeProvenance
	for rows.Next() {
		var nodeID, facts string
		var recordedAt time.Time
		if err := rows.Scan(&nodeID, &facts, &recordedAt); err != nil {
			return nil, err
		}
		if rec, ok := decodeProvenanceRow(runID, nodeID, facts, recordedAt); ok {
			out = append(out, rec)
		}
	}
	return out, rows.Err()
}

// decodeProvenanceRow turns a stored row back into a record.
//
// A row this build cannot read is reported as not ok and skipped by the
// caller rather than failing the whole request: one unreadable node must
// not hide the rest of the provenance somebody is looking at.
func decodeProvenanceRow(runID, nodeID, facts string, recordedAt time.Time) (models.NodeProvenance, bool) {
	var decoded struct {
		Inputs []models.DatasetFact `json:"inputs"`
		Output *models.DatasetFact  `json:"output"`
	}
	if err := json.Unmarshal([]byte(facts), &decoded); err != nil {
		return models.NodeProvenance{}, false
	}
	return models.NodeProvenance{
		RunID: runID, NodeID: nodeID,
		Inputs: decoded.Inputs, Output: decoded.Output,
		RecordedAt: recordedAt,
	}, true
}

// Batch read for the lineage graph, which needs the record behind every
// node it draws: one query for all the runs its profiles came from, not
// one per pipeline.
//
// The run ids travel as ONE JSON array parameter, never as a placeholder
// list built by concatenation. The SQL is therefore a literal per dialect,
// with nothing string-built for a reader or a scanner to worry about, and
// there is no ceiling on how many ids one query can take.
//
// Deliberately not part of store.Store. The lineage handler reaches it by
// type assertion, as it reaches the profile store; adding it to the
// interface would break every out-of-tree implementation for a read that
// only the API server's own store needs.
const (
	sqliteProvenanceForRuns = `SELECT run_id, node_id, facts, recorded_at FROM run_provenance
		WHERE run_id IN (SELECT value FROM json_each(?)) ORDER BY run_id, node_id`
	postgresProvenanceForRuns = `SELECT run_id, node_id, facts, recorded_at FROM run_provenance
		WHERE run_id IN (SELECT jsonb_array_elements_text($1::jsonb)) ORDER BY run_id, node_id`
)

// getNodeProvenanceForRuns returns every record for the given runs, keyed
// by run id. A run with no records is absent from the map.
func getNodeProvenanceForRuns(db *sql.DB, query string, runIDs []string) (map[string][]models.NodeProvenance, error) {
	out := map[string][]models.NodeProvenance{}
	if len(runIDs) == 0 {
		return out, nil
	}
	ids, err := json.Marshal(runIDs)
	if err != nil {
		return nil, fmt.Errorf("provenance for runs: %w", err)
	}
	rows, err := db.Query(query, string(ids))
	if err != nil {
		return nil, fmt.Errorf("provenance for runs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var runID, nodeID, facts string
		var recordedAt time.Time
		if err := rows.Scan(&runID, &nodeID, &facts, &recordedAt); err != nil {
			return nil, err
		}
		if rec, ok := decodeProvenanceRow(runID, nodeID, facts, recordedAt); ok {
			out[runID] = append(out[runID], rec)
		}
	}
	return out, rows.Err()
}

func (s *SQLiteStore) GetNodeProvenanceForRuns(runIDs []string) (map[string][]models.NodeProvenance, error) {
	return getNodeProvenanceForRuns(s.db, sqliteProvenanceForRuns, runIDs)
}

func (s *PostgresStore) GetNodeProvenanceForRuns(runIDs []string) (map[string][]models.NodeProvenance, error) {
	return getNodeProvenanceForRuns(s.db, postgresProvenanceForRuns, runIDs)
}

func (s *SQLiteStore) SaveNodeProvenance(p *models.NodeProvenance) error {
	return saveNodeProvenance(s.db, "sqlite", p)
}

func (s *SQLiteStore) GetRunProvenance(runID string) ([]models.NodeProvenance, error) {
	return getRunProvenance(s.db, "sqlite", runID)
}

func (s *PostgresStore) SaveNodeProvenance(p *models.NodeProvenance) error {
	return saveNodeProvenance(s.db, "postgres", p)
}

func (s *PostgresStore) GetRunProvenance(runID string) ([]models.NodeProvenance, error) {
	return getRunProvenance(s.db, "postgres", runID)
}
