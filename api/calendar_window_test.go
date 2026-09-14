package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func getCalendar(t *testing.T, s store.Store, query string) (*httptest.ResponseRecorder, []store.CalendarDay) {
	t.Helper()
	rec := httptest.NewRecorder()
	calendarHandler(s)(rec, httptest.NewRequest(http.MethodGet, "/runs/calendar"+query, nil))
	if rec.Code != http.StatusOK {
		return rec, nil
	}
	var out []store.CalendarDay
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v; body=%s", query, err, rec.Body.String())
	}
	return rec, out
}

// seedCalendarRuns creates a pipeline and n runs starting at an explicit
// instant. dashboard_rollup_test.go's seedRuns takes an age rather than a
// time, which cannot place a run on a specific UTC day.
func seedCalendarRuns(t *testing.T, s store.Store, pipelineID, name string, n int, status models.RunStatus, started time.Time) {
	t.Helper()
	if err := s.CreatePipeline(&models.Pipeline{
		ID: pipelineID, Name: name, Enabled: true,
		WorkspaceID: models.DefaultWorkspaceID,
		Nodes:       []models.Node{{ID: "s1", Type: models.NodeTypeSourceFile, Name: "src"}},
		CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	finished := started.Add(time.Second)
	for i := 0; i < n; i++ {
		if err := s.CreateRun(&models.Run{
			ID:         pipelineID + "-run-" + string(rune('a'+i)),
			PipelineID: pipelineID,
			Status:     status,
			StartedAt:  &started,
			FinishedAt: &finished,
		}); err != nil {
			t.Fatalf("create run: %v", err)
		}
	}
}

// #611: every one of these silently became 90 days of data with a 200, so
// a client could not tell a honoured request from a substituted one.
func TestCalendarRefusesADaysValueItCannotHonour(t *testing.T) {
	s := newDashboardTestStore(t)

	for _, q := range []string{"?days=0", "?days=-5", "?days=400", "?days=abc", "?days=30junk", "?days=%20"} {
		rec, _ := getCalendar(t, s, q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET /runs/calendar%s: status = %d, want 400; body=%s", q, rec.Code, rec.Body.String())
		}
	}

	// The other direction: the bounds themselves are accepted, and so is
	// an absent parameter.
	for _, q := range []string{"", "?days=1", "?days=365", "?days=90"} {
		rec, _ := getCalendar(t, s, q)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /runs/calendar%s: status = %d, want 200; body=%s", q, rec.Code, rec.Body.String())
		}
	}
}

// The response now carries the window: exactly the days asked for, oldest
// first, with quiet days present as zeroes rather than absent.
func TestCalendarReturnsEveryDayInTheWindow(t *testing.T) {
	s := newDashboardTestStore(t)
	seedCalendarRuns(t, s, "pipe-1", "daily", 2, models.RunStatusSuccess, time.Now().UTC().Add(-2*time.Hour))

	_, cal := getCalendar(t, s, "?days=7")
	if len(cal) != 7 {
		t.Fatalf("calendar = %d entries, want 7 (the window asked for)", len(cal))
	}

	// Oldest first, contiguous, ending on today in UTC.
	wantDates := store.CalendarWindowDates(7)
	for i, d := range cal {
		if d.Date != wantDates[i] {
			t.Fatalf("calendar[%d].date = %q, want %q", i, d.Date, wantDates[i])
		}
	}
	today := time.Now().UTC().Format("2006-01-02")
	if cal[len(cal)-1].Date != today {
		t.Errorf("last day = %q, want today in UTC (%q)", cal[len(cal)-1].Date, today)
	}
	if cal[len(cal)-1].Total != 2 || cal[len(cal)-1].Success != 2 {
		t.Errorf("today = %+v, want 2 total / 2 success", cal[len(cal)-1])
	}
	// A day with no runs is a zero, not a gap.
	if cal[0].Total != 0 || cal[0].Date == "" {
		t.Errorf("oldest day = %+v, want a zero-filled entry", cal[0])
	}
}

// A request for one day is one day, not two. SQLite read from midnight N
// days ago, which is N+1 calendar days.
func TestCalendarWindowIsExactlyTheDaysAsked(t *testing.T) {
	s := newDashboardTestStore(t)
	yesterday := time.Now().UTC().AddDate(0, 0, -1)
	seedCalendarRuns(t, s, "pipe-old", "yesterday", 3, models.RunStatusSuccess, yesterday)
	seedCalendarRuns(t, s, "pipe-new", "today", 1, models.RunStatusSuccess, time.Now().UTC().Add(-time.Minute))

	_, cal := getCalendar(t, s, "?days=1")
	if len(cal) != 1 {
		t.Fatalf("calendar = %d entries, want 1", len(cal))
	}
	if cal[0].Date != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("date = %q, want today in UTC", cal[0].Date)
	}
	if cal[0].Total != 1 {
		t.Errorf("total = %d, want 1; yesterday's runs are outside a one-day window", cal[0].Total)
	}

	// Two days reaches yesterday.
	_, cal = getCalendar(t, s, "?days=2")
	if len(cal) != 2 {
		t.Fatalf("calendar = %d entries, want 2", len(cal))
	}
	if cal[0].Total != 3 {
		t.Errorf("yesterday total = %d, want 3", cal[0].Total)
	}
}
