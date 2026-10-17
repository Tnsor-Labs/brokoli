package api

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/engine"
)

// Outside the store's migrations, a server's startup creates schema in two
// more places: the users table and the artifact store. Every process of an
// installation runs them at once, and on an empty Postgres database
// concurrent CREATE ... IF NOT EXISTS failed on the catalog. Each must take
// the store's migration lock. Each runs alone here, eight processes at
// once on an empty database: run after the store's migrations, which take
// turns, they rarely met, and a missing lock went unnoticed.
func TestConcurrentStartupSchema(t *testing.T) {
	base := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	together := func(t *testing.T, create func(db *sql.DB) error) {
		for round := 0; round < 5; round++ {
			name := fmt.Sprintf("startup_concurrent_%d_%d", time.Now().UnixNano(), round)
			if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
				t.Fatalf("this test creates a database per round and needs CREATEDB: %v", err)
			}
			u, err := url.Parse(base)
			if err != nil {
				t.Fatal(err)
			}
			u.Path = "/" + name
			// Each process is already connected when it reaches this step,
			// so connect first, then start all eight at once: as close to
			// simultaneous as a test can make them.
			var wg, ready sync.WaitGroup
			start := make(chan struct{})
			errs := make(chan error, 16)
			for i := 0; i < 8; i++ {
				wg.Add(1)
				ready.Add(1)
				go func() {
					defer wg.Done()
					db, err := sql.Open("pgx", u.String())
					if err == nil {
						err = db.Ping()
					}
					ready.Done()
					if err != nil {
						errs <- err
						return
					}
					defer db.Close()
					<-start
					if err := create(db); err != nil {
						errs <- err
					}
				}()
			}
			ready.Wait()
			close(start)
			wg.Wait()
			close(errs)
			for err := range errs {
				t.Errorf("round %d: a concurrent start failed: %v", round, err)
			}
			if _, err := admin.Exec("DROP DATABASE " + name + " WITH (FORCE)"); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("users table", func(t *testing.T) {
		together(t, func(db *sql.DB) error { _, err := NewUserStore(db); return err })
	})
	t.Run("artifact store", func(t *testing.T) {
		// Made once, outside the race, so the eight starts do nothing but
		// open the store.
		spill := t.TempDir()
		together(t, func(db *sql.DB) error {
			_, err := engine.NewSQLArtifactStore(db, "postgres", spill)
			return err
		})
	})
}
