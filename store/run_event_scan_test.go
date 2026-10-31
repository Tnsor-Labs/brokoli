package store

import (
	"os"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func TestScanRunEventsAcrossRuns(t *testing.T) {
	run := func(t *testing.T, s interface {
		Store
		RunEventScanner
	}) {
		now := time.Now().UTC()
		pid := "scan-" + now.Format("150405.000000")
		if err := s.CreatePipeline(&models.Pipeline{ID: pid, Name: pid, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		var ids []int64
		appendEv := func(runID string, typ models.RunEventType, status models.RunStatus) {
			ev := &models.RunEvent{RunID: runID, EventType: typ, Payload: models.RunEventPayload{Status: status}}
			if err := s.AppendEvent(ev); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, ev.ID)
		}
		for i, status := range []models.RunStatus{models.RunStatusFailed, models.RunStatusSuccess} {
			rid := pid + "-r" + string(rune('a'+i))
			if err := s.CreateRun(&models.Run{ID: rid, PipelineID: pid, Status: models.RunStatusRunning, StartedAt: &now}); err != nil {
				t.Fatal(err)
			}
			appendEv(rid, models.RunEventCreated, models.RunStatusRunning)
			appendEv(rid, models.RunEventTerminal, status)
		}
		types := []models.RunEventType{models.RunEventTerminal}
		future := now.Add(time.Hour)

		got, err := s.ScanRunEvents(ids[0]-1, future, types, 10)
		if err != nil || len(got) != 2 || got[0].ID != ids[1] || got[1].ID != ids[3] {
			t.Fatalf("terminal events after the start = %+v, %v; want ids %d and %d", got, err, ids[1], ids[3])
		}
		if got[0].Payload.Status != models.RunStatusFailed || got[0].EventType != models.RunEventTerminal {
			t.Errorf("first = %+v", got[0])
		}
		if got, _ := s.ScanRunEvents(ids[1], future, types, 10); len(got) != 1 || got[0].ID != ids[3] {
			t.Errorf("after the first terminal event = %+v", got)
		}
		if got, _ := s.ScanRunEvents(ids[0]-1, future, types, 1); len(got) != 1 {
			t.Errorf("limit 1 gave %d", len(got))
		}
		// The recent margin brings back events below the cursor.
		if got, _ := s.ScanRunEvents(ids[3], now.Add(-time.Minute), types, 10); len(got) != 2 {
			t.Errorf("with a recent margin, past the cursor = %d events, want both terminal events", len(got))
		}
		if got, _ := s.ScanRunEvents(0, future, nil, 10); len(got) != 0 {
			t.Errorf("no types = %d events", len(got))
		}
	}
	t.Run("sqlite", func(t *testing.T) { run(t, newTestStore(t)) })
	t.Run("postgres", func(t *testing.T) {
		uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
		if uri == "" {
			t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
		}
		s, err := NewPostgresStore(uri)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		run(t, s)
	})
}
