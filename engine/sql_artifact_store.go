package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
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
// Rows hold plain text — the same NDJSON EncodeNDJSON/DecodeNDJSON
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

	// sharedBlobs backs SharedBlobs()/SharedBlobStoreProvider: a store
	// another pod can read. Nil unless the deployment configured object
	// storage, which is the honest default (ADR-038).
	sharedBlobs artifact.Store
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
	s := &SQLArtifactStore{
		db:      db,
		dialect: dialect,
		blobs:   artifact.NewLocalDiskStore(spillDir),
		// Configured separately, and absent by default: see SharedBlobs.
		sharedBlobs: sharedBlobStoreFromEnv(),
	}
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

// SharedBlobs implements SharedBlobStoreProvider: the store another pod
// can read, or nil when this deployment has not configured one.
//
// Nil is the honest answer for the default configuration, and it is why
// this method exists. Blobs() above is local disk on purpose, and a
// distributed deployment has many pods with many local disks. Handing
// that store to a caller staging bytes for a different pod produced
// exactly the failure it sounds like: the write succeeded, a capability
// was minted, a work order was dispatched, and the fetch three hops
// later returned 404 from a component that had done nothing wrong
// (#572, ADR-038).
func (s *SQLArtifactStore) SharedBlobs() artifact.Store { return s.sharedBlobs }

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
		attempt INTEGER NOT NULL DEFAULT 0,
		fencing_generation INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (run_id, node_id, instance_key)
	)`); err != nil {
		return err
	}
	// Additive columns for installs whose artifacts table predates
	// WriteArtifactFenced. Both dialects error on a duplicate column and
	// both are fine to ignore: this runs on every boot, so "already
	// exists" is the normal case, not a failure — the same pattern
	// api.NewUserStore's own additive columns already use.
	_, _ = s.db.Exec(`ALTER TABLE artifacts ADD COLUMN attempt INTEGER NOT NULL DEFAULT 0`)
	_, _ = s.db.Exec(`ALTER TABLE artifacts ADD COLUMN fencing_generation INTEGER NOT NULL DEFAULT 0`)
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

	// The WHERE clause is the fencing guard WriteArtifactFenced relies on:
	// on conflict, the UPDATE (and so the write) only takes effect when no
	// (attempt, fencing_generation) at least as high already owns this key
	// — attempt is the primary ordering key (see FencedArtifactWriter's
	// doc comment for why), fencing_generation only a tiebreaker within
	// the same attempt. Per SQLite's and Postgres's own upsert semantics,
	// a WHERE clause that evaluates false makes the whole statement a
	// no-op — the existing row is left exactly as it was, not merely
	// un-updated — so RowsAffected distinguishes "written" from "a newer
	// attempt already owns this" for both the insert and the update path.
	writeArtifactFencedPostgres = `INSERT INTO artifacts (run_id, node_id, instance_key, columns_json, data, created_at, attempt, fencing_generation)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (run_id, node_id, instance_key) DO UPDATE
		SET columns_json = excluded.columns_json, data = excluded.data, created_at = excluded.created_at, attempt = excluded.attempt, fencing_generation = excluded.fencing_generation
		WHERE artifacts.attempt < excluded.attempt
		   OR (artifacts.attempt = excluded.attempt AND artifacts.fencing_generation <= excluded.fencing_generation)`
	writeArtifactFencedSQLite = `INSERT INTO artifacts (run_id, node_id, instance_key, columns_json, data, created_at, attempt, fencing_generation)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (run_id, node_id, instance_key) DO UPDATE
		SET columns_json = excluded.columns_json, data = excluded.data, created_at = excluded.created_at, attempt = excluded.attempt, fencing_generation = excluded.fencing_generation
		WHERE artifacts.attempt < excluded.attempt
		   OR (artifacts.attempt = excluded.attempt AND artifacts.fencing_generation <= excluded.fencing_generation)`

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
// defaultSQLArtifactMaxBytes caps how large a resume artifact this store
// will copy into the database.
//
// An artifact here is a value in a TEXT column, so writing one means
// holding the whole encoded dataset in memory and handing it to the driver
// as a single statement parameter. At 100 MB that killed both ends: the
// worker (a 460 MiB pod doing io.ReadAll on the blob) and a Postgres
// backend, which the kernel OOM-killed, taking the database into recovery
// and failing every other run on the cluster with it.
//
// So there is a size past which writing the resume cache costs more than
// the run it is meant to save. Past it this store declines, the runner
// logs that a future resume of that node will fail loudly — which it
// already does for any artifact write failure — and the run continues.
// The data itself is never at risk: it lives in the blob store, and
// downstream nodes read it from there.
//
// 32 MiB is chosen to sit well under a default Postgres backend's
// comfortable allocation while still covering the artifacts real
// pipelines produce. BROKOLI_SQL_ARTIFACT_MAX_BYTES overrides it; 0 or
// negative removes the cap.
const defaultSQLArtifactMaxBytes int64 = 32 << 20

func sqlArtifactMaxBytes() int64 {
	if v := os.Getenv("BROKOLI_SQL_ARTIFACT_MAX_BYTES"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return defaultSQLArtifactMaxBytes
}

// errArtifactTooLarge explains a declined write in the terms an operator
// can act on.
func errArtifactTooLarge(nodeID string, size, limit int64) error {
	return fmt.Errorf(
		"resume artifact for node %s is %s, over the %s this store will copy into the database: "+
			"an artifact is a single column value here, so writing it would hold the whole dataset in memory "+
			"and send it as one statement parameter. The run is unaffected and its data is safe in the blob store; "+
			"only resuming this node from a durable artifact is unavailable. "+
			"Configure a shared blob store (S3) for large-artifact resume, or raise BROKOLI_SQL_ARTIFACT_MAX_BYTES",
		nodeID, humanBytes(size), humanBytes(limit))
}

func (s *SQLArtifactStore) WriteArtifact(runID, nodeID, instanceKey string, ds *common.DataSet) error {
	if runID == "" || nodeID == "" {
		return fmt.Errorf("write artifact: runID and nodeID are required")
	}
	var buf bytes.Buffer
	if err := EncodeNDJSON(&buf, ds); err != nil {
		return fmt.Errorf("write artifact: encode: %w", err)
	}
	if limit := sqlArtifactMaxBytes(); limit > 0 && int64(buf.Len()) > limit {
		return errArtifactTooLarge(nodeID, int64(buf.Len()), limit)
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

// WriteArtifactFenced implements FencedArtifactWriter (see that interface's
// doc comment for why this exists, including why ordering is
// (attempt, fencingGeneration) rather than fencingGeneration alone).
// Identical to WriteArtifact except the write is conditioned on that pair:
// if an attempt/generation at least as high already owns
// (runID, nodeID, instanceKey) — the row was last written by an attempt
// this one has already been superseded by — the statement is a no-op and
// written comes back false rather than clobbering that row.
func (s *SQLArtifactStore) WriteArtifactFenced(runID, nodeID, instanceKey string, ds *common.DataSet, attempt int, fencingGeneration int64) (bool, error) {
	if runID == "" || nodeID == "" {
		return false, fmt.Errorf("write artifact: runID and nodeID are required")
	}
	var buf bytes.Buffer
	if err := EncodeNDJSON(&buf, ds); err != nil {
		return false, fmt.Errorf("write artifact: encode: %w", err)
	}
	cols := []string{}
	if ds != nil && ds.Columns != nil {
		cols = ds.Columns
	}
	colsJSON, err := json.Marshal(cols)
	if err != nil {
		return false, fmt.Errorf("write artifact: encode columns: %w", err)
	}

	query := writeArtifactFencedSQLite
	if s.dialect == "postgres" {
		query = writeArtifactFencedPostgres
	}
	res, err := s.db.Exec(query, runID, nodeID, instanceKey, string(colsJSON), buf.String(), time.Now().UTC().Format(time.RFC3339Nano), attempt, fencingGeneration)
	if err != nil {
		return false, fmt.Errorf("write artifact: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("write artifact: rows affected: %w", err)
	}
	return n > 0, nil
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
	ds, err := DecodeNDJSON(strings.NewReader(data), cols)
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

// DeleteTransientBlobs implements TransientBlobJanitor: this store's
// artifacts are database rows, so a run's local blob namespace is pure
// scratch (spill + reference-passing transport) and safe to reclaim the
// moment the run is terminal. Persisted artifacts are untouched — resume
// reads them from the database, never from this scratch space.
func (s *SQLArtifactStore) DeleteTransientBlobs(runID string) error {
	if runID == "" {
		return nil
	}
	return s.blobs.DeleteNamespace(context.Background(), runID)
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
	// Checked before the blob is opened, because the point is not to read
	// it: SizeBytes is the exact stored length, so an oversized artifact
	// costs nothing to decline.
	if limit := sqlArtifactMaxBytes(); limit > 0 && ref.SizeBytes > limit {
		return errArtifactTooLarge(nodeID, ref.SizeBytes, limit)
	}
	rc, err := s.blobs.Open(context.Background(), &ref.ArtifactRef)
	if err != nil {
		return fmt.Errorf("write artifact ref: open blob: %w", err)
	}
	defer rc.Close()
	data, err := ndjsonBytesForTextColumn(rc, ref)
	if err != nil {
		return fmt.Errorf("write artifact ref: %w", err)
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

// ndjsonBytesForTextColumn returns the blob's contents as the NDJSON text
// the artifact column holds.
//
// The column is text, and ReadArtifact decodes it as NDJSON. This read
// the blob verbatim, which was correct while NDJSON was the only format
// a ref could name and became a bug the moment spill() started choosing
// Arrow for uniformly typed datasets (#521): Arrow's binary IPC bytes
// went into a text column and came back out as an empty dataset, with no
// error, for any node whose output happened to be typed.
//
// Converting rather than refusing, because the caller has a valid
// artifact in hand and the storage layer's encoding is not the caller's
// concern. It costs a decode and re-encode of an artifact already capped
// by sqlArtifactMaxBytes, and only for the formats that need it.
func ndjsonBytesForTextColumn(r io.Reader, ref *artifact.DatasetRef) ([]byte, error) {
	if ref.Format == artifact.FormatNDJSON || ref.Format == "" {
		data, err := io.ReadAll(r)
		if err != nil {
			return nil, fmt.Errorf("read blob: %w", err)
		}
		return data, nil
	}
	ds, err := decodeDatasetRef(r, ref)
	if err != nil {
		return nil, fmt.Errorf("decode %s blob: %w", ref.Format, err)
	}
	var buf bytes.Buffer
	if err := EncodeNDJSON(&buf, ds); err != nil {
		return nil, fmt.Errorf("re-encode %s blob as ndjson: %w", ref.Format, err)
	}
	return buf.Bytes(), nil
}
