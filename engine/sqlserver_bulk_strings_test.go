package engine

import (
	"context"
	"database/sql"
	"io"
	"os"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestSQLServerBulkValueConvertsStringsLikeTheServer(t *testing.T) {
	for _, tc := range []struct {
		in, typ string
		want    any // nil means refused
	}{
		{"42", "int", int64(42)},
		{" -7 ", "bigint", int64(-7)},
		{"+3", "smallint", int64(3)},
		{"", "int", int64(0)},
		{"1.5", "int", nil},
		{"x", "bigint", nil},
		{"2.5", "float", 2.5},
		{" 1e3 ", "real", 1000.0},
		{"", "float", 0.0},
		{"NaN", "float", nil},
		{"TRUE", "bit", true},
		{"0", "bit", false},
		{"", "bit", false},
		{"yes", "bit", nil},
		// Other columns take the string as it is.
		{"2026-10-31", "datetime2", "2026-10-31"},
		{" padded ", "nvarchar", " padded "},
		{"12.30", "decimal", "12.30"},
	} {
		got, err := sqlServerBulkValue(tc.in, tc.typ)
		if tc.want == nil {
			if err == nil {
				t.Errorf("%q into %s = %v, want refused", tc.in, tc.typ, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("%q into %s = %v (%v), want %v", tc.in, tc.typ, got, err, tc.want)
		}
	}
	// A value that is not a string is never touched.
	if got, _ := sqlServerBulkValue(int64(5), "nvarchar"); got != int64(5) {
		t.Errorf("non-string value changed to %v", got)
	}
}

// The bulk path must store what the statement path stores. Each string
// goes into an integer, a float and a bit column twice: as the literal the
// statement path renders, and through bulk copy. Both must fail, or both
// must store the same value.
func TestSQLServerBulkStringsMatchTheStatementPath(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_SQLSERVER_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_SQLSERVER_URL not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	driver, dsn, err := DetectDriver(uri)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	const table = "brokoli_bulk_strings"
	read := func() (string, error) {
		var v sql.NullString
		err := db.QueryRowContext(ctx, "SELECT CONVERT(nvarchar(100), v) FROM "+table).Scan(&v)
		return v.String, err
	}
	reset := func(typ string) {
		if _, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS "+table+"; CREATE TABLE "+table+" (v "+typ+")"); err != nil {
			t.Fatal(err)
		}
	}
	defer db.ExecContext(context.Background(), "DROP TABLE IF EXISTS "+table) //nolint:errcheck

	values := []string{"42", " 42 ", "+42", "-1", "", "1.5", "1e3", " 2.50 ", "x", "1,000",
		"9223372036854775807", "true", "FALSE", "1", "0", " true ", "yes", "300"}
	for _, typ := range []string{"int", "bigint", "tinyint", "float", "real", "bit"} {
		for _, v := range values {
			reset(typ)
			_, stmtErr := db.ExecContext(ctx, "INSERT INTO "+table+" VALUES ("+getDialect("sqlserver").quoteString(v)+")")
			stmtVal, _ := read()

			reset(typ)
			ds := &common.DataSet{Columns: []string{"v"}, Rows: []common.DataRow{{"v": v}}}
			sent := false
			_, bulkErr := copyBatchesToSQLServer(ctx, uri, SQLGenConfig{Table: table, Mode: ModeAppend, Dialect: "sqlserver"}, ds.Columns,
				func() (*common.DataSet, error) {
					if sent {
						return nil, io.EOF
					}
					sent = true
					return ds, nil
				})
			bulkVal, _ := read()

			switch {
			case (stmtErr == nil) != (bulkErr == nil):
				t.Errorf("%q into %s: statement path err=%v, bulk path err=%v; want the same outcome", v, typ, stmtErr, bulkErr)
			case stmtErr == nil && stmtVal != bulkVal:
				t.Errorf("%q into %s: statement path stored %q, bulk path %q", v, typ, stmtVal, bulkVal)
			}
		}
	}
}
