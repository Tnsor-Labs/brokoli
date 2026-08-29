package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/go-chi/chi/v5"
)

// The backfill endpoint's request contract after ADR-028 phase 3 (#397):
// interval-native start/end (RFC3339), with the pre-ADR date-only fields
// still accepted -- a bare start_date means midnight UTC and end_date keeps
// its historical inclusive-day meaning. The response is the plan, not a
// run-ID list: runs execute in the background.

func TestBackfillHandler_DateCompatAndPlan(t *testing.T) {
	withoutOrgResolver(t)
	s := newDepAPIStore(t)
	e := engine.NewEngine(s)
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	h := NewRunHandler(s, e)

	pipe := createOrgPipe(t, s, "bf-pipe", "BF Pipeline", "org-a", nil)
	pipe.Schedule = "0 * * * *"
	pipe.Nodes = []models.Node{{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
		Config: map[string]interface{}{"path": "in-${interval.start}.csv", "format": "csv"}}}
	if err := s.UpdatePipeline(pipe); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.Post("/pipelines/{id}/backfill", h.Backfill)

	post := func(body string) (*httptest.ResponseRecorder, map[string]interface{}) {
		t.Helper()
		req := reqWithOrg("POST", "/pipelines/"+pipe.ID+"/backfill", []byte(body), "org-a")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var out map[string]interface{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec, out
	}

	// Date-only, one inclusive day: [Aug 20 00:00, Aug 21 00:00) on an
	// hourly schedule is exactly 24 intervals -- the count only comes out
	// right if end_date still covers THROUGH its day.
	rec, plan := post(`{"start_date":"2026-08-20","end_date":"2026-08-20"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("date-only backfill: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := plan["intervals"]; got != float64(24) {
		t.Errorf("one inclusive day of hourly slices = %v intervals, want 24", got)
	}
	if _, hasRuns := plan["runs"]; hasRuns {
		t.Error("the response is a plan now, not the old run-ID list")
	}

	// RFC3339 half-open range: [06:00, 09:00) = 3 hourly intervals.
	rec, plan = post(`{"start":"2026-08-20T06:00:00Z","end":"2026-08-20T09:00:00Z"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("RFC3339 backfill: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := plan["intervals"]; got != float64(3) {
		t.Errorf("06:00..09:00 hourly = %v intervals, want 3", got)
	}
	if first, _ := plan["first_interval_start"].(string); first != "2026-08-20T06:00:00Z" {
		t.Errorf("first_interval_start = %q, want the 06:00 tick", first)
	}

	// Neither form supplied: 400 naming both accepted shapes.
	rec, _ = post(`{}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty request: status %d, want 400", rec.Code)
	}
}
