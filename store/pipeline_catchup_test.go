package store

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// ADR-028 phase 2 (#397): the catchup opt-in is a real pipeline column and
// survives the round trip on both stores, defaulting to false.
func TestPipelineCatchupRoundTrips(t *testing.T) {
	for name, s := range intervalTestStores(t) {
		t.Run(name, func(t *testing.T) {
			id := common.NewID()
			p := &models.Pipeline{
				ID: id, Name: "cu " + id[:8], Enabled: true, Catchup: true,
				Nodes:     []models.Node{{ID: "n", Type: models.NodeTypeSourceFile, Name: "n"}},
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := s.CreatePipeline(p); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.DeletePipeline(id) })

			got, err := s.GetPipeline(id)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Catchup {
				t.Error("catchup=true did not survive create/get")
			}

			got.Catchup = false
			got.UpdatedAt = time.Now().UTC()
			if err := s.UpdatePipeline(got); err != nil {
				t.Fatal(err)
			}
			got, err = s.GetPipeline(id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Catchup {
				t.Error("catchup=false did not survive update/get")
			}
		})
	}
}
