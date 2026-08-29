package store

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// #400: GridNodeRuns returns the LATEST attempt per (run, node), batched
// over many runs in one query, on both stores.
func TestGridNodeRunsLatestAttemptWins(t *testing.T) {
	for name, s := range intervalTestStores(t) {
		t.Run(name, func(t *testing.T) {
			pid := mkIntervalPipeline(t, s)
			now := time.Now().UTC()

			mkRun := func() string {
				id := common.NewID()
				if err := s.CreateRun(&models.Run{
					ID: id, PipelineID: pid, Status: models.RunStatusSuccess, StartedAt: &now,
				}); err != nil {
					t.Fatal(err)
				}
				return id
			}
			run1, run2 := mkRun(), mkRun()

			mkNR := func(runID, nodeID string, attempt int, status models.RunStatus) {
				if err := s.CreateNodeRun(&models.NodeRun{
					ID: common.NewID(), RunID: runID, NodeID: nodeID,
					Status: status, Attempt: attempt, StartedAt: &now,
					DurationMs: int64(100 + attempt), RowCount: attempt,
				}); err != nil {
					t.Fatal(err)
				}
			}
			// run1 node a: attempt 0 failed, attempt 1 succeeded -- the
			// grid must show attempt 1.
			mkNR(run1, "a", 0, models.RunStatusFailed)
			mkNR(run1, "a", 1, models.RunStatusSuccess)
			mkNR(run1, "b", 0, models.RunStatusSuccess)
			mkNR(run2, "a", 0, models.RunStatusFailed)

			got, err := s.GridNodeRuns([]string{run1, run2})
			if err != nil {
				t.Fatal(err)
			}
			if len(got[run1]) != 2 {
				t.Fatalf("run1 rows = %d, want 2 (latest per node)", len(got[run1]))
			}
			for _, nr := range got[run1] {
				if nr.NodeID == "a" {
					if nr.Attempt != 1 || nr.Status != models.RunStatusSuccess {
						t.Errorf("run1/a = attempt %d %s, want the retry (attempt 1, success)",
							nr.Attempt, nr.Status)
					}
				}
			}
			if len(got[run2]) != 1 || got[run2][0].Status != models.RunStatusFailed {
				t.Errorf("run2 = %+v, want the one failed attempt", got[run2])
			}

			// Empty input: empty map, no query error.
			empty, err := s.GridNodeRuns(nil)
			if err != nil || len(empty) != 0 {
				t.Errorf("empty input: %v %v", empty, err)
			}
		})
	}
}
