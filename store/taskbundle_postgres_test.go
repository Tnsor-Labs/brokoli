package store

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundle"
)

// Live-Postgres coverage of the TaskBundleStore capability. Same
// skip-if-unreachable discipline as postgres_leader_test.go and the
// other live-PG suites: BROKOLI_TEST_POSTGRES_URL, or a conventional
// local default, and skip cleanly when nothing is reachable. The real
// task_bundles table is created by NewPostgresStore's migration; each
// test keeps distinct org keys, so the shared table stays collision-free.
func openTaskBundleTestPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres@localhost:5432/brokoli_test?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := NewPostgresStore(dsn)
	if err != nil {
		t.Skipf("skipping live-Postgres task bundle test: %v", err)
	}
	if err := s.db.PingContext(ctx); err != nil {
		s.Close()
		t.Skipf("skipping live-Postgres task bundle test: no reachable Postgres at %s", dsn)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func pgSeed(t *testing.T) *PostgresStore {
	t.Helper()
	s := openTaskBundleTestPostgresStore(t)
	archive := testBundles(t)
	digest := taskbundle.DigestOf(archive)
	if created, err := s.PutTaskBundle("pg-org-a", digest, archive); err != nil || !created {
		t.Fatalf("first postgres upload: created=%v err=%v", created, err)
	}
	return s
}

func TestTaskBundleStore_PostgresSemantics(t *testing.T) {
	s := pgSeed(t)
	archive := testBundles(t)
	digest := taskbundle.DigestOf(archive)

	created, err := s.PutTaskBundle("pg-org-a", digest, archive)
	if err != nil || created {
		t.Fatalf("byte-identical re-upload: created=%v err=%v (want false,nil)", created, err)
	}
	if _, err := s.GetTaskBundle("pg-org-b", digest); err != ErrTaskBundleNotFound {
		t.Fatalf("tenant read leaked across orgs: err=%v", err)
	}
	missing := "sha256:" + string(bytes.Repeat([]byte("a"), 64))
	if _, err := s.GetTaskBundle("pg-org-a", missing); err != ErrTaskBundleNotFound {
		t.Fatalf("missing digest: err=%v", err)
	}
	if _, err := s.PutTaskBundle("pg-org-a", missing, archive); err == nil {
		t.Fatal("postgres accepted bytes that do not hash to the claimed digest")
	}
	if created, err := s.PutTaskBundle("pg-org-b", digest, archive); err != nil || !created {
		t.Fatalf("same digest, new tenant: created=%v err=%v (must be a fresh key)", created, err)
	}
	got, err := s.GetTaskBundle("pg-org-a", digest)
	if err != nil || !bytes.Equal(got, archive) {
		t.Fatalf("org-a's copy after org-b wrote the same digest: %v", err)
	}
}