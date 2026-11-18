package engine

// Mapping a group of nodes over a collection (ADR-050 phase 2, #968).
//
// A `group` node is a named subgraph -- its own Nodes and Edges -- with one
// input port and one output port. With a `map` block (the same block every
// mappable node carries, in Config["map"]) it runs its subgraph once per
// item of the collection on its single incoming edge: each item, a dataset
// of exactly that one row, is fed to the subgraph's entry node, the subgraph
// runs to its single terminal node, and that terminal's output is the item's
// result. `collect` names how the per-item results become the group's one
// output: "union" concatenates them into one dataset, "none" discards them
// (a group run for its sinks' side effects, yielding an empty dataset).
//
// What PR 1 does NOT do yet, and refuses rather than mis-running: a
// `condition` inside the group (per-item routing, #968 phase 2b), a group
// inside a group (nested mapping, #968 phase 2c), and `collect:"list"` (its
// only consumer is nested re-mapping). Per-item durability is recorded at
// the group-item granularity (one models.ExpansionInstance per item), not
// per inner node -- the finer physical plan is ADR-015 follow-up work.

import (
	"context"
	"fmt"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// mappedGroupErrors checks a group node's shape, both when the pipeline is
// saved and again when the node runs (a pipeline stored before the check, or
// reaching a worker by another route, must not run a malformed group). It
// returns human-readable messages; an empty slice means the group is sound.
func mappedGroupErrors(n models.Node) []string {
	if n.Type != models.NodeTypeGroup {
		return nil
	}
	var errs []string

	// A group in this version is always mapped: the map block is what makes
	// it run once per item. parseMapConfig also enforces the shared map
	// limits (max_items, max_parallel, on_item_failure).
	if _, ok := n.Config["map"]; !ok {
		errs = append(errs, fmt.Sprintf("Node %q: a group needs a 'map' block; a group that does not map is not supported in this version", n.Name))
	} else if _, err := parseMapConfig(n); err != nil {
		errs = append(errs, err.Error())
	}

	switch n.Collect {
	case models.CollectUnion, models.CollectNone:
	case models.CollectList:
		errs = append(errs, fmt.Sprintf("Node %q: collect:\"list\" is not supported in this version; it arrives with nested mapping. Use \"union\" or \"none\"", n.Name))
	default:
		errs = append(errs, fmt.Sprintf("Node %q: collect must be \"union\" or \"none\", got %q", n.Name, n.Collect))
	}

	if len(n.Nodes) == 0 {
		errs = append(errs, fmt.Sprintf("Node %q: a group needs at least one inner node", n.Name))
		return errs
	}

	// Inner node identities, and the per-PR refusals.
	ids := make(map[string]bool, len(n.Nodes))
	for _, inner := range n.Nodes {
		if inner.ID == "" {
			errs = append(errs, fmt.Sprintf("Node %q: an inner node has no id", n.Name))
			continue
		}
		if ids[inner.ID] {
			errs = append(errs, fmt.Sprintf("Node %q: duplicate inner node id %q", n.Name, inner.ID))
		}
		ids[inner.ID] = true
		switch inner.Type {
		case models.NodeTypeGroup:
			errs = append(errs, fmt.Sprintf("Node %q: a group inside a group (nested mapping) is not supported in this version", n.Name))
		case models.NodeTypeCondition:
			errs = append(errs, fmt.Sprintf("Node %q: a condition inside a group (per-item routing) is not supported in this version", n.Name))
		}
		if _, mapped := inner.Config["map"]; mapped {
			errs = append(errs, fmt.Sprintf("Node %q: inner node %q cannot itself map in this version", n.Name, inner.ID))
		}
	}

	// Edges reference only inner nodes, the subgraph is acyclic, and it has
	// exactly one entry (no incoming edge) and one terminal (no outgoing).
	incoming := make(map[string]int, len(n.Nodes))
	outgoing := make(map[string]int, len(n.Nodes))
	for _, e := range n.Edges {
		if !ids[e.From] {
			errs = append(errs, fmt.Sprintf("Node %q: inner edge references unknown node %q", n.Name, e.From))
		}
		if !ids[e.To] {
			errs = append(errs, fmt.Sprintf("Node %q: inner edge references unknown node %q", n.Name, e.To))
		}
		outgoing[e.From]++
		incoming[e.To]++
	}
	if _, err := topoWaves(n.Nodes, n.Edges); err != nil {
		errs = append(errs, fmt.Sprintf("Node %q: the group's subgraph %v", n.Name, err))
	}
	var entries, terminals int
	for _, inner := range n.Nodes {
		if incoming[inner.ID] == 0 {
			entries++
		}
		if outgoing[inner.ID] == 0 {
			terminals++
		}
	}
	if entries != 1 {
		errs = append(errs, fmt.Sprintf("Node %q: a group's subgraph must have exactly one entry node (no inner input), got %d", n.Name, entries))
	}
	if terminals != 1 {
		errs = append(errs, fmt.Sprintf("Node %q: a group's subgraph must have exactly one terminal node (its output), got %d", n.Name, terminals))
	}
	return errs
}

// runMappedGroup runs a mapped group node through the shared fanOut core,
// executing the subgraph once per item and combining per item by collect.
func (r *Runner) runMappedGroup(ctx context.Context, node models.Node, edgeInputsByFrom map[string]*common.DataSet, nodeAttempt int) (*common.DataSet, error) {
	if errs := mappedGroupErrors(node); len(errs) > 0 {
		return nil, fmt.Errorf("%s", errs[0])
	}
	cfg, err := parseMapConfig(node)
	if err != nil {
		return nil, err
	}
	items, _, err := resolveExpansionItems(node, cfg, edgeInputsByFrom)
	if err != nil {
		return nil, err
	}
	if r.dryRun && len(items) > dryRunMappedItems {
		r.log(node.ID, models.LogLevelInfo, "dry run: mapping the group over the first %d of %d item(s)", dryRunMappedItems, len(items))
		items = items[:dryRunMappedItems]
	}
	timeoutSec := 0
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}
	discard := node.Collect == models.CollectNone

	// A group's subgraph runs in this process; there is no remote dispatch
	// of a whole subgraph in this version.
	return r.fanOut(ctx, node, cfg, items, nodeAttempt, false, func(ctx context.Context, i int, key string) (*common.DataSet, error) {
		itemDS := &common.DataSet{Columns: columnsOf(items[i]), Rows: []common.DataRow{items[i]}}
		out, err := r.executeMappedInstance(ctx, node, nodeAttempt, i, key, len(items), timeoutSec, false, func(ctx context.Context, fencingGen int64, dispatched bool) (*common.DataSet, string, error) {
			ds, err := r.runGroupSubgraph(ctx, node, itemDS, key, nodeAttempt)
			return ds, "", err
		})
		if err != nil {
			return nil, err
		}
		if discard {
			// collect:"none" -- the subgraph ran for its side effects; the
			// group contributes nothing. fanOut unions these empties into
			// one empty dataset.
			return &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}}, nil
		}
		return out, nil
	})
}

// runGroupSubgraph executes one group item's subgraph: each inner node in
// topological order, the entry node fed the item row, inner outputs threaded
// along the group's inner edges, and the single terminal node's output
// returned as the item's result. It reuses runNodeLogic for the real
// per-node work, so every built-in node type runs inside a group exactly as
// it does at the top level. Inner nodes carry no separate durability,
// leasing or schema inference in this version (nil schemas, fencing 0): that
// finer physical plan is deferred. mappedGroupErrors has already proved the
// subgraph is acyclic with one entry and one terminal.
func (r *Runner) runGroupSubgraph(ctx context.Context, group models.Node, seed *common.DataSet, itemKey string, nodeAttempt int) (*common.DataSet, error) {
	order := TopoOrderedNodes(group.Nodes, group.Edges)

	// Inner edges, indexed by their target, in edge order so a multi-input
	// inner node (a join) sees its inputs left-to-right as authored.
	incoming := make(map[string][]string, len(group.Nodes))
	hasOutgoing := make(map[string]bool, len(group.Nodes))
	for _, e := range group.Edges {
		incoming[e.To] = append(incoming[e.To], e.From)
		hasOutgoing[e.From] = true
	}

	innerOutputs := make(map[string]*common.DataSet, len(group.Nodes))
	empty := func() *common.DataSet { return &common.DataSet{Columns: []string{}, Rows: []common.DataRow{}} }
	var terminalOut *common.DataSet

	for _, inner := range order {
		froms := incoming[inner.ID]
		var input *common.DataSet
		var allInputs []*common.DataSet
		if len(froms) == 0 {
			// The subgraph entry receives the mapped item.
			input = seed
			allInputs = []*common.DataSet{seed}
		} else {
			allInputs = make([]*common.DataSet, 0, len(froms))
			for _, f := range froms {
				ds := innerOutputs[f]
				if ds == nil {
					ds = empty()
				}
				allInputs = append(allInputs, ds)
			}
			input = allInputs[0]
		}

		// A stable, item-and-node-scoped idempotency key, so a retry of the
		// group re-running this item dedups an inner sink's writes rather
		// than doubling them.
		idemKey := fmt.Sprintf("%s:%s:%s", group.ID, itemKey, inner.ID)
		res, err := r.runNodeLogic(inner, input, nil, allInputs, make([]columnSchema, len(allInputs)), innerOutputs, nodeAttempt, idemKey, ctx, 0)
		if err != nil {
			return nil, fmt.Errorf("group %s (%s) item %s: inner node %q (%s): %w", group.Name, group.ID, itemKey, inner.Name, inner.ID, err)
		}
		out := res.output
		if out == nil {
			out = empty()
		}
		innerOutputs[inner.ID] = out
		if !hasOutgoing[inner.ID] {
			terminalOut = out
		}
	}

	if terminalOut == nil {
		return empty(), nil
	}
	return terminalOut, nil
}
