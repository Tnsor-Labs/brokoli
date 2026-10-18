package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// pipeline_id is unique within a workspace: a staging workspace holds the
// same pipelines as production in the same organization.
func TestPipelineIDIsUniqueWithinAWorkspace(t *testing.T) {
	testPipelineIDIsUniqueWithinAWorkspace(t, newTestStore(t))
}

func TestPipelineIDIsUniqueWithinAWorkspacePostgres(t *testing.T) {
	url := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	s, err := NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testPipelineIDIsUniqueWithinAWorkspace(t, s)
}

func testPipelineIDIsUniqueWithinAWorkspace(t *testing.T, s Store) {
	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	org, other, slug := "org-"+tag, "other-"+tag, "orders-"+tag
	inWS := func(id, org, ws string) error {
		p := slugPipeline(id+"-"+tag, org, slug)
		p.WorkspaceID = ws
		return s.CreatePipeline(p)
	}
	t.Cleanup(func() {
		for _, id := range []string{"prod", "staging", "other"} {
			_ = s.DeletePipeline(id + "-" + tag)
		}
	})
	if err := inWS("prod", org, "ws-prod"); err != nil {
		t.Fatal(err)
	}
	if err := inWS("staging", org, "ws-staging"); err != nil {
		t.Fatalf("the same pipeline_id in another workspace of the organization: %v", err)
	}
	if err := inWS("again", org, "ws-prod"); !errors.Is(err, ErrDuplicatePipelineID) {
		t.Fatalf("a second one in the same workspace: %v, want ErrDuplicatePipelineID", err)
	}
	if err := inWS("other", other, "ws-prod"); err != nil {
		t.Fatalf("another organization's workspace of the same name: %v", err)
	}

	for ws, want := range map[string]string{"ws-prod": "prod-" + tag, "ws-staging": "staging-" + tag} {
		got, err := s.GetPipelineByPipelineIDInWorkspace(org, ws, slug)
		if err != nil || got.ID != want {
			t.Errorf("in %s: %+v (%v), want %s", ws, got, err, want)
		}
	}
	if _, err := s.GetPipelineByPipelineIDInWorkspace(org, "ws-none", slug); err == nil {
		t.Error("found a pipeline in a workspace that has none")
	}
	// Organization-wide, the answer is ambiguous now, and says so rather
	// than picking one.
	if _, err := s.GetPipelineByPipelineIDInOrg(org, slug); !errors.Is(err, ErrAmbiguousPipelineID) {
		t.Fatalf("organization-wide lookup: %v, want ErrAmbiguousPipelineID", err)
	}
	if got, err := s.GetPipelineByPipelineIDInOrg(other, slug); err != nil || got.ID != "other-"+tag {
		t.Fatalf("an organization with one: %+v (%v)", got, err)
	}
}

// A database from the per-organization era: opening it replaces that index
// with the per-workspace one.
func TestOpeningAPerOrganizationDatabaseMovesTheIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "org-era.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS idx_pipeline_ws_pid`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX idx_pipeline_org_pid ON pipelines(org_id, pipeline_id) WHERE pipeline_id != ''`); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var name string
	if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_pipeline_org_pid'`).Scan(&name); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("the per-organization index is still there (%v)", err)
	}
	a, b := slugPipeline("a", "org", "x"), slugPipeline("b", "org", "x")
	a.WorkspaceID, b.WorkspaceID = "ws-1", "ws-2"
	if err := s.CreatePipeline(a); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePipeline(b); err != nil {
		t.Errorf("same pipeline_id in another workspace after the move: %v", err)
	}
}
