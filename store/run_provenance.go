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
		var decoded struct {
			Inputs []models.DatasetFact `json:"inputs"`
			Output *models.DatasetFact  `json:"output"`
		}
		if err := json.Unmarshal([]byte(facts), &decoded); err != nil {
			// A row this build cannot read is skipped rather than failing
			// the whole request: one unreadable node must not hide the
			// rest of the run's provenance, which is the part somebody is
			// looking at.
			continue
		}
		out = append(out, models.NodeProvenance{
			RunID: runID, NodeID: nodeID,
			Inputs: decoded.Inputs, Output: decoded.Output,
			RecordedAt: recordedAt,
		})
	}
	return out, rows.Err()
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
