package store

import (
	"bytes"
	"context"
	"fmt"
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
// test keeps distinct org keys -- unique per invocation -- so the shared
// table stays collision-free and re-runs cannot hit stale rows (the
// archive digest is deterministic, so a fixed org key would collide
// with any earlier run's leftover data on the same database).
func openTaskBundleTestPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres@localhost:5432/brokoli_leader_test?sslmode=disable"
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

// pgScopedOrgs mints unique org keys for this invocation and deletes their
// rows when the test ends, leaving no state behind for later runs.
func pgScopedOrgs(t *testing.T, s *PostgresStore, n int) []string {
	t.Helper()
	base := fmt.Sprintf("pg-tb-%d-", time.Now().UnixNano())
	orgs := make([]string, n)
	for i := range orgs {
		orgs[i] = fmt.Sprintf("%s%d", base, i)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for _, org := range orgs {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM task_bundles WHERE org_id = $1`, org)
		}
	})
	return orgs
}

func TestTaskBundleStore_PostgresSemantics(t *testing.T) {
	s := openTaskBundleTestPostgresStore(t)
	archive := testBundles(t)
	digest := taskbundle.DigestOf(archive)

	orgs := pgScopedOrgs(t, s, 2)
	orgA, orgB := orgs[0], orgs[1]

	created, err := s.PutTaskBundle(orgA, digest, archive)
	if err != nil || !created {
		t.Fatalf("first postgres upload: created=%v err=%v", created, err)
	}
	created, err = s.PutTaskBundle(orgA, digest, archive)
	if err != nil || created {
		t.Fatalf("byte-identical re-upload: created=%v err=%v (want false,nil)", created, err)
	}
	if _, err := s.GetTaskBundle(orgB, digest); err != ErrTaskBundleNotFound {
		t.Fatalf("tenant read leaked across orgs: err=%v", err)
	}
	missing := "sha256:" + string(bytes.Repeat([]byte("a"), 64))
	if _, err := s.GetTaskBundle(orgA, missing); err != ErrTaskBundleNotFound {
		t.Fatalf("missing digest: err=%v", err)
	}
	if _, err := s.PutTaskBundle(orgA, missing, archive); err == nil {
		t.Fatal("postgres accepted bytes that do not hash to the claimed digest")
	}
	created, err = s.PutTaskBundle(orgB, digest, archive)
	if err != nil || !created {
		t.Fatalf("same digest, new tenant: created=%v err=%v (must be a fresh key)", created, err)
	}
	got, err := s.GetTaskBundle(orgA, digest)
	if err != nil || !bytes.Equal(got, archive) {
		t.Fatalf("org-a's copy after org-b wrote the same digest: %v", err)
	}
}
