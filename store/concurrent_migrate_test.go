package store

import (
	"os"
	"sync"
	"testing"
)

// Replicas start together and migrate the same database at the same
// moment. CREATE INDEX IF NOT EXISTS can still fail the slower one on
// Postgres's catalog, and a migration treating that as fatal exited the
// pod (seen on the first deploy of the per-tenant uniqueness indexes).
// Every concurrent start must succeed.
func TestConcurrentStartsMigrateTogether(t *testing.T) {
	url := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	first, err := NewPostgresStore(url)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	for round := 0; round < 5; round++ {
		if _, err := first.db.Exec(`DROP INDEX IF EXISTS idx_connections_ws_conn_id; DROP INDEX IF EXISTS idx_pipeline_org_pid`); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, err := NewPostgresStore(url)
				if err != nil {
					errs <- err
					return
				}
				_ = s.Close()
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Errorf("round %d: a concurrent start failed: %v", round, err)
		}
	}
}
