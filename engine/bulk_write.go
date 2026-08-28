package engine

import (
	"context"
	"io"
	"strings"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// bulkBatchWriter is the write-path capability ADR-024 names: streaming
// rows into a table without rendering them as SQL text. A writer pulls
// batches from next until io.EOF and never holds more than one, which is
// what lets a sink write a table that does not fit in worker memory.
type bulkBatchWriter func(ctx context.Context, uri string, cfg SQLGenConfig, columns []string, next func() (*common.DataSet, error)) (int64, error)

// bulkWriterFor reports whether cfg's write can go through a dialect's bulk
// path, and hands back the writer. This is the one place the decision is
// made -- the sink handler, the stream-eligibility check and the streamed
// writer all ask here, so they cannot disagree about which path a write
// takes.
//
// Scope is identical for every backend: append and overwrite only. Upsert
// needs a conflict clause no bulk protocol carries, and CreateTable emits
// DDL that belongs on the statement path. A dialect with no writer here
// simply keeps today's statement path -- absence degrades, it does not
// error.
func bulkWriterFor(cfg SQLGenConfig) (bulkBatchWriter, bool) {
	if copyFastPathDisabled() {
		return nil, false
	}
	if cfg.CreateTable {
		return nil, false
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Mode)) {
	case "", ModeAppend, ModeOverwrite, "replace":
	default:
		return nil, false
	}
	switch cfg.Dialect {
	case "postgres":
		return copyBatchesToPostgres, true
	case "mysql":
		return loadBatchesToMySQL, true
	}
	return nil, false
}

// bulkCreateReady moves a pending CREATE TABLE off the statement path and
// into the bulk writer's transaction, when a writer would otherwise take
// this write (#376).
//
// The refusal in bulkWriterFor above is about the DDL, not the rows -- but
// GenerateSQL emits both as one string, so one statement used to pin fifty
// thousand rows to the statement path. Rendering the DDL here and handing
// it to the writer separates them: the creation still happens, in the same
// transaction as the load, and the rows go through COPY or LOAD DATA.
//
// Returns cfg untouched when there is nothing to create, or when no writer
// would take this write anyway -- the statement path then emits the DDL
// exactly as it always did. ds is needed because a column the caller could
// not describe has its type inferred from the values.
func bulkCreateReady(cfg SQLGenConfig, ds *common.DataSet) (SQLGenConfig, error) {
	if !cfg.CreateTable {
		return cfg, nil
	}
	moved := cfg
	moved.CreateTable = false
	if _, ok := bulkWriterFor(moved); !ok {
		return cfg, nil
	}
	ddl, err := createTableDDL(getDialect(cfg.Dialect), cfg, ds)
	if err != nil {
		return cfg, err
	}
	moved.CreateDDL = ddl
	return moved, nil
}

// bulkWriteRows writes one materialized dataset through a bulk writer. It
// is the batch-path entry point; the writers themselves are the streaming
// ones, and both paths share the implementation so the two cannot drift.
func bulkWriteRows(w bulkBatchWriter, uri string, cfg SQLGenConfig, ds *common.DataSet) (int64, error) {
	sent := false
	return w(context.Background(), uri, cfg, ds.Columns, func() (*common.DataSet, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return ds, nil
	})
}
