package fetchers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// The bug in Tnsor-Labs/brokoli#58: the connection resolver injected
// auth_user/auth_password into the node config, sink_api applied them, and
// the read path silently dropped them — requests went out unauthenticated
// and surfaced as a 401 far from the misconfiguration.

func TestRESTFetcher_BasicAuth_AppliedOnReads(t *testing.T) {
	var gotUser, gotPass string
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, hadAuth = r.BasicAuth()
		if !hadAuth {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`[{"id":1}]`))
	}))
	t.Cleanup(srv.Close)

	ds, err := (&RESTFetcher{}).Fetch(srv.URL, map[string]interface{}{
		"auth_user":     "svc-reader",
		"auth_password": "s3cret",
	})
	if err != nil {
		t.Fatalf("fetch failed — before the fix this was the silent 401: %v", err)
	}
	if !hadAuth {
		t.Fatal("no Authorization header reached the server")
	}
	if gotUser != "svc-reader" || gotPass != "s3cret" {
		t.Errorf("server saw %q/%q, want svc-reader/s3cret", gotUser, gotPass)
	}
	if len(ds.Rows) != 1 {
		t.Errorf("rows = %d, want 1", len(ds.Rows))
	}
}

// An empty password is legitimate basic auth (some APIs use token-as-user),
// and mirrors the sink's gate: user non-empty is what enables auth.
func TestRESTFetcher_BasicAuth_EmptyPasswordStillSent(t *testing.T) {
	var hadAuth bool
	var gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, _, hadAuth = r.BasicAuth()
		_, _ = w.Write([]byte(`[{"ok":true}]`))
	}))
	t.Cleanup(srv.Close)

	if _, err := (&RESTFetcher{}).Fetch(srv.URL, map[string]interface{}{
		"auth_user": "token-as-user",
	}); err != nil {
		t.Fatal(err)
	}
	if !hadAuth || gotUser != "token-as-user" {
		t.Errorf("hadAuth=%v user=%q, want auth with token-as-user", hadAuth, gotUser)
	}
}

// No credentials configured must mean no Authorization header — not an
// empty one.
func TestRESTFetcher_BasicAuth_AbsentWhenNotConfigured(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`[{"ok":true}]`))
	}))
	t.Cleanup(srv.Close)

	if _, err := (&RESTFetcher{}).Fetch(srv.URL, map[string]interface{}{}); err != nil {
		t.Fatal(err)
	}
	if authHeader != "" {
		t.Errorf("unexpected Authorization header %q on an unauthenticated fetch", authHeader)
	}
}

// Connection credentials take precedence over a handwritten Authorization
// header — the same order runSinkAPI already gives the write path, asserted
// here so the two directions cannot silently diverge again.
func TestRESTFetcher_BasicAuth_WinsOverExplicitHeader(t *testing.T) {
	var gotUser string
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, _, hadAuth = r.BasicAuth()
		_, _ = w.Write([]byte(`[{"ok":true}]`))
	}))
	t.Cleanup(srv.Close)

	if _, err := (&RESTFetcher{}).Fetch(srv.URL, map[string]interface{}{
		"auth_user":     "from-connection",
		"auth_password": "pw",
		"headers":       map[string]interface{}{"Authorization": "Bearer handwritten"},
	}); err != nil {
		t.Fatal(err)
	}
	if !hadAuth || gotUser != "from-connection" {
		t.Errorf("hadAuth=%v user=%q — connection credentials should win, matching the sink", hadAuth, gotUser)
	}
}

// Every page of a paginated fetch must carry the credentials, not only the
// first — the funnel through executeRequest makes this true, and this test
// keeps it true.
func TestRESTFetcher_BasicAuth_AppliedToEveryPaginatedPage(t *testing.T) {
	const pageSize = 2
	const total = 6

	var mu sync.Mutex
	authedPages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "pager" || pass != "pw" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mu.Lock()
		authedPages++
		mu.Unlock()

		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		items := []map[string]interface{}{}
		for i := offset; i < offset+pageSize && i < total; i++ {
			items = append(items, map[string]interface{}{"id": i})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"items":    items,
			"end_flag": offset+pageSize >= total,
		})
	}))
	t.Cleanup(srv.Close)

	ds, err := (&RESTFetcher{}).Fetch(srv.URL, map[string]interface{}{
		"auth_user":     "pager",
		"auth_password": "pw",
		"records":       "items",
		"pagination": map[string]interface{}{
			"strategy":  "offset",
			"page_size": float64(pageSize),
			"end_flag":  "end_flag",
		},
	})
	if err != nil {
		t.Fatalf("paginated fetch failed: %v", err)
	}
	if len(ds.Rows) != total {
		t.Errorf("rows = %d, want %d", len(ds.Rows), total)
	}
	mu.Lock()
	defer mu.Unlock()
	if authedPages < total/pageSize {
		t.Errorf("only %d page requests carried credentials, want at least %d", authedPages, total/pageSize)
	}
}
