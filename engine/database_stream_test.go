package engine

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// StreamQueryDatabase must return exactly what QueryDatabase returns, or
// the planner's choice of path changes the data — the same equivalence
// property TestStreamTransformToRef_EquivalentToBatchTransform asserts for
// transforms, applied to the source side.
//
// Needs a real server, because the thing being checked is driver value
// conversion across types (timestamps, numerics, NULL against empty
// string), which a fake would define away. Set BROKOLI_TEST_POSTGRES_URL
// to a scratch database to run it; skipped otherwise.
func TestStreamQueryEqualsBatchQuery(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	// Deterministic: two separate executions must be comparable, so no
	// now() and no random().
	q := `SELECT g::bigint AS id, (g%7)::int AS grp,
	         (timestamptz '2026-01-01 00:00:00+00' + ((g%1000)||' minute')::interval) AS ts,
	         round((g%100000)::numeric/100,2) AS amt,
	         CASE WHEN g%13=0 THEN NULL ELSE 'v'||g END AS maybe_null,
	         (g%2=0) AS flag, ''::text AS empty_str
	      FROM generate_series(1,5000) g ORDER BY g`

	batch, err := QueryDatabase(uri, q)
	if err != nil {
		t.Fatal(err)
	}

	var streamed []common.DataRow
	cols, n, err := StreamQueryDatabase(context.Background(), uri, q, 137, func(b *common.DataSet) error {
		// Copy: the batch is reused after this returns.
		streamed = append(streamed, append([]common.DataRow(nil), b.Rows...)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if n != int64(len(batch.Rows)) {
		t.Fatalf("streamed count %d != batch count %d", n, len(batch.Rows))
	}
	if fmt.Sprint(cols) != fmt.Sprint(batch.Columns) {
		t.Fatalf("columns differ:\n stream %v\n batch  %v", cols, batch.Columns)
	}
	if len(streamed) != len(batch.Rows) {
		t.Fatalf("row counts differ: %d vs %d", len(streamed), len(batch.Rows))
	}
	for i := range batch.Rows {
		for _, c := range batch.Columns {
			want, got := batch.Rows[i][c], streamed[i][c]
			if fmt.Sprintf("%#v", want) != fmt.Sprintf("%#v", got) {
				t.Fatalf("row %d column %q: batch %#v, streamed %#v", i, c, want, got)
			}
		}
	}
	t.Logf("%d rows identical across both paths, %d columns, batch size 137", len(streamed), len(cols))
}
