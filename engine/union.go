package engine

import (
	"fmt"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// UnionDatasets concatenates two or more datasets into one, row-wise.
//
// This is a straightforward in-memory concatenation, not a manifest/
// reference combination — Tnsor-Labs/brokoli#8's ArtifactStore is scoped
// to node-output resume persistence, not a general dataset/artifact
// manifest system this could build on instead, and the engine has no such
// manifest system yet. The output stays a plain *common.DataSet, matching
// every other node today — this was deliberate groundwork for
// Tnsor-Labs/brokoli#31 (implemented): CollectionRef.collect(mode="union")'s
// dynamic-instance output needed to be consumable by this same union node
// type, and a plain DataSet turned out to be exactly the right shape —
// engine/expansion.go's runCodeExpansion already combines an expansion
// node's per-item results into one plain DataSet (reusing UnionDatasets
// internally for 2+ results) before this union node ever sees it, so a
// downstream union node fed by a single expansion node just needs to pass
// that single input through — see runUnion's identity-union exception.
//
// Schema-mismatch decision: the output's column set is the UNION of every
// input's columns, in first-seen order across inputs (so the result is
// deterministic for a given edge order — see Runner.executeNode, which
// collects allInputs in r.pipe.Edges order). A row from an input that is
// missing one of those columns gets that column filled with nil rather
// than the whole union failing. This "outer union" / coalesced-schema
// behavior is the more useful default for pipelines that combine
// near-but-not-identical sources (e.g. paginated API results where one
// page lacks an optional field the others have) — a pipeline author who
// wants strict schema equality can still enforce it upstream (e.g. via a
// quality_check node) before the union node. Erroring on any column
// mismatch was considered and rejected as too strict for this first
// version.
func UnionDatasets(inputs []*common.DataSet) (*common.DataSet, error) {
	nonNil := make([]*common.DataSet, 0, len(inputs))
	for _, ds := range inputs {
		if ds != nil {
			nonNil = append(nonNil, ds)
		}
	}
	if len(nonNil) < 2 {
		return nil, fmt.Errorf("union requires at least 2 input datasets, got %d", len(nonNil))
	}

	seen := make(map[string]bool)
	var columns []string
	for _, ds := range nonNil {
		for _, c := range ds.Columns {
			if !seen[c] {
				seen[c] = true
				columns = append(columns, c)
			}
		}
	}

	var rows []common.DataRow
	for _, ds := range nonNil {
		for _, row := range ds.Rows {
			out := make(common.DataRow, len(columns))
			for _, c := range columns {
				out[c] = row[c] // zero value (nil) when the source dataset lacks this column
			}
			rows = append(rows, out)
		}
	}

	return &common.DataSet{Columns: columns, Rows: rows}, nil
}
