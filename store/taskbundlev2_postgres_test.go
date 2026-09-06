package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
)

// Live-Postgres coverage of the TaskBundleV2Store capability. Same
// skip-if-unreachable discipline as taskbundle_postgres_test.go.
func openTaskBundleV2TestPostgresStore(t *testing.T) *PostgresStore {
	t.Helper()
	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres@localhost:5432/brokoli_leader_test?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s, err := NewPostgresStore(dsn)
	if err != nil {
		t.Skipf("skipping live-Postgres task bundle v2 test: %v", err)
	}
	if err := s.db.PingContext(ctx); err != nil {
		s.Close()
		t.Skipf("skipping live-Postgres task bundle v2 test: no reachable Postgres at %s", dsn)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func pgScopedOrgsV2(t *testing.T, s *PostgresStore, n int) []string {
	t.Helper()
	base := fmt.Sprintf("pg-tbv2-%d-", time.Now().UnixNano())
	orgs := make([]string, n)
	for i := range orgs {
		orgs[i] = fmt.Sprintf("%s%d", base, i)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for _, org := range orgs {
			_, _ = s.db.ExecContext(ctx, `DELETE FROM task_bundles_v2 WHERE org_id = $1`, org)
		}
	})
	return orgs
}

func TestTaskBundleV2Store_PostgresSemantics(t *testing.T) {
	s := openTaskBundleV2TestPostgresStore(t)
	archive := buildV2Archive(t, "def run():\n    return 1\n")
	digest := taskbundlev2.DigestOf(archive)

	orgs := pgScopedOrgsV2(t, s, 2)
	orgA, orgB := orgs[0], orgs[1]

	created, err := s.PutTaskBundleV2(orgA, digest, archive)
	if err != nil || !created {
		t.Fatalf("first postgres upload: created=%v err=%v", created, err)
	}
	created, err = s.PutTaskBundleV2(orgA, digest, archive)
	if err != nil || created {
		t.Fatalf("byte-identical re-upload: created=%v err=%v (want false,nil)", created, err)
	}
	if _, err := s.GetTaskBundleV2(orgB, digest); err != ErrTaskBundleV2NotFound {
		t.Fatalf("tenant read leaked across orgs: err=%v", err)
	}
	missing := "sha256:" + strings.Repeat("a", 64)
	if _, err := s.GetTaskBundleV2(orgA, missing); err != ErrTaskBundleV2NotFound {
		t.Fatalf("missing digest: err=%v", err)
	}
	if _, err := s.PutTaskBundleV2(orgA, missing, archive); err == nil {
		t.Fatal("postgres accepted bytes that do not hash to the claimed digest")
	}
	created, err = s.PutTaskBundleV2(orgB, digest, archive)
	if err != nil || !created {
		t.Fatalf("same digest, new tenant: created=%v err=%v (must be a fresh key)", created, err)
	}
	got, err := s.GetTaskBundleV2(orgA, digest)
	if err != nil || string(got) != string(archive) {
		t.Fatalf("org-a's copy after org-b wrote the same digest: %v", err)
	}
}
