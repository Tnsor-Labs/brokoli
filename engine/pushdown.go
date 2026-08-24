package engine

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
)

// ADR-023 in practice: a source_db hands its consumer a TableRef instead of
// rows, a compilable transform rewrites that reference's query, and a sink_db
// on the same server turns the whole chain into one INSERT ... SELECT. The
// engine issues a statement and reads a row count; no row crosses the worker.
//
// Every step refuses rather than guesses. A refusal costs a pushdown; a wrong
// guess writes rows through the wrong connection or changes what a pipeline
// produces, so the fallback to today's interpreted path is the default and
// the pushdown is the exception that has to earn itself.

// tableRefFromSourceDB decides whether a source_db node can hand its consumer
// a reference instead of rows.
func (r *Runner) tableRefFromSourceDB(node models.Node) (*TableRef, bool) {
	if dataPlaneInterpreted() {
		return nil, false
	}
	uri, _ := node.Config["uri"].(string)
	query, _ := node.Config["query"].(string)
	if uri == "" || query == "" {
		return nil, false
	}
	dialectName := dialectForURI(uri)
	d, ok := dbdialect.For(dialectName)
	if !ok {
		return nil, false
	}
	if _, ok := d.(dbdialect.Addresser); !ok {
		// Without a same-server rule there is no way to know a write may read
		// this query directly, so the segment stays in the engine.
		return nil, false
	}
	// A node the pipeline reads more than once would have its query executed
	// once per consumer, which is a different amount of work against the
	// source than materialising once. Only a single consumer keeps the
	// pushdown honest about cost.
	if r.consumerCount(node.ID) != 1 {
		return nil, false
	}
	order, err := queryColumnOrder(r.ctx, uri, query, d)
	if err != nil || len(order) == 0 {
		return nil, false
	}
	ref := &TableRef{
		ConnURI:  uri,
		Query:    query,
		Columns:  order,
		Dialect:  dialectName,
		Absorbed: []string{node.ID},
	}

	// Look ahead before committing. A source that hands out a reference its
	// consumers cannot use leaves them with no input at all, and a transform
	// handed nothing writes nothing — the run reports success having moved
	// zero rows. So the whole chain is planned here, and the reference is
	// only produced if every node between this one and a sink on the same
	// server can consume it.
	if !r.segmentPushesDown(node.ID, ref) {
		return nil, false
	}
	return ref, true
}

// segmentPushesDown walks the single-consumer chain from a source and reports
// whether the entire segment can execute in the database. It is the same
// composition the nodes will perform when they run, done in advance, so that
// commitment happens once with full information rather than node by node with
// none.
func (r *Runner) segmentPushesDown(fromNodeID string, ref *TableRef) bool {
	current := ref
	nodeID := fromNodeID
	for hops := 0; hops < len(r.pipe.Nodes)+1; hops++ {
		next, ok := r.singleConsumer(nodeID)
		if !ok {
			return false
		}
		switch next.Type {
		case models.NodeTypeTransform:
			composed, ok := r.composeTransformOntoTableRef(next, current)
			if !ok {
				return false
			}
			current, nodeID = composed, next.ID
		case models.NodeTypeSinkDB:
			return r.sinkAcceptsTableRef(next, current)
		default:
			return false
		}
	}
	return false
}

// singleConsumer returns the one node reading this node's output, or false if
// there is not exactly one.
func (r *Runner) singleConsumer(nodeID string) (models.Node, bool) {
	var toID string
	n := 0
	for _, e := range r.pipe.Edges {
		if e.From == nodeID {
			n++
			toID = e.To
		}
	}
	if n != 1 {
		return models.Node{}, false
	}
	for _, node := range r.pipe.Nodes {
		if node.ID == toID {
			return node, true
		}
	}
	return models.Node{}, false
}

// sinkAcceptsTableRef reports whether a sink could write this reference
// without the engine reading rows. It answers the same question
// runSinkDBFromTableRef does, without touching the database.
func (r *Runner) sinkAcceptsTableRef(node models.Node, in *TableRef) bool {
	uri, _ := node.Config["uri"].(string)
	table, _ := node.Config["table"].(string)
	if uri == "" || table == "" || in == nil || len(in.Columns) == 0 {
		return false
	}
	if !sameServer(in.Dialect, uri, in.ConnURI) {
		return false
	}
	mode, _ := node.Config["mode"].(string)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "append", "overwrite":
		return true
	default:
		return false
	}
}

// materializeTableRef reads a reference's rows into the engine.
//
// It exists as a guard, not as a path anyone should take: the lookahead above
// means a node should never receive a reference it cannot use. If that is ever
// wrong, this is what stops the failure from being a run that reports success
// having written nothing.
func (r *Runner) materializeTableRef(nodeID string, in *TableRef) (*common.DataSet, error) {
	r.log(nodeID, models.LogLevelWarning,
		"received a database reference this node cannot consume; reading its rows into the engine "+
			"(this should not happen — the segment plan is supposed to prevent it)")
	ds, err := QueryDatabase(in.ConnURI, in.Query)
	if err != nil {
		return nil, fmt.Errorf("materialize pushed-down input: %w", err)
	}
	return ds, nil
}

// composeTransformOntoTableRef rewrites a reference's query with a transform's
// compiled SQL, or reports that the transform must run in the engine.
func (r *Runner) composeTransformOntoTableRef(node models.Node, in *TableRef) (*TableRef, bool) {
	if in == nil || dataPlaneInterpreted() {
		return nil, false
	}
	if r.consumerCount(node.ID) != 1 {
		return nil, false
	}
	rules, err := parseNodeTransformRules(node)
	if err != nil {
		return nil, false
	}
	plan, ok := planTransformRules(rules)
	if !ok {
		return nil, false
	}
	d, ok := dbdialect.For(in.Dialect)
	if !ok {
		return nil, false
	}
	kinds, err := describeQueryColumns(r.ctx, in.ConnURI, in.Query, d)
	if err != nil {
		return nil, false
	}
	// The dialect is the connection's, not a literal. It was threaded through
	// this compiler from the start and every caller passed "postgres"
	// (ADR-024).
	compiled, ok := compilePlanToSQL(plan, in.Query, in.Columns, in.Dialect, kinds)
	if !ok {
		return nil, false
	}
	return &TableRef{
		ConnURI:  in.ConnURI,
		Query:    compiled.Query,
		Columns:  compiled.Columns,
		Dialect:  in.Dialect,
		Absorbed: append(append([]string{}, in.Absorbed...), node.ID),
	}, true
}

// runSinkDBFromTableRef writes a reference's rows straight into the sink's
// table with one statement, and returns how many rows the server reported.
func (r *Runner) runSinkDBFromTableRef(node models.Node, in *TableRef) (int64, bool, error) {
	if in == nil || dataPlaneInterpreted() {
		return 0, false, nil
	}
	uri, _ := node.Config["uri"].(string)
	table, _ := node.Config["table"].(string)
	if uri == "" || table == "" {
		return 0, false, nil
	}
	if !sameServer(in.Dialect, uri, in.ConnURI) {
		return 0, false, nil
	}
	mode, _ := node.Config["mode"].(string)
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "append", "overwrite":
	default:
		// upsert needs a conflict target and its own equivalence argument.
		return 0, false, nil
	}

	d, ok := dbdialect.For(in.Dialect)
	if !ok {
		return 0, false, nil
	}
	target := d.QuoteQualifiedIdent(table)
	cols := make([]string, 0, len(in.Columns))
	for _, c := range in.Columns {
		cols = append(cols, d.QuoteIdent(c))
	}
	if len(cols) == 0 {
		return 0, false, nil
	}
	insert := fmt.Sprintf("INSERT INTO %s (%s) SELECT %s FROM (%s) AS brokoli_pushdown",
		target, strings.Join(cols, ", "), strings.Join(cols, ", "), in.Query)

	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return 0, false, err
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return 0, false, fmt.Errorf("open %s: %w", driver, err)
	}
	defer db.Close()

	// One transaction: the read, any clear, and the write either all happen
	// or none do. ADR-023's resume rule rests on this — inside a segment the
	// commit is the artifact, so there is no half-written state to resume
	// from, only a segment to re-run from its barrier.
	tx, err := db.BeginTx(r.ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()

	if strings.EqualFold(strings.TrimSpace(mode), "overwrite") {
		truncate, _ := node.Config["truncate"].(bool)
		clear := fmt.Sprintf("DELETE FROM %s", target)
		if truncate {
			clear = fmt.Sprintf("TRUNCATE TABLE %s", target)
		}
		if _, err := tx.ExecContext(r.ctx, clear); err != nil {
			return 0, false, fmt.Errorf("clear %s: %w", table, err)
		}
	}

	res, err := tx.ExecContext(r.ctx, insert)
	if err != nil {
		return 0, false, fmt.Errorf("pushdown write to %s: %w", table, err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		// ADR-023: a path is only eligible when it has a free count source.
		// Without one there is nothing honest to report, so fail rather than
		// commit a write whose size is unknown.
		return 0, false, fmt.Errorf("pushdown write to %s reported no row count: %w", table, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit pushdown write to %s: %w", table, err)
	}
	return affected, true, nil
}

// consumerCount is how many active edges read this node's output.
func (r *Runner) consumerCount(nodeID string) int {
	n := 0
	for _, e := range r.pipe.Edges {
		if e.From == nodeID {
			n++
		}
	}
	return n
}

// queryColumnOrder returns a query's columns in order. describeQueryColumns
// answers what type each column is but not their order, and the order is what
// an INSERT column list has to agree with.
func queryColumnOrder(ctx context.Context, uri, query string, d dbdialect.Dialect) ([]string, error) {
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, d.ProbeColumnsSQL(query))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return rows.Columns()
}

// tryPushdown asks whether a node's work can stay in its database. handled is
// false for every node and every shape the engine has not proven it can push
// down, which is the overwhelming majority; those fall through to the
// execution path they used before this existed.
func (r *Runner) tryPushdown(node models.Node, inputTable *TableRef) (nodeExecutionResult, bool, error) {
	switch node.Type {
	case models.NodeTypeSourceDB:
		ref, ok := r.tableRefFromSourceDB(node)
		if !ok {
			return nodeExecutionResult{}, false, nil
		}
		r.log(node.ID, models.LogLevelInfo,
			"Kept in the database as a reference; no rows read into the engine")
		return nodeExecutionResult{outputTable: ref}, true, nil

	case models.NodeTypeTransform:
		ref, ok := r.composeTransformOntoTableRef(node, inputTable)
		if !ok {
			return nodeExecutionResult{}, false, nil
		}
		r.log(node.ID, models.LogLevelInfo,
			"Compiled into the upstream query; no rows read into the engine")
		return nodeExecutionResult{outputTable: ref}, true, nil

	case models.NodeTypeSinkDB:
		if inputTable == nil {
			return nodeExecutionResult{}, false, nil
		}
		affected, ok, err := r.runSinkDBFromTableRef(node, inputTable)
		if err != nil {
			return nodeExecutionResult{}, true, err
		}
		if !ok {
			return nodeExecutionResult{}, false, nil
		}
		table, _ := node.Config["table"].(string)
		r.log(node.ID, models.LogLevelInfo,
			"Wrote %d row(s) to %s inside the database; the engine moved no rows (nodes: %s)",
			affected, table, strings.Join(inputTable.Absorbed, ", "))
		return nodeExecutionResult{
			pushedRowCount: affected,
			pushedAbsorbed: inputTable.Absorbed,
		}, true, nil
	}
	return nodeExecutionResult{}, false, nil
}

// creditAbsorbedNodes records the segment's row count against the nodes whose
// work the database performed.
//
// ADR-023 makes a segment that executed in the database one unit of work with
// one count source — the server's report at the consuming write. The nodes
// upstream of it did run, and did produce those rows; they simply never held
// them. Leaving them at zero would answer "how many rows did this source
// read?" with a number that is true of the engine and false of the pipeline.
func (r *Runner) creditAbsorbedNodes(absorbed []string, rowCount int64, sinkNodeID string) {
	if len(absorbed) == 0 || rowCount <= 0 {
		return
	}
	want := make(map[string]bool, len(absorbed))
	for _, id := range absorbed {
		if id != sinkNodeID {
			want[id] = true
		}
	}
	if len(want) == 0 {
		return
	}
	runs, err := r.store.ListNodeRunsByRun(r.run.ID)
	if err != nil {
		// Best effort: the run itself is complete and correct, and a wrong
		// count in the run view is not worth failing it over.
		r.log(sinkNodeID, models.LogLevelWarning,
			"could not credit pushed-down nodes with the segment row count: %v", err)
		return
	}
	for i := range runs {
		nr := runs[i]
		if !want[nr.NodeID] || nr.RowCount == int(rowCount) {
			continue
		}
		nr.RowCount = int(rowCount)
		if err := r.store.UpdateNodeRun(&nr); err != nil {
			r.log(nr.NodeID, models.LogLevelWarning,
				"could not record the segment row count: %v", err)
		}
	}
}
