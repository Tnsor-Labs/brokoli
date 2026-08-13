package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
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

	// blobs backs Blobs()/BlobStoreProvider — see that method's doc comment
	// for why this exists and is deliberately local disk, not the SQL
	// database WriteArtifact/ReadArtifact use.
	blobs artifact.Store
}

// NewSQLArtifactStore creates (if not already present) the artifacts table
// and returns a store backed by it. dialect must be "postgres" or
// "sqlite". spillDir roots the local-disk blob store Blobs() exposes for
// intra-run spill scratch space (see that method); empty uses the same
// "./brokoli-artifacts" default engine.NewEngine's own LocalDiskArtifactStore
// falls back to when BROKOLI_ARTIFACT_DIR is unset.
func NewSQLArtifactStore(db *sql.DB, dialect string, spillDir string) (*SQLArtifactStore, error) {
	if dialect != "postgres" && dialect != "sqlite" {
		return nil, fmt.Errorf("sql artifact store: unsupported dialect %q (want postgres or sqlite)", dialect)
	}
	if spillDir == "" {
		spillDir = "./brokoli-artifacts"
	}
	s := &SQLArtifactStore{db: db, dialect: dialect, blobs: artifact.NewLocalDiskStore(spillDir)}
	if err := s.ensureSchema(); err != nil {
		return nil, fmt.Errorf("sql artifact store: %w", err)
	}
	return s, nil
}

// Blobs implements BlobStoreProvider (see artifact_store.go), so
// docs/adr/018-chunked-execution-and-backpressure.md's node-level spill
// mechanism (node_output_store.go's nodeOutputs, gated on
// r.artifactStore.(BlobStoreProvider)) works under this store too.
//
// Found live implementing that ADR: cmd/serve.go swaps eng.ArtifactStore
// to SQLArtifactStore only alongside instance-level remote dispatch
// (instanceDispatchEnabled) — exactly the deployment shape most likely to
// run large, memory-heavy pipelines across real worker pods — and
// SQLArtifactStore did not implement BlobStoreProvider at all. Since
// spillEnabled() requires the type assertion to succeed, every node's
// output stayed fully in memory regardless of size in that entire
// deployment mode: the one place the spill mechanism matters most had it
// silently disabled.
//
// Backed by local disk, not the SQL database WriteArtifact/ReadArtifact
// use: spill scratch space is read back only by the same run, on the same
// pod, within the same process — it has no cross-pod visibility
// requirement (that is specifically what WriteArtifact/ReadArtifact solve
// for a run's genuinely durable, resumable output). Routing spill traffic
// through the database instead would add write load and row bloat to
// solve a problem local disk already solves for free.
func (s *SQLArtifactStore) Blobs() artifact.Store { return s.blobs }

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

// Each statement is spelled out in full per dialect — never built with
// fmt.Sprintf against a placeholder — so nothing about query construction
// depends on caller-supplied data in a way a static analyzer (or a human
// skimming this file) would need to trace through a helper to be sure of.
// Only the argument VALUES vary per call, passed through database/sql's
// own parameterization, never string-interpolated.
const (
	writeArtifactPostgres = `INSERT INTO artifacts (run_id, node_id, instance_key, columns_json, data, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (run_id, node_id, instance_key) DO UPDATE
		SET columns_json = excluded.columns_json, data = excluded.data, created_at = excluded.created_at`
	writeArtifactSQLite = `INSERT INTO artifacts (run_id, node_id, instance_key, columns_json, data, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (run_id, node_id, instance_key) DO UPDATE
		SET columns_json = excluded.columns_json, data = excluded.data, created_at = excluded.created_at`

	readArtifactPostgres = `SELECT columns_json, data FROM artifacts WHERE run_id = $1 AND node_id = $2 AND instance_key = $3`
	readArtifactSQLite   = `SELECT columns_json, data FROM artifacts WHERE run_id = ? AND node_id = ? AND instance_key = ?`

	deleteRunArtifactsPostgres = `DELETE FROM artifacts WHERE run_id = $1`
	deleteRunArtifactsSQLite   = `DELETE FROM artifacts WHERE run_id = ?`
)

// WriteArtifact implements ArtifactStore.
//
// Unlike LocalDiskArtifactStore.WriteArtifact and Blobs()'s spill path,
// this cannot stream the encode straight into the write: the destination
// is a single TEXT column value passed to database/sql's Exec, which has
// no streaming-write API in either the postgres or sqlite driver this
// store supports — the whole encoded value must exist before Exec can be
// called, full stop. This is an inherent cost of storing an artifact as a
// SQL column rather than a real blob store, not an oversight matching the
// other two paths' io.Pipe pattern would fix. It is bounded in practice:
// this method is called once per completed remote instance result
// (ADR-017), not on the hot per-node spill path Blobs() exists for.
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

	query := writeArtifactSQLite
	if s.dialect == "postgres" {
		query = writeArtifactPostgres
	}
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
	query := readArtifactSQLite
	if s.dialect == "postgres" {
		query = readArtifactPostgres
	}
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

// DeleteRunArtifacts implements ArtifactStore. Also clears any spill
// scratch space Blobs() holds under this run's namespace (nodeOutputs
// spills using r.run.ID as the namespace — see node_output_store.go's
// newOutputs) — the SQL rows and the local-disk spill blobs are two
// separate stores for two different purposes (see Blobs()'s doc comment)
// but share one lifetime, same as LocalDiskArtifactStore's own manifests
// and blobs already do.
func (s *SQLArtifactStore) DeleteRunArtifacts(runID string) error {
	if runID == "" {
		return nil
	}
	query := deleteRunArtifactsSQLite
	if s.dialect == "postgres" {
		query = deleteRunArtifactsPostgres
	}
	if _, err := s.db.Exec(query, runID); err != nil {
		return fmt.Errorf("delete run artifacts: %w", err)
	}
	if err := s.blobs.DeleteNamespace(context.Background(), runID); err != nil {
		return fmt.Errorf("delete run artifacts: spill blobs: %w", err)
	}
	return nil
}

// WriteArtifactRef implements RefArtifactWriter (see artifact_store.go).
// Unlike LocalDiskArtifactStore — where this is a zero-copy manifest write
// because the spill blobs and artifact blobs are one store — the SQL
// store's artifact IS a TEXT column value, so the blob's bytes must be
// read into memory once to become the INSERT's argument. That is the same
// inherent cost WriteArtifact's own doc comment records for this store,
// bounded to one encoded dataset, only in instance-dispatch deployments —
// still strictly better than the non-ref fallback, which would decode the
// blob into a full DataSet (5-10x the encoded size) and re-encode it.
func (s *SQLArtifactStore) WriteArtifactRef(runID, nodeID, instanceKey string, ref *artifact.DatasetRef) error {
	if runID == "" || nodeID == "" {
		return fmt.Errorf("write artifact ref: runID and nodeID are required")
	}
	if ref == nil {
		return fmt.Errorf("write artifact ref: nil ref")
	}
	rc, err := s.blobs.Open(context.Background(), &ref.ArtifactRef)
	if err != nil {
		return fmt.Errorf("write artifact ref: open blob: %w", err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("write artifact ref: read blob: %w", err)
	}
	cols := ref.Columns
	if cols == nil {
		cols = []string{}
	}
	colsJSON, err := json.Marshal(cols)
	if err != nil {
		return fmt.Errorf("write artifact ref: encode columns: %w", err)
	}
	query := writeArtifactSQLite
	if s.dialect == "postgres" {
		query = writeArtifactPostgres
	}
	if _, err := s.db.Exec(query, runID, nodeID, instanceKey, string(colsJSON), string(data), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return fmt.Errorf("write artifact ref: %w", err)
	}
	return nil
}
