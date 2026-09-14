package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// Triggering a run through the real handler and engine records who did
// it (#241). The unit tests above cover the pieces; this is the one that
// would catch the plumbing being wired to nothing.

func attributionE2E(t *testing.T) (store.Store, *chi.Mux, string) {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "attr-e2e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	csvPath := filepath.Join(t.TempDir(), "input.csv")
	if err := os.WriteFile(csvPath, []byte("id,name\n1,brokoli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipeline := &models.Pipeline{
		ID: "attributed-pipeline", Name: "Attributed",
		Nodes: []models.Node{{
			ID: "source", Type: models.NodeTypeSourceFile, Name: "Source",
			Config: map[string]interface{}{"path": csvPath, "format": "csv"},
		}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := engine.NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})
	h := NewRunHandler(s, eng)

	r := chi.NewRouter()
	r.Post("/api/pipelines/{id}/run", h.TriggerRun)
	r.Get("/api/runs", h.ListStartedBy)
	r.Get("/api/runs/{id}", h.Get)
	return s, r, pipeline.ID
}

func signedIn(req *http.Request, userID, name string) *http.Request {
	claims := jwt.MapClaims{"sub": userID, "display_name": name, "role": "admin"}
	return req.WithContext(context.WithValue(req.Context(), "claims", &claims))
}

func TestTriggeringARunRecordsWhoDidIt(t *testing.T) {
	s, router, pipelineID := attributionE2E(t)

	req := signedIn(httptest.NewRequest(http.MethodPost, "/api/pipelines/"+pipelineID+"/run",
		strings.NewReader(`{}`)), "u1", "Alice A")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("trigger: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var triggered struct {
		RunID string `json:"run_id"`
		ID    string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &triggered)
	runID := triggered.RunID
	if runID == "" {
		runID = triggered.ID
	}
	if runID == "" {
		t.Fatalf("no run id in the response: %s", rec.Body.String())
	}

	// TriggerRun returns the id before the runner has created the row:
	// the run itself is written asynchronously, so the attribution is
	// too. Waiting for it is the test's job, not the server's.
	a, ok := waitForAttribution(t, s, runID)
	if !ok {
		t.Fatalf("run %s never got an attribution; the plumbing is not connected", runID)
	}
	if a.Kind != models.RunTriggerKindUser {
		t.Errorf("kind = %q, want user", a.Kind)
	}
	if a.UserID != "u1" || a.UserName != "Alice A" {
		t.Errorf("attribution = %+v, want u1/Alice A", a)
	}

	// And it comes back on the run.
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, "/api/runs/"+runID, nil), "u1", "Alice A"))
	if rec.Code != http.StatusOK {
		t.Fatalf("get run: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var run models.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.TriggeredBy == nil || run.TriggeredBy.UserName != "Alice A" {
		t.Fatalf("GET /runs/{id} triggered_by = %+v, want Alice A", run.TriggeredBy)
	}
}

// waitForAttribution polls until a run's attribution has been written,
// or gives up. The run row and its attribution are created by the runner
// goroutine after the handler has already replied.
func waitForAttribution(t *testing.T, s store.Store, runID string) (models.RunAttribution, bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := s.GetRunAttribution([]string{runID})
		if err != nil {
			t.Fatalf("read attribution: %v", err)
		}
		if a, ok := got[runID]; ok {
			return a, true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return models.RunAttribution{}, false
}

func triggerAs(t *testing.T, router *chi.Mux, pipelineID, userID, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodPost,
		"/api/pipelines/"+pipelineID+"/run", strings.NewReader(`{}`)), userID, name))
	if rec.Code != http.StatusOK && rec.Code != http.StatusAccepted {
		t.Fatalf("trigger as %s: status = %d; body=%s", userID, rec.Code, rec.Body.String())
	}
	var out struct {
		RunID string `json:"run_id"`
		ID    string `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.RunID != "" {
		return out.RunID
	}
	return out.ID
}

func TestRunsStartedByMe(t *testing.T) {
	s, router, pipelineID := attributionE2E(t)

	for i := 0; i < 2; i++ {
		runID := triggerAs(t, router, pipelineID, "u1", "Alice")
		if _, ok := waitForAttribution(t, s, runID); !ok {
			t.Fatalf("run %s never got an attribution", runID)
		}
	}
	// Somebody else's run must not appear in the caller's list.
	otherID := triggerAs(t, router, pipelineID, "u2", "Bob")
	if _, ok := waitForAttribution(t, s, otherID); !ok {
		t.Fatalf("run %s never got an attribution", otherID)
	}

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, "/api/runs?started_by=me", nil), "u1", "Alice"))
	if rec.Code != http.StatusOK {
		t.Fatalf("started_by=me: status = %d; body=%s", rec.Code, rec.Body.String())
	}
	var runs []models.Run
	if err := json.Unmarshal(rec.Body.Bytes(), &runs); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want the 2 u1 started (not u2's)", len(runs))
	}
	for _, run := range runs {
		if run.TriggeredBy == nil || run.TriggeredBy.UserID != "u1" {
			t.Errorf("run %s triggered_by = %+v, want u1", run.ID, run.TriggeredBy)
		}
	}
}

// started_by is required and only accepts "me". A user id would be an
// audit question with its own access rule, and treating an unsupported
// value as "me" would answer a question nobody asked.
func TestStartedByRefusesWhatItCannotAnswer(t *testing.T) {
	_, router, _ := attributionE2E(t)

	for _, q := range []string{"/api/runs", "/api/runs?started_by=u2", "/api/runs?started_by=", "/api/runs?started_by=all"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, q, nil), "u1", "Alice"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status = %d, want 400; body=%s", q, rec.Code, rec.Body.String())
		}
	}
	// And a bad limit is refused rather than clamped.
	for _, q := range []string{"/api/runs?started_by=me&limit=0", "/api/runs?started_by=me&limit=500", "/api/runs?started_by=me&limit=abc"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, signedIn(httptest.NewRequest(http.MethodGet, q, nil), "u1", "Alice"))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s: status = %d, want 400; body=%s", q, rec.Code, rec.Body.String())
		}
	}
	// Unauthenticated cannot ask for "my" runs.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runs?started_by=me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: status = %d, want 401; body=%s", rec.Code, rec.Body.String())
	}
}

// The scheduler's runs are attributed to the schedule, not to a person.
func TestScheduledRunsAreAttributedToTheSchedule(t *testing.T) {
	s, _, pipelineID := attributionE2E(t)
	eng := engine.NewEngine(s)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = eng.Close(ctx)
	})

	run, err := eng.RunPipelineOpts(pipelineID, engine.RunOptions{
		Trigger:     models.RunTriggerScheduled,
		TriggeredBy: &models.RunAttribution{Kind: models.RunTriggerKindSchedule},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := s.GetRunAttribution([]string{run.ID})
	if err != nil {
		t.Fatal(err)
	}
	a, ok := got[run.ID]
	if !ok {
		t.Fatalf("scheduled run %s has no attribution", run.ID)
	}
	if a.Kind != models.RunTriggerKindSchedule {
		t.Errorf("kind = %q, want schedule", a.Kind)
	}
	if a.UserID != "" || a.UserName != "" {
		t.Errorf("attribution = %+v, want no person on a scheduled run", a)
	}
}
