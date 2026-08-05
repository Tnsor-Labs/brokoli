package fetchers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// recordingSaver collects every checkpoint save call, so tests can assert
// both the cadence (how often onCheckpoint fires) and the content (position
// + accumulated records at each point).
type recordingSaver struct {
	mu    sync.Mutex
	calls []recordedCheckpoint
}

type recordedCheckpoint struct {
	checkpoint PaginationCheckpoint
	records    []map[string]interface{}
}

func (s *recordingSaver) save(cp PaginationCheckpoint, records []map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	recordsCopy := make([]map[string]interface{}, len(records))
	copy(recordsCopy, records)
	s.calls = append(s.calls, recordedCheckpoint{checkpoint: cp, records: recordsCopy})
	return nil
}

func (s *recordingSaver) snapshot() []recordedCheckpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]recordedCheckpoint, len(s.calls))
	copy(out, s.calls)
	return out
}

func TestFetchPaginatedResumable_Offset_CheckspointsAtCadence(t *testing.T) {
	tracker := &concurrencyTracker{}
	server := offsetPageServer(t, 10, tracker)
	defer server.Close()

	saver := &recordingSaver{}
	f := &RESTFetcher{}
	ds, err := f.FetchPaginatedResumable(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
			"end_flag":  "end_flag",
		},
		"execution": map[string]interface{}{
			"checkpoint_every": 2,
		},
		"records": "items",
	}, nil, nil, saver.save)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(ds.Rows) != 10 {
		t.Fatalf("got %d records, want 10", len(ds.Rows))
	}

	calls := saver.snapshot()
	// 5 real pages, checkpoint_every=2 → checkpoints fire after page 2 and
	// page 4 (page 5's append triggers the natural stop before a 3rd
	// checkpoint would fire, since checkpointing happens after append, not
	// before the stop check).
	if len(calls) != 2 {
		t.Fatalf("expected 2 checkpoint saves, got %d: %+v", len(calls), calls)
	}
	if calls[0].checkpoint.PagesFetched != 2 || calls[0].checkpoint.Offset != 4 || calls[0].checkpoint.RecordCount != 4 {
		t.Errorf("checkpoint 1: got %+v, want PagesFetched=2 Offset=4 RecordCount=4", calls[0].checkpoint)
	}
	if len(calls[0].records) != 4 {
		t.Errorf("checkpoint 1: got %d records, want 4 (all of them — nothing checkpointed yet)", len(calls[0].records))
	}
	if calls[1].checkpoint.PagesFetched != 4 || calls[1].checkpoint.Offset != 8 || calls[1].checkpoint.RecordCount != 8 {
		t.Errorf("checkpoint 2: got %+v, want PagesFetched=4 Offset=8 RecordCount=8", calls[1].checkpoint)
	}
	if len(calls[1].records) != 4 {
		t.Errorf("checkpoint 2: got %d records, want 4 (only the delta since checkpoint 1, not all 8 — issue #52)", len(calls[1].records))
	}
}

func TestFetchPaginatedResumable_Offset_ResumesFromCheckpoint(t *testing.T) {
	tracker := &concurrencyTracker{}
	server := offsetPageServer(t, 10, tracker)
	defer server.Close()

	f := &RESTFetcher{}
	resume := &PaginationCheckpoint{Strategy: "offset", Offset: 6, PagesFetched: 3}
	resumeRecords := []map[string]interface{}{
		{"id": float64(0)}, {"id": float64(1)}, {"id": float64(2)},
		{"id": float64(3)}, {"id": float64(4)}, {"id": float64(5)},
	}

	ds, err := f.FetchPaginatedResumable(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
			"end_flag":  "end_flag",
		},
		"records": "items",
	}, resume, resumeRecords, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	ids := extractIDs(t, ds.Rows)
	if len(ids) != 10 {
		t.Fatalf("got %d records, want 10 (6 resumed + 4 newly fetched): %v", len(ids), ids)
	}
	for i := 0; i < 10; i++ {
		if ids[i] != i {
			t.Errorf("record %d: got id %d, want %d", i, ids[i], i)
		}
	}

	// Only offsets 6 and 8 should actually be fetched over the wire — pages
	// 0/2/4 came from the checkpoint, not a real request.
	if total := tracker.total; total != 2 {
		t.Errorf("expected exactly 2 HTTP requests (resuming, not re-fetching), got %d", total)
	}
}

func TestFetchPaginatedResumable_Cursor_ResumesFromCheckpoint(t *testing.T) {
	pages := map[string][]map[string]interface{}{
		"":  {{"id": 0}, {"id": 1}},
		"a": {{"id": 2}, {"id": 3}},
		"b": {{"id": 4}, {"id": 5}},
	}
	next := map[string]string{"": "a", "a": "b", "b": ""}

	var requestedCursors []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		mu.Lock()
		requestedCursors = append(requestedCursors, cursor)
		mu.Unlock()

		body := map[string]interface{}{"items": pages[cursor]}
		if n := next[cursor]; n != "" {
			body["next_cursor"] = n
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()

	f := &RESTFetcher{}
	// Simulate having already fetched the "" and "a" pages (cursor="a" was
	// discovered as the next cursor to fetch, 2 pages already in).
	resume := &PaginationCheckpoint{Strategy: "cursor", Cursor: "a", PagesFetched: 1}
	resumeRecords := []map[string]interface{}{{"id": float64(0)}, {"id": float64(1)}}

	ds, err := f.FetchPaginatedResumable(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":     "cursor",
			"cursor_path":  "next_cursor",
			"cursor_param": "cursor",
		},
		"records": "items",
	}, resume, resumeRecords, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if len(ds.Rows) != 6 {
		t.Fatalf("got %d records, want 6: %v", len(ds.Rows), ds.Rows)
	}

	mu.Lock()
	defer mu.Unlock()
	want := []string{"a", "b"}
	if len(requestedCursors) != len(want) {
		t.Fatalf("expected requests for cursors %v, got %v", want, requestedCursors)
	}
	for i, c := range want {
		if requestedCursors[i] != c {
			t.Errorf("request %d: got cursor %q, want %q", i, requestedCursors[i], c)
		}
	}
}

func TestFetchPaginatedResumable_StrategyMismatchIgnoresCheckpoint(t *testing.T) {
	tracker := &concurrencyTracker{}
	server := offsetPageServer(t, 4, tracker)
	defer server.Close()

	f := &RESTFetcher{}
	// A checkpoint from a different strategy (e.g. pipeline edited between
	// attempts) must not be misinterpreted — the fetch should start fresh.
	resume := &PaginationCheckpoint{Strategy: "numbered", Page: 5, PagesFetched: 4}
	resumeRecords := []map[string]interface{}{{"id": "bogus"}}

	ds, err := f.FetchPaginatedResumable(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
			"end_flag":  "end_flag",
		},
		"records": "items",
	}, resume, resumeRecords, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	ids := extractIDs(t, ds.Rows)
	want := []int{0, 1, 2, 3}
	if len(ids) != len(want) {
		t.Fatalf("got %d records, want a fresh fetch of %d: %v", len(ids), len(want), ids)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("record %d: got id %d, want %d — mismatched checkpoint must be discarded, not applied", i, ids[i], id)
		}
	}
}

// TestFetchPaginatedResumable_NoCheckpointEvery_NeverCallsSaver confirms
// checkpoint_every unset means zero overhead and zero calls to onCheckpoint
// — checkpointing must be strictly opt-in.
func TestFetchPaginatedResumable_NoCheckpointEvery_NeverCallsSaver(t *testing.T) {
	tracker := &concurrencyTracker{}
	server := offsetPageServer(t, 10, tracker)
	defer server.Close()

	saver := &recordingSaver{}
	f := &RESTFetcher{}
	_, err := f.FetchPaginatedResumable(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
			"end_flag":  "end_flag",
		},
		"records": "items",
	}, nil, nil, saver.save)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if calls := saver.snapshot(); len(calls) != 0 {
		t.Errorf("checkpoint_every unset: expected zero checkpoint saves, got %d", len(calls))
	}
}

// TestRESTFetcherImplementsCheckpointingFetcher is a compile-time-adjacent
// sanity check that RESTFetcher satisfies the interface runSourceAPI type-
// asserts against.
func TestRESTFetcherImplementsCheckpointingFetcher(t *testing.T) {
	var _ CheckpointingFetcher = (*RESTFetcher)(nil)
}

// TestFetchPaginatedResumable_FailedCheckpointSaveRetriedNextInterval
// covers issue #52's "don't lose records on a failed save" guarantee: if
// onCheckpoint returns an error, lastCheckpointedCount must not advance —
// the next successful checkpoint's delta should include the records from
// the failed attempt too, not skip past them.
func TestFetchPaginatedResumable_FailedCheckpointSaveRetriedNextInterval(t *testing.T) {
	tracker := &concurrencyTracker{}
	server := offsetPageServer(t, 12, tracker)
	defer server.Close()

	var callCount int
	var mu sync.Mutex
	var calls []recordedCheckpoint
	saver := func(cp PaginationCheckpoint, records []map[string]interface{}) error {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		if callCount == 1 {
			return errors.New("simulated transient save failure")
		}
		recordsCopy := make([]map[string]interface{}, len(records))
		copy(recordsCopy, records)
		calls = append(calls, recordedCheckpoint{checkpoint: cp, records: recordsCopy})
		return nil
	}

	f := &RESTFetcher{}
	ds, err := f.FetchPaginatedResumable(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
			"end_flag":  "end_flag",
		},
		"execution": map[string]interface{}{
			"checkpoint_every": 2,
		},
		"records": "items",
	}, nil, nil, saver)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(ds.Rows) != 12 {
		t.Fatalf("got %d records, want 12", len(ds.Rows))
	}

	mu.Lock()
	defer mu.Unlock()
	// 12 records / page_size 2 = 6 real pages; checkpoints are attempted
	// after pagesFetched=2 and pagesFetched=4 (the 6th and final page
	// triggers the stop condition and returns before its own checkpoint
	// call, same as the no-failure cadence test above) — so exactly 2
	// attempts total: the first fails, the second succeeds.
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 successful save, got %d: %+v", len(calls), calls)
	}
	// That one successful save must carry BOTH the failed attempt's delta
	// and its own — 8 records, not 4 — since the failed delta was never
	// consumed.
	if len(calls[0].records) != 8 {
		t.Errorf("successful save: got %d records, want 8 (the failed attempt's delta + this checkpoint's own delta)", len(calls[0].records))
	}
	if calls[0].checkpoint.RecordCount != 8 {
		t.Errorf("successful save: RecordCount = %d, want 8", calls[0].checkpoint.RecordCount)
	}
}
