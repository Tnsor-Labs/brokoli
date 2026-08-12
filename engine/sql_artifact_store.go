package engine

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// SQLArtifactStore implements ArtifactStore against the same SQL database
// every pod in a distributed deployment already connects to, instead of
// local disk — closing a real gap found live: LocalDiskArtifactStore (the
// default) writes to whichever pod's own ephemeral filesystem happened to
// run WriteArtifact, so a remote-dispatched instance's result (ADR-017)
// is invisible to the dispatcher pod that needs to read it back, and the
// run fails with "result could not be read back" even though the worker
// finished successfully. A shared hostPath volume works around this on a
// single node; this is the general fix for any multi-pod deployment,
// including genuinely multi-node ones, using infrastructure a distributed
// deployment already requires (Postgres) rather than new infrastructure
// (an object store, a network filesystem).
//
// Deliberately not the default for every deployment: an "all"-mode single
// process has no cross-pod problem to solve, and routing every artifact
// through the database instead of local disk would add write load and row
// bloat for zero benefit there. See instanceDispatchEnabled's own gate in
// cmd/serve.go — this store activates alongside the same opt-in, since
// remote instance dispatch is precisely the case that needs it.
//
// Rows hold plain text — the same NDJSON EncodeArrowJSON/DecodeArrowJSON
// already produce for LocalDiskArtifactStore — not a binary column, so
// there is no dialect-specific BYTEA/BLOB handling to maintain. Schema is
// created lazily on first use (CREATE TABLE IF NOT EXISTS), matching
// LocalDiskArtifactStore's own lazy directory creation, rather than being
// wired into store/postgres.go's central schema init: this table is
// optional, engine-owned state, not part of the core store contract every
// deployment needs.
type SQLArtifactStore struct {
	db      *sql.DB
	dialect string // "postgres" or "sqlite" — the two backends store.Store supports
}

// NewSQLArtifactStore creates (if not already present) the artifacts table
// and returns a store backed by it. dialect must be "postgres" or
// "sqlite".
func NewSQLArtifactStore(db *sql.DB, dialect string) (*SQLArtifactStore, error) {
	if dialect != "postgres" && dialect != "sqlite" {
		return nil, fmt.Errorf("sql artifact store: unsupported dialect %q (want postgres or sqlite)", dialect)
	}
	s := &SQLArtifactStore{db: db, dialect: dialect}
	if err := s.ensureSchema(); err != nil {
		return nil, fmt.Errorf("sql artifact store: %w", err)
	}
	return s, nil
}

func (s *SQLArtifactStore) ensureSchema() error {
	// TEXT even for created_at rather than a dialect-specific TIMESTAMP
	// type: this table only ever needs an equality lookup by (run_id,
	// node_id, instance_key) and a bulk delete by run_id, so there is
	// nothing to gain from a native timestamp type and one less dialect
	// difference to maintain.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS artifacts (
		run_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		instance_key TEXT NOT NULL DEFAULT '',
		columns_json TEXT NOT NULL,
		data TEXT NOT NULL,
		created_at TEXT NOT NULL,
		PRIMARY KEY (run_id, node_id, instance_key)
	)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_artifacts_run_id ON artifacts (run_id)`)
	return err
}

// ph returns the positional placeholder this store's dialect expects for
// argument position i (1-indexed) — "$1"/"$2"/... for postgres, "?" for
// sqlite (positionless, so i is unused there but kept for a uniform call
// shape at every use site below).
func (s *SQLArtifactStore) ph(i int) string {
	if s.dialect == "postgres" {
		return fmt.Sprintf("$%d", i)
	}
	return "?"
}

// WriteArtifact implements ArtifactStore.
func (s *SQLArtifactStore) WriteArtifact(runID, nodeID, instanceKey string, ds *common.DataSet) error {
	if runID == "" || nodeID == "" {
		return fmt.Errorf("write artifact: runID and nodeID are required")
	}
	var buf bytes.Buffer
	if err := EncodeArrowJSON(&buf, ds); err != nil {
		return fmt.Errorf("write artifact: encode: %w", err)
	}
	cols := []string{}
	if ds != nil && ds.Columns != nil {
		cols = ds.Columns
	}
	colsJSON, err := json.Marshal(cols)
	if err != nil {
		return fmt.Errorf("write artifact: encode columns: %w", err)
	}

	query := fmt.Sprintf(`INSERT INTO artifacts (run_id, node_id, instance_key, columns_json, data, created_at)
		VALUES (%s, %s, %s, %s, %s, %s)
		ON CONFLICT (run_id, node_id, instance_key) DO UPDATE
		SET columns_json = excluded.columns_json, data = excluded.data, created_at = excluded.created_at`,
		s.ph(1), s.ph(2), s.ph(3), s.ph(4), s.ph(5), s.ph(6))
	if _, err := s.db.Exec(query, runID, nodeID, instanceKey, string(colsJSON), buf.String(), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("write artifact: %w", err)
	}
	return nil
}

// ReadArtifact implements ArtifactStore.
func (s *SQLArtifactStore) ReadArtifact(runID, nodeID, instanceKey string) (*common.DataSet, error) {
	if runID == "" || nodeID == "" {
		return nil, fmt.Errorf("read artifact: runID and nodeID are required")
	}
	query := fmt.Sprintf(`SELECT columns_json, data FROM artifacts WHERE run_id = %s AND node_id = %s AND instance_key = %s`,
		s.ph(1), s.ph(2), s.ph(3))
	var colsJSON, data string
	if err := s.db.QueryRow(query, runID, nodeID, instanceKey).Scan(&colsJSON, &data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: run=%s node=%s instance=%s", ErrArtifactNotFound, runID, nodeID, instanceKey)
		}
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	var cols []string
	if err := json.Unmarshal([]byte(colsJSON), &cols); err != nil {
		return nil, fmt.Errorf("read artifact: decode columns: %w", err)
	}
	ds, err := DecodeArrowJSON(strings.NewReader(data), cols)
	if err != nil {
		return nil, fmt.Errorf("read artifact: decode: %w", err)
	}
	return ds, nil
}

// DeleteRunArtifacts implements ArtifactStore.
func (s *SQLArtifactStore) DeleteRunArtifacts(runID string) error {
	if runID == "" {
		return nil
	}
	query := fmt.Sprintf(`DELETE FROM artifacts WHERE run_id = %s`, s.ph(1))
	if _, err := s.db.Exec(query, runID); err != nil {
		return fmt.Errorf("delete run artifacts: %w", err)
	}
	return nil
}
