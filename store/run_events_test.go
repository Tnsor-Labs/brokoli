package store

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// seedRunForEvents creates the pipeline + run rows run_events.run_id (and
// runs.pipeline_id) foreign keys require.
func seedRunForEvents(t *testing.T, s *SQLiteStore, pipelineID, runID string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.CreatePipeline(&models.Pipeline{
		ID: pipelineID, Name: "Event Test Pipeline", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	if err := s.CreateRun(&models.Run{ID: runID, PipelineID: pipelineID, Status: models.RunStatusRunning, StartedAt: &now}); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
}

// TestMigrationAppliesCleanly verifies a fresh SQLite DB gets a working
// run_events table (issue #6 acceptance criteria: migration applies cleanly).
func TestMigrationAppliesCleanly(t *testing.T) {
	s := newTestStore(t)
	seedRunForEvents(t, s, "pipe-migrate", "run-migrate")

	if err := s.AppendEvent(&models.RunEvent{
		RunID:     "run-migrate",
		EventType: models.RunEventCreated,
		Payload:   models.RunEventPayload{Status: models.RunStatusRunning},
	}); err != nil {
		t.Fatalf("run_events table not usable after fresh migrate: %v", err)
	}
}

func TestAppendEventAndListEventsByRun(t *testing.T) {
	s := newTestStore(t)
	seedRunForEvents(t, s, "pipe-1", "run-1")

	now := time.Now().UTC().Truncate(time.Millisecond)
	attempt0 := 0
	attempt1 := 1

	events := []*models.RunEvent{
		{
			RunID:     "run-1",
			EventType: models.RunEventCreated,
			Payload: models.RunEventPayload{
				Status:     models.RunStatusRunning,
				PipelineID: "pipe-1",
				StartedAt:  &now,
				TraceID:    "trace-1",
				Params:     map[string]string{"env": "prod"},
			},
		},
		{
			RunID:     "run-1",
			NodeID:    "node-a",
			Attempt:   &attempt0,
			EventType: models.AttemptStarted,
			Payload: models.RunEventPayload{
				Status:    models.RunStatusRunning,
				NodeRunID: "nr-1",
				StartedAt: &now,
			},
		},
		{
			RunID:     "run-1",
			NodeID:    "node-a",
			Attempt:   &attempt0,
			EventType: models.AttemptFailed,
			Payload: models.RunEventPayload{
				Status:    models.RunStatusFailed,
				NodeRunID: "nr-1",
				Error:     "boom",
			},
		},
		{
			RunID:     "run-1",
			NodeID:    "node-a",
			Attempt:   &attempt1,
			EventType: models.RetryScheduled,
			Payload: models.RunEventPayload{
				Error:     "boom",
				BackoffMs: 2000,
			},
		},
	}

	for i, e := range events {
		if err := s.AppendEvent(e); err != nil {
			t.Fatalf("AppendEvent[%d]: %v", i, err)
		}
		if e.ID == 0 {
			t.Errorf("AppendEvent[%d]: expected a non-zero assigned ID", i)
		}
		if e.CreatedAt.IsZero() {
			t.Errorf("AppendEvent[%d]: expected CreatedAt to be set", i)
		}
		if e.SchemaVersion != 1 {
			t.Errorf("AppendEvent[%d]: SchemaVersion = %d, want 1", i, e.SchemaVersion)
		}
	}

	got, err := s.ListEventsByRun("run-1")
	if err != nil {
		t.Fatalf("ListEventsByRun: %v", err)
	}
	if len(got) != len(events) {
		t.Fatalf("ListEventsByRun returned %d events, want %d", len(got), len(events))
	}

	// Ordering must be monotonic (ascending id / append order).
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Errorf("events not monotonically ordered: id[%d]=%d <= id[%d]=%d", i, got[i].ID, i-1, got[i-1].ID)
		}
	}

	// Run-level event: NodeID/Attempt empty, payload round-trips.
	if got[0].EventType != models.RunEventCreated {
		t.Errorf("got[0].EventType = %q, want %q", got[0].EventType, models.RunEventCreated)
	}
	if got[0].NodeID != "" || got[0].Attempt != nil {
		t.Errorf("got[0] should be run-level: NodeID=%q Attempt=%v", got[0].NodeID, got[0].Attempt)
	}
	if got[0].Payload.PipelineID != "pipe-1" || got[0].Payload.TraceID != "trace-1" {
		t.Errorf("got[0].Payload = %+v, want pipeline_id/trace_id preserved", got[0].Payload)
	}
	if got[0].Payload.Params["env"] != "prod" {
		t.Errorf("got[0].Payload.Params = %+v, want env=prod", got[0].Payload.Params)
	}
	if !got[0].Payload.StartedAt.Equal(now) {
		t.Errorf("got[0].Payload.StartedAt = %v, want %v", got[0].Payload.StartedAt, now)
	}

	// Node-attempt-level events: NodeID/Attempt populated.
	if got[1].NodeID != "node-a" || got[1].Attempt == nil || *got[1].Attempt != 0 {
		t.Errorf("got[1] node/attempt = %q/%v, want node-a/0", got[1].NodeID, got[1].Attempt)
	}
	if got[1].Payload.NodeRunID != "nr-1" {
		t.Errorf("got[1].Payload.NodeRunID = %q, want nr-1", got[1].Payload.NodeRunID)
	}

	if got[2].EventType != models.AttemptFailed || got[2].Payload.Error != "boom" {
		t.Errorf("got[2] = %+v, want AttemptFailed with error=boom", got[2])
	}

	if got[3].EventType != models.RetryScheduled || got[3].Attempt == nil || *got[3].Attempt != 1 || got[3].Payload.BackoffMs != 2000 {
		t.Errorf("got[3] = %+v, want RetryScheduled attempt=1 backoff_ms=2000", got[3])
	}

	// A different run must not see these events.
	seedRunForEvents(t, s, "pipe-2", "run-2")
	other, err := s.ListEventsByRun("run-2")
	if err != nil {
		t.Fatalf("ListEventsByRun(run-2): %v", err)
	}
	if len(other) != 0 {
		t.Errorf("ListEventsByRun(run-2) = %d events, want 0 (events must be scoped by run_id)", len(other))
	}
}

func TestAppendEventTxRollsBackWithTransaction(t *testing.T) {
	s := newTestStore(t)
	seedRunForEvents(t, s, "pipe-tx", "run-tx")

	// A failing WithTx must roll back the appended event along with it —
	// this is the "usable inside WithTx" requirement from issue #6.
	wantErr := errors.New("rollback for test")
	err := s.WithTx(func(tx *sql.Tx) error {
		if err := s.AppendEventTx(tx, &models.RunEvent{
			RunID:     "run-tx",
			EventType: models.RunEventCreated,
			Payload:   models.RunEventPayload{Status: models.RunStatusRunning},
		}); err != nil {
			return err
		}
		return wantErr
	})
	if err != wantErr {
		t.Fatalf("WithTx error = %v, want %v", err, wantErr)
	}

	events, err := s.ListEventsByRun("run-tx")
	if err != nil {
		t.Fatalf("ListEventsByRun: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("ListEventsByRun after rolled-back tx = %d events, want 0", len(events))
	}

	// A committed WithTx must persist the event.
	if err := s.WithTx(func(tx *sql.Tx) error {
		return s.AppendEventTx(tx, &models.RunEvent{
			RunID:     "run-tx",
			EventType: models.RunEventCreated,
			Payload:   models.RunEventPayload{Status: models.RunStatusRunning},
		})
	}); err != nil {
		t.Fatalf("WithTx (commit): %v", err)
	}
	events, err = s.ListEventsByRun("run-tx")
	if err != nil {
		t.Fatalf("ListEventsByRun: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("ListEventsByRun after committed tx = %d events, want 1", len(events))
	}
}
