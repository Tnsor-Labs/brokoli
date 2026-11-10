package engine

// Mapping a task node over a collection (ADR-050 phase 1, #967).
//
// A task node with a `map` block runs its bundle once per item of the
// upstream node named in map.over, each item handed to the task as its
// input: a dataset of exactly that one row, validated against the task's
// declared input like any other input. Each item is its own instance,
// locally or dispatched to a remote worker under the item's own attempt.

import (
	"context"
	"fmt"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// mappedTaskErrors checks a task node's `map` block, when the pipeline is
// saved and when the node runs.
func mappedTaskErrors(n models.Node) []string {
	if !nodeIsMapped(n) {
		return nil
	}
	var errs []string
	if _, err := parseMapConfig(n); err != nil {
		errs = append(errs, err.Error())
	}
	if _, declaresInput := portValueFromInterface(effectiveNodeInterface(n), "inputs", "input"); !declaresInput {
		errs = append(errs, fmt.Sprintf("Node %q: a mapped task receives each item as its input, but this task declares no input port", n.Name))
	}
	if taskNeedsOutputBlobStore(n) {
		// A remote item writes stored outputs under a grant scoped to the
		// node's attempt, not the item's, so every item's worker would
		// hold the same grant. Dataset outputs travel back in the job's
		// result and need none.
		errs = append(errs, fmt.Sprintf("Node %q: a mapped task can only output datasets in this version; this task declares an artifact or collection output", n.Name))
	}
	return errs
}

// runMappedTask runs a mapped task node through fanOut.
func (r *Runner) runMappedTask(ctx context.Context, node models.Node, edgeInputsByFrom map[string]*common.DataSet, nodeAttempt int) (*common.DataSet, error) {
	if errs := mappedTaskErrors(node); len(errs) > 0 {
		return nil, fmt.Errorf("%s", errs[0])
	}
	cfg, err := parseMapConfig(node)
	if err != nil {
		return nil, err
	}
	digest, err := taskBundleV2Reference(node.Config)
	if err != nil {
		return nil, err
	}
	items, _, err := resolveExpansionItems(node, cfg, edgeInputsByFrom)
	if err != nil {
		return nil, err
	}
	if r.dryRun && len(items) > dryRunMappedItems {
		r.log(node.ID, models.LogLevelInfo, "dry run: mapping the first %d of %d item(s)", dryRunMappedItems, len(items))
		items = items[:dryRunMappedItems]
	}
	timeoutSec := 30
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}
	// The item's task sees the node's config without the fan-out block,
	// which is the engine's to act on.
	itemNode := node
	itemNode.Config = make(map[string]interface{}, len(node.Config))
	for k, v := range node.Config {
		if k != "map" {
			itemNode.Config[k] = v
		}
	}
	_, hasAttempts := r.store.(store.ExecutionAttemptStore)
	remote := hasAttempts && r.instanceJobQueue != nil
	return r.fanOut(ctx, node, cfg, items, nodeAttempt, remote, func(ctx context.Context, i int, key string) (*common.DataSet, error) {
		itemDS := &common.DataSet{Columns: columnsOf(items[i]), Rows: []common.DataRow{items[i]}}
		return r.executeMappedInstance(ctx, node, nodeAttempt, i, key, len(items), timeoutSec, remote, func(ctx context.Context, fencingGen int64, dispatched bool) (*common.DataSet, string, error) {
			if dispatched {
				ds, err := r.dispatchTaskInstanceRemotely(itemNode, digest, itemDS, timeoutSec, nodeAttempt, key, fencingGen)
				return ds, "", err
			}
			ds, err := r.runTaskLocally(ctx, itemNode, digest, itemDS, timeoutSec)
			return ds, "", err
		})
	})
}
