package sodp

import (
	"testing"
	"time"
)

// A run whose start this replica never saw still records whose it is when
// it finishes. Only run.started used to carry the organization, so after a
// restart a finished run was stored with none: gone from its owner's view,
// and readable by a session that accepts runs without an organization.
func TestFinishedRunRecordsItsOrganizationWithoutAStart(t *testing.T) {
	for _, typ := range []string{"run.completed", "run.failed"} {
		srv := NewServer()
		bridgeEvent(srv, BridgeEvent{Type: typ, RunID: "r1", PipelineID: "p1", PipelineName: "P", OrgID: "acme", Timestamp: time.Now()})

		val, _ := srv.State.Get("runs.r1")
		m, _ := val.(map[string]any)
		if m["org_id"] != "acme" || m["pipeline_id"] != "p1" {
			t.Errorf("%s with no prior start stored %v, want org_id acme and pipeline_id p1", typ, m)
		}
		owner, stranger := NewSession("a", "acme"), NewSession("d", "default")
		owner.MarkAuthenticated()
		stranger.MarkAuthenticated()
		if !srv.keyAllowedForSession(owner, "runs.r1") {
			t.Errorf("%s: the owning organization cannot watch its run", typ)
		}
		if srv.keyAllowedForSession(stranger, "runs.r1") {
			t.Errorf("%s: a default-organization session can watch another organization's run", typ)
		}
	}
}
