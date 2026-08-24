package engine

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// Two pipelines overwriting the same table at once must both succeed.
//
// An overwrite says "this table's contents become exactly these rows", and
// two of those running together have no defined outcome unless they
// serialize. Unserialized they interleave and fail two different ways,
// both observed on a cluster with four pipelines writing one table:
// "deadlock detected (40P01)", and "duplicate key value violates unique
// constraint" — the latter because one transaction's DELETE cannot see the
// other's uncommitted rows, so both insert the same keys. Neither is
// something the pipeline author can fix.
//
// Needs a real server: the thing under test is lock behaviour between
// concurrent transactions, which a fake defines away.
func TestConcurrentOverwritesDoNotCollide(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const table = "copy_concurrent_test"
	if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
		t.Fatal(err)
	}
	// A primary key is what turns an interleaved overwrite into a visible
	// failure rather than silent duplication.
	if _, err := db.Exec(`CREATE TABLE ` + table + ` (id bigint PRIMARY KEY, payload text)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + table) })

	rows := func(n int, tag string) *common.DataSet {
		ds := &common.DataSet{Columns: []string{"id", "payload"}}
		for i := 0; i < n; i++ {
			ds.Rows = append(ds.Rows, common.DataRow{"id": i, "payload": fmt.Sprintf("%s-%d", tag, i)})
		}
		return ds
	}
	cfg := SQLGenConfig{Dialect: "postgres", Table: table, Mode: ModeOverwrite}

	// Deliberately overlapping key ranges: without serialization the two
	// writers insert the same ids and one loses.
	const writers, attempts = 4, 5
	for attempt := 0; attempt < attempts; attempt++ {
		var wg sync.WaitGroup
		errs := make([]error, writers)
		for w := 0; w < writers; w++ {
			wg.Add(1)
			go func(w int) {
				defer wg.Done()
				_, errs[w] = copyBatchesToPostgres(context.Background(), uri, cfg,
					[]string{"id", "payload"}, oneShot(rows(2000, fmt.Sprintf("w%d", w))))
			}(w)
		}
		wg.Wait()
		for w, err := range errs {
			if err != nil {
				t.Fatalf("attempt %d, writer %d: concurrent overwrites must serialize, not fail: %v", attempt, w, err)
			}
		}
	}

	// Exactly one writer's rows survive — a whole-table overwrite leaves
	// the table as one writer wrote it, not a mixture.
	var total, distinctTags int
	if err := db.QueryRow(`SELECT count(*), count(DISTINCT split_part(payload,'-',1)) FROM `+table).Scan(&total, &distinctTags); err != nil {
		t.Fatal(err)
	}
	if total != 2000 {
		t.Errorf("row count = %d, want 2000 (one writer's contents)", total)
	}
	if distinctTags != 1 {
		t.Errorf("found %d writers' rows mixed together; an overwrite should leave exactly one", distinctTags)
	}
}

// oneShot adapts a materialized dataset to the batch puller.
func oneShot(ds *common.DataSet) func() (*common.DataSet, error) {
	sent := false
	return func() (*common.DataSet, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return ds, nil
	}
}
