package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// minimalSourcePipeline returns the smallest pipeline that passes
// ValidatePipeline (at least one source node) — a single source_file node
// reading a trivial CSV, no downstream nodes needed for these tests.
func minimalSourcePipeline(t *testing.T, id, orgID string, s *store.SQLiteStore) *models.Pipeline {
	t.Helper()
	csvPath := writeCSV(t, t.TempDir(), "data.csv", "id\n1\n")
	pipeline := &models.Pipeline{
		ID: id, Name: id, Enabled: true, OrgID: orgID,
		Nodes: []models.Node{
			{ID: "n1", Type: models.NodeTypeSourceFile, Name: "Load CSV", Config: map[string]interface{}{"path": csvPath}},
		},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	return pipeline
}

// TestRunPipeline_StampsRunWithPipelineOrgID is the end-to-end regression
// test for Tnsor-Labs/brokoli#50: a run created by RunPipeline must carry
// its pipeline's real OrgID, not the schema default. Runner.orgID was
// already resolved from pipe.OrgID for WebSocket/SODP tenant isolation
// before this fix — the gap was that it never reached the run.orgID
// database column, so org-scoped queries (PurgeRunsOlderThanByOrg,
// GetRunCalendarByOrg, ListRunIDsOlderThanByOrg) could never match a real
// org.
func TestRunPipeline_StampsRunWithPipelineOrgID(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	pipeline := minimalSourcePipeline(t, "p-acme", "acme-corp", s)

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.OrgID != "acme-corp" {
		t.Errorf("run.OrgID = %q, want %q", run.OrgID, "acme-corp")
	}

	// Confirm it's actually durable, not just set on the in-memory struct.
	stored, err := s.GetRun(run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if stored.OrgID != "acme-corp" {
		t.Errorf("stored run.OrgID = %q, want %q", stored.OrgID, "acme-corp")
	}
}

// TestRunPipeline_NoOrgPipelineProducesEmptyRunOrgID covers the
// community/single-tenant convention: a pipeline with no org produces a
// run with org_id == "" (matching pipelines.org_id's own "no org"
// convention), not the schema's 'default' placeholder.
func TestRunPipeline_NoOrgPipelineProducesEmptyRunOrgID(t *testing.T) {
	eng, s := newResumeTestEngine(t)
	pipeline := minimalSourcePipeline(t, "p-community", "", s)

	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.OrgID != "" {
		t.Errorf("run.OrgID = %q, want empty", run.OrgID)
	}
}
