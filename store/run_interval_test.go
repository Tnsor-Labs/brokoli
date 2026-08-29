package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ADR-028 phase 1 (#397): a run's data interval and trigger round-trip
// through both stores, and scheduled dispatch is idempotent on
// (pipeline_id, data_interval_start) -- the leader-failover guard, scoped
// to scheduled runs only.

func intervalTestStores(t *testing.T) map[string]Store {
	t.Helper()
	stores := map[string]Store{}

	sq, err := NewSQLiteStore(filepath.Join(t.TempDir(), "iv.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sq.Close() })
	stores["sqlite"] = sq

	if dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL"); dsn != "" {
		pg, err := NewPostgresStore(dsn)
		if err != nil {
			t.Fatalf("postgres store: %v", err)
		}
		t.Cleanup(func() { pg.Close() })
		stores["postgres"] = pg
	}
	return stores
}

// mkIntervalPipeline creates the pipeline a run's foreign key needs. The
// ID is fresh per call because the Postgres store is a shared database
// across test invocations, and the scheduled-uniqueness index would
// otherwise trip over a previous run's rows.
func mkIntervalPipeline(t *testing.T, s Store) string {
	t.Helper()
	id := common.NewID()
	if err := s.CreatePipeline(&models.Pipeline{
		ID: id, Name: "iv " + id[:8], Enabled: true,
		Nodes:     []models.Node{{ID: "n", Type: models.NodeTypeSourceFile, Name: "n"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.DeletePipeline(id) })
	return id
}

func mkIntervalRun(pipelineID, trigger string, start, end *time.Time) *models.Run {
	now := time.Now().UTC()
	return &models.Run{
		ID:                common.NewID(),
		PipelineID:        pipelineID,
		Status:            models.RunStatusRunning,
		StartedAt:         &now,
		Trigger:           trigger,
		DataIntervalStart: start,
		DataIntervalEnd:   end,
	}
}

func TestRunIntervalRoundTrips(t *testing.T) {
	start := time.Date(2026, 8, 27, 6, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC)

	for name, s := range intervalTestStores(t) {
		t.Run(name, func(t *testing.T) {
			pipe := mkIntervalPipeline(t, s)
			r := mkIntervalRun(pipe, models.RunTriggerScheduled, &start, &end)
			if err := s.CreateRun(r); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetRun(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Trigger != models.RunTriggerScheduled {
				t.Errorf("trigger = %q, want scheduled", got.Trigger)
			}
			if got.DataIntervalStart == nil || !got.DataIntervalStart.Equal(start) {
				t.Errorf("interval start = %v, want %v", got.DataIntervalStart, start)
			}
			if got.DataIntervalEnd == nil || !got.DataIntervalEnd.Equal(end) {
				t.Errorf("interval end = %v, want %v", got.DataIntervalEnd, end)
			}

			// A run with no interval stays without one: absence means "no
			// interval", never a zero time.
			plain := mkIntervalRun(pipe, "", nil, nil)
			if err := s.CreateRun(plain); err != nil {
				t.Fatal(err)
			}
			got, err = s.GetRun(plain.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Trigger != "" || got.DataIntervalStart != nil || got.DataIntervalEnd != nil {
				t.Errorf("a plain run gained provenance it never had: %+v", got)
			}
		})
	}
}

func TestScheduledDispatchIsIdempotentPerInterval(t *testing.T) {
	start := time.Date(2026, 8, 28, 5, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 28, 6, 0, 0, 0, time.UTC)

	for name, s := range intervalTestStores(t) {
		t.Run(name, func(t *testing.T) {
			pipe := mkIntervalPipeline(t, s)

			if err := s.CreateRun(mkIntervalRun(pipe, models.RunTriggerScheduled, &start, &end)); err != nil {
				t.Fatal(err)
			}
			// The same tick fired twice -- a leader-failover race. The
			// second insert must lose with the named sentinel, not
			// succeed and not fail generically.
			err := s.CreateRun(mkIntervalRun(pipe, models.RunTriggerScheduled, &start, &end))
			if !errors.Is(err, ErrDuplicateScheduledRun) {
				t.Fatalf("second scheduled fire: err = %v, want ErrDuplicateScheduledRun", err)
			}

			// The constraint binds SCHEDULED dispatch only (ADR-028): a
			// non-scheduled run at the same interval -- the shape phase 3's
			// backfill will create on purpose -- inserts fine.
			if err := s.CreateRun(mkIntervalRun(pipe, "", &start, &end)); err != nil {
				t.Fatalf("a non-scheduled run at the same interval must be allowed: %v", err)
			}
			// And interval-less runs never collide, however many exist.
			for i := 0; i < 2; i++ {
				if err := s.CreateRun(mkIntervalRun(pipe, "", nil, nil)); err != nil {
					t.Fatalf("interval-less run %d refused: %v", i, err)
				}
			}
			// A different interval for the same pipeline is a different
			// tick and inserts fine.
			s2, e2 := end, end.Add(time.Hour)
			if err := s.CreateRun(mkIntervalRun(pipe, models.RunTriggerScheduled, &s2, &e2)); err != nil {
				t.Fatalf("the next tick's run refused: %v", err)
			}
		})
	}
}
