package store

import (
	"database/sql"
	"errors"
	"time"
)

// Incremental node state (#, EE-ADR-016): the high-water mark a node has
// read its source up to, so the next run reads only rows past it. Keyed by
// the pipeline and node, so it persists across runs. Today the migrate node
// uses it for cursor-incremental reads; the value is the string form of the
// cursor column's maximum.

// GetNodeWatermark returns the stored high-water value for a node, and
// whether one exists.
func (s *SQLiteStore) GetNodeWatermark(pipelineID, nodeID string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM node_watermarks WHERE pipeline_id = ? AND node_id = ?`, pipelineID, nodeID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetNodeWatermark stores (or advances) a node's high-water value.
func (s *SQLiteStore) SetNodeWatermark(pipelineID, nodeID, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO node_watermarks (pipeline_id, node_id, value, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(pipeline_id, node_id) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		pipelineID, nodeID, value, formatTime(time.Now().UTC()),
	)
	return err
}

// GetNodeWatermark returns the stored high-water value for a node, and
// whether one exists.
func (s *PostgresStore) GetNodeWatermark(pipelineID, nodeID string) (string, bool, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM node_watermarks WHERE pipeline_id = $1 AND node_id = $2`, pipelineID, nodeID).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return value, true, nil
}

// SetNodeWatermark stores (or advances) a node's high-water value.
func (s *PostgresStore) SetNodeWatermark(pipelineID, nodeID, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO node_watermarks (pipeline_id, node_id, value, updated_at) VALUES ($1, $2, $3, NOW())
		 ON CONFLICT (pipeline_id, node_id) DO UPDATE SET value = EXCLUDED.value, updated_at = NOW()`,
		pipelineID, nodeID, value,
	)
	return err
}
