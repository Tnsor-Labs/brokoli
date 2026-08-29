package store

import (
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// #398: max_concurrent is a real connection column on both stores,
// defaulting to 0 (unlimited).
func TestConnectionMaxConcurrentRoundTrips(t *testing.T) {
	for name, s := range intervalTestStores(t) {
		t.Run(name, func(t *testing.T) {
			id := common.NewID()
			slug := "mc-" + id[:8]
			c := &models.Connection{
				ID: id, ConnID: slug, Type: models.ConnTypePostgres,
				Host: "h", MaxConcurrent: 4,
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := s.CreateConnection(c); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.DeleteConnection(slug) })

			got, err := s.GetConnection(slug)
			if err != nil {
				t.Fatal(err)
			}
			if got.MaxConcurrent != 4 {
				t.Errorf("max_concurrent = %d after create/get, want 4", got.MaxConcurrent)
			}

			got.MaxConcurrent = 0
			got.UpdatedAt = time.Now().UTC()
			if err := s.UpdateConnection(got); err != nil {
				t.Fatal(err)
			}
			got, err = s.GetConnection(slug)
			if err != nil {
				t.Fatal(err)
			}
			if got.MaxConcurrent != 0 {
				t.Errorf("max_concurrent = %d after update to 0, want 0", got.MaxConcurrent)
			}

			// The list paths read the column too.
			conns, err := s.ListConnections()
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, cc := range conns {
				if cc.ConnID == slug {
					found = true
				}
			}
			if !found {
				t.Error("connection missing from list")
			}
		})
	}
}
