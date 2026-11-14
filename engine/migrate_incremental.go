package engine

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// nodeWatermarkStore is the slice of the store the incremental migrate path
// needs. A store that does not implement it (an API-only worker's store)
// makes incremental degrade to a full read, logged, rather than fail.
type nodeWatermarkStore interface {
	GetNodeWatermark(pipelineID, nodeID string) (string, bool, error)
	SetNodeWatermark(pipelineID, nodeID, value string) error
}

// cursorIdentifier is a conservative column name -- letters, digits and
// underscores -- so it cannot inject SQL into the predicate built around the
// source query.
var cursorIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// numericWatermark matches an integer or decimal cursor value, injected as
// a bare literal rather than a quoted string.
var numericWatermark = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

// prepareIncrementalMigrate returns the source query filtered to rows at or
// past the stored high-water mark, and a function that advances the mark
// (called by the caller only after a successful write). When the store
// cannot keep a watermark, it returns the base query and a nil save, logging
// that the run reads in full.
func (r *Runner) prepareIncrementalMigrate(ctx context.Context, node models.Node, sourceURI, baseQuery, cursorCol string) (string, func(), error) {
	if !cursorIdentifier.MatchString(cursorCol) {
		return "", nil, fmt.Errorf("cursor_column %q is not a simple column name", cursorCol)
	}
	ws, ok := r.store.(nodeWatermarkStore)
	if !ok || r.run == nil {
		r.log(node.ID, models.LogLevelWarning, "incremental: this runner cannot keep a high-water mark; reading the source in full")
		return baseQuery, nil, nil
	}
	pipelineID := r.run.PipelineID

	last, has, err := ws.GetNodeWatermark(pipelineID, node.ID)
	if err != nil {
		return "", nil, fmt.Errorf("read watermark: %w", err)
	}

	// The new high-water mark is the source's current maximum cursor value,
	// read before the copy and stored only after the write succeeds.
	newHW, err := r.maxCursorValue(ctx, sourceURI, baseQuery, cursorCol)
	if err != nil {
		return "", nil, fmt.Errorf("read source high-water mark: %w", err)
	}

	filtered := baseQuery
	if has && last != "" {
		// ">=" with upsert: re-reading the boundary rows is idempotent and
		// avoids missing a row written at exactly the last mark. A numeric
		// mark is injected unquoted -- quoting it as text compares wrong
		// where a subquery has dropped the column's numeric affinity
		// (SQLite); a timestamp or string mark is quoted.
		literal := "'" + strings.ReplaceAll(last, "'", "''") + "'"
		if numericWatermark.MatchString(last) {
			literal = last
		}
		filtered = fmt.Sprintf("SELECT * FROM (%s) AS brk_inc WHERE brk_inc.%s >= %s", baseQuery, cursorCol, literal)
		r.log(node.ID, models.LogLevelInfo, "incremental: reading rows with %s >= the last high-water mark", cursorCol)
	} else {
		r.log(node.ID, models.LogLevelInfo, "incremental: first run, reading the source in full and recording a high-water mark")
	}

	save := func() {
		if newHW == "" {
			return // source empty / all-null cursor: keep the previous mark
		}
		if serr := ws.SetNodeWatermark(pipelineID, node.ID, newHW); serr != nil {
			r.log(node.ID, models.LogLevelWarning, "incremental: could not store the new high-water mark: %v", serr)
			return
		}
		r.log(node.ID, models.LogLevelInfo, "incremental: high-water mark advanced to %s", newHW)
	}
	return filtered, save, nil
}

// maxCursorValue reads MAX(cursorCol) over the base source query as a string.
// An empty string means the source has no rows (or an all-null cursor), in
// which case the mark is left unchanged.
func (r *Runner) maxCursorValue(ctx context.Context, sourceURI, baseQuery, cursorCol string) (string, error) {
	q := fmt.Sprintf("SELECT MAX(brk_src.%s) AS brk_max FROM (%s) AS brk_src", cursorCol, baseQuery)
	ds, err := QueryDatabaseContext(ctx, sourceURI, q)
	if err != nil {
		return "", err
	}
	if len(ds.Rows) == 0 || len(ds.Columns) == 0 {
		return "", nil
	}
	return watermarkString(ds.Rows[0][ds.Columns[0]]), nil
}

// watermarkString renders a cursor value to the canonical string stored and
// later injected as a literal. Numbers and timestamps are rendered so they
// compare correctly when the database casts the literal back.
func watermarkString(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}
