package fetchers

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/errors"
)

// TestRESTFetcher_Records_ExtractsNestedArray is the concrete regression
// test for issue #30: a {"results": [...], "endOfRecords": true}-shaped
// envelope must have its nested array extracted as rows via `records`, not
// wrapped whole as a single row (ParseJSONData's auto-detection previously
// mishandled exactly this GBIF-style shape).
func TestRESTFetcher_Records_ExtractsNestedArray(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		errors.CheckErrorMultiple(w.Write([]byte(`{
			"results": [
				{"key": 1, "scientificName": "Aves"},
				{"key": 2, "scientificName": "Mammalia"}
			],
			"endOfRecords": true,
			"count": 2
		}`)))
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"response": "dataset",
		"records":  "results",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("expected 2 rows extracted from 'results', got %d: %+v", len(ds.Rows), ds.Rows)
	}
	if ds.Rows[0]["scientificName"] != "Aves" {
		t.Errorf("expected first row scientificName=Aves, got %v", ds.Rows[0]["scientificName"])
	}
	if ds.Rows[1]["key"] != float64(2) {
		t.Errorf("expected second row key=2, got %v", ds.Rows[1]["key"])
	}
	// Must NOT contain the envelope's own top-level keys as columns.
	for _, col := range ds.Columns {
		if col == "endOfRecords" || col == "count" {
			t.Errorf("envelope key %q leaked into extracted record columns %v", col, ds.Columns)
		}
	}
}

// TestRESTFetcher_Records_NestedDotPath covers a nested envelope shape,
// e.g. {"data": {"items": [...]}}.
func TestRESTFetcher_Records_NestedDotPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		errors.CheckErrorMultiple(w.Write([]byte(`{"data": {"items": [{"id": 1}, {"id": 2}, {"id": 3}]}}`)))
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"records": "data.items",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(ds.Rows))
	}
}

// TestRESTFetcher_Scalar_ExtractsValuePath covers response="scalar" +
// value_path, returning a single extracted value.
func TestRESTFetcher_Scalar_ExtractsValuePath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		errors.CheckErrorMultiple(w.Write([]byte(`{"meta": {"total_count": 42}}`)))
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"response":   "scalar",
		"value_path": "meta.total_count",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("expected exactly 1 row for scalar response, got %d", len(ds.Rows))
	}
	if ds.Rows[0]["value"] != float64(42) {
		t.Errorf("expected value=42, got %v", ds.Rows[0]["value"])
	}
}

// TestRESTFetcher_Scalar_MissingPathErrors ensures an absent value_path
// surfaces as an error rather than silently returning an empty/zero value.
func TestRESTFetcher_Scalar_MissingPathErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		errors.CheckErrorMultiple(w.Write([]byte(`{"meta": {"total_count": 42}}`)))
	}))
	defer server.Close()

	f := &RESTFetcher{}
	_, err := f.Fetch(server.URL, map[string]interface{}{
		"response":   "scalar",
		"value_path": "meta.does_not_exist",
	})
	if err == nil {
		t.Fatal("expected error for missing value_path, got nil")
	}
}

// TestRESTFetcher_Artifact_ReturnsRawBody covers response="artifact".
func TestRESTFetcher_Artifact_ReturnsRawBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		errors.CheckErrorMultiple(w.Write([]byte(`not-json-at-all`)))
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"response": "artifact",
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 1 || ds.Rows[0]["value"] != "not-json-at-all" {
		t.Errorf("expected raw body preserved as artifact value, got %+v", ds.Rows)
	}
}

// TestRESTFetcher_NoNewFields_PinsAutoDetectBehavior is the regression test:
// a node with none of response/records/value_path/params/pagination set
// must behave exactly as before this change (ParseJSONData auto-detection),
// for pipelines deployed before brokoli-sdk#1.
func TestRESTFetcher_NoNewFields_PinsAutoDetectBehavior(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/array":
			errors.CheckErrorMultiple(w.Write([]byte(`[{"id":1},{"id":2}]`)))
		case "/envelope":
			// Pre-brokoli-sdk#1 behavior: an envelope-shaped object with no
			// `records` config is wrapped whole as ONE row — this is the
			// (arguably wrong, but load-bearing for backward compatibility)
			// legacy behavior this PR must not change for old pipelines.
			errors.CheckErrorMultiple(w.Write([]byte(`{"results":[{"id":1},{"id":2}],"endOfRecords":true}`)))
		}
	}))
	defer server.Close()

	f := &RESTFetcher{}

	ds, err := f.Fetch(server.URL+"/array", map[string]interface{}{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("expected 2 rows for array auto-detect, got %d", len(ds.Rows))
	}

	ds, err = f.Fetch(server.URL+"/envelope", map[string]interface{}{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("expected legacy auto-detect to wrap the envelope as exactly 1 row, got %d", len(ds.Rows))
	}
	if _, ok := ds.Rows[0]["results"]; !ok {
		t.Errorf("expected legacy row to contain the raw 'results' key, got %+v", ds.Rows[0])
	}
}

// TestRESTFetcher_Params_AppendedToURL verifies query params are merged
// onto the request URL.
func TestRESTFetcher_Params_AppendedToURL(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		errors.CheckErrorMultiple(w.Write([]byte(`[{"id":1}]`)))
	}))
	defer server.Close()

	f := &RESTFetcher{}
	_, err := f.Fetch(server.URL, map[string]interface{}{
		"params": map[string]interface{}{
			"hasCoordinate":    true,
			"occurrenceStatus": "PRESENT",
		},
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}

	q := httptest.NewRequest("GET", "/?"+gotQuery, nil).URL.Query()
	if q.Get("hasCoordinate") != "true" {
		t.Errorf("expected hasCoordinate=true in query, got query=%q", gotQuery)
	}
	if q.Get("occurrenceStatus") != "PRESENT" {
		t.Errorf("expected occurrenceStatus=PRESENT in query, got query=%q", gotQuery)
	}
}

// TestRESTFetcher_Params_MergedWithExistingURLQuery ensures params are
// merged onto (not replacing) any query string already present in the URL.
func TestRESTFetcher_Params_MergedWithExistingURLQuery(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		errors.CheckErrorMultiple(w.Write([]byte(`[{"id":1}]`)))
	}))
	defer server.Close()

	f := &RESTFetcher{}
	_, err := f.Fetch(server.URL+"?existing=1", map[string]interface{}{
		"params": map[string]interface{}{"extra": "2"},
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	q := httptest.NewRequest("GET", "/?"+gotQuery, nil).URL.Query()
	if q.Get("existing") != "1" || q.Get("extra") != "2" {
		t.Errorf("expected both existing and extra params present, got query=%q", gotQuery)
	}
}

// TestRESTFetcher_Pagination_Offset drives the "offset" strategy across a
// small paginated fixture server until max_records/end_flag terminates it,
// and verifies the combined dataset across all pages.
func TestRESTFetcher_Pagination_Offset(t *testing.T) {
	var requestCount int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requestCount, 1)
		offset := r.URL.Query().Get("offset")
		limit := r.URL.Query().Get("limit")
		if limit != "2" {
			t.Errorf("expected limit=2 on every page, got %q", limit)
		}
		w.Header().Set("Content-Type", "application/json")
		switch offset {
		case "0", "":
			errors.CheckErrorMultiple(w.Write([]byte(`{"results":[{"id":1},{"id":2}],"endOfRecords":false}`)))
		case "2":
			errors.CheckErrorMultiple(w.Write([]byte(`{"results":[{"id":3},{"id":4}],"endOfRecords":false}`)))
		case "4":
			errors.CheckErrorMultiple(w.Write([]byte(`{"results":[{"id":5}],"endOfRecords":true}`)))
		default:
			t.Fatalf("unexpected offset requested: %q", offset)
		}
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"records": "results",
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": float64(2),
			"end_flag":  "endOfRecords",
		},
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 5 {
		t.Fatalf("expected 5 combined rows across 3 pages, got %d: %+v", len(ds.Rows), ds.Rows)
	}
	if got := atomic.LoadInt32(&requestCount); got != 3 {
		t.Errorf("expected exactly 3 page requests, got %d", got)
	}
}

// TestRESTFetcher_Pagination_Offset_MaxRecords verifies max_records
// terminates the loop (and truncates the final page) even if end_flag never
// fires.
func TestRESTFetcher_Pagination_Offset_MaxRecords(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "application/json")
		switch offset {
		case "0", "":
			errors.CheckErrorMultiple(w.Write([]byte(`{"results":[{"id":1},{"id":2},{"id":3}]}`)))
		case "3":
			errors.CheckErrorMultiple(w.Write([]byte(`{"results":[{"id":4},{"id":5},{"id":6}]}`)))
		default:
			t.Fatalf("should not have requested a third page, got offset=%q", offset)
		}
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"records": "results",
		"pagination": map[string]interface{}{
			"strategy":    "offset",
			"page_size":   float64(3),
			"max_records": float64(4),
		},
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 4 {
		t.Fatalf("expected max_records=4 to truncate combined rows to 4, got %d", len(ds.Rows))
	}
}

// TestRESTFetcher_Pagination_Numbered drives the "numbered" strategy using
// total_pages_path to know when to stop.
func TestRESTFetcher_Pagination_Numbered(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		w.Header().Set("Content-Type", "application/json")
		switch page {
		case "1":
			errors.CheckErrorMultiple(w.Write([]byte(`{"items":[{"id":1}],"total_pages":2}`)))
		case "2":
			errors.CheckErrorMultiple(w.Write([]byte(`{"items":[{"id":2}],"total_pages":2}`)))
		default:
			t.Fatalf("unexpected page requested: %q", page)
		}
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"records": "items",
		"pagination": map[string]interface{}{
			"strategy":         "numbered",
			"page_param":       "page",
			"total_pages_path": "total_pages",
		},
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("expected 2 combined rows, got %d", len(ds.Rows))
	}
}

// TestRESTFetcher_Pagination_Cursor drives the "cursor" strategy, following
// a next-cursor value out of the response body until it's absent.
func TestRESTFetcher_Pagination_Cursor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		w.Header().Set("Content-Type", "application/json")
		switch cursor {
		case "":
			errors.CheckErrorMultiple(w.Write([]byte(`{"data":[{"id":1}],"meta":{"next_cursor":"abc"}}`)))
		case "abc":
			errors.CheckErrorMultiple(w.Write([]byte(`{"data":[{"id":2}],"meta":{"next_cursor":""}}`)))
		default:
			t.Fatalf("unexpected cursor requested: %q", cursor)
		}
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"records": "data",
		"pagination": map[string]interface{}{
			"strategy":     "cursor",
			"cursor_path":  "meta.next_cursor",
			"cursor_param": "cursor",
		},
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("expected 2 combined rows, got %d", len(ds.Rows))
	}
}

// TestRESTFetcher_Pagination_NextLink follows a next-page URL embedded in
// the response body.
func TestRESTFetcher_Pagination_NextLink(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/page1":
			errors.CheckErrorMultiple(w.Write([]byte(`{"data":[{"id":1}],"paging":{"next":"` + server.URL + `/page2"}}`)))
		case "/page2":
			errors.CheckErrorMultiple(w.Write([]byte(`{"data":[{"id":2}],"paging":{"next":null}}`)))
		default:
			t.Fatalf("unexpected path requested: %q", r.URL.Path)
		}
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL+"/page1", map[string]interface{}{
		"records": "data",
		"pagination": map[string]interface{}{
			"strategy":  "next_link",
			"next_path": "paging.next",
		},
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("expected 2 combined rows, got %d", len(ds.Rows))
	}
}

// TestRESTFetcher_Pagination_LinkHeader follows RFC 8288 Link headers.
func TestRESTFetcher_Pagination_LinkHeader(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/page1":
			w.Header().Set("Link", `<`+server.URL+`/page2>; rel="next"`)
			errors.CheckErrorMultiple(w.Write([]byte(`[{"id":1}]`)))
		case "/page2":
			// No Link header at all on the last page.
			errors.CheckErrorMultiple(w.Write([]byte(`[{"id":2}]`)))
		default:
			t.Fatalf("unexpected path requested: %q", r.URL.Path)
		}
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL+"/page1", map[string]interface{}{
		"pagination": map[string]interface{}{
			"strategy": "link_header",
			"rel":      "next",
		},
	})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(ds.Rows) != 2 {
		t.Fatalf("expected 2 combined rows, got %d", len(ds.Rows))
	}
}

// TestRESTFetcher_Pagination_PageRetry_SucceedsAfterTransientFailure covers
// page-level retry: a page that fails once (500) and succeeds on retry must
// not fail the whole node.
func TestRESTFetcher_Pagination_PageRetry_SucceedsAfterTransientFailure(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset := r.URL.Query().Get("offset")
		w.Header().Set("Content-Type", "application/json")
		if offset == "0" || offset == "" {
			n := atomic.AddInt32(&attempts, 1)
			if n == 1 {
				w.WriteHeader(http.StatusInternalServerError)
				errors.CheckErrorMultiple(w.Write([]byte(`{"error":"transient"}`)))
				return
			}
			errors.CheckErrorMultiple(w.Write([]byte(`{"results":[{"id":1}],"endOfRecords":true}`)))
			return
		}
		t.Fatalf("unexpected offset: %q", offset)
	}))
	defer server.Close()

	f := &RESTFetcher{}
	ds, err := f.Fetch(server.URL, map[string]interface{}{
		"records": "results",
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": float64(10),
			"end_flag":  "endOfRecords",
		},
	})
	if err != nil {
		t.Fatalf("expected page-level retry to recover from one transient failure, got error: %v", err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("expected 1 row after retry recovery, got %d", len(ds.Rows))
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("expected exactly 2 attempts (1 failure + 1 success), got %d", got)
	}
}

// TestRESTFetcher_Pagination_PageRetry_FailsPastRetryLimit covers the other
// half: a page that fails on every attempt must fail the whole node once
// max_retries is exhausted, without retrying forever.
func TestRESTFetcher_Pagination_PageRetry_FailsPastRetryLimit(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
		errors.CheckErrorMultiple(w.Write([]byte(`{"error":"down"}`)))
	}))
	defer server.Close()

	f := &RESTFetcher{}
	_, err := f.Fetch(server.URL, map[string]interface{}{
		"records":     "results",
		"max_retries": float64(1),
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": float64(10),
			"end_flag":  "endOfRecords",
		},
	})
	if err == nil {
		t.Fatal("expected error once retries are exhausted, got nil")
	}
	// max_retries=1 means 1 retry after the first attempt => 2 total attempts.
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("expected exactly 2 attempts (1 initial + 1 retry), got %d", got)
	}
}
