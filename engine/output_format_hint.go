package engine

import "github.com/Tnsor-Labs/brokoli/models"

// singleStoredInputFormat returns the format a node's one stored input was
// written in, for the node's own output to follow (#641).
//
// Empty -- no preference -- when the node has no active input, more than
// one distinct upstream, or an input held in memory rather than stored.
// With two inputs there is no single format to follow, and an output that
// matched one of them could still not be attested, since attestation needs
// exactly one input.
func (r *Runner) singleStoredInputFormat(node models.Node, outputs *nodeOutputs, edgeStates []edgeResolution) string {
	from := ""
	for i, e := range r.pipe.Edges {
		if e.To != node.ID || i >= len(edgeStates) || edgeStates[i] != edgeActive {
			continue
		}
		if from != "" && from != e.From {
			return ""
		}
		from = e.From
	}
	if from == "" {
		return ""
	}
	ref, ok := outputs.GetRef(from)
	if !ok || ref == nil {
		return ""
	}
	return ref.Format
}
