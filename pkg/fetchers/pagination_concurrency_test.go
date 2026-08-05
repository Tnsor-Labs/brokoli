package fetchers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// concurrencyTracker records how many requests were in flight at once and
// how many requests were made in total, so tests can assert both the
// max_concurrency bound was respected and that concurrency actually
// happened (not just that it didn't exceed the bound trivially).
type concurrencyTracker struct {
	inFlight int32
	peak     int32
	total    int64
}

func (c *concurrencyTracker) enter() func() {
	cur := atomic.AddInt32(&c.inFlight, 1)
	for {
		old := atomic.LoadInt32(&c.peak)
		if cur <= old || atomic.CompareAndSwapInt32(&c.peak, old, cur) {
			break
		}
	}
	atomic.AddInt64(&c.total, 1)
	return func() { atomic.AddInt32(&c.inFlight, -1) }
}

// offsetPageServer wraps each page's records in an {"items": [...], "end_flag":
// bool} envelope rather than returning a bare JSON array, using the
// explicit `records`/`end_flag` config path. This isn't required for
// correctness (brokoli#44 fixed extractDatasetRecords's default,
// no-`records`-config path to treat a bare empty array as zero records
// too), but the envelope lets these tests assert termination explicitly via
// `end_flag` rather than relying on "ran out of real pages," which keeps
// the max_concurrency assertions below exact and independent of that fix.
func offsetPageServer(t *testing.T, totalRecords int, tracker *concurrencyTracker) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer tracker.enter()()
		// Hold the handler open briefly so concurrent requests actually
		// overlap in wall-clock time instead of completing before the next
		// one starts.
		time.Sleep(15 * time.Millisecond)

		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

		items := []map[string]interface{}{}
		for i := offset; i < offset+limit && i < totalRecords; i++ {
			items = append(items, map[string]interface{}{"id": i})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items":    items,
			"end_flag": offset+limit >= totalRecords,
		})
	}))
}

func numberedPageServer(t *testing.T, totalRecords, pageSize, totalPages int, tracker *concurrencyTracker) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer tracker.enter()()
		time.Sleep(15 * time.Millisecond)

		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		offset := (page - 1) * pageSize

		data := []map[string]interface{}{}
		for i := offset; i < offset+pageSize && i < totalRecords; i++ {
			data = append(data, map[string]interface{}{"id": i})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"data":        data,
			"total_pages": totalPages,
		})
	}))
}

func extractIDs(t *testing.T, rows []common.DataRow) []int {
	t.Helper()
	ids := make([]int, len(rows))
	for i, row := range rows {
		switch v := row["id"].(type) {
		case float64:
			ids[i] = int(v)
		case int:
			ids[i] = v
		default:
			t.Fatalf("row %d: id has unexpected type %T", i, row["id"])
		}
	}
	return ids
}

func TestFetchPaginated_Offset_SequentialByDefault(t *testing.T) {
	tracker := &concurrencyTracker{}
	server := offsetPageServer(t, 6, tracker)
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
			"end_flag":  "end_flag",
		},
		"records": "items",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	ids := extractIDs(t, ds.Rows)
	want := []int{0, 1, 2, 3, 4, 5}
	if len(ids) != len(want) {
		t.Fatalf("got %d records, want %d: %v", len(ids), len(want), ids)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("record %d: got id %d, want %d (full: %v)", i, ids[i], id, ids)
		}
	}

	if peak := atomic.LoadInt32(&tracker.peak); peak != 1 {
		t.Errorf("max_concurrency unset: expected peak concurrency 1, got %d", peak)
	}
}

func TestFetchPaginated_Offset_BoundedConcurrency(t *testing.T) {
	tracker := &concurrencyTracker{}
	server := offsetPageServer(t, 10, tracker)
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
			"end_flag":  "end_flag",
		},
		"execution": map[string]interface{}{
			"max_concurrency": 3,
		},
		"records": "items",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	ids := extractIDs(t, ds.Rows)
	want := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
	if len(ids) != len(want) {
		t.Fatalf("got %d records, want %d: %v", len(ids), len(want), ids)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("record %d: got id %d, want %d — ordering must be preserved even though pages were fetched concurrently (full: %v)", i, ids[i], id, ids)
		}
	}

	if peak := atomic.LoadInt32(&tracker.peak); peak < 2 {
		t.Errorf("expected pages to actually be fetched concurrently (peak > 1), got peak=%d", peak)
	}
	if peak := atomic.LoadInt32(&tracker.peak); peak > 3 {
		t.Errorf("max_concurrency=3 exceeded: peak concurrent requests was %d", peak)
	}

	// 10 records / page_size 2 = 5 real pages (end_flag arrives on the 5th).
	// Batched 3-at-a-time: batch 1 (offsets 0,2,4) has no way to know ahead
	// of time that only 2 more pages remain, so batch 2 (offsets 6,8,10)
	// speculatively dispatches a 3rd page (offset 10) that turns out to be
	// unneeded once offset 8's end_flag is seen — that's the documented
	// "wasted work, correctness preserved" tradeoff of batching ahead of
	// the stop point.
	if total := atomic.LoadInt64(&tracker.total); total != 6 {
		t.Errorf("expected 6 HTTP requests (5 data pages + 1 speculative page wasted past the stop point), got %d", total)
	}
}

func TestFetchPaginated_Offset_MaxRecordsTruncatesMidBatch(t *testing.T) {
	tracker := &concurrencyTracker{}
	server := offsetPageServer(t, 1000, tracker)
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":    "offset",
			"page_size":   2,
			"max_records": 5,
		},
		"execution": map[string]interface{}{
			"max_concurrency": 3,
		},
		"records": "items",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	ids := extractIDs(t, ds.Rows)
	want := []int{0, 1, 2, 3, 4}
	if len(ids) != len(want) {
		t.Fatalf("got %d records, want exactly max_records=%d: %v", len(ids), len(want), ids)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("record %d: got id %d, want %d", i, ids[i], id)
		}
	}

	// max_records=5 is hit while processing the 3rd page of the first
	// (and only) batch of 3 — no second batch should ever be dispatched.
	if total := atomic.LoadInt64(&tracker.total); total != 3 {
		t.Errorf("expected exactly 3 requests (truncation stops before batch 2 dispatches), got %d", total)
	}
}

func TestFetchPaginated_Numbered_BoundedConcurrency(t *testing.T) {
	tracker := &concurrencyTracker{}
	// 4 pages of 3 records each = 12 records.
	server := numberedPageServer(t, 12, 3, 4, tracker)
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":         "numbered",
			"total_pages_path": "total_pages",
		},
		"execution": map[string]interface{}{
			"max_concurrency": 4,
		},
		"records": "data",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	ids := extractIDs(t, ds.Rows)
	if len(ids) != 12 {
		t.Fatalf("got %d records, want 12: %v", len(ids), ids)
	}
	for i := 0; i < 12; i++ {
		if ids[i] != i {
			t.Errorf("record %d: got id %d, want %d — ordering must be preserved", i, ids[i], i)
		}
	}

	// total_pages=4 is known from the very first page's response, so with
	// max_concurrency=4 all 4 pages are dispatched in a single batch — no
	// wasted "discovery" request needed here, unlike the offset/empty-page
	// stop signal.
	if total := atomic.LoadInt64(&tracker.total); total != 4 {
		t.Errorf("expected exactly 4 requests, got %d", total)
	}
	if peak := atomic.LoadInt32(&tracker.peak); peak < 2 {
		t.Errorf("expected concurrent dispatch (peak > 1), got peak=%d", peak)
	}
}

func TestFetchPaginated_CursorStrategy_IgnoresMaxConcurrency(t *testing.T) {
	tracker := &concurrencyTracker{}
	pages := [][]map[string]interface{}{
		{{"id": 0}, {"id": 1}},
		{{"id": 2}, {"id": 3}},
		{},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer tracker.enter()()
		time.Sleep(10 * time.Millisecond)

		cursor := r.URL.Query().Get("cursor")
		idx := 0
		if cursor != "" {
			n, _ := strconv.Atoi(cursor)
			idx = n
		}
		body := map[string]interface{}{"items": pages[idx]}
		if idx < len(pages)-1 {
			body["next_cursor"] = strconv.Itoa(idx + 1)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":     "cursor",
			"cursor_path":  "next_cursor",
			"cursor_param": "cursor",
		},
		"execution": map[string]interface{}{
			// Deliberately set on a strategy that can't use it — must be a
			// no-op, not an error, and must not change the sequential
			// fetch order (each request depends on the previous response).
			"max_concurrency": 5,
		},
		"records": "items",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	if len(ds.Rows) != 4 {
		t.Fatalf("got %d records, want 4: %v", len(ds.Rows), ds.Rows)
	}

	if peak := atomic.LoadInt32(&tracker.peak); peak != 1 {
		t.Errorf("cursor strategy must stay sequential regardless of max_concurrency, got peak concurrency %d", peak)
	}
}

// TestFetchPaginated_ConcurrencyWithRateLimit exercises max_concurrency and
// requests_per_second together — the combination this PR's rate-limiter
// mutex exists to make safe. Run with -race; the assertion here is just
// that it completes without racing and returns correct data.
func TestFetchPaginated_ConcurrencyWithRateLimit(t *testing.T) {
	tracker := &concurrencyTracker{}
	server := offsetPageServer(t, 8, tracker)
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": 2,
			"end_flag":  "end_flag",
		},
		"execution": map[string]interface{}{
			"max_concurrency":     3,
			"requests_per_second": 50.0,
		},
		"records": "items",
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(ds.Rows) != 8 {
		t.Fatalf("got %d records, want 8: %v", len(ds.Rows), ds.Rows)
	}
}
