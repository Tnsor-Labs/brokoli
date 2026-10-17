package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Replicas start together and migrate the same database at the same
// moment. Concurrent CREATE ... IF NOT EXISTS can still fail the slower one
// on Postgres's catalog (pg_type_typname_nsp_index, pg_class_relname_nsp_index),
// and two seeders can insert the same template row; each failure exited a
// pod. Seen on a first install (five pods, fourteen restarts) and on the
// first deploy of the per-tenant indexes. Every concurrent start must
// succeed, on an empty database and on one that needs only new indexes.
func TestConcurrentStartsMigrateTogether(t *testing.T) {
	base := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	startTogether := func(t *testing.T, uri string, round int) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan error, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s, err := NewPostgresStore(uri)
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

	t.Run("empty database", func(t *testing.T) {
		for round := 0; round < 5; round++ {
			name := fmt.Sprintf("brokoli_concurrent_%d_%d", time.Now().UnixNano(), round)
			if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
				t.Fatalf("this test creates a database per round and needs CREATEDB: %v", err)
			}
			u, err := url.Parse(base)
			if err != nil {
				t.Fatal(err)
			}
			u.Path = "/" + name
			startTogether(t, u.String(), round)
			if _, err := admin.Exec("DROP DATABASE " + name + " WITH (FORCE)"); err != nil {
				t.Fatal(err)
			}
		}
	})

	t.Run("new indexes only", func(t *testing.T) {
		first, err := NewPostgresStore(base)
		if err != nil {
			t.Fatal(err)
		}
		defer first.Close()
		for round := 0; round < 5; round++ {
			if _, err := first.db.Exec(`DROP INDEX IF EXISTS idx_connections_ws_conn_id; DROP INDEX IF EXISTS idx_pipeline_org_pid`); err != nil {
				t.Fatal(err)
			}
			startTogether(t, base, round)
		}
	})
}

// Every process of an installation starts with Postgres, so a refused
// connection is waited out rather than exiting the process. A server that
// answers and refuses the credentials fails at once: waiting cannot fix it.
func TestWaitForPostgres(t *testing.T) {
	refused, err := sql.Open("pgx", "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	defer refused.Close()
	start := time.Now()
	if err := waitForPostgres(refused, 2*time.Second); err == nil {
		t.Fatal("a server that never comes up must still fail")
	}
	if waited := time.Since(start); waited < 2*time.Second {
		t.Errorf("gave up on a refused connection after %s, want the full 2s", waited)
	}

	base := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(u.User.Username(), "not-the-password")
	wrong, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	start = time.Now()
	if err := waitForPostgres(wrong, 30*time.Second); err == nil {
		t.Fatal("a wrong password must fail")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("waited %s on a wrong password, want an immediate failure", waited)
	}
}

// Code built on this module runs its own startup migrations under the same
// lock. Processes with separate connection pools must take turns: no two
// critical sections overlap.
func TestWithMigrationLockSerializesProcesses(t *testing.T) {
	base := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	var inside, overlaps, ran atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		db, err := sql.Open("pgx", base)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := WithMigrationLock(db, func() error {
				if inside.Add(1) > 1 {
					overlaps.Add(1)
				}
				time.Sleep(50 * time.Millisecond)
				inside.Add(-1)
				ran.Add(1)
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ran.Load() != 6 || overlaps.Load() != 0 {
		t.Fatalf("ran %d of 6, %d overlapping", ran.Load(), overlaps.Load())
	}
}
