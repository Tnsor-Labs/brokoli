package api

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/Tnsor-Labs/brokoli/engine"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/templates"
	"github.com/Tnsor-Labs/brokoli/store"
)

// newTemplateTestEngine sets up a real store + engine pinned entirely to
// a fresh temp dir, mirroring engine's own newResumeTestEngine helper
// (engine/resume_test.go) so nothing leaks into the repo working
// directory across test runs.
func newTemplateTestEngine(t *testing.T) (*engine.Engine, store.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "templates.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	eng := engine.NewEngine(s)
	eng.ArtifactStore = engine.NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts"))
	eng.PaginationCheckpointStore = engine.NewLocalDiskPaginationCheckpointStore(filepath.Join(dir, "pagination-checkpoints"))
	return eng, s
}

// pipelineFromTemplate builds a runnable models.Pipeline from a template,
// redirecting any sink_file node's output into sinkDir instead of the
// template's real /tmp path — keeps the test hermetic without needing to
// mutate templates.Builtin (a shared package-level value).
func pipelineFromTemplate(id string, tmpl templates.Template, sinkDir string) *models.Pipeline {
	nodes := make([]models.Node, len(tmpl.Nodes))
	for i, n := range tmpl.Nodes {
		nodes[i] = n
		if n.Type == models.NodeTypeSinkFile {
			cfg := make(map[string]interface{}, len(n.Config))
			for k, v := range n.Config {
				cfg[k] = v
			}
			cfg["path"] = filepath.Join(sinkDir, n.ID+".out")
			nodes[i].Config = cfg
		}
	}
	return &models.Pipeline{
		ID:      id,
		Name:    tmpl.Name,
		Enabled: true,
		Nodes:   nodes,
		Edges:   tmpl.Edges,
	}
}

// newSampleDataTestServer serves the real samplesDataHandler at the exact
// path production uses (/api/samples/data/{file}), so a template's
// relative "/api/samples/data/employees.json" URL resolves against it
// exactly as it would against a real running server — same handler code,
// not a stand-in fixture.
func newSampleDataTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	r := chi.NewRouter()
	r.Get("/api/samples/data/{file}", samplesDataHandler())
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	return server
}

func templateByID(t *testing.T, id string) templates.Template {
	t.Helper()
	for _, tmpl := range templates.Builtin {
		if tmpl.ID == id {
			return tmpl
		}
	}
	t.Fatalf("no builtin template with id %q", id)
	return templates.Template{}
}

// TestBuiltinTemplates_RunSuccessfully is the regression guard for the
// class of bug that shipped three times over in the JS-literal era of
// these templates (brokoli#67 area, PRs #68/#70): a config shape that
// looks plausible but doesn't match what the engine actually parses,
// caught by nothing until a human manually ran the template. Every
// template that's expected to complete cleanly is run here through the
// real engine, against the real sample-data endpoint, and must succeed
// end to end.
func TestBuiltinTemplates_RunSuccessfully(t *testing.T) {
	server := newSampleDataTestServer(t)
	t.Setenv("BROKOLI_SERVER_URL", server.URL)

	for _, id := range []string{"hello-world", "api-fetch", "join-aggregate"} {
		t.Run(id, func(t *testing.T) {
			eng, s := newTemplateTestEngine(t)
			tmpl := templateByID(t, id)
			pipeline := pipelineFromTemplate("p-"+id, tmpl, t.TempDir())
			if err := s.CreatePipeline(pipeline); err != nil {
				t.Fatalf("create pipeline: %v", err)
			}

			run, err := eng.RunPipeline(pipeline.ID)
			if err != nil {
				t.Fatalf("run pipeline: %v", err)
			}
			if run.Status != models.RunStatusSuccess {
				t.Fatalf("run status = %s, want success (error: %s)", run.Status, run.Error)
			}
		})
	}
}

// TestBuiltinTemplate_DataQuality_BlocksOnBadData asserts the "Data
// Quality" template's actual point: api/samples_data/employees.json has
// one row with a null email specifically so this template's quality
// gate (on_failure: "block" for the not_null check) has something real
// to catch. A silent pass here would mean the template stopped
// demonstrating what it's named for.
func TestBuiltinTemplate_DataQuality_BlocksOnBadData(t *testing.T) {
	server := newSampleDataTestServer(t)
	t.Setenv("BROKOLI_SERVER_URL", server.URL)

	eng, s := newTemplateTestEngine(t)
	tmpl := templateByID(t, "data-quality")
	pipeline := pipelineFromTemplate("p-data-quality", tmpl, t.TempDir())
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	// RunPipeline returns a non-nil err alongside a fully populated run
	// whenever a node fails (the error carries the same message the run
	// itself records) — a null err here would mean the pipeline never
	// started at all, which is the only case worth failing the test on.
	run, err := eng.RunPipeline(pipeline.ID)
	if run == nil {
		t.Fatalf("run pipeline: %v", err)
	}
	if run.Status != models.RunStatusFailed {
		t.Fatalf("run status = %s, want failed (the quality gate is supposed to block)", run.Status)
	}

	nodeRuns, err := s.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatalf("list node runs: %v", err)
	}
	statuses := make(map[string]models.RunStatus, len(nodeRuns))
	errs := make(map[string]string, len(nodeRuns))
	for _, nr := range nodeRuns {
		statuses[nr.NodeID] = nr.Status
		errs[nr.NodeID] = nr.Error
	}

	if statuses["s1"] != models.RunStatusSuccess {
		t.Errorf("s1 (fetch) status = %s, want success", statuses["s1"])
	}
	if statuses["q1"] != models.RunStatusFailed {
		t.Fatalf("q1 (quality gate) status = %s, want failed", statuses["q1"])
	}
	if !strings.Contains(errs["q1"], "email") {
		t.Errorf("q1 error should name the email column that failed the not_null check, got: %s", errs["q1"])
	}
}
