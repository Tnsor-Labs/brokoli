package fetchers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The scenario from #737, end to end through the real read path: a
// source_api node fetching a URL. Fifteen production runs failed here
// because the request arrived as "Go-http-client/1.1" and the endpoint
// answered 406.
func TestSourceAPIRequestsIdentifyThemselves(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	t.Cleanup(srv.Close)

	if _, err := (&RESTFetcher{}).Fetch(srv.URL, nil); err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if strings.Contains(seen, "Go-http-client") {
		t.Fatalf("source_api still identifies as %q", seen)
	}
	if !strings.HasPrefix(seen, "brokoli/") {
		t.Errorf("User-Agent = %q, want it to name brokoli", seen)
	}
}

// A node config naming its own agent still wins, which is how a user
// satisfies an endpoint that wants a specific string.
func TestSourceAPIHonoursAConfiguredUserAgent(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	t.Cleanup(srv.Close)

	_, err := (&RESTFetcher{}).Fetch(srv.URL, map[string]interface{}{
		"headers": map[string]interface{}{"User-Agent": "acme-integration/2.1"},
	})
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if seen != "acme-integration/2.1" {
		t.Errorf("User-Agent = %q, want the configured string", seen)
	}
}
