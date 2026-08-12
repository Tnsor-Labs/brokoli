// Dynamic node expansion (Tnsor-Labs/brokoli#31) — execution support for
// brokoli-sdk's `.expand()` (`_TaskWrapper.expand()`, brokoli/pipeline.py).
//
// ── The architectural tension, and how it's resolved here ──────────────
//
// engine/runner.go's Execute schedules the pipeline as a STATIC DAG: Kahn's-
// algorithm wave scheduling over pipe.Edges, where every node produces
// exactly one *common.DataSet consumed by its statically-known downstream
// edges. `.expand()`'s fan-out count is only known at RUNTIME (it depends on
// the size of an upstream collection) — a real mismatch with that model, not
// a detail to paper over.
//
// Tracing brokoli-sdk confirms the exact compiled shape (see
// _TaskWrapper.expand() and CollectionRef.collect(), brokoli/pipeline.py):
// `.expand()` does NOT introduce a new node type or N static nodes. It
// compiles onto the existing `code` node type, adding a single `expansion`
// config block: {"over": {param: upstream_node_id, ...}, "key": {...}?}.
// `.collect(mode="union")` on the resulting CollectionRef calls the same
// `_build_union_node` helper the module-level `union()` function uses, with
// refs=[the CollectionRef] — i.e. exactly ONE static edge from the expand
// node into an explicit `union` node. There is no way for the SDK to emit N
// edges for a fan-out whose count it doesn't know at authoring time.
//
// Given that, "expand node produces N dynamic outputs, then a downstream
// union node combines them" does not fit: engine/union.go's UnionDatasets
// requires >=2 non-nil inputs and errors otherwise, but the graph only ever
// has one static edge into that union node here.
//
// The resolution implemented below: the expansion node does its own
// per-item fan-out AND its own internal combination as part of executing
// that single static node, producing one *common.DataSet as its output —
// matching every other node's one-output-per-node contract. Concretely,
// runCodeExpansion:
//  1. resolves the upstream collection(s) named in expansion.over (each
//     item = one row of the upstream dataset — there is no separate
//     "collection" type in the engine; ordinary edge-resolved
//     *common.DataSet rows ARE the items),
//  2. executes the task script once per item, in-process and sequentially
//     (an acceptable first version — the same scope decision issue #30
//     made for in-process pagination; see runSourceAPI's doc comments),
//     reusing the existing code-node subprocess bridge (ExecuteCodeNode,
//     engine/codenode.go — the same mechanism #34 reused for
//     dataset_map/dataset_filter),
//  3. combines the per-item results into that one output dataset via
//     combineExpansionResults, which reuses UnionDatasets (#34) once
//     there are 2+ per-item results.
//
// A downstream explicit `union`/`.collect(mode="union")` node, if present,
// then receives this one already-combined dataset as its single input —
// which would otherwise trip UnionDatasets's >=2-input check, since it's
// the union node's ONLY input. Decision: option (a) from the issue — the
// union node's single-input case becomes a documented no-op pass-through
// ("identity union") exactly when validation has established it's fed by a
// single dynamic-expansion node (see validateEdgeSemantics's union case and
// runUnion in engine/node_handlers.go) — a legitimate scenario this issue
// introduces, not a validation gap. Option (b) (not compiling a node at
// all) was checked against the SDK trace above and ruled out: `.collect()`
// always compiles a real `union` node, even for a single-ref collection.
package engine

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/tracing"
	"github.com/Tnsor-Labs/brokoli/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// defaultMaxExpansionInstances bounds how many per-item instances a single
// expansion node may fan out into, per the issue's requirement that an
// unbounded upstream collection can't silently create an unmanageable
// number of instances. 1000 is a sane default for an in-process, sequential
// first version (a genuinely large collection is better served by the
// distributed/parallel physical planner the issue explicitly scopes out).
// Overridable per-node via expansion.max_instances.
const defaultMaxExpansionInstances = 1000

// remoteInstanceDispatchMargin is added to an instance's own timeoutSec to
// get dispatchExpansionInstanceRemotely's wait deadline — dispatch and
// queueing latency the synchronous local-execution path never had to
// account for (ADR-017, 2026-08-12 wait-and-fetch design, which names this
// an explicit tunable rather than a permanent constant). A package
// variable rather than an inline literal so a test can shrink it instead
// of waiting out a real multi-minute timeout to exercise that path.
var remoteInstanceDispatchMargin = 2 * time.Minute

// remoteInstanceStatusPollInterval is how often
// dispatchExpansionInstanceRemotely checks whether a dispatched instance
// has settled yet. Deliberately its own variable, not a reuse of
// store.DefaultRenewInterval (also 5s by default, coincidentally): that
// interval's job is keeping a lease from expiring — coarse is fine and
// even desirable there — while this one's job is user-facing latency
// after a worker actually finishes. Coupling the two would mean a fast
// worker's result sits unnoticed for a full lease-renewal tick, which
// found this exact bug when a 50ms test double's response wasn't picked
// up until 5 seconds later.
var remoteInstanceStatusPollInterval = time.Second

// funcRef mirrors brokoli-sdk's _func_ref(fn) output — a name/description
// reference, NOT executable code (see brokoli/pipeline.py's _func_ref doc
// comment). Used for expansion.key.
type funcRef struct {
	Name string
	Doc  string
}

// expansionConfig is the parsed form of a code node's `expansion` config
// block, as compiled by brokoli-sdk's _TaskWrapper.expand():
//
//	{"over": {param: upstream_node_id, ...}, "key": {"name": ..., "doc": ...}?, "max_instances": N?}
type expansionConfig struct {
	// Over maps a task parameter name to the upstream node ID whose output
	// rows are the items to fan out over, e.g. {"file": "files_node_id"}.
	Over map[string]string
	// Key is expand()'s optional key= callable, serialized as a name/doc
	// reference only (see funcRef) — not executable. Nil when absent.
	Key *funcRef
	// MaxInstances bounds the fan-out; defaultMaxExpansionInstances when
	// not set in config.
	MaxInstances int
}

// nodeHasExpansion reports whether n is a `code` node carrying an
// `expansion` config block — the engine's sole signal for routing to
// runCodeExpansion instead of the ordinary runCode (see runNodeLogic's
// NodeTypeCode case). Deliberately keys off Config directly rather than
// Node.Capabilities' "dynamic-expansion" tag: Config is authoritative and
// present regardless of whether a given SDK/client version also sets
// capabilities (see models.CapabilityDynamicExpansion's doc comment).
func nodeHasExpansion(n models.Node) bool {
	if n.Type != models.NodeTypeCode {
		return false
	}
	_, ok := n.Config["expansion"]
	return ok
}

// parseExpansionConfig parses and structurally validates node.Config's
// `expansion` block. Returns an error naming the node if malformed — both
// runCodeExpansion (execution time) and validate.go's validateNodeConfig/
// validateNodeConfigDetailed (deploy time) call this directly for the
// actual shape rules, so the two stay in sync by construction rather than
// by convention.
func parseExpansionConfig(node models.Node) (*expansionConfig, error) {
	raw, ok := node.Config["expansion"]
	if !ok {
		return nil, fmt.Errorf("node %s (%s) has no 'expansion' config", node.Name, node.ID)
	}
	expMap, ok := raw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("node %s (%s): 'expansion' must be an object", node.Name, node.ID)
	}

	overRaw, ok := expMap["over"]
	if !ok {
		return nil, fmt.Errorf("node %s (%s): 'expansion.over' is required", node.Name, node.ID)
	}
	overMap, ok := overRaw.(map[string]interface{})
	if !ok || len(overMap) == 0 {
		return nil, fmt.Errorf("node %s (%s): 'expansion.over' must be a non-empty object mapping parameter name(s) to upstream node id(s)", node.Name, node.ID)
	}
	over := make(map[string]string, len(overMap))
	for param, v := range overMap {
		s, ok := v.(string)
		if !ok || s == "" {
			return nil, fmt.Errorf("node %s (%s): 'expansion.over[%q]' must be a non-empty upstream node id string", node.Name, node.ID, param)
		}
		over[param] = s
	}

	cfg := &expansionConfig{Over: over, MaxInstances: defaultMaxExpansionInstances}

	if keyRaw, ok := expMap["key"]; ok {
		keyMap, ok := keyRaw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("node %s (%s): 'expansion.key' must be an object (a name/doc reference — see brokoli-sdk's _func_ref)", node.Name, node.ID)
		}
		name, _ := keyMap["name"].(string)
		doc, _ := keyMap["doc"].(string)
		cfg.Key = &funcRef{Name: name, Doc: doc}
	}

	if miRaw, ok := expMap["max_instances"]; ok {
		mi, ok := miRaw.(float64)
		if !ok || mi <= 0 {
			return nil, fmt.Errorf("node %s (%s): 'expansion.max_instances' must be a positive number", node.Name, node.ID)
		}
		cfg.MaxInstances = int(mi)
	}

	return cfg, nil
}

// deriveInstanceKey computes this item's stable per-item instance identity.
//
// expansion.key, when present, is a name/doc REFERENCE to the SDK-side
// key= callable (brokoli-sdk's _func_ref) — not executable code, the same
// gap #34 encountered for DatasetRef.map()/.filter()'s function references
// (see engine/partition_transform.go). There is no serialized script to run
// server-side, so it cannot be executed here. This is a documented
// limitation, not a silent one: callers log a warning when key is present
// (see runCodeExpansion) and fall back to this deterministic index-based
// scheme, which is stable across retries/re-runs as long as the upstream
// collection's row order doesn't change between runs — the same assumption
// an index-based key implicitly makes anywhere ordering substitutes for
// identity.
func deriveInstanceKey(index int) string {
	return fmt.Sprintf("idx:%d", index)
}

// expansionItemSource is one named collection contributing items to an
// expansion (one entry per expansion.over[param]).
type expansionItemSource struct {
	param  string
	nodeID string
	ds     *common.DataSet
}

// resolveExpansionItems resolves expansion.over against edgeInputsByFrom
// (this node's upstream outputs, keyed by upstream node ID — see
// Runner.executeNode) and builds the per-item input rows.
//
// Per the issue's framing, there is no separate "collection" type in the
// engine: the resolved upstream node's output IS the collection, and each
// of its ROWS is one item to fan out over (mirrors CollectionRef's doc
// comment — "one entry per file/page/record"). When expansion.over names
// more than one collection (multiple task parameters), items are zipped by
// index and each instance's input row is the union of that index's row
// across all named collections — all named collections must have equal
// row counts, or this errors naming the mismatched pair.
func resolveExpansionItems(node models.Node, cfg *expansionConfig, edgeInputsByFrom map[string]*common.DataSet) ([]common.DataRow, []string, error) {
	params := make([]string, 0, len(cfg.Over))
	for p := range cfg.Over {
		params = append(params, p)
	}
	sort.Strings(params) // deterministic column/merge order

	sources := make([]expansionItemSource, 0, len(params))
	for _, p := range params {
		nodeID := cfg.Over[p]
		ds, ok := edgeInputsByFrom[nodeID]
		if !ok || ds == nil {
			return nil, nil, fmt.Errorf("node %s (%s): expansion.over[%q] references upstream node %q but no input data was found for it (missing edge, or the upstream node produced no output)", node.Name, node.ID, p, nodeID)
		}
		sources = append(sources, expansionItemSource{param: p, nodeID: nodeID, ds: ds})
	}

	n := len(sources[0].ds.Rows)
	for _, s := range sources[1:] {
		if len(s.ds.Rows) != n {
			return nil, nil, fmt.Errorf("node %s (%s): expansion.over collections have mismatched item counts: %q (%s) has %d, %q (%s) has %d — expand() requires every named collection to have the same number of items",
				node.Name, node.ID, sources[0].param, sources[0].nodeID, n, s.param, s.nodeID, len(s.ds.Rows))
		}
	}

	var columns []string
	seen := make(map[string]bool)
	for _, s := range sources {
		for _, c := range s.ds.Columns {
			if !seen[c] {
				seen[c] = true
				columns = append(columns, c)
			}
		}
	}

	items := make([]common.DataRow, n)
	for i := 0; i < n; i++ {
		row := make(common.DataRow, len(columns))
		for _, s := range sources {
			for k, v := range s.ds.Rows[i] {
				row[k] = v
			}
		}
		items[i] = row
	}

	return items, columns, nil
}

// combineExpansionResults folds each item's script output into this node's
// single output dataset. See this file's top-of-file doc comment for why
// this happens INSIDE the expansion node rather than in a downstream union
// node fed by N dynamic edges.
//
// 0 results (an empty upstream collection — a legitimate, non-error case)
// yields an empty dataset. Exactly 1 result is returned as-is: UnionDatasets
// requires >=2 non-nil inputs, and one item's output needs no combining
// anyway. 2+ results are combined via UnionDatasets (#34), inheriting its
// column-union/schema-mismatch behavior for consistency with every other
// multi-input combination in the engine.
func combineExpansionResults(results []*common.DataSet) (*common.DataSet, error) {
	switch len(results) {
	case 0:
		return &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}, nil
	case 1:
		return results[0], nil
	default:
		return UnionDatasets(results)
	}
}

// runCodeExpansion executes a dynamic-expansion `code` node (#31): once per
// item in the upstream collection(s) named by expansion.over, in-process
// and sequentially (concurrency across items is out of scope for this first
// version — consistent with #30's in-process-sequential scope decision for
// source_api pagination; see runSourceAPI), tracking each item's outcome as
// its own durable models.ExpansionInstance row (see that type's doc comment
// for why this is a dedicated table rather than reusing NodeRun/
// ExecutionAttempt's (node_id, attempt) keying) so each item's success or
// failure is independently visible, not one aggregate status for the whole
// expansion.
//
// nodeAttempt is the expansion node's own retry-attempt number (from
// executeNode's outer retry loop). A retry of the whole node re-invokes
// this function, but items that already succeeded on an earlier attempt of
// the SAME Runner (i.e. the same run, still the same process — see
// Runner.expansionResults) are not re-executed: their cached output is
// reused directly and only items that failed or were never reached run
// again (Tnsor-Labs/brokoli#90 M3, ADR-015 §6). This does not yet survive a
// process crash mid-expansion — that needs durable per-item output storage
// (not just the status/row-count models.ExpansionInstance already
// persists), which is the larger per-instance dispatch work #90 M3's
// acceptance gate covers.
func (r *Runner) runCodeExpansion(node models.Node, edgeInputsByFrom map[string]*common.DataSet, nodeAttempt int) (*common.DataSet, error) {
	cfg, err := parseExpansionConfig(node)
	if err != nil {
		return nil, err
	}

	items, _, err := resolveExpansionItems(node, cfg, edgeInputsByFrom)
	if err != nil {
		return nil, err
	}

	if len(items) > cfg.MaxInstances {
		return nil, fmt.Errorf("node %s (%s): expansion would create %d instances, exceeding the configured limit of %d (expansion.max_instances) — refusing to fan out rather than silently truncating the collection", node.Name, node.ID, len(items), cfg.MaxInstances)
	}

	if cfg.Key != nil {
		r.log(node.ID, models.LogLevelWarning,
			"expansion.key (%q) is a name/description reference, not executable code (brokoli-sdk records it via _func_ref, never serializes it as runnable code) — this backend version cannot execute it, so instance identity falls back to a deterministic collection-index scheme (idx:N) instead",
			cfg.Key.Name)
	}

	script, _ := node.Config["script"].(string)
	if script == "" {
		return nil, fmt.Errorf("node %s (%s): expansion node requires 'script' in config", node.Name, node.ID)
	}

	timeoutSec := 30
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}

	// Strip 'script' (avoid circular ref, matches runCode) and 'expansion'
	// (engine-only dispatch metadata the per-item script has no use for)
	// before handing config to the subprocess.
	configForScript := make(map[string]interface{})
	for k, v := range node.Config {
		if k != "script" && k != "expansion" {
			configForScript[k] = v
		}
	}

	var runParams map[string]string
	if r.varCtx != nil {
		runParams = r.varCtx.Params
	}

	r.log(node.ID, models.LogLevelInfo, "expansion: fanning out into %d instance(s)", len(items))

	results := make([]*common.DataSet, 0, len(items))
	reused := 0
	for i, item := range items {
		instanceKey := deriveInstanceKey(i)

		if cached, ok := r.reusedExpansionInstance(node.ID, instanceKey); ok {
			if err := r.recordReusedExpansionInstance(node, nodeAttempt, i, instanceKey, len(items), cached); err != nil {
				return nil, fmt.Errorf("node %s (%s): recording reused expansion instance %d/%d (key=%s): %w", node.Name, node.ID, i+1, len(items), instanceKey, err)
			}
			results = append(results, cached)
			reused++
			continue
		}

		itemDS := &common.DataSet{
			Columns: columnsOf(item),
			Rows:    []common.DataRow{item},
		}

		out, err := r.executeExpansionInstance(node, nodeAttempt, i, instanceKey, len(items), itemDS, script, configForScript, runParams, timeoutSec)
		if err != nil {
			return nil, fmt.Errorf("node %s (%s): expansion instance %d/%d (key=%s) failed: %w", node.Name, node.ID, i+1, len(items), instanceKey, err)
		}
		if out != nil {
			r.rememberExpansionInstance(node.ID, instanceKey, out)
			results = append(results, out)
		}
	}
	if reused > 0 {
		r.log(node.ID, models.LogLevelInfo, "expansion: retry reused %d instance(s) that already succeeded on a prior attempt, re-ran %d", reused, len(items)-reused)
	}

	combined, err := combineExpansionResults(results)
	if err != nil {
		return nil, fmt.Errorf("node %s (%s): combining %d expansion instance result(s): %w", node.Name, node.ID, len(results), err)
	}
	r.log(node.ID, models.LogLevelInfo, "expansion: combined %d instance result(s) into %d rows (%d columns)",
		len(results), len(combined.Rows), len(combined.Columns))
	return combined, nil
}

// columnsOf returns row's keys as a column list — used to give each
// single-row per-item DataSet passed to ExecuteCodeNode a Columns list
// (rows themselves are just maps, but the code-node subprocess bridge's
// CodeNodeInput.Columns is what the Python wrapper's `columns` variable
// reflects).
func columnsOf(row common.DataRow) []string {
	cols := make([]string, 0, len(row))
	for k := range row {
		cols = append(cols, k)
	}
	sort.Strings(cols) // deterministic across runs
	return cols
}

// executeExpansionInstance runs the script once for a single item, tracking
// it as its own durable models.ExpansionInstance attempt (best-effort: a
// store that doesn't implement store.ExpansionInstanceStore simply doesn't
// get per-item durability — see that interface's doc comment — expansion
// still executes either way).
func (r *Runner) executeExpansionInstance(node models.Node, nodeAttempt, index int, instanceKey string, total int, itemDS *common.DataSet, script string, configForScript map[string]interface{}, runParams map[string]string, timeoutSec int) (*common.DataSet, error) {
	instStore, _ := r.store.(store.ExpansionInstanceStore)

	startTime := time.Now().UTC()
	ei := &models.ExpansionInstance{
		ID:            common.NewID(),
		RunID:         r.run.ID,
		NodeID:        node.ID,
		NodeAttempt:   nodeAttempt,
		InstanceIndex: index,
		InstanceKey:   instanceKey,
		Status:        models.RunStatusRunning,
		StartedAt:     &startTime,
	}
	if instStore != nil && !r.dryRun {
		if err := instStore.CreateExpansionInstance(ei); err != nil {
			return nil, fmt.Errorf("persist expansion instance %d: %w", index, err)
		}
	}

	// execAttemptStore extends #144's node-level claim/lease/fencing
	// wiring down to instance granularity via ADR-017's instance_key
	// dimension (#147): this item gets its own claim/lease/fencing record,
	// keyed (run_id, node_id, instance_key, attempt=nodeAttempt) — attempt
	// here tracks which dispatch generation of the ENCLOSING node this
	// instance ran under, exactly matching models.ExpansionInstance's own
	// NodeAttempt field, not an independent per-item retry counter (an
	// item is still only retried by the node's own outer retry loop
	// re-invoking runCodeExpansion — see that function's doc comment).
	var execAttemptStore store.ExecutionAttemptStore
	var execFencingGen int64
	if as, ok := r.store.(store.ExecutionAttemptStore); ok && !r.dryRun {
		execAttemptStore = as
		idempotencyKey := fmt.Sprintf("%s:%s:%s:%d", r.run.ID, node.ID, instanceKey, nodeAttempt)
		if err := r.store.WithTx(func(tx *sql.Tx) error {
			return execAttemptStore.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
				RunID: r.run.ID, NodeID: node.ID, InstanceKey: instanceKey, Attempt: nodeAttempt,
				Status: models.AttemptStatusQueued, IdempotencyKey: idempotencyKey,
			})
		}); err != nil {
			return nil, fmt.Errorf("persist execution attempt for expansion instance %d: %w", index, err)
		}
		// Local execution is one bounded, synchronous call: a lease sized
		// to cover it (timeoutSec plus a margin) needs no renewal — claim
		// through settle all happen within this one call, so there is no
		// window where the lease needs keeping alive across a separate
		// goroutine's lifetime. Remote dispatch (ADR-017, 2026-08-12
		// wait-and-fetch design) is not bounded by one blocking call — the
		// wait spans dispatch, queueing, and remote execution — so it
		// claims with the same short, renewed lease #144 already uses for
		// node-level attempts instead; dispatchExpansionInstanceRemotely
		// starts that renewal goroutine itself.
		leaseDuration := time.Duration(timeoutSec)*time.Second + 30*time.Second
		if r.instanceJobQueue != nil {
			leaseDuration = store.DefaultLeaseDuration
		}
		gen, ok, err := execAttemptStore.ClaimAttempt(r.run.ID, node.ID, instanceKey, nodeAttempt, r.instanceID, leaseDuration)
		if err != nil {
			return nil, fmt.Errorf("claim execution attempt for expansion instance %d: %w", index, err)
		}
		if !ok {
			return nil, fmt.Errorf("claim execution attempt for expansion instance %d: already claimed or terminal", index)
		}
		execFencingGen = gen
		if err := execAttemptStore.AckAttempt(r.run.ID, node.ID, instanceKey, nodeAttempt, r.instanceID, execFencingGen); err != nil {
			return nil, fmt.Errorf("ack execution attempt for expansion instance %d: %w", index, err)
		}
	}

	var span trace.Span
	if !r.dryRun {
		_, span = tracing.Tracer().Start(r.ctx, "brokoli.node.expansion_instance",
			trace.WithAttributes(
				attribute.String("run_id", r.run.ID),
				attribute.String("node_id", node.ID),
				attribute.Int("node_attempt", nodeAttempt),
				attribute.Int("instance_index", index),
				attribute.String("instance_key", instanceKey),
			))
	}

	r.logWithTrace(node.ID, models.LogLevelInfo, "", nodeAttempt, map[string]string{
		"instance_index": fmt.Sprintf("%d", index),
		"instance_key":   instanceKey,
	}, "expansion instance %d/%d (key=%s): starting", index+1, total, instanceKey)

	var result *common.DataSet
	var stderr string
	var err error
	if execAttemptStore != nil && r.instanceJobQueue != nil {
		result, err = r.dispatchExpansionInstanceRemotely(node, nodeAttempt, instanceKey, execFencingGen, itemDS, script, configForScript, runParams, timeoutSec)
	} else {
		result, stderr, err = ExecuteCodeNode(script, itemDS, configForScript, runParams, timeoutSec)
	}
	if stderr != "" {
		for _, line := range splitLines(stderr) {
			if line != "" {
				r.log(node.ID, models.LogLevelWarning, "python[instance %d]: %s", index, line)
			}
		}
	}

	duration := time.Since(startTime).Milliseconds()
	ei.DurationMs = duration

	if err != nil {
		ei.Status = models.RunStatusFailed
		ei.Error = err.Error()
		if instStore != nil && !r.dryRun {
			if uErr := instStore.UpdateExpansionInstance(ei); uErr != nil {
				r.log(node.ID, models.LogLevelWarning, "failed to persist failed expansion instance %d: %v", index, uErr)
			}
		}
		if execAttemptStore != nil {
			// Non-fatal: the ExpansionInstance row above is already the
			// durable, authoritative record of this item's failure. This
			// settles the supplementary claim/lease record to match.
			if failErr := execAttemptStore.FailAttempt(r.run.ID, node.ID, instanceKey, nodeAttempt, execFencingGen, err.Error()); failErr != nil {
				r.log(node.ID, models.LogLevelWarning, "failed to settle execution attempt for expansion instance %d as failed: %v", index, failErr)
			}
		}
		if !r.dryRun {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			span.End()
		}
		r.logWithTrace(node.ID, models.LogLevelError, "", nodeAttempt, nil,
			"expansion instance %d/%d (key=%s) failed after %dms: %v", index+1, total, instanceKey, duration, err)
		return nil, err
	}

	rowCount := 0
	if result != nil {
		rowCount = len(result.Rows)
	}
	ei.Status = models.RunStatusSuccess
	ei.RowCount = rowCount
	if instStore != nil && !r.dryRun {
		if uErr := instStore.UpdateExpansionInstance(ei); uErr != nil {
			r.log(node.ID, models.LogLevelWarning, "failed to persist successful expansion instance %d: %v", index, uErr)
		}
	}
	if execAttemptStore != nil {
		// Non-fatal, same reasoning as the failure path above. For a
		// remote dispatch this is also redundant — the enterprise
		// instance-result endpoint already called CompleteAttempt when
		// the worker reported in, which is how
		// dispatchExpansionInstanceRemotely below knew to return — but
		// CompleteAttempt is a documented idempotent no-op when the row
		// is already Completed, so calling it again here needs no
		// branching between the local and remote paths.
		if cErr := execAttemptStore.CompleteAttempt(r.run.ID, node.ID, instanceKey, nodeAttempt, execFencingGen); cErr != nil {
			r.log(node.ID, models.LogLevelWarning, "failed to settle execution attempt for expansion instance %d as completed: %v", index, cErr)
		}
	}
	if !r.dryRun {
		span.SetAttributes(attribute.Int("row_count", rowCount), attribute.Int64("duration_ms", duration))
		span.SetStatus(codes.Ok, "")
		span.End()
	}
	r.logWithTrace(node.ID, models.LogLevelInfo, "", nodeAttempt, nil,
		"expansion instance %d/%d (key=%s) completed: %d rows in %dms", index+1, total, instanceKey, rowCount, duration)

	return result, nil
}

// dispatchExpansionInstanceRemotely enqueues this instance as a
// WorkOrder-bearing extensions.RunJob on r.instanceJobQueue and waits for
// a remote worker to complete it (ADR-017, 2026-08-12 wait-and-fetch
// design), returning the same (result, error) shape ExecuteCodeNode
// would — so executeExpansionInstance's post-processing (settling
// ExpansionInstance, and CompleteAttempt/FailAttempt above) needs no
// branching between this path and the local one.
//
// execFencingGen is the fencing generation this instance's attempt was
// already claimed under by the caller, before this function runs. The
// worker never claims anything itself; it executes on behalf of that
// claim and reports back holding the same token — the enterprise
// instance-result endpoint reads FencingGeneration from the job row
// rather than the worker for exactly this reason, so the two sides were
// designed to click together without either changing.
func (r *Runner) dispatchExpansionInstanceRemotely(node models.Node, nodeAttempt int, instanceKey string, execFencingGen int64, itemDS *common.DataSet, script string, configForScript map[string]interface{}, runParams map[string]string, timeoutSec int) (*common.DataSet, error) {
	execAttemptStore, ok := r.store.(store.ExecutionAttemptStore)
	if !ok {
		// Cannot happen through executeExpansionInstance's own call site
		// (it only calls this when execAttemptStore != nil already), kept
		// as a direct, named failure rather than a panic for any other
		// caller.
		return nil, fmt.Errorf("dispatch expansion instance %s remotely: store does not support execution attempts", instanceKey)
	}
	if r.artifactStore == nil {
		return nil, fmt.Errorf("dispatch expansion instance %s remotely: no artifact store configured to read the result back from", instanceKey)
	}

	var itemRow map[string]interface{}
	if len(itemDS.Rows) > 0 {
		itemRow = map[string]interface{}(itemDS.Rows[0])
	}
	idempotencyKey := fmt.Sprintf("%s:%s:%s:%d", r.run.ID, node.ID, instanceKey, nodeAttempt)
	job := extensions.RunJob{
		ID: common.NewID(), PipelineID: r.pipe.ID, RunID: r.run.ID, OrgID: r.pipe.OrgID,
		NodeID: node.ID, InstanceKey: instanceKey, Attempt: nodeAttempt,
		IdempotencyKey: idempotencyKey, FencingGeneration: execFencingGen,
		EnqueuedAt: time.Now().UTC(),
		WorkOrder: &extensions.InstanceWorkOrder{
			NodeType: string(node.Type), Script: script, Config: configForScript,
			ItemColumns: itemDS.Columns, ItemRow: itemRow,
			RunParams: runParams, TimeoutSeconds: timeoutSec,
		},
	}
	if err := r.instanceJobQueue.Enqueue(job); err != nil {
		return nil, fmt.Errorf("enqueue remote instance dispatch for %s: %w", instanceKey, err)
	}

	// See remoteInstanceDispatchMargin's own doc comment for the deadline.
	waitCtx, cancel := context.WithTimeout(r.ctx, time.Duration(timeoutSec)*time.Second+remoteInstanceDispatchMargin)
	defer cancel()
	go r.renewAttemptLease(waitCtx, execAttemptStore, node.ID, instanceKey, nodeAttempt, execFencingGen)

	// checkAttempt polls current status; ok=true means it returned a
	// terminal result (result/error already the return values), ok=false
	// means still queued/claimed/started — keep waiting.
	checkAttempt := func() (result *common.DataSet, err error, ok bool) {
		attempt, getErr := execAttemptStore.GetExecutionAttempt(r.run.ID, node.ID, instanceKey, nodeAttempt)
		if getErr != nil {
			r.log(node.ID, models.LogLevelWarning, "failed to poll remote instance %s status: %v", instanceKey, getErr)
			return nil, nil, false
		}
		switch attempt.Status {
		case models.AttemptStatusCompleted:
			ds, readErr := r.artifactStore.ReadArtifact(r.run.ID, node.ID, instanceKey)
			if readErr != nil {
				return nil, fmt.Errorf("remote instance %s completed but its result could not be read back: %w", instanceKey, readErr), true
			}
			return ds, nil, true
		case models.AttemptStatusFailed:
			if attempt.Error != "" {
				return nil, fmt.Errorf("%s", attempt.Error), true
			}
			return nil, fmt.Errorf("remote instance %s failed with no error detail", instanceKey), true
		}
		return nil, nil, false
	}

	// Check once immediately — a fast worker (or one whose report was
	// already in flight when this call started, e.g. a redelivered/retried
	// dispatch) should not wait out a full poll interval before its
	// already-available result is noticed.
	if result, err, ok := checkAttempt(); ok {
		return result, err
	}

	ticker := time.NewTicker(remoteInstanceStatusPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			reason := fmt.Sprintf("remote instance dispatch for %s timed out waiting for a worker", instanceKey)
			if r.ctx.Err() != nil {
				reason = fmt.Sprintf("pipeline cancelled while waiting for remote instance %s", instanceKey)
			}
			// Fencing-checked: if the worker's report is already in flight
			// and lands right after this, its own settlement call loses
			// this race honestly (fencing mismatch) rather than silently
			// overwriting whatever this decided — the same safety net
			// every other settlement path in this ADR relies on.
			if failErr := execAttemptStore.FailAttempt(r.run.ID, node.ID, instanceKey, nodeAttempt, execFencingGen, reason); failErr != nil {
				r.log(node.ID, models.LogLevelWarning, "failed to settle timed-out remote instance %s: %v", instanceKey, failErr)
			}
			return nil, fmt.Errorf("%s", reason)
		case <-ticker.C:
			if result, err, ok := checkAttempt(); ok {
				return result, err
			}
		}
	}
}

// reusedExpansionInstance returns a prior attempt's cached successful
// output for (nodeID, instanceKey) within this Runner's lifetime, if any —
// see Runner.expansionResults.
func (r *Runner) reusedExpansionInstance(nodeID, instanceKey string) (*common.DataSet, bool) {
	byKey, ok := r.expansionResults[nodeID]
	if !ok {
		return nil, false
	}
	ds, ok := byKey[instanceKey]
	return ds, ok
}

// rememberExpansionInstance caches a successful item's output so a later
// retry of the same node (same Runner, same run) can skip re-executing it.
func (r *Runner) rememberExpansionInstance(nodeID, instanceKey string, ds *common.DataSet) {
	if r.expansionResults == nil {
		r.expansionResults = make(map[string]map[string]*common.DataSet)
	}
	byKey, ok := r.expansionResults[nodeID]
	if !ok {
		byKey = make(map[string]*common.DataSet)
		r.expansionResults[nodeID] = byKey
	}
	byKey[instanceKey] = ds
}

// recordReusedExpansionInstance writes this attempt's models.ExpansionInstance
// row for an item skipped because it already succeeded on an earlier
// attempt of the same node — without re-running the subprocess. Every
// attempt still gets exactly one row per item (matching
// executeExpansionInstance's own invariant), so per-attempt history stays
// complete instead of silently missing reused items.
func (r *Runner) recordReusedExpansionInstance(node models.Node, nodeAttempt, index int, instanceKey string, total int, result *common.DataSet) error {
	r.logWithTrace(node.ID, models.LogLevelInfo, "", nodeAttempt, map[string]string{
		"instance_index": fmt.Sprintf("%d", index),
		"instance_key":   instanceKey,
	}, "expansion instance %d/%d (key=%s): reusing the result from a prior attempt, not re-running", index+1, total, instanceKey)

	rowCount := 0
	if result != nil {
		rowCount = len(result.Rows)
	}
	now := time.Now().UTC()

	if execAttemptStore, ok := r.store.(store.ExecutionAttemptStore); ok && !r.dryRun {
		// Written directly as Completed, not via Claim/Ack: nothing is
		// actually claimed or executed for a reused item, so going through
		// the claim dance for it would be dishonest about what happened.
		// FencingGeneration stays 0 (its zero value) — accurately
		// reflecting "never actually claimed" for this attempt.
		idempotencyKey := fmt.Sprintf("%s:%s:%s:%d", r.run.ID, node.ID, instanceKey, nodeAttempt)
		if err := r.store.WithTx(func(tx *sql.Tx) error {
			return execAttemptStore.CreateExecutionAttemptTx(tx, &models.ExecutionAttempt{
				RunID: r.run.ID, NodeID: node.ID, InstanceKey: instanceKey, Attempt: nodeAttempt,
				Status: models.AttemptStatusCompleted, IdempotencyKey: idempotencyKey,
			})
		}); err != nil {
			r.log(node.ID, models.LogLevelWarning, "failed to persist execution attempt for reused expansion instance %d: %v", index, err)
		}
	}

	instStore, ok := r.store.(store.ExpansionInstanceStore)
	if !ok || r.dryRun {
		return nil
	}
	return instStore.CreateExpansionInstance(&models.ExpansionInstance{
		ID:            common.NewID(),
		RunID:         r.run.ID,
		NodeID:        node.ID,
		NodeAttempt:   nodeAttempt,
		InstanceIndex: index,
		InstanceKey:   instanceKey,
		Status:        models.RunStatusSuccess,
		RowCount:      rowCount,
		StartedAt:     &now,
	})
}
