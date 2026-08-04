package models

import "time"

// ExpansionInstance is the durable per-item execution record for one
// instance of a dynamic-expansion `code` node (Tnsor-Labs/brokoli#31) — the
// `expansion` config block compiled by brokoli-sdk's `_TaskWrapper.expand()`
// (see brokoli/pipeline.py). One row is created per item in the upstream
// collection a `.expand()` node fans out over, giving each item's
// success/failure its own durable, independently visible record instead of
// one aggregate status for the whole expansion.
//
// Deliberately NOT stored as a NodeRun row keyed by the existing
// (node_id, attempt) pair, and NOT stored via ExecutionAttempt's
// (run_id, node_id, attempt) claim/lease table either — both of those
// already use "attempt" exclusively as the *retry* counter for a node's
// single dispatch (engine/runner.go's executeNode retry loop). Both
// engine.ProjectRun's nodeRunKey and engine/recovery.go's
// latestNodeOutcome assume exactly one logical execution per
// (node_id, attempt); overloading either key to also carry per-item
// identity would silently collapse every item's row into one on
// replay/recovery instead of extending the existing pattern. NodeAttempt
// below records which retry of the *expansion node itself* (all items run
// again together on a node-level retry — see runCodeExpansion's doc
// comment for why per-item retry isolation is out of scope for this first
// version) an instance belongs to, while InstanceIndex/InstanceKey are the
// new, orthogonal per-item dimension. This table is additive: nothing in
// the recovery/notification/stats path reads it, so it cannot perturb
// their existing behavior.
type ExpansionInstance struct {
	ID     string `json:"id"`
	RunID  string `json:"run_id"`
	NodeID string `json:"node_id"`

	// NodeAttempt is the expansion node's own retry-attempt number (mirrors
	// models.NodeRun.Attempt) — which dispatch of the whole expansion node
	// this instance ran under.
	NodeAttempt int `json:"node_attempt"`

	// InstanceIndex is the 0-based position of this item within the
	// resolved upstream collection (expansion.over) at the time this
	// attempt ran.
	InstanceIndex int `json:"instance_index"`

	// InstanceKey is this item's stable identity. Derived from
	// expansion.key when present — see the doc comment on
	// engine.deriveInstanceKey for why the key reference itself can't be
	// executed, and what fallback is used instead.
	InstanceKey string `json:"instance_key"`

	Status     RunStatus  `json:"status"`
	RowCount   int        `json:"row_count"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	DurationMs int64      `json:"duration_ms"`
	Error      string     `json:"error,omitempty"`
}
