package engine

import (
	"log"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Recording what a node execution consumed and produced (ADR-039 part 4).
//
// The lineage graph answers "how is this pipeline wired". This answers
// "what did this run actually do", which is the question behind every
// report of a wrong number, and the two can differ: a pipeline is edited,
// and yesterday's run is not what is on screen today.
//
// Best effort and logged, for the same reason attribution is: the run is
// the product and provenance is a report about it. Failing a run because
// a provenance row could not be written would trade an outage for a
// reporting gap.
//
// Nothing is recorded for a dry run, which by definition did not consume
// or produce anything.

// recordNodeProvenance writes one node's execution record.
//
// Called after a successful execution, where the output is known. A
// failed node is deliberately not recorded: its output either does not
// exist or is the partial result of an attempt nothing downstream
// consumed, and a record of it would be an attestation of bytes that
// never became anybody's input.
func (r *Runner) recordNodeProvenance(node models.Node, outputs *nodeOutputs, edgeStates []edgeResolution,
	output *common.DataSet, outputRef *artifact.DatasetRef) {
	if r.dryRun || r.run == nil || r.run.ID == "" {
		return
	}
	provStore, ok := r.store.(interface {
		SaveNodeProvenance(*models.NodeProvenance) error
	})
	if !ok {
		return
	}

	record := &models.NodeProvenance{
		RunID:      r.run.ID,
		NodeID:     node.ID,
		Inputs:     r.inputFacts(node, outputs, edgeStates),
		Output:     datasetFact("", output, outputRef),
		RecordedAt: time.Now().UTC(),
	}
	if err := provStore.SaveNodeProvenance(record); err != nil {
		log.Printf("run %s node %s: could not record provenance: %v", r.run.ID, node.ID, err)
	}
}

// inputFacts describes every upstream dataset this execution consumed.
//
// Only ACTIVE edges. A branch that a condition node routed away from
// contributed nothing, and recording it with a zero row count would read
// as "it ran and produced nothing" -- a different and false statement.
func (r *Runner) inputFacts(node models.Node, outputs *nodeOutputs, edgeStates []edgeResolution) []models.DatasetFact {
	var facts []models.DatasetFact
	seen := map[string]bool{}
	for edgeIndex, edge := range r.pipe.Edges {
		if edge.To != node.ID || edgeIndex >= len(edgeStates) || edgeStates[edgeIndex] != edgeActive {
			continue
		}
		if seen[edge.From] {
			// Two edges from the same upstream node are one dataset
			// consumed once, not two inputs.
			continue
		}
		seen[edge.From] = true

		if ref, ok := outputs.GetRef(edge.From); ok && ref != nil {
			facts = append(facts, *datasetFact(edge.From, nil, ref))
			continue
		}
		// Held in memory: the row count and columns are known, the bytes
		// were never stored so there is nothing to digest.
		ds, _, err := outputs.Get(edge.From)
		if err != nil || ds == nil {
			// An upstream whose output cannot be read now is recorded by
			// name with nothing else, rather than omitted. That it was an
			// input is a fact; what it held is not one this can state.
			facts = append(facts, models.DatasetFact{Node: edge.From})
			continue
		}
		facts = append(facts, *datasetFact(edge.From, ds, nil))
	}
	return facts
}

// datasetFact describes one dataset, from whichever form is present.
//
// Returns nil when there is no dataset at all, which is the honest record
// for a sink or a notify: they produce no dataset, and an empty fact
// would say they produced an empty one.
func datasetFact(nodeID string, ds *common.DataSet, ref *artifact.DatasetRef) *models.DatasetFact {
	switch {
	case ref != nil:
		return &models.DatasetFact{
			Node: nodeID, Digest: ref.Checksum,
			RowCount: ref.RowCount, Columns: ref.Columns,
		}
	case ds != nil:
		return &models.DatasetFact{
			Node: nodeID, RowCount: int64(len(ds.Rows)), Columns: ds.Columns,
		}
	default:
		return nil
	}
}

// ProvenanceStore is the slice of store.Store this needs, named so a
// caller can see what provenance depends on without reading the runner.
type ProvenanceStore interface {
	SaveNodeProvenance(p *models.NodeProvenance) error
	GetRunProvenance(runID string) ([]models.NodeProvenance, error)
}

var _ ProvenanceStore = (store.Store)(nil)
