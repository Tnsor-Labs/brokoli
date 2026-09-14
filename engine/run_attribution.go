package engine

import (
	"log"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// recordRunAttribution writes who started a run (#241).
//
// Best effort on purpose: the run already exists and is about to
// execute, and failing it because a provenance row could not be written
// would turn a reporting gap into an outage. The failure is logged
// rather than swallowed, so a store that is refusing these writes is
// visible instead of quietly producing runs nobody appears to have
// started.
func recordRunAttribution(s store.Store, runID string, a *models.RunAttribution) {
	if a == nil || runID == "" {
		return
	}
	if err := s.SetRunAttribution(runID, a); err != nil {
		log.Printf("run %s: could not record who started it: %v", runID, err)
	}
}
