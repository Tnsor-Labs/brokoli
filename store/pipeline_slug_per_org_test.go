package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

func slugPipeline(id, org, slug string) *models.Pipeline {
	now := time.Now()
	return &models.Pipeline{ID: id, Name: id, PipelineID: slug, OrgID: org,
		Nodes: []models.Node{}, Edges: []models.Edge{}, CreatedAt: now, UpdatedAt: now}
}

// pipeline_id is unique within an organization. Unique everywhere, one
// tenant's create was refused for a name another tenant used -- telling it
// the name existed, and letting it claim names before their owners did.
func TestPipelineIDIsUniqueWithinAnOrganization(t *testing.T) {
	testPipelineIDIsUniqueWithinAnOrganization(t, newTestStore(t))
}

func TestPipelineIDIsUniqueWithinAnOrganizationPostgres(t *testing.T) {
	url := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	s, err := NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	testPipelineIDIsUniqueWithinAnOrganization(t, s)
}

func testPipelineIDIsUniqueWithinAnOrganization(t *testing.T, s Store) {
	// Unique per run: the Postgres database is shared between tests.
	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	orgA, orgB, slug := "org-a-"+tag, "org-b-"+tag, "orders-etl-"+tag
	t.Cleanup(func() {
		for _, id := range []string{"a1-" + tag, "b1-" + tag} {
			_ = s.DeletePipeline(id)
		}
	})
	if err := s.CreatePipeline(slugPipeline("a1-"+tag, orgA, slug)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePipeline(slugPipeline("b1-"+tag, orgB, slug)); err != nil {
		t.Fatalf("another organization's pipeline with the same pipeline_id: %v", err)
	}
	if err := s.CreatePipeline(slugPipeline("a2-"+tag, orgA, slug)); !errors.Is(err, ErrDuplicatePipelineID) {
		t.Fatalf("a second one in the same organization: %v, want ErrDuplicatePipelineID", err)
	}
	for org, want := range map[string]string{orgA: "a1-" + tag, orgB: "b1-" + tag} {
		got, err := s.GetPipelineByPipelineIDInOrg(org, slug)
		if err != nil || got.ID != want {
			t.Errorf("in %s: %+v (%v), want %s", org, got, err, want)
		}
	}
	if _, err := s.GetPipelineByPipelineIDInOrg("org-c-"+tag, slug); err == nil {
		t.Error("found a pipeline in an organization that has none")
	}
}

// A database from before keeps working: opening it replaces the
// server-wide index with the per-organization one.
func TestOpeningAnOlderDatabaseMovesThePipelineIDIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DROP INDEX IF EXISTS idx_pipeline_org_pid`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE UNIQUE INDEX idx_pipeline_pid ON pipelines(pipeline_id) WHERE pipeline_id != ''`); err != nil {
		t.Fatal(err)
	}
	forgetMigrations(t, s.db)
	s.Close()

	s, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var name string
	if err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_pipeline_pid'`).Scan(&name); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("the server-wide index is still there (%v)", err)
	}
	if err := s.CreatePipeline(slugPipeline("a1", "org-a", "x")); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePipeline(slugPipeline("b1", "org-b", "x")); err != nil {
		t.Errorf("same pipeline_id in another organization after the move: %v", err)
	}
}
