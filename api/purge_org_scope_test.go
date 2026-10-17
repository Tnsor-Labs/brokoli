package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
)

// A purge removes the caller's organization's old runs and no one else's,
// whatever the organization is called. "default" is a real organization in
// a multi-tenant deployment, and its id used to switch the org filter off.
func TestPurgeStaysInTheCallersOrganization(t *testing.T) {
	for _, callerOrg := range []string{"default", "org-a"} {
		t.Run(callerOrg, func(t *testing.T) {
			s := newOrgCheckStore(t)
			e := engine.NewEngine(s)
			t.Cleanup(func() { _ = e.Close(context.Background()) })

			old := time.Now().AddDate(0, 0, -60).UTC()
			runOf := map[string]string{}
			for _, org := range []string{callerOrg, "org-other"} {
				p := &models.Pipeline{ID: "p-" + org, PipelineID: "p-" + org, Name: "p-" + org, OrgID: org,
					Nodes: []models.Node{}, Edges: []models.Edge{}}
				if err := s.CreatePipeline(p); err != nil {
					t.Fatal(err)
				}
				run := &models.Run{ID: "r-" + org, PipelineID: p.ID, Status: models.RunStatusSuccess, StartedAt: &old, FinishedAt: &old, OrgID: org}
				if err := s.CreateRun(run); err != nil {
					t.Fatal(err)
				}
				runOf[org] = run.ID
			}

			r := httptest.NewRequest(http.MethodPost, "/api/system/purge", strings.NewReader(`{"days":30}`))
			r = r.WithContext(context.WithValue(r.Context(), OrgIDContextKey{}, callerOrg))
			rec := httptest.NewRecorder()
			systemPurge(s, e).ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				t.Fatalf("purge: %d %s", rec.Code, rec.Body.String())
			}
			if _, err := s.GetRun(runOf[callerOrg]); err == nil {
				t.Errorf("the caller's own old run survived the purge")
			}
			if _, err := s.GetRun(runOf["org-other"]); err != nil {
				t.Errorf("a purge by %q deleted another organization's run: %v", callerOrg, err)
			}
		})
	}
}
