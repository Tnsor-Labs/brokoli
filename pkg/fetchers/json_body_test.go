package fetchers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A body given as JSON in the node's config is sent as JSON, with a JSON
// content type unless the node sets one. It used to be dropped, so the
// request went out empty.
func TestRESTFetcherSendsAJSONBody(t *testing.T) {
	var gotBody, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotType = string(b), r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"ok": true}]`))
	}))
	defer srv.Close()

	for _, c := range []struct {
		body             interface{}
		headers          map[string]interface{}
		wantBody, wantCT string
	}{
		{map[string]interface{}{"id": int64(9007199254740993), "tags": []interface{}{"a"}}, nil, `{"id":9007199254740993,"tags":["a"]}`, "application/json"},
		{[]interface{}{float64(1), "x"}, nil, `[1,"x"]`, "application/json"},
		{map[string]interface{}{"a": "b"}, map[string]interface{}{"Content-Type": "application/vnd.api+json"}, `{"a":"b"}`, "application/vnd.api+json"},
		{"raw text", nil, "raw text", ""},
	} {
		opts := map[string]interface{}{"method": "POST", "body": c.body}
		if c.headers != nil {
			opts["headers"] = c.headers
		}
		if _, err := (&RESTFetcher{}).Fetch(srv.URL, opts); err != nil {
			t.Fatal(err)
		}
		if gotBody != c.wantBody || gotType != c.wantCT {
			t.Errorf("body %#v: sent %q as %q, want %q as %q", c.body, gotBody, gotType, c.wantBody, c.wantCT)
		}
	}
}
