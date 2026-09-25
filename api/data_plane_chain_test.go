package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	_ "modernc.org/sqlite"
)

// realUserStore backs JWTAuth for the cases that reach it. A nil store
// is only safe on the exempt paths, which would quietly make the
// negative test vacuous.
func realUserStore(t *testing.T) *UserStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "chain.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	us, err := NewUserStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return us
}

// Three middlewares sit in front of the data-plane blob routes, and a
// worker must pass all of them to reach blobAuth, which is the only one
// that can actually authorise it. Each was exempted separately, months
// apart, and each fix uncovered the next: #528 did the enterprise auth
// middleware, #563 the workspace gate, and JWTAuth was still refusing.
//
// Every one of those fixes was tested against the middleware it changed,
// in isolation, so all three passed while a real deployment could not
// fetch a single blob. This asserts the property that actually matters:
// the request arrives.
func TestDataPlaneReachesItsHandler(t *testing.T) {
	previous := UserWorkspaceResolverFunc
	// A workspace resolver installed, i.e. a multi-tenant deployment.
	// Without one the workspace gate's rejecting branch never runs and
	// this test cannot see the defect it exists for.
	UserWorkspaceResolverFunc = func(string) []string { return []string{"ws-1"} }
	t.Cleanup(func() { UserWorkspaceResolverFunc = previous })

	reached := false
	r := chi.NewRouter()
	// The same order api.NewServer applies them in.
	r.Use(JWTAuth(realUserStore(t)))
	r.Use(WorkspaceMiddleware)
	r.With(blobAuth).Get("/api/runs/{runID}/nodes/{nodeID}/attempts/{attempt}/blobs/{objectID}",
		func(w http.ResponseWriter, _ *http.Request) {
			reached = true
			w.WriteHeader(http.StatusNoContent)
		})

	req := httptest.NewRequest(http.MethodGet,
		"/api/runs/run-1/nodes/t_py/attempts/0/blobs/sha256:"+
			"d7e4d755ebc3af1c5b26c9a5b9b1f143a55837cc9e44784452fe35dcf42d2032", nil)
	// What a worker actually sends: an opaque credential, no session.
	req.Header.Set("Authorization", "Bearer brk_wp_a1b2c3")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if !reached {
		t.Fatalf("the data plane did not reach its handler: status %d, body %q",
			rec.Code, rec.Body.String())
	}
}

// And the chain still refuses an ordinary API route with no session, so
// the exemptions above are the data-plane shape and nothing wider.
func TestOrdinaryRoutesStillNeedAuthInTheSameChain(t *testing.T) {
	previous := UserWorkspaceResolverFunc
	UserWorkspaceResolverFunc = func(string) []string { return []string{"ws-1"} }
	t.Cleanup(func() { UserWorkspaceResolverFunc = previous })

	reached := false
	r := chi.NewRouter()
	r.Use(JWTAuth(realUserStore(t)))
	r.Use(WorkspaceMiddleware)
	r.Get("/api/pipelines", func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/pipelines", nil)
	req.Header.Set("Authorization", "Bearer brk_wp_a1b2c3")
	r.ServeHTTP(rec, req)

	// Refused, not which refusal: with an empty user store the count
	// fails closed as 503 before the 401 is reached, and pinning the code
	// would make this test about store state rather than about the
	// exemption being narrow.
	if reached || rec.Code < 400 {
		t.Errorf("an ordinary route was reachable with a worker token: status %d, reached %v",
			rec.Code, reached)
	}
}
