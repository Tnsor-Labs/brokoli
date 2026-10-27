package fetchers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/errors"
)

const bigID = int64(9007199254740993)

// A source_api row decoded every number as a float64, so an id above 2^53
// arrived changed.
func TestRESTFetcherKeepsIntegerIDsExact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		errors.CheckErrorMultiple(w.Write([]byte(`[{"id": 9007199254740993, "score": 0.25}]`)))
	}))
	defer server.Close()

	ds, err := (&RESTFetcher{}).Fetch(server.URL, map[string]interface{}{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got, ok := ds.Rows[0]["id"].(int64); !ok || got != bigID {
		t.Fatalf("id = %v (%T), want int64 %d", ds.Rows[0]["id"], ds.Rows[0]["id"], bigID)
	}
	if got, ok := ds.Rows[0]["score"].(float64); !ok || got != 0.25 {
		t.Fatalf("score = %v (%T), want float64 0.25", ds.Rows[0]["score"], ds.Rows[0]["score"])
	}
}

func TestRESTFetcherScalarKeepsIntegersExact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		errors.CheckErrorMultiple(w.Write([]byte(`{"stats": {"max_id": 9007199254740993}}`)))
	}))
	defer server.Close()

	ds, err := (&RESTFetcher{}).Fetch(server.URL, map[string]interface{}{"response": "scalar", "value_path": "stats.max_id"})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got, ok := ds.Rows[0]["value"].(int64); !ok || got != bigID {
		t.Fatalf("value = %v (%T), want int64 %d", ds.Rows[0]["value"], ds.Rows[0]["value"], bigID)
	}
}

// A numeric cursor was read as a float64 and sent back formatted with %v:
// 1234567 went out as "1.234567e+06", and a large one lost digits, so the
// API was asked for a page that does not exist.
func TestRESTFetcherSendsANumericCursorBackAsWritten(t *testing.T) {
	var requested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cursor := r.URL.Query().Get("cursor")
		requested = append(requested, cursor)
		w.Header().Set("Content-Type", "application/json")
		switch cursor {
		case "":
			errors.CheckErrorMultiple(w.Write([]byte(`{"data":[{"id":1}],"next":1234567}`)))
		case "1234567":
			errors.CheckErrorMultiple(w.Write([]byte(`{"data":[{"id":2}],"next":9007199254740993}`)))
		case "9007199254740993":
			errors.CheckErrorMultiple(w.Write([]byte(`{"data":[{"id":3}],"next":null}`)))
		default:
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()

	ds, err := (&RESTFetcher{}).Fetch(server.URL, map[string]interface{}{
		"records": "data",
		"pagination": map[string]interface{}{
			"strategy": "cursor", "cursor_path": "next", "cursor_param": "cursor",
		},
	})
	if err != nil {
		t.Fatalf("Fetch: %v (cursors requested: %q)", err, requested)
	}
	if len(ds.Rows) != 3 {
		t.Fatalf("got %d rows, want 3 (cursors requested: %q)", len(ds.Rows), requested)
	}
}

// total_pages_path now reads an int64; the numbered strategy must still
// stop after that many pages.
func TestRESTFetcherNumberedStopsAtAnIntegerTotalPages(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		errors.CheckErrorMultiple(w.Write([]byte(`{"data":[{"id":1}],"total_pages":2}`)))
	}))
	defer server.Close()

	ds, err := (&RESTFetcher{}).Fetch(server.URL, map[string]interface{}{
		"records": "data",
		"pagination": map[string]interface{}{
			"strategy": "numbered", "page_param": "page", "total_pages_path": "total_pages",
		},
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if calls != 2 || len(ds.Rows) != 2 {
		t.Fatalf("fetched %d pages and %d rows, want 2 and 2", calls, len(ds.Rows))
	}
}
