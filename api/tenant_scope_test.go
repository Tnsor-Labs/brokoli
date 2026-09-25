package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
	"github.com/go-chi/chi/v5"
)

// Tenant scoping and secret disclosure on the read and mutate paths.
//
// Every one of these was a check that existed and guarded the wrong
// thing, or a masking rule applied on one branch and not its neighbour.
// None of them is a missing feature, so the tests assert the boundary
// rather than the behaviour.

func scopeStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "scope.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func scopeReq(method, path, orgID, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	if orgID != "" {
		r = r.WithContext(context.WithValue(r.Context(), OrgIDContextKey{}, orgID))
	}
	return r
}

func scopePipeline(t *testing.T, s store.Store, id, orgID, workspaceID string) {
	t.Helper()
	if workspaceID == "" {
		workspaceID = models.DefaultWorkspaceID
	}
	if err := s.CreatePipeline(&models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: orgID, WorkspaceID: workspaceID,
		Nodes:     []models.Node{{ID: "s1", Type: models.NodeTypeSourceFile, Name: "src"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create pipeline %s: %v", id, err)
	}
}

// A caller passes their own pipeline, which satisfies the organization
// check, and any dead-letter id at all. Resolving another tenant's entry
// marks their failure dealt with, in their inbox, without them seeing it.
func TestResolvingADeadLetterEntryOfAnotherTenantIsRefused(t *testing.T) {
	s := scopeStore(t)
	scopePipeline(t, s, "mine", "org-1", "")
	scopePipeline(t, s, "theirs", "org-2", "")

	if err := s.AddToDLQ("theirs", "run-1", "n1", "node", "it broke", "{}"); err != nil {
		t.Fatal(err)
	}
	theirs, err := s.ListDLQ("theirs", true, 10)
	if err != nil || len(theirs) != 1 {
		t.Fatalf("seed: %v (%d entries)", err, len(theirs))
	}
	victimID := theirs[0].ID

	router := chi.NewRouter()
	router.Post("/pipelines/{id}/dlq/{dlqId}/resolve", dlqResolveHandler(s))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, scopeReq(http.MethodPost, "/pipelines/mine/dlq/"+victimID+"/resolve", "org-1", ""))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}

	// And it really is still unresolved, not merely reported as refused.
	after, _ := s.ListDLQ("theirs", true, 10)
	if len(after) != 1 || after[0].Resolved {
		t.Fatalf("the other tenant's entry was resolved: %+v", after)
	}
}

// The other direction: resolving your own entry through your own
// pipeline still works, or triage is broken.
func TestResolvingYourOwnDeadLetterEntryWorks(t *testing.T) {
	s := scopeStore(t)
	scopePipeline(t, s, "mine", "org-1", "")
	if err := s.AddToDLQ("mine", "run-1", "n1", "node", "it broke", "{}"); err != nil {
		t.Fatal(err)
	}
	mine, _ := s.ListDLQ("mine", true, 10)
	if len(mine) != 1 {
		t.Fatalf("seed: %d entries", len(mine))
	}

	router := chi.NewRouter()
	router.Post("/pipelines/{id}/dlq/{dlqId}/resolve", dlqResolveHandler(s))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, scopeReq(http.MethodPost, "/pipelines/mine/dlq/"+mine[0].ID+"/resolve", "org-1", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	after, _ := s.ListDLQ("mine", true, 10)
	if !after[0].Resolved {
		t.Error("the entry was not resolved")
	}
}

// An empty org means "no filter" to the store, so the dependency graph
// returned every pipeline in the deployment to a caller whose org could
// not be resolved.
func TestTheDependencyGraphShowsNothingWithoutAnOrg(t *testing.T) {
	s := scopeStore(t)
	scopePipeline(t, s, "one", "org-1", "")
	scopePipeline(t, s, "two", "org-2", "")

	prev := OrgResolverFunc
	OrgResolverFunc = func(string) string { return "" } // multi-tenant mode
	t.Cleanup(func() { OrgResolverFunc = prev })

	rec := httptest.NewRecorder()
	pipelineDependencyGraphHandler(s)(rec, scopeReq(http.MethodGet, "/pipelines/dependency-graph", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var out struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Nodes) != 0 {
		t.Fatalf("a caller with no organization saw %d pipelines: %+v", len(out.Nodes), out.Nodes)
	}

	// A caller who does have an org still sees their own, and only theirs.
	rec = httptest.NewRecorder()
	pipelineDependencyGraphHandler(s)(rec, scopeReq(http.MethodGet, "/pipelines/dependency-graph", "org-1", ""))
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Nodes) != 1 || out.Nodes[0]["id"] != "one" {
		t.Fatalf("org-1 saw %+v, want only its own pipeline", out.Nodes)
	}
}

// A single-tenant deployment has no OrgResolverFunc and must keep
// working exactly as before.
func TestTheDependencyGraphIsUnchangedWithoutTenancy(t *testing.T) {
	s := scopeStore(t)
	scopePipeline(t, s, "one", "", "")

	prev := OrgResolverFunc
	OrgResolverFunc = nil
	t.Cleanup(func() { OrgResolverFunc = prev })

	rec := httptest.NewRecorder()
	pipelineDependencyGraphHandler(s)(rec, scopeReq(http.MethodGet, "/pipelines/dependency-graph", "", ""))
	var out struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Nodes) != 1 {
		t.Fatalf("a single-tenant deployment saw %d pipelines, want 1", len(out.Nodes))
	}
}

// The unpaged list masked the encrypted reference and the paged branch
// three lines above it did not, so one query parameter decided whether
// the ciphertext of every stored credential was disclosed.
//
// Only "encrypted://" is masked, and that is deliberate: it carries the
// secret material itself, while "env://NAME", "vault://path#key" and
// "k8s://ns/secret/key" name a LOCATION. A settings page has to show
// which variable a connection reads, and unmaskCredentials relies on
// the unmasked forms round-tripping so an edit does not have to resend
// them. Both halves are pinned here, because masking everything would
// look like a security improvement and would break editing.
func TestBothConnectionListsMaskTheSecretReferences(t *testing.T) {
	s := scopeStore(t)
	if err := s.CreateConnection(&models.Connection{
		ID: "c1", ConnID: "warehouse", Type: "postgres",
		WorkspaceID: models.DefaultWorkspaceID,
		PasswordRef: "encrypted://Zm9vYmFyc2VjcmV0", ExtraRef: "env://WAREHOUSE_EXTRA",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	h := NewConnectionHandler(s, nil)

	for _, query := range []string{"", "?page=1"} {
		rec := httptest.NewRecorder()
		h.List(rec, scopeReq(http.MethodGet, "/connections"+query, "", ""))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /connections%s: status = %d; body=%s", query, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		// The ciphertext must never appear, on either branch.
		if strings.Contains(body, "Zm9vYmFyc2VjcmV0") {
			t.Errorf("GET /connections%s disclosed the stored ciphertext: %s", query, body)
		}
		// The location must still appear, or the settings page cannot say
		// which variable this connection reads and an edit cannot round
		// trip.
		if !strings.Contains(body, "env://WAREHOUSE_EXTRA") {
			t.Errorf("GET /connections%s hid a non-secret reference: %s", query, body)
		}
		if !strings.Contains(body, "warehouse") {
			t.Errorf("GET /connections%s returned no connection at all: %s", query, body)
		}
	}
}

// A bare limit reached SQL unbounded, so one request could ask the
// database for the whole table and then serialise it.
func TestListLimitsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", defaultListLimit},
		{"0", defaultListLimit},
		{"-5", defaultListLimit},
		{"abc", defaultListLimit},
		{"25", 25},
		{"100000000", maxListLimit},
	} {
		if got := boundedListLimit(tc.raw); got != tc.want {
			t.Errorf("boundedListLimit(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

// Import kept a body-supplied workspace_id, so a caller could place a
// pipeline into a workspace they do not work in: invisible to them
// afterwards, and visible to people who never imported it. Create has
// always taken the workspace from the request context.
func TestImportIgnoresABodySuppliedWorkspace(t *testing.T) {
	s := scopeStore(t)
	h := NewPipelineHandler(s, nil)

	body := `{
	  "pipeline_id":"imported","name":"imported","workspace_id":"someone-elses-workspace",
	  "nodes":[{"id":"s1","type":"source_file","name":"src","config":{"path":"/tmp/x.csv","format":"csv"}}],
	  "edges":[]
	}`
	req := scopeReq(http.MethodPost, "/pipelines/import", "", body)
	// The caller's actual workspace, as the middleware would have set it.
	req = req.WithContext(context.WithValue(req.Context(), workspaceKey, "mine"))

	rec := httptest.NewRecorder()
	h.Import(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("import: status = %d; body=%s", rec.Code, rec.Body.String())
	}

	var created models.Pipeline
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	stored, err := s.GetPipeline(created.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.WorkspaceID != "mine" {
		t.Fatalf("workspace = %q, want the caller's own; the body named %q",
			stored.WorkspaceID, "someone-elses-workspace")
	}
}
