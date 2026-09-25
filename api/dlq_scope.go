package api

import "github.com/Tnsor-Labs/brokoli/store"

// Which pipeline a dead-letter entry belongs to.

// dlqEntryBelongsToPipeline reports whether this entry is one of that
// pipeline's.
//
// The resolve route checks the caller's organization against the
// pipeline named in the path, and then resolves an entry named by a
// separate id. Nothing tied the two together, so the check guarded an
// object the request was not acting on.
//
// Answered by listing the pipeline's own entries rather than by adding a
// store method that fetches one by id: the entries are already scoped
// per pipeline, the list is bounded, and a new store method would need
// implementing twice and would be one more place to forget the scope.
// If a pipeline ever holds enough dead letters for this to matter, the
// fix is a scoped lookup in the store, not a wider check here.
func dlqEntryBelongsToPipeline(s store.Store, pipelineID, dlqID string) bool {
	if pipelineID == "" || dlqID == "" {
		return false
	}
	entries, err := s.ListDLQ(pipelineID, true, maxListLimit)
	if err != nil {
		// A read failure is not permission to proceed.
		return false
	}
	for _, e := range entries {
		if e.ID == dlqID {
			return true
		}
	}
	return false
}
