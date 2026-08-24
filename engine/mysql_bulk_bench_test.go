package engine

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// The three MySQL write paths against a live server, so the numbers in
// bulk_mysql.go's header can be re-measured rather than believed. Run with:
//
//	BROKOLI_TEST_MYSQL_URL=... go test ./engine/ -run xxx \
//	  -bench BenchmarkMySQLWritePaths -benchtime 1x
func BenchmarkMySQLWritePaths(b *testing.B) {
	uriEnv := os.Getenv("BROKOLI_TEST_MYSQL_URL")
	if uriEnv == "" {
		b.Skip("BROKOLI_TEST_MYSQL_URL not set")
	}
	db, err := sql.Open("mysql", strings.TrimPrefix(uriEnv, "mysql://"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	const rows = 1_000_000
	const batch = 5000
	mkNext := func() func() (*common.DataSet, error) {
		sent := 0
		return func() (*common.DataSet, error) {
			if sent >= rows {
				return nil, io.EOF
			}
			ds := &common.DataSet{Columns: []string{"id", "v"}}
			for i := 0; i < batch && sent < rows; i++ {
				ds.Rows = append(ds.Rows, common.DataRow{
					"id": sent, "v": fmt.Sprintf("value-%d-with-some-padding-to-be-realistic", sent)})
				sent++
			}
			return ds, nil
		}
	}
	reset := func(name string) {
		if _, err := db.Exec("DROP TABLE IF EXISTS " + name); err != nil {
			b.Fatal(err)
		}
		if _, err := db.Exec("CREATE TABLE " + name + " (id INT, v TEXT)"); err != nil {
			b.Fatal(err)
		}
	}

	b.Run("load_data", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			reset("bench_load")
			b.ResetTimer()
			n, err := loadBatchesToMySQL(context.Background(), uriEnv,
				SQLGenConfig{Dialect: "mysql", Table: "bench_load", Mode: ModeAppend},
				[]string{"id", "v"}, mkNext())
			if err != nil || n != rows {
				b.Fatalf("n=%d err=%v", n, err)
			}
		}
	})

	b.Run("insert_fallback", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			reset("bench_ins")
			b.ResetTimer()
			conn, err := db.Conn(context.Background())
			if err != nil {
				b.Fatal(err)
			}
			tx, err := conn.BeginTx(context.Background(), nil)
			if err != nil {
				b.Fatal(err)
			}
			n, err := execInsertBatches(context.Background(), tx, getDialect("mysql"), "bench_ins", []string{"id", "v"}, mkNext())
			if err != nil || n != rows {
				b.Fatalf("n=%d err=%v", n, err)
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			conn.Close()
		}
	})

	b.Run("statement_path", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			reset("bench_stmt")
			// The pre-Phase-3 shape: materialize everything, render one SQL
			// text, execute it. Included so the comparison is against what
			// MySQL sinks actually did.
			ds := &common.DataSet{Columns: []string{"id", "v"}}
			next := mkNext()
			for {
				chunk, err := next()
				if err == io.EOF {
					break
				}
				ds.Rows = append(ds.Rows, chunk.Rows...)
			}
			b.ResetTimer()
			stmt, err := GenerateSQL(SQLGenConfig{Dialect: "mysql", Table: "bench_stmt", Mode: ModeAppend, BatchSize: batch}, ds)
			if err != nil {
				b.Fatal(err)
			}
			if _, err := ExecuteSQL(uriEnv, stmt); err != nil {
				b.Fatal(err)
			}
		}
	})
}
