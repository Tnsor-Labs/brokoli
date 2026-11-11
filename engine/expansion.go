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
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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

// remoteInstanceInitialPollInterval is where that polling STARTS, before
// backing off toward remoteInstanceStatusPollInterval.
//
// A fixed one-second tick means an instance whose worker finished a
// millisecond after the previous check waits out the rest of the second
// for nothing, and a pipeline of N remote instances pays that up to N
// times. The interval's own job, per the note above, is user-facing
// latency after a worker actually finishes -- so it should be short
// exactly when a result is most likely to have just landed, and only
// coarse once waiting has clearly become the norm.
//
// Backing off preserves the old steady-state cost: a long-running
// instance settles at the same one-second cadence it polled at before,
// so nothing polls harder than it used to for the case the ceiling was
// chosen for.
var remoteInstanceInitialPollInterval = 25 * time.Millisecond

// maxConcurrentRemoteExpansionDispatch bounds how many of one expansion
// node's instances runCodeExpansion dispatches to remote workers at once.
// Not unbounded: defaultMaxExpansionInstances allows up to 1000 items, and
// firing 1000 simultaneous JobQueue enqueues plus ClaimAttempt calls at
// once would hammer the store and the queue for no real benefit once the
// worker fleet is saturated. Not maxParallel's much smaller 4, either:
// that bound exists for concurrent *nodes* sharing one pod's CPU within a
// single RunPipeline call — remote instances execute on other pods
// entirely, so the limiting resource is worker fleet size and queue
// throughput, not this pod's own cores. A package variable, not a
// literal, so a test can shrink it without needing hundreds of fake
// workers to prove the bound is actually respected.
var maxConcurrentRemoteExpansionDispatch = 16

// funcRef mirrors brokoli-sdk's _func_ref(fn) output — a name/description
// reference, NOT executable code (see brokoli/pipeline.py's _func_ref doc
// comment). Used for expansion.key.
type funcRef struct {
	Name string
	Doc  string
}

// defaultMapMaxParallel is how many items of a node's `map` block run at
// once when the block does not say. Local items share this pod's CPU and
// code worker pool, the same resources maxParallel (4) bounds for whole
// nodes; remote items are bounded by maxConcurrentRemoteExpansionDispatch
// instead.
const defaultMapMaxParallel = 4

// maxKeyLength bounds an instance key taken from an item's key column. The
// key is stored with every instance record and travels in work orders; a
// column holding whole documents is a mistake to report, not to store.
const maxKeyLength = 256

// On-item-failure policies of a `map` block (ADR-050).
const (
	// onItemFailureFail stops starting new items at the first failure,
	// waits for those in flight, and fails the node.
	onItemFailureFail = "fail"
	// onItemFailureContinue runs every item, then fails the node naming
	// the items that failed.
	onItemFailureContinue = "continue"
	// onItemFailureSkip runs every item and leaves the failed ones out of
	// the output. The node fails only when no item succeeded.
	onItemFailureSkip = "skip"
)

// expansionConfig is the parsed form of a code node's fan-out: either the
// `expansion` block brokoli-sdk's _TaskWrapper.expand() compiles,
//
//	{"over": {param: upstream_node_id, ...}, "key": {"name": ..., "doc": ...}?, "max_instances": N?}
//
// or the `map` block ADR-050 introduces,
//
//	{"over": upstream_node_id | {param: upstream_node_id, ...},
//	 "key": column?, "max_items": N?, "max_parallel": N?,
//	 "on_item_failure": "fail" | "continue" | "skip"?}
//
// Both run through runCodeExpansion. The `expansion` form keeps its
// original behaviour (index keys, sequential local execution, fail on the
// first failed item) so pipelines compiled before `map` existed run as
// they did.
type expansionConfig struct {
	// Over maps a task parameter name to the upstream node ID whose output
	// rows are the items to fan out over, e.g. {"file": "files_node_id"}.
	Over map[string]string
	// Key is expand()'s optional key= callable, serialized as a name/doc
	// reference only (see funcRef) — not executable. Nil when absent.
	Key *funcRef
	// MaxInstances bounds the fan-out: the block's max_items or
	// max_instances, or the server default (mapLimits.DefaultItems).
	MaxInstances int
	// maxInstancesDeclared reports MaxInstances came from the block, not
	// from the server default.
	maxInstancesDeclared bool
	// KeyColumn names the item column whose value is the instance key
	// (map's "key"). Empty means index keys (idx:N).
	KeyColumn string
	// MaxParallel is the declared number of items to run at once, or 0
	// when the config does not say (see parallelism).
	MaxParallel int
	// OnItemFailure is one of the onItemFailure* policies.
	OnItemFailure string
	// fromMap reports the config came from a `map` block, which runs
	// local items in parallel by default; `expansion` stays sequential.
	fromMap bool
	// limits are the server's mapping limits when the block was parsed.
	limits mapLimits
}

// parallelism is how many items run at once on this runner.
// Remote items are also bounded by mapLimits.MaxRemoteParallel, which
// lowers a higher max_parallel instead of refusing it: it protects the job
// queue, and the same pipeline may run locally elsewhere.
func (c *expansionConfig) parallelism(remote bool) int {
	limits := c.limits
	if limits.MaxRemoteParallel == 0 {
		limits = currentMapLimits()
	}
	if c.MaxParallel > 0 {
		if remote && c.MaxParallel > limits.MaxRemoteParallel {
			return limits.MaxRemoteParallel
		}
		return c.MaxParallel
	}
	switch {
	case remote:
		return limits.MaxRemoteParallel
	case c.fromMap:
		return limits.DefaultParallel
	default:
		return 1
	}
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
	_, hasExpansion := n.Config["expansion"]
	_, hasMap := n.Config["map"]
	return hasExpansion || hasMap
}

// mapUnsupportedError refuses a `map` block on a node type that cannot fan
// out yet, or returns nil. Without it the block would be ignored and the
// node would run once over its whole input, which looks like success.
func mapUnsupportedError(n models.Node) error {
	if _, ok := n.Config["map"]; !ok || mappableTypes[n.Type] {
		return nil
	}
	return fmt.Errorf("node %s (%s): 'map' is not supported on %s nodes in this version; code, task, source_db and source_api nodes can map", n.Name, n.ID, n.Type)
}

// mappableTypes are the node types a `map` block can fan out.
var mappableTypes = map[models.NodeType]bool{
	models.NodeTypeCode:      true,
	models.NodeTypeSourceDB:  true,
	models.NodeTypeSourceAPI: true,
	models.NodeTypeTask:      true,
}

// CheckMapSupported is mapUnsupportedError for callers outside the
// engine: it refuses a `map` block on a type that cannot map.
func CheckMapSupported(n models.Node) error { return mapUnsupportedError(n) }

// MappableNodeTypes lists the node types a `map` block can fan out, sorted.
// /api/capabilities reports it, so an SDK can refuse to deploy a `map` on
// a type this server would refuse, rather than find out at save.
func MappableNodeTypes() []string {
	out := make([]string, 0, len(mappableTypes))
	for t := range mappableTypes {
		out = append(out, string(t))
	}
	sort.Strings(out)
	return out
}

// mapOverEdge reports whether an edge from `from` feeds n's `map` block:
// the one kind of incoming edge a source node can have.
func mapOverEdge(n models.Node, from string) bool {
	if !nodeIsMapped(n) {
		return false
	}
	cfg, err := parseMapConfig(n)
	if err != nil {
		return false
	}
	for _, id := range cfg.Over {
		if id == from {
			return true
		}
	}
	return false
}

// parseExpansionConfig parses and structurally validates node.Config's
// `expansion` block. Returns an error naming the node if malformed — both
// runCodeExpansion (execution time) and validate.go's validateNodeConfig/
// validateNodeConfigDetailed (deploy time) call this directly for the
// actual shape rules, so the two stay in sync by construction rather than
// by convention.
func parseExpansionConfig(node models.Node) (*expansionConfig, error) {
	if _, hasMap := node.Config["map"]; hasMap {
		if _, hasExpansion := node.Config["expansion"]; hasExpansion {
			return nil, fmt.Errorf("node %s (%s): 'map' and 'expansion' cannot both be set; 'map' replaces 'expansion'", node.Name, node.ID)
		}
		return parseMapConfig(node)
	}
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

	limits := currentMapLimits()
	cfg := &expansionConfig{Over: over, MaxInstances: limits.DefaultItems, limits: limits}

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
		cfg.maxInstancesDeclared = true
		if err := limits.itemCeilingError(fmt.Sprintf("node %s (%s)", node.Name, node.ID), "expansion.max_instances", cfg.MaxInstances); err != nil {
			return nil, err
		}
	}

	cfg.OnItemFailure = onItemFailureFail
	return cfg, nil
}

// parseMapConfig parses a node's `map` block (ADR-050).
func parseMapConfig(node models.Node) (*expansionConfig, error) {
	where := fmt.Sprintf("node %s (%s)", node.Name, node.ID)
	block, ok := node.Config["map"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s: 'map' must be an object", where)
	}
	for k := range block {
		switch k {
		case "over", "key", "max_items", "max_parallel", "on_item_failure":
		default:
			return nil, fmt.Errorf("%s: 'map.%s' is not a map setting; the settings are over, key, max_items, max_parallel and on_item_failure", where, k)
		}
	}
	limits := currentMapLimits()
	cfg := &expansionConfig{MaxInstances: limits.DefaultItems, OnItemFailure: onItemFailureFail, fromMap: true, limits: limits}
	switch over := block["over"].(type) {
	case string:
		if over == "" {
			return nil, fmt.Errorf("%s: 'map.over' must name an upstream node", where)
		}
		cfg.Over = map[string]string{"item": over}
	case map[string]interface{}:
		if len(over) == 0 {
			return nil, fmt.Errorf("%s: 'map.over' must name an upstream node", where)
		}
		cfg.Over = make(map[string]string, len(over))
		for param, v := range over {
			id, ok := v.(string)
			if !ok || id == "" {
				return nil, fmt.Errorf("%s: 'map.over[%q]' must be an upstream node id", where, param)
			}
			cfg.Over[param] = id
		}
	default:
		return nil, fmt.Errorf("%s: 'map.over' is required: an upstream node id, or an object of parameter name to upstream node id", where)
	}
	if raw, ok := block["key"]; ok {
		key, ok := raw.(string)
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%s: 'map.key' must be the name of an item column", where)
		}
		cfg.KeyColumn = key
	}
	positive := func(name string) (int, error) {
		raw, ok := block[name]
		if !ok {
			return 0, nil
		}
		n, ok := raw.(float64)
		if !ok || n < 1 || n != float64(int(n)) {
			return 0, fmt.Errorf("%s: 'map.%s' must be a whole number of at least 1", where, name)
		}
		return int(n), nil
	}
	maxItems, err := positive("max_items")
	if err != nil {
		return nil, err
	}
	if maxItems > 0 {
		if err := limits.itemCeilingError(where, "map.max_items", maxItems); err != nil {
			return nil, err
		}
		cfg.MaxInstances, cfg.maxInstancesDeclared = maxItems, true
	}
	if cfg.MaxParallel, err = positive("max_parallel"); err != nil {
		return nil, err
	}
	if err := limits.parallelCeilingError(where, cfg.MaxParallel); err != nil {
		return nil, err
	}
	if raw, ok := block["on_item_failure"]; ok {
		policy, _ := raw.(string)
		switch policy {
		case onItemFailureFail, onItemFailureContinue, onItemFailureSkip:
			cfg.OnItemFailure = policy
		default:
			return nil, fmt.Errorf("%s: 'map.on_item_failure' must be fail, continue or skip", where)
		}
	}
	return cfg, nil
}

// instanceKeys returns each item's instance key: the item's key column when
// the config names one, otherwise its index (deriveInstanceKey). A key
// column must hold a value in every item, distinct across items, so two
// items can never share an identity that resume and retries rely on.
func instanceKeys(node models.Node, cfg *expansionConfig, items []common.DataRow) ([]string, error) {
	keys := make([]string, len(items))
	if cfg.KeyColumn == "" {
		for i := range items {
			keys[i] = deriveInstanceKey(i)
		}
		return keys, nil
	}
	seen := make(map[string]int, len(items))
	for i, item := range items {
		raw, ok := item[cfg.KeyColumn]
		if !ok || raw == nil {
			return nil, fmt.Errorf("node %s (%s): item %d has no value in key column %q (map.key)", node.Name, node.ID, i+1, cfg.KeyColumn)
		}
		value := fmt.Sprint(raw)
		if len(value) > maxKeyLength {
			return nil, fmt.Errorf("node %s (%s): item %d's key in column %q is %d characters, over the %d a key may have (map.key)", node.Name, node.ID, i+1, cfg.KeyColumn, len(value), maxKeyLength)
		}
		if first, dup := seen[value]; dup {
			return nil, fmt.Errorf("node %s (%s): items %d and %d have the same key %q in column %q; map.key must be unique per item", node.Name, node.ID, first+1, i+1, value, cfg.KeyColumn)
		}
		seen[value] = i
		keys[i] = "key:" + value
	}
	return keys, nil
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
// item in the upstream collection(s) named by expansion.over, tracking each
// item's outcome as its own durable models.ExpansionInstance row (see that
// type's doc comment for why this is a dedicated table rather than reusing
// NodeRun/ExecutionAttempt's (node_id, attempt) keying) so each item's
// success or failure is independently visible, not one aggregate status for
// the whole expansion.
//
// How many items run at once is expansionConfig.parallelism: an
// `expansion` block runs local items sequentially, as it always has, while
// a `map` block (ADR-050) runs up to map.max_parallel of them, 4 when it
// does not say. map.on_item_failure decides what a failed item does to the
// node.
//
// Remote dispatch (r.instanceJobQueue != nil, ADR-017) dispatches up to
// maxConcurrentRemoteExpansionDispatch items at once. This was
// originally sequential too, inherited unchanged when remote dispatch was
// added — a real, measured cost: a live test with 6 idle worker pods and 30
// items (~1.5s of real work each) showed items round-robining correctly
// across all 6 pods but with zero timestamp overlap between any two —
// exactly one instance in flight at a time regardless of fleet size, node
// timing out at 30 items × ~2s/item. Each remote instance executes on
// whatever worker claims it, independent of any other instance, so nothing
// about correctness requires sequencing them.
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
// runCodeExpansion runs local instances under ctx, the attempt's
// context, so a timed-out or cancelled attempt stops their scripts.
// Remote instances still wait under the run's context: stopping the local
// wait would not stop the work order on its worker, and a retry would then
// dispatch a second one beside it.
func (r *Runner) runCodeExpansion(ctx context.Context, node models.Node, edgeInputsByFrom map[string]*common.DataSet, nodeAttempt int) (*common.DataSet, error) {
	cfg, err := parseExpansionConfig(node)
	if err != nil {
		return nil, err
	}

	items, _, err := resolveExpansionItems(node, cfg, edgeInputsByFrom)
	if err != nil {
		return nil, err
	}

	if cfg.Key != nil {
		r.log(node.ID, models.LogLevelWarning,
			"expansion.key (%q) is a name/description reference, not executable code (brokoli-sdk records it via _func_ref, never serializes it as runnable code) — this backend version cannot execute it, so instance identity falls back to a deterministic collection-index scheme (idx:N) instead",
			cfg.Key.Name)
	}

	// A code node's source is a script or a task bundle (ADR-031). A
	// mapped bundle node was saved and then refused here on every run
	// (#1019); it now runs each item through the bundle.
	var bundle *codeBundleSpec
	script, _ := node.Config["script"].(string)
	if script == "" {
		digest, isBundle, tbErr := taskBundleReference(node.Config)
		if tbErr != nil {
			return nil, fmt.Errorf("node %s (%s): %w", node.Name, node.ID, tbErr)
		}
		if !isBundle {
			return nil, fmt.Errorf("node %s (%s): a mapped code node needs 'script' or 'task_bundle' in config", node.Name, node.ID)
		}
		b, err := r.materializeTaskBundle(digest)
		if err != nil {
			return nil, fmt.Errorf("node %s (%s): %w", node.Name, node.ID, err)
		}
		defer os.RemoveAll(b.dir)
		bundle = b
	}

	timeoutSec := 30
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}

	// Strip 'script' (avoid circular ref, matches runCode) and the fan-out
	// block (engine-only dispatch metadata the per-item script has no use
	// for) before handing config to the subprocess.
	configForScript := make(map[string]interface{})
	for k, v := range node.Config {
		if k != "script" && k != "expansion" && k != "map" && k != "task_bundle" {
			configForScript[k] = v
		}
	}

	var runParams map[string]string
	if r.varCtx != nil {
		runParams = r.varCtx.Params
	}

	if bundle != nil {
		// Bundle items run on this runner, never dispatched as remote
		// instance jobs: a remote worker that predates this would take the
		// job and refuse a bundle work order, and no capability keeps such
		// jobs off it. They still run up to the node's parallelism.
		spec := *bundle
		return r.fanOut(ctx, node, cfg, items, nodeAttempt, false, func(ctx context.Context, i int, key string) (*common.DataSet, error) {
			itemDS := &common.DataSet{Columns: columnsOf(items[i]), Rows: []common.DataRow{items[i]}}
			return r.executeMappedInstance(ctx, node, nodeAttempt, i, key, len(items), timeoutSec, false, func(ctx context.Context, _ int64, _ bool) (*common.DataSet, string, error) {
				return ExecuteTaskBundleNodeProgress(ctx, spec, itemDS, configForScript, runParams, r.parameters, timeoutSec, nil)
			})
		})
	}

	return r.fanOut(ctx, node, cfg, items, nodeAttempt, r.instanceJobQueue != nil, func(ctx context.Context, i int, key string) (*common.DataSet, error) {
		itemDS := &common.DataSet{
			Columns: columnsOf(items[i]),
			Rows:    []common.DataRow{items[i]},
		}
		return r.executeExpansionInstance(ctx, node, nodeAttempt, i, key, len(items), itemDS, script, configForScript, runParams, timeoutSec)
	})
}

// itemLimitError refuses a collection with more items than the node may
// fan out into, naming the count, the limit and where the limit came from.
func itemLimitError(node models.Node, cfg *expansionConfig, count int) error {
	setting := "expansion.max_instances"
	if cfg.fromMap {
		setting = "map.max_items"
	}
	source := fmt.Sprintf("(%s)", setting)
	if !cfg.maxInstancesDeclared {
		source = fmt.Sprintf("(this server's default, %s; set %s to raise it, up to %d)", envMapDefaultMaxItems, setting, cfg.limits.MaxItems)
	}
	return fmt.Errorf("node %s (%s): expansion would create %d instances, exceeding the configured limit of %d %s — refusing to fan out rather than silently truncating the collection", node.Name, node.ID, count, cfg.MaxInstances, source)
}

// fanOut runs runItem once per item and combines the results: the part of
// mapping every node type shares (ADR-050). It enforces the item limit,
// derives the instance keys, reuses items that succeeded on an earlier
// attempt of this run, runs the rest up to cfg.parallelism(remote) at
// once, and applies cfg.OnItemFailure. runItem runs one item and records
// it; executeMappedInstance does the recording for every node type.
func (r *Runner) fanOut(ctx context.Context, node models.Node, cfg *expansionConfig, items []common.DataRow, nodeAttempt int, remote bool, runItem func(ctx context.Context, i int, key string) (*common.DataSet, error)) (*common.DataSet, error) {
	for _, w := range cfg.limits.Warnings {
		r.log(node.ID, models.LogLevelWarning, "%s", w)
	}
	if len(items) > cfg.MaxInstances {
		return nil, itemLimitError(node, cfg, len(items))
	}
	keys, err := instanceKeys(node, cfg, items)
	if err != nil {
		return nil, err
	}

	parallel := cfg.parallelism(remote)
	if remote && cfg.MaxParallel > parallel {
		r.log(node.ID, models.LogLevelInfo, "expansion: map.max_parallel is %d; dispatching at most %d item(s) to remote workers at once (%s)", cfg.MaxParallel, parallel, envMapMaxRemoteParallel)
	}
	r.log(node.ID, models.LogLevelInfo, "expansion: fanning out into %d instance(s), %d at a time", len(items), parallel)

	// slots holds each item's eventual result at its original index, so
	// combineExpansionResults sees results in item order regardless of
	// dispatch order or completion order below.
	slots := make([]*common.DataSet, len(items))
	present := make([]bool, len(items))
	reused := 0

	// First pass, always sequential and cheap: resolve cache hits from a
	// prior attempt of this same run (see the doc comment above) before
	// deciding what actually needs dispatching.
	var pending []int
	for i := range items {
		if cached, ok := r.reusedExpansionInstance(node.ID, keys[i]); ok {
			if err := r.recordReusedExpansionInstance(node, nodeAttempt, i, keys[i], len(items), cached); err != nil {
				return nil, fmt.Errorf("node %s (%s): recording reused expansion instance %d/%d (key=%s): %w", node.Name, node.ID, i+1, len(items), keys[i], err)
			}
			slots[i], present[i] = cached, true
			reused++
			continue
		}
		pending = append(pending, i)
	}

	runInstance := func(i int) error {
		out, err := runItem(ctx, i, keys[i])
		if err != nil {
			return fmt.Errorf("node %s (%s): expansion instance %d/%d (key=%s) failed: %w", node.Name, node.ID, i+1, len(items), keys[i], err)
		}
		if out != nil {
			// Written by index only: safe for concurrent goroutines
			// writing distinct indices of a pre-sized slice.
			// r.expansionResults is a plain map and is written later,
			// sequentially, once every item has finished.
			slots[i], present[i] = out, true
		}
		return nil
	}

	// One path for local and remote items: at most `parallel` run at once.
	// With parallel == 1 this is the sequential loop `expansion` always
	// had. Under the fail policy no new item starts after a failure, but
	// items already running finish, so their results are remembered for a
	// retry rather than thrown away.
	failures := make([]error, len(items))
	var stopped atomic.Bool
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, i := range pending {
		sem <- struct{}{}
		if cfg.OnItemFailure == onItemFailureFail && stopped.Load() {
			<-sem
			break
		}
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := runInstance(idx); err != nil {
				failures[idx] = err
				stopped.Store(true)
			}
		}(i)
	}
	wg.Wait()

	// Sequential on purpose: r.expansionResults is a plain map, not safe
	// for concurrent writes.
	for _, i := range pending {
		if present[i] {
			r.rememberExpansionInstance(node.ID, keys[i], slots[i])
		}
	}

	var failed []int
	for i, err := range failures {
		if err != nil {
			failed = append(failed, i)
		}
	}
	if len(failed) > 0 {
		failedKeys := make([]string, 0, len(failed))
		for _, i := range failed {
			if len(failedKeys) == 10 {
				failedKeys = append(failedKeys, fmt.Sprintf("and %d more", len(failed)-10))
				break
			}
			failedKeys = append(failedKeys, keys[i])
		}
		switch {
		case cfg.OnItemFailure == onItemFailureSkip && len(failed) < len(items):
			r.log(node.ID, models.LogLevelWarning, "expansion: %d of %d item(s) failed and were left out of the output (map.on_item_failure=skip): %s",
				len(failed), len(items), strings.Join(failedKeys, ", "))
		case len(failed) == 1:
			return nil, failures[failed[0]]
		default:
			return nil, fmt.Errorf("node %s (%s): %d of %d expansion instances failed (%s); first: %w", node.Name, node.ID, len(failed), len(items), strings.Join(failedKeys, ", "), failures[failed[0]])
		}
	}

	results := make([]*common.DataSet, 0, len(items))
	for i := range items {
		if present[i] {
			results = append(results, slots[i])
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
func (r *Runner) executeExpansionInstance(ctx context.Context, node models.Node, nodeAttempt, index int, instanceKey string, total int, itemDS *common.DataSet, script string, configForScript map[string]interface{}, runParams map[string]string, timeoutSec int) (*common.DataSet, error) {
	remote := r.instanceJobQueue != nil
	return r.executeMappedInstance(ctx, node, nodeAttempt, index, instanceKey, total, timeoutSec, remote, func(ctx context.Context, fencingGen int64, dispatched bool) (*common.DataSet, string, error) {
		if dispatched {
			result, err := r.dispatchExpansionInstanceRemotely(node, nodeAttempt, instanceKey, fencingGen, itemDS, script, configForScript, runParams, timeoutSec)
			return result, "", err
		}
		return ExecuteCodeNodeContext(ctx, script, itemDS, configForScript, runParams, r.parameters, timeoutSec)
	})
}

// executeMappedInstance runs one item of a mapped node through exec and
// records it: its models.ExpansionInstance row, its execution attempt's
// claim, lease and settlement, its span and its log lines. remote says the
// item may be dispatched to a remote worker, which sizes its lease for
// renewal instead of one bounded call; exec is told whether it was
// (dispatched is true only with an attempt store and a remote queue).
// exec returns the item's output, any stderr to log, and its error.
func (r *Runner) executeMappedInstance(ctx context.Context, node models.Node, nodeAttempt, index int, instanceKey string, total, timeoutSec int, remote bool, exec func(ctx context.Context, fencingGen int64, dispatched bool) (*common.DataSet, string, error)) (*common.DataSet, error) {
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
	// A local item's bookkeeping in two transactions instead of six
	// separate writes, when the store can (store.InstanceAttemptStore):
	// with many items those per-statement commits were most of an item's
	// cost. A remote item keeps the separate calls: its attempt is settled
	// by the worker that runs it.
	batched, _ := r.store.(store.InstanceAttemptStore)
	_, hasAttempts := r.store.(store.ExecutionAttemptStore)
	if r.dryRun || remote || instStore == nil || !hasAttempts {
		batched = nil
	}
	if instStore != nil && !r.dryRun && batched == nil {
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
		if remote {
			leaseDuration = store.DefaultLeaseDuration
		}
		attempt := &models.ExecutionAttempt{
			RunID: r.run.ID, NodeID: node.ID, InstanceKey: instanceKey, Attempt: nodeAttempt,
			Status:         models.AttemptStatusQueued,
			IdempotencyKey: fmt.Sprintf("%s:%s:%s:%d", r.run.ID, node.ID, instanceKey, nodeAttempt),
		}
		if batched != nil {
			gen, ok, err := batched.StartInstanceAttempt(ei, attempt, r.instanceID, leaseDuration)
			if err != nil {
				return nil, fmt.Errorf("start expansion instance %d: %w", index, err)
			}
			if !ok {
				return nil, fmt.Errorf("claim execution attempt for expansion instance %d: already claimed or terminal", index)
			}
			execFencingGen = gen
		} else {
			if err := r.store.WithTx(func(tx *sql.Tx) error {
				return execAttemptStore.CreateExecutionAttemptTx(tx, attempt)
			}); err != nil {
				return nil, fmt.Errorf("persist execution attempt for expansion instance %d: %w", index, err)
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
	}

	var span trace.Span
	if !r.dryRun {
		_, span = tracing.Tracer().Start(ctx, "brokoli.node.expansion_instance",
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
	result, stderr, err = exec(ctx, execFencingGen, execAttemptStore != nil && remote)
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
		if batched != nil {
			uErr, failErr := batched.SettleInstanceAttempt(ei, instanceKey, execFencingGen)
			if uErr != nil {
				r.log(node.ID, models.LogLevelWarning, "failed to persist failed expansion instance %d: %v", index, uErr)
			}
			if failErr != nil {
				r.log(node.ID, models.LogLevelWarning, "failed to settle execution attempt for expansion instance %d as failed: %v", index, failErr)
			}
		} else if instStore != nil && !r.dryRun {
			if uErr := instStore.UpdateExpansionInstance(ei); uErr != nil {
				r.log(node.ID, models.LogLevelWarning, "failed to persist failed expansion instance %d: %v", index, uErr)
			}
		}
		if execAttemptStore != nil && batched == nil {
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
	if batched != nil {
		uErr, cErr := batched.SettleInstanceAttempt(ei, instanceKey, execFencingGen)
		if uErr != nil {
			r.log(node.ID, models.LogLevelWarning, "failed to persist successful expansion instance %d: %v", index, uErr)
		}
		if cErr != nil {
			r.log(node.ID, models.LogLevelWarning, "failed to settle execution attempt for expansion instance %d as completed: %v", index, cErr)
		}
	} else if instStore != nil && !r.dryRun {
		if uErr := instStore.UpdateExpansionInstance(ei); uErr != nil {
			r.log(node.ID, models.LogLevelWarning, "failed to persist successful expansion instance %d: %v", index, uErr)
		}
	}
	if execAttemptStore != nil && batched == nil {
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
	var itemRow map[string]interface{}
	if len(itemDS.Rows) > 0 {
		itemRow = map[string]interface{}(itemDS.Rows[0])
	}
	return r.dispatchInstanceWorkOrderRemotely(node.ID, nodeAttempt, instanceKey, execFencingGen, &extensions.InstanceWorkOrder{
		NodeType: string(node.Type), Script: script, Config: configForScript,
		ItemColumns: itemDS.Columns, ItemRow: itemRow,
		RunParams: runParams, TypedParams: r.parameters, TimeoutSeconds: timeoutSec,
	}, timeoutSec, nil)
}

// dispatchInstanceWorkOrderRemotely enqueues an already-built WorkOrder and
// waits for its fenced execution attempt to settle. Expansion items,
// pagination pages, and task-node instances (engine/task.go) share this
// transport-side lifecycle; only their WorkOrder payloads and
// extraCapabilities differ. extraCapabilities are appended after
// r.requiredCapabilities (the run-level tags every job already carries) —
// nil for the two existing callers, which need nothing beyond that.
// remoteTimeoutReason says why a remote instance's wait ran out. It said
// "timed out waiting for a worker" and no more, which reads the same
// whether no worker was running or none advertised what the job needs: a
// queue hands a job only to a worker advertising every capability it
// requires, so a missing one looks exactly like no workers at all (#529).
// It now names the capabilities the job needed. The attempt's own state
// cannot tell a job no worker took from one a worker ran too long, since
// the dispatcher marks it started itself before enqueuing.
func remoteTimeoutReason(instanceKey string, required []string, waited time.Duration) string {
	if len(required) == 0 {
		return fmt.Sprintf("remote instance dispatch for %s timed out waiting for a worker: no result within %s; check that a worker is running and connected", instanceKey, waited)
	}
	return fmt.Sprintf("remote instance dispatch for %s timed out waiting for a worker: no result within %s; only a worker advertising all of %s takes it, so check that one is running", instanceKey, waited, strings.Join(required, ", "))
}

func (r *Runner) dispatchInstanceWorkOrderRemotely(nodeID string, nodeAttempt int, instanceKey string, execFencingGen int64, workOrder *extensions.InstanceWorkOrder, timeoutSec int, extraCapabilities []string) (*common.DataSet, error) {
	execAttemptStore, ok := r.store.(store.ExecutionAttemptStore)
	if !ok {
		// Cannot happen through executeExpansionInstance's own call site
		// (it only calls this when execAttemptStore != nil already), kept
		// as a direct, named failure rather than a panic for any other
		// caller.
		return nil, fmt.Errorf("dispatch instance %s remotely: store does not support execution attempts", instanceKey)
	}
	if r.artifactStore == nil {
		return nil, fmt.Errorf("dispatch instance %s remotely: no artifact store configured to read the result back from", instanceKey)
	}

	// A cancelled run sends no more work. The runner reports a cancel
	// without waiting for the goroutines dispatching instances, so a page
	// retry, partition or item can still reach this after it; enqueued,
	// the job would run on a worker in full and only then find the
	// attempt settled. The attempt is settled here instead, fenced like
	// the wait below.
	if r.ctx.Err() != nil {
		reason := fmt.Sprintf("pipeline cancelled before remote instance %s was dispatched", instanceKey)
		if failErr := execAttemptStore.FailAttempt(r.run.ID, nodeID, instanceKey, nodeAttempt, execFencingGen, reason); failErr != nil {
			r.log(nodeID, models.LogLevelWarning, "failed to settle cancelled remote instance %s: %v", instanceKey, failErr)
		}
		return nil, fmt.Errorf("%s", reason)
	}

	idempotencyKey := fmt.Sprintf("%s:%s:%s:%d", r.run.ID, nodeID, instanceKey, nodeAttempt)
	job := extensions.RunJob{
		ID: common.NewID(), PipelineID: r.pipe.ID, RunID: r.run.ID, OrgID: r.pipe.OrgID,
		NodeID: nodeID, InstanceKey: instanceKey, Attempt: nodeAttempt,
		IdempotencyKey: idempotencyKey, FencingGeneration: execFencingGen,
		EnqueuedAt:           time.Now().UTC(),
		RequiredCapabilities: append(append([]string(nil), r.requiredCapabilities...), extraCapabilities...),
		WorkOrder:            workOrder,
	}
	if err := r.instanceJobQueue.Enqueue(job); err != nil {
		return nil, fmt.Errorf("enqueue remote instance dispatch for %s: %w", instanceKey, err)
	}

	// See remoteInstanceDispatchMargin's own doc comment for the deadline.
	waitCtx, cancel := context.WithTimeout(r.ctx, time.Duration(timeoutSec)*time.Second+remoteInstanceDispatchMargin)
	defer cancel()
	go r.renewAttemptLease(waitCtx, execAttemptStore, nodeID, instanceKey, nodeAttempt, execFencingGen)

	// checkAttempt polls current status; ok=true means it returned a
	// terminal result (result/error already the return values), ok=false
	// means still queued/claimed/started — keep waiting.
	checkAttempt := func() (result *common.DataSet, err error, ok bool) {
		attempt, getErr := execAttemptStore.GetExecutionAttempt(r.run.ID, nodeID, instanceKey, nodeAttempt)
		if getErr != nil {
			r.log(nodeID, models.LogLevelWarning, "failed to poll remote instance %s status: %v", instanceKey, getErr)
			return nil, nil, false
		}
		switch attempt.Status {
		case models.AttemptStatusCompleted:
			ds, readErr := r.artifactStore.ReadArtifact(r.run.ID, nodeID, instanceKey)
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

	// Adaptive: start fast, double on each miss, settle at the ceiling.
	interval := remoteInstanceInitialPollInterval
	if interval > remoteInstanceStatusPollInterval {
		interval = remoteInstanceStatusPollInterval
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-waitCtx.Done():
			reason := remoteTimeoutReason(instanceKey, job.RequiredCapabilities, time.Duration(timeoutSec)*time.Second+remoteInstanceDispatchMargin)
			if r.ctx.Err() != nil {
				reason = fmt.Sprintf("pipeline cancelled while waiting for remote instance %s", instanceKey)
			}
			// Fencing-checked: if the worker's report is already in flight
			// and lands right after this, its own settlement call loses
			// this race honestly (fencing mismatch) rather than silently
			// overwriting whatever this decided — the same safety net
			// every other settlement path in this ADR relies on.
			if failErr := execAttemptStore.FailAttempt(r.run.ID, nodeID, instanceKey, nodeAttempt, execFencingGen, reason); failErr != nil {
				r.log(nodeID, models.LogLevelWarning, "failed to settle timed-out remote instance %s: %v", instanceKey, failErr)
			}
			return nil, fmt.Errorf("%s", reason)
		case <-timer.C:
			if result, err, ok := checkAttempt(); ok {
				return result, err
			}
			if interval < remoteInstanceStatusPollInterval {
				interval *= 2
				if interval > remoteInstanceStatusPollInterval {
					interval = remoteInstanceStatusPollInterval
				}
			}
			timer.Reset(interval)
		}
	}
}

// reusedExpansionInstance returns a prior attempt's cached successful
// output for (nodeID, instanceKey) within this Runner's lifetime, if any —
// see Runner.expansionResults.
func (r *Runner) reusedExpansionInstance(nodeID, instanceKey string) (*common.DataSet, bool) {
	r.expansionResultsMu.RLock()
	defer r.expansionResultsMu.RUnlock()
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
	r.expansionResultsMu.Lock()
	defer r.expansionResultsMu.Unlock()
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
