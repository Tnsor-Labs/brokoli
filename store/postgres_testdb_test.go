package store

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var throwawayDatabaseSeq atomic.Int64

// throwawayPostgresURL creates a database of its own for a test that
// changes the store's schema (forgetMigrations, dropping an index or a
// constraint) and returns its URL; the database is dropped when the test
// ends. Such a test must never run on BROKOLI_TEST_POSTGRES_URL itself: go
// test runs packages in parallel against that one database, so dropping
// schema_migrations there made another package's store fail to open with
// "relation schema_migrations does not exist". envs names the variables to
// take the server from, first set wins; without any, the test is skipped.
func throwawayPostgresURL(t *testing.T, envs ...string) string {
	t.Helper()
	if len(envs) == 0 {
		envs = []string{"BROKOLI_TEST_POSTGRES_URL"}
	}
	var base string
	for _, name := range envs {
		if base = os.Getenv(name); base != "" {
			break
		}
	}
	if base == "" {
		t.Skipf("%s not set", strings.Join(envs, " or "))
	}
	admin, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("brokoli_t%d_%d_%d", os.Getpid(), time.Now().UnixNano(), throwawayDatabaseSeq.Add(1))
	if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
		admin.Close()
		t.Fatalf("this test changes the store's schema, so it needs a database of its own and CREATEDB: %v", err)
	}
	t.Cleanup(func() {
		defer admin.Close()
		if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			t.Errorf("drop throwaway database %s: %v", name, err)
		}
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}
