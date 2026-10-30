package engine

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// A nested value reaches Postgres as JSON that Postgres itself parses,
// through both writers: COPY and the statement path. A jsonb column
// refuses anything else, and Go's map[...] syntax was what it got.
func TestNestedValuesRoundTripThroughPostgresAsJSON(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const table = "nested_values_json_test"
	if _, err := db.Exec(`DROP TABLE IF EXISTS ` + table); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE ` + table + ` (id bigint, payload jsonb)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS ` + table) })

	row := func(id int64) common.DataRow {
		return common.DataRow{"id": id, "payload": map[string]interface{}{
			"tags": []interface{}{"a", "x<y & z"}, "big": int64(9007199254740993),
		}}
	}
	cols := []string{"id", "payload"}
	cfg := SQLGenConfig{Dialect: "postgres", Table: table, Mode: ModeAppend}

	if _, err := copyBatchesToPostgres(context.Background(), uri, cfg, cols, oneShot(&common.DataSet{Columns: cols, Rows: []common.DataRow{row(1)}})); err != nil {
		t.Fatalf("COPY: %v", err)
	}
	stmt, err := GenerateSQL(cfg, &common.DataSet{Columns: cols, Rows: []common.DataRow{row(2)}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteSQL(uri, stmt); err != nil {
		t.Fatalf("statement path: %v", err)
	}

	rows, err := db.Query(`SELECT id, payload->'tags'->>1, (payload->>'big') FROM ` + table + ` ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id int64
		var tag, big string
		if err := rows.Scan(&id, &tag, &big); err != nil {
			t.Fatal(err)
		}
		if tag != "x<y & z" || big != "9007199254740993" {
			t.Errorf("row %d read back tag %q, big %q", id, tag, big)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("%d rows, want 2", n)
	}
}
