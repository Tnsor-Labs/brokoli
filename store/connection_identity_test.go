package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/drivers"
)

func TestConnectionDriverIdentityRoundTrips(t *testing.T) {
	s, err := NewSQLiteStore(filepath.Join(t.TempDir(), "connections.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	identity := &drivers.DriverIdentity{Name: "adbc-flightsql", Version: "1.0.0", LibrarySHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
	if err := s.CreateConnection(&models.Connection{ID: "c1", ConnID: "flight", Type: models.ConnTypeFlightSQL, DriverIdentity: identity, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetConnection("flight")
	if err != nil {
		t.Fatal(err)
	}
	if got.DriverIdentity == nil || *got.DriverIdentity != *identity {
		t.Fatalf("driver identity = %#v, want %#v", got.DriverIdentity, identity)
	}
}
