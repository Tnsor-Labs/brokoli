package store

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// #399: parked waits round-trip, due-listing respects next_poll_at and
// expiry, the delete is a claim, and the waiting->running/failed flips
// are conditional -- on both stores.
func TestParkedWaitLifecycle(t *testing.T) {
	for name, s := range intervalTestStores(t) {
		t.Run(name, func(t *testing.T) {
			pid := mkIntervalPipeline(t, s)
			now := time.Now().UTC().Truncate(time.Second)
			runID := common.NewID()
			if err := s.CreateRun(&models.Run{
				ID: runID, PipelineID: pid, Status: models.RunStatusWaiting, StartedAt: &now,
			}); err != nil {
				t.Fatal(err)
			}

			w := &models.ParkedWait{
				RunID: runID, PipelineID: pid, NodeID: "gate",
				Condition: `{"condition":"file_exists","path":"/x"}`, PollInterval: 30000,
				NextPollAt: now.Add(30 * time.Second), ExpiresAt: now.Add(6 * time.Hour), CreatedAt: now,
			}
			if err := s.CreateParkedWait(w); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _, _ = s.DeleteParkedWait(runID) })

			// Not due yet.
			due, err := s.ListDueParkedWaits(now.Add(time.Second), 10)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range due {
				if d.RunID == runID {
					t.Fatal("listed as due before next_poll_at")
				}
			}
			// Due after next_poll_at.
			due, err = s.ListDueParkedWaits(now.Add(31*time.Second), 10)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range due {
				if d.RunID == runID {
					found = true
					if d.Condition != w.Condition || d.PollInterval != 30000 {
						t.Errorf("round-trip mangled the park: %+v", d)
					}
				}
			}
			if !found {
				t.Fatal("due park not listed")
			}

			// Bump pushes it out of the due window.
			if err := s.BumpParkedWait(runID, now.Add(2*time.Minute)); err != nil {
				t.Fatal(err)
			}
			due, _ = s.ListDueParkedWaits(now.Add(31*time.Second), 10)
			for _, d := range due {
				if d.RunID == runID {
					t.Fatal("still due after bump")
				}
			}

			// The waiting->running claim: exactly one winner, second is a no-op.
			ok, err := s.ClaimWaitingRun(runID)
			if err != nil || !ok {
				t.Fatalf("claim: %v %v", ok, err)
			}
			ok, err = s.ClaimWaitingRun(runID)
			if err != nil || ok {
				t.Fatalf("second claim must lose: %v %v", ok, err)
			}
			// FailWaitingRun cannot clobber a run that already woke.
			ok, err = s.FailWaitingRun(runID, now, "too late")
			if err != nil || ok {
				t.Fatalf("fail-after-wake must be a no-op: %v %v", ok, err)
			}

			// The delete is a claim: true once, false after.
			ok, err = s.DeleteParkedWait(runID)
			if err != nil || !ok {
				t.Fatalf("delete: %v %v", ok, err)
			}
			ok, err = s.DeleteParkedWait(runID)
			if err != nil || ok {
				t.Fatalf("second delete must report false: %v %v", ok, err)
			}
		})
	}
}
