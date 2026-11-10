package engine

// Mapping a source_db node over a collection (ADR-050 phase 1, #967).
//
// A source_db node with a `map` block runs its query once per item of the
// upstream node named in map.over, with ${item.column} references bound to
// that item's values the way ${param.x} is bound (sql_params.go): as
// driver placeholders, never as SQL text. Item values come from data, so a
// value that could be spliced would let whatever wrote that data change the
// query. Table and column names therefore cannot come from an item; a
// pipeline that needs one source per table is a generated pipeline
// (ADR-050 generators), not a mapped node.

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// dryRunMappedItems is how many items a dry run maps. A dry run previews
// what the node produces; running every item's query to show a sample
// would cost a full run.
const dryRunMappedItems = 3

// defaultMappedSourceTimeoutSec sizes the lease of one mapped source item,
// which runs as one bounded call on this runner.
const defaultMappedSourceTimeoutSec = 300

// nodeIsMapped reports whether n carries a `map` block.
func nodeIsMapped(n models.Node) bool {
	_, ok := n.Config["map"]
	return ok
}

// runMappedSourceDB runs a mapped source_db node: once per item, through
// fanOut, each item recorded as its own instance. Items run on this runner;
// they are not dispatched to remote workers.
func (r *Runner) runMappedSourceDB(ctx context.Context, node models.Node, edgeInputsByFrom map[string]*common.DataSet, nodeAttempt int) (nodeExecutionResult, error) {
	cfg, err := parseMapConfig(node)
	if err != nil {
		return nodeExecutionResult{}, err
	}
	items, _, err := resolveExpansionItems(node, cfg, edgeInputsByFrom)
	if err != nil {
		return nodeExecutionResult{}, err
	}
	if r.dryRun && len(items) > dryRunMappedItems {
		r.log(node.ID, models.LogLevelInfo, "dry run: mapping the first %d of %d item(s)", dryRunMappedItems, len(items))
		items = items[:dryRunMappedItems]
	}
	timeoutSec := defaultMappedSourceTimeoutSec
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}
	output, err := r.fanOut(ctx, node, cfg, items, nodeAttempt, false, func(ctx context.Context, i int, key string) (*common.DataSet, error) {
		return r.executeMappedInstance(ctx, node, nodeAttempt, i, key, len(items), timeoutSec, false, func(ctx context.Context, _ int64, _ bool) (*common.DataSet, string, error) {
			res, err := r.runSourceDB(withMapItem(ctx, items[i]), node, nodeAttempt)
			if err != nil {
				return nil, "", err
			}
			if res.output == nil {
				// A native or pushed-down read answers with a reference
				// rather than rows; neither applies to a query with item
				// references, and combining references is not built.
				return nil, "", fmt.Errorf("item %d's read returned no rows in memory; a mapped source_db cannot combine references", i+1)
			}
			return res.output, "", nil
		})
	})
	if err != nil {
		return nodeExecutionResult{}, err
	}
	return nodeExecutionResult{output: output}, nil
}

// mappedSourceErrors checks a source_db node's `map` block and its
// ${item...} references when the pipeline is saved: a malformed block, and
// an item reference in a node that has no item to give it.
func mappedSourceErrors(n models.Node) []string {
	query, _ := n.Config["query"].(string)
	if !nodeIsMapped(n) {
		if strings.Contains(query, "${item.") {
			return []string{fmt.Sprintf("Node %q: ${item...} references are only available in a node with a 'map' block", n.Name)}
		}
		return nil
	}
	if _, err := parseMapConfig(n); err != nil {
		return []string{err.Error()}
	}
	return nil
}
