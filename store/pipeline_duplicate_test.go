package store

import (
	"errors"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Both backends must report the same sentinel for a pipeline_id
// collision, because they report entirely different driver text for it:
// Postgres names the index, SQLite names the column. A mapping written
// against one of them silently does nothing on the other, which is how
// the draft column shipped broken earlier today.
func TestDuplicatePipelineIDIsTypedOnBothBackends(t *testing.T) {
	for name, s := range intervalTestStores(t) {
		t.Run(name, func(t *testing.T) {
			id := common.NewID()
			mk := func() *models.Pipeline {
				return &models.Pipeline{
					ID: common.NewID(), Name: "dup " + id[:8], PipelineID: "dup-" + id[:8],
					Enabled:     true,
					WorkspaceID: models.DefaultWorkspaceID,
					Nodes:       []models.Node{{ID: "n", Type: models.NodeTypeSourceFile, Name: "S"}},
					CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
				}
			}
			first := mk()
			if err := s.CreatePipeline(first); err != nil {
				t.Fatalf("first create: %v", err)
			}
			t.Cleanup(func() { _ = s.DeletePipeline(first.ID) })

			err := s.CreatePipeline(mk())
			if !errors.Is(err, ErrDuplicatePipelineID) {
				t.Fatalf("second create = %v, want ErrDuplicatePipelineID", err)
			}
		})
	}
}

// The sentinel must not swallow other failures: a create that fails for
// any other reason has to keep its own error.
func TestNonDuplicateCreateErrorsAreNotMasked(t *testing.T) {
	for name, s := range intervalTestStores(t) {
		t.Run(name, func(t *testing.T) {
			// Same primary key twice: a different unique violation.
			p := &models.Pipeline{
				ID: common.NewID(), Name: "pk", Enabled: true,
				WorkspaceID: models.DefaultWorkspaceID,
				Nodes:       []models.Node{{ID: "n", Type: models.NodeTypeSourceFile, Name: "S"}},
				CreatedAt:   time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := s.CreatePipeline(p); err != nil {
				t.Fatalf("first create: %v", err)
			}
			t.Cleanup(func() { _ = s.DeletePipeline(p.ID) })

			err := s.CreatePipeline(p)
			if err == nil {
				t.Fatal("re-creating the same primary key succeeded")
			}
			if errors.Is(err, ErrDuplicatePipelineID) {
				t.Errorf("a primary-key collision was reported as a pipeline_id conflict: %v", err)
			}
		})
	}
}
