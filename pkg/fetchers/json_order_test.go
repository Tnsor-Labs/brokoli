package fetchers

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
)

// An API source's columns follow the order of the response's keys, across
// every page of a paginated one, instead of a map's.

func TestAResponsesColumnsKeepItsKeyOrder(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"zeta": 1, "alpha": 2, "mid": 3}]`))
	}))
	defer srv.Close()
	want := []string{"zeta", "alpha", "mid"}
	for i := 0; i < 20; i++ {
		ds, err := (&RESTFetcher{}).Fetch(srv.URL, map[string]interface{}{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ds.Columns, want) {
			t.Fatalf("fetch %d: columns %v, want %v", i, ds.Columns, want)
		}
	}
}

func TestPaginatedColumnsKeepFirstAppearanceAcrossPages(t *testing.T) {
	pages := []string{
		`{"data": [{"zeta": 1, "alpha": 2}], "total_pages": 2}`,
		`{"data": [{"alpha": 3, "zeta": 4, "added": 5}], "total_pages": 2}`,
	}
	want := []string{"zeta", "alpha", "added"}
	for i := 0; i < 20; i++ {
		var n int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := atomic.AddInt32(&n, 1) - 1
			if int(k) >= len(pages) {
				k = int32(len(pages) - 1)
			}
			_, _ = w.Write([]byte(pages[k]))
		}))
		ds, err := (&RESTFetcher{}).Fetch(srv.URL, map[string]interface{}{
			"pagination": map[string]interface{}{"strategy": "numbered", "total_pages_path": "total_pages"},
			"records":    "data",
		})
		srv.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ds.Columns, want) {
			t.Fatalf("fetch %d: columns %v, want %v", i, ds.Columns, want)
		}
	}
}
