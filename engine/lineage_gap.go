package engine

import (
	"errors"
	"fmt"
	"sync"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

/*
 * Making a missing lineage record visible on the run that is missing it.
 *
 * Lineage profiles and provenance records are best effort by design: the
 * run is the product, and failing a run because a report about it could
 * not be written would trade an outage for a reporting gap. That decision
 * stands. What did not work was the other half of it -- a gap left no
 * mark anyone would find.
 *
 * A production run finished success across 20 nodes and 2335 rows holding
 * zero node_profiles and zero run_provenance rows. The only trace was a
 * debug line per node (filtered out of the default view) and, for
 * provenance, a log.Printf to the process log that was never associated
 * with the run at all. Nothing on the run said its record was partial, so
 * an audit finding no provenance could not tell whether the run predated
 * the feature, ran against a store that cannot save it, or hit a
 * transient failure.
 *
 * Two causes, deliberately treated differently:
 *
 *   store.ErrUnsupported is a permanent, known capability limit. It is
 *   not a malfunction and repeating it per node says nothing new, so it
 *   gets one warning and one event for the whole run.
 *
 *   Anything else is a real write failure and is logged at error level.
 *   It is also announced once per run, for the same reason: twenty
 *   identical error lines bury the first one, which is the one that
 *   matters.
 *
 * Later occurrences still log at debug, so the per-node detail that
 * existed before this is not lost for anyone who goes looking.
 */

// lineageKind names which half of the lineage record is missing. It is
// part of the announce-once key, so a run whose profiles and provenance
// both fail says so about each.
type lineageKind string

const (
	lineageProfile    lineageKind = "profile"
	lineageProvenance lineageKind = "provenance"
)

// lineageGaps remembers which gaps a run has already announced.
//
// The zero value works: a Runner built as a bare struct (as tests do)
// needs no initialisation. saveNodeProfile writes from a background
// goroutine per node while recordNodeProvenance writes from the run's own
// path, so the mutex is load-bearing, not decoration.
type lineageGaps struct {
	mu        sync.Mutex
	announced map[string]bool
}

// firstOf reports whether this (kind, cause) pair is new to the run, and
// records it either way.
func (g *lineageGaps) firstOf(kind lineageKind, unsupported bool) bool {
	key := fmt.Sprintf("%s/%t", kind, unsupported)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.announced[key] {
		return false
	}
	if g.announced == nil {
		g.announced = make(map[string]bool, 2)
	}
	g.announced[key] = true
	return true
}

// noteLineageGap records that one lineage write did not land.
//
// The first of its kind and cause in a run is logged against the run and
// appended as a RunEventLineageIncomplete, which is the durable part: log
// levels get filtered and logs rotate, and the question this answers
// ("is this run's lineage complete?") is usually asked long afterwards.
func (r *Runner) noteLineageGap(kind lineageKind, nodeID string, err error) {
	if err == nil || r.run == nil || r.run.ID == "" {
		return
	}
	unsupported := errors.Is(err, store.ErrUnsupported)

	if !r.lineageGaps.firstOf(kind, unsupported) {
		// Announced already. Keep the per-node detail at debug, which is
		// exactly where it was before this existed.
		r.logWithTrace(nodeID, models.LogLevelDebug, "", 0, nil,
			"%s not recorded for this node: %v", kind, err)
		return
	}

	level := models.LogLevelError
	explanation := fmt.Sprintf(
		"could not write %s for this run, so its lineage record is incomplete: %v", kind, err)
	if unsupported {
		// Not a malfunction: this store does not implement the operation,
		// so every node in this run will be missing it. Say that once, and
		// say what it means, instead of twenty error lines about a
		// limitation nobody can act on mid-run.
		level = models.LogLevelWarning
		explanation = fmt.Sprintf(
			"this store cannot record %s, so no node in this run will have it: %v", kind, err)
	}
	r.logWithTrace(nodeID, level, "", 0,
		map[string]string{"lineage_kind": string(kind), "unsupported": fmt.Sprintf("%t", unsupported)},
		"%s", explanation)

	r.appendEvent(models.RunEvent{
		RunID:     r.run.ID,
		NodeID:    nodeID,
		EventType: models.RunEventLineageIncomplete,
		Payload:   models.RunEventPayload{Error: explanation},
	})
}
