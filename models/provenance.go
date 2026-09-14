package models

import "time"

// What one node execution actually consumed and produced (ADR-039 part 4).
//
// The lineage graph describes a pipeline as it is defined now. This
// describes a run: which bytes went in, which came out, how many rows,
// and which columns were observed on each side. It is the difference
// between "this is how the pipeline is wired" and "this is what
// happened", and only the second is an answer to "why is this number
// wrong".
//
// It is also the substrate for the `attested` evidence level. A declared
// column mapping says the pipeline intends a derivation; an execution
// record with digests on both sides can show that the derivation is what
// ran.

// DatasetFact is one dataset as a node execution saw it.
type DatasetFact struct {
	// Node is the upstream node that produced this dataset. Empty on the
	// output fact, which belongs to the node recording it.
	Node string `json:"node,omitempty"`

	// Digest is "sha256:<hex>" over the stored bytes, or EMPTY when the
	// dataset was never written to the artifact store.
	//
	// Empty is common and is not a failure: a small dataset is passed
	// between nodes in memory, and there are no stored bytes to hash.
	// Hashing it anyway would put an O(rows) pass on the hot path of
	// every node in the product to attest a dataset nobody kept, so the
	// absence is recorded honestly instead.
	//
	// A consumer must treat an empty digest as "not attestable", never as
	// "the empty dataset" or "unchanged".
	Digest string `json:"digest,omitempty"`

	// RowCount is how many rows the execution saw. Always recorded.
	RowCount int64 `json:"row_count"`

	// Columns is the column order observed on this dataset.
	Columns []string `json:"columns,omitempty"`
}

// NodeProvenance is one node's execution record within one run.
type NodeProvenance struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"node_id"`

	// Inputs is one fact per upstream node whose edge was active for this
	// execution. An inactive branch contributed nothing and is absent,
	// rather than present with a zero row count, which would read as "it
	// ran and produced nothing".
	Inputs []DatasetFact `json:"inputs,omitempty"`

	// Output is what this node produced, or nil for a node that produces
	// no dataset -- a sink, a notify.
	Output *DatasetFact `json:"output,omitempty"`

	RecordedAt time.Time `json:"recorded_at"`
}
