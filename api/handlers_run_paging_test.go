package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

func newRunPagingStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// newRunPagingRouter mirrors routes.go's wiring for this endpoint. The
// engine is nil: ListByPipeline only reads from the store.
func newRunPagingRouter(s store.Store) *chi.Mux {
	h := NewRunHandler(s, nil)
	r := chi.NewRouter()
	r.Get("/pipelines/{id}/runs", h.ListByPipeline)
	return r
}

func seedPipelineRuns(t *testing.T, s store.Store, pipelineID string, n int) {
	t.Helper()
	if err := s.CreatePipeline(&models.Pipeline{
		ID: pipelineID, Name: pipelineID, Enabled: true,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}
	for i := 0; i < n; i++ {
		started := time.Now().UTC().Add(time.Duration(i) * time.Second)
		if err := s.CreateRun(&models.Run{
			ID: common.NewID(), PipelineID: pipelineID,
			Status: models.RunStatusSuccess, StartedAt: &started,
		}); err != nil {
			t.Fatalf("CreateRun %d: %v", i, err)
		}
	}
}

func getRuns(t *testing.T, rt *chi.Mux, url string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %s", url, rec.Code, rec.Body.String())
	}
	return rec
}

// The unparameterised call is what the UI makes today, so it has to keep
// returning a bare JSON array rather than a pagination envelope.
func TestListByPipeline_BareCallStillReturnsArray(t *testing.T) {
	s := newRunPagingStore(t)
	seedPipelineRuns(t, s, "p1", 5)
	rt := newRunPagingRouter(s)

	rec := getRuns(t, rt, "/pipelines/p1/runs")
	var runs []models.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
		t.Fatalf("expected a plain array: %v (body %s)", err, rec.Body.String())
	}
	if len(runs) != 5 {
		t.Errorf("got %d runs, want 5", len(runs))
	}
}

// A pipeline with no runs must serialise as [] and not null, or clients
// iterating the response break.
func TestListByPipeline_EmptyIsArrayNotNull(t *testing.T) {
	s := newRunPagingStore(t)
	if err := s.CreatePipeline(&models.Pipeline{
		ID: "empty", Name: "empty", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	rec := getRuns(t, newRunPagingRouter(s), "/pipelines/empty/runs")
	if got := rec.Body.String(); got != "[]\n" && got != "[]" {
		t.Errorf("body = %q, want an empty array", got)
	}
}

// The point of the issue: history beyond the old hard-coded 50 is now
// reachable, and the walk terminates.
func TestListByPipeline_CursorReachesRunsBeyondFifty(t *testing.T) {
	s := newRunPagingStore(t)
	const total = 220
	seedPipelineRuns(t, s, "big", total)
	rt := newRunPagingRouter(s)

	seen := map[string]bool{}
	url := "/pipelines/big/runs?limit=50"
	pages := 0
	for {
		rec := getRuns(t, rt, url)
		var res struct {
			Items   []models.Run `json:"items"`
			HasNext bool         `json:"has_next"`
			Cursor  string       `json:"cursor"`
			Limit   int          `json:"limit"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		if res.Limit != 50 {
			t.Errorf("page %d: limit echoed as %d, want 50", pages, res.Limit)
		}
		for _, r := range res.Items {
			if seen[r.ID] {
				t.Fatalf("run %s served on two pages", r.ID)
			}
			seen[r.ID] = true
		}
		pages++
		if !res.HasNext {
			if res.Cursor != "" {
				t.Errorf("last page still advertised cursor %q", res.Cursor)
			}
			break
		}
		if res.Cursor == "" {
			t.Fatal("has_next was true but no cursor was returned — the caller cannot continue")
		}
		url = fmt.Sprintf("/pipelines/big/runs?limit=50&after=%s", res.Cursor)
		if pages > 20 {
			t.Fatal("walk did not terminate")
		}
	}
	if len(seen) != total {
		t.Fatalf("reached %d runs, want %d", len(seen), total)
	}
}

// ?page= used to slice an already-truncated 50 runs, so total was capped at
// 50 and deep pages came back empty.
func TestListByPipeline_OffsetPageTotalIsRealAndDeepPagesHaveRows(t *testing.T) {
	s := newRunPagingStore(t)
	seedPipelineRuns(t, s, "paged", 137)
	rt := newRunPagingRouter(s)

	var first struct {
		Items []models.Run `json:"items"`
		Total int          `json:"total"`
	}
	rec := getRuns(t, rt, "/pipelines/paged/runs?page=1&page_size=20")
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode page 1: %v (body %s)", err, rec.Body.String())
	}
	if first.Total != 137 {
		t.Errorf("total = %d, want 137 (was capped at 50 before)", first.Total)
	}
	if len(first.Items) != 20 {
		t.Errorf("page 1 returned %d runs, want 20", len(first.Items))
	}

	// Page 6 sits past the old ceiling and used to be empty.
	var deep struct {
		Items []models.Run `json:"items"`
		Total int          `json:"total"`
	}
	rec = getRuns(t, rt, "/pipelines/paged/runs?page=6&page_size=20")
	if err := json.Unmarshal(rec.Body.Bytes(), &deep); err != nil {
		t.Fatalf("decode page 6: %v", err)
	}
	if len(deep.Items) == 0 {
		t.Fatal("page 6 was empty — deep pages are still unreachable")
	}
	if deep.Total != 137 {
		t.Errorf("total on page 6 = %d, want 137", deep.Total)
	}
	if first.Items[0].ID == deep.Items[0].ID {
		t.Error("page 6 returned the same rows as page 1")
	}
}

// A caller must not be able to turn one request into an unbounded response.
func TestListByPipeline_LimitIsClamped(t *testing.T) {
	s := newRunPagingStore(t)
	seedPipelineRuns(t, s, "clamp", 600)
	rt := newRunPagingRouter(s)

	rec := getRuns(t, rt, "/pipelines/clamp/runs?limit=100000")
	var res struct {
		Items []models.Run `json:"items"`
		Limit int          `json:"limit"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Limit != 500 {
		t.Errorf("limit = %d, want it clamped to 500", res.Limit)
	}
	if len(res.Items) != 500 {
		t.Errorf("returned %d runs, want 500", len(res.Items))
	}
}

// A garbage or non-positive limit falls back to the default rather than
// returning nothing.
func TestListByPipeline_BadLimitFallsBackToDefault(t *testing.T) {
	s := newRunPagingStore(t)
	seedPipelineRuns(t, s, "bad", 80)
	rt := newRunPagingRouter(s)

	for _, q := range []string{"limit=abc", "limit=0", "limit=-5"} {
		rec := getRuns(t, rt, "/pipelines/bad/runs?"+q)
		var res struct {
			Items []models.Run `json:"items"`
			Limit int          `json:"limit"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if res.Limit != 50 {
			t.Errorf("%s: limit = %d, want the default 50", q, res.Limit)
		}
		if len(res.Items) != 50 {
			t.Errorf("%s: returned %d runs, want 50", q, len(res.Items))
		}
	}
}

// An unknown cursor must not spill another pipeline's history or error.
func TestListByPipeline_CursorScopedToPipeline(t *testing.T) {
	s := newRunPagingStore(t)
	seedPipelineRuns(t, s, "a", 10)
	seedPipelineRuns(t, s, "b", 10)
	rt := newRunPagingRouter(s)

	rec := getRuns(t, rt, "/pipelines/a/runs?limit=100")
	var res struct {
		Items   []models.Run `json:"items"`
		HasNext bool         `json:"has_next"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 10 {
		t.Fatalf("got %d runs for pipeline a, want 10", len(res.Items))
	}
	for _, r := range res.Items {
		if r.PipelineID != "a" {
			t.Errorf("leaked a run belonging to %s", r.PipelineID)
		}
	}
	if res.HasNext {
		t.Error("has_next should be false — pipeline b's runs are not a's next page")
	}
}
