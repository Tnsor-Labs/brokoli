package engine

import (
	"math"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
)

// inferColumnType parsed values with a full 64-bit ParseInt and then
// declared the column INTEGER, which every dialect renders as a 32-bit
// type. A table created from a bigint column could not hold its own
// rows: the script generated cleanly and failed on load with "integer
// out of range" (#547).
func TestInferColumnTypeWidensPastInt32(t *testing.T) {
	cases := []struct {
		name string
		vals []interface{}
		want string
	}{
		{"small ints stay INTEGER", []interface{}{1, 2, 3}, "INTEGER"},
		{"int32 boundary stays INTEGER", []interface{}{math.MaxInt32, 1}, "INTEGER"},
		{"one past int32 widens", []interface{}{int64(math.MaxInt32) + 1, 1}, "BIGINT"},
		{"negative past int32 widens", []interface{}{int64(math.MinInt32) - 1, 1}, "BIGINT"},
		{"past 2^53 widens", []interface{}{int64(9007199254740994), 1}, "BIGINT"},
		{"floats are unaffected", []interface{}{1.5, 2.5}, "FLOAT"},
		{"text is unaffected", []interface{}{"a", "b"}, "TEXT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows := make([]common.DataRow, len(tc.vals))
			for i, v := range tc.vals {
				rows[i] = common.DataRow{"c": v}
			}
			if got := inferColumnType("c", rows); got != tc.want {
				t.Errorf("inferColumnType = %q, want %q", got, tc.want)
			}
		})
	}
}

// A pseudo-type missing from a dialect's TypeMap renders as TEXT rather
// than failing, so a dialect that forgot BIGINT would silently declare a
// numeric column as text. Check the whole registry rather than the
// handful of dialects that happened to come to mind.
func TestEveryDialectRendersBigint(t *testing.T) {
	names := []string{"postgres", "mysql", "sqlite", "sqlserver", "clickhouse", "generic"}
	for _, name := range names {
		d, ok := dbdialect.For(name)
		if !ok {
			t.Fatalf("dialect %q is not registered; update this list or the registry", name)
		}
		sw, ok := d.(dbdialect.StatementWriter)
		if !ok {
			t.Fatalf("dialect %q has no write vocabulary", name)
		}
		tm := sw.WriteSyntax().TypeMap
		got, present := tm["BIGINT"]
		if !present || got == "" {
			t.Errorf("dialect %q has no BIGINT in its TypeMap; a column past int32 "+
				"would silently render as TEXT", name)
			continue
		}
		if strings.EqualFold(got, "TEXT") {
			t.Errorf("dialect %q maps BIGINT to %q", name, got)
		}
	}
}

// The end-to-end shape of the bug: DDL and rows generated together must
// agree, so the declared type has to admit the values in the INSERTs.
func TestGenerateSQLCreateTableAdmitsItsOwnRows(t *testing.T) {
	const wide = int64(9007199254740994)
	ds := &common.DataSet{
		Columns: []string{"id", "external_id", "name"},
		Rows: []common.DataRow{
			{"id": 1, "external_id": wide, "name": "Ada"},
			{"id": 2, "external_id": wide + 1, "name": "Grace"},
		},
	}

	for _, dialectName := range []string{"postgres", "mysql", "sqlserver", "generic"} {
		t.Run(dialectName, func(t *testing.T) {
			out, err := GenerateSQL(SQLGenConfig{
				Dialect: dialectName, Table: "t", CreateTable: true,
			}, ds)
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}
			ddl := out[:strings.Index(out, ";")]
			if !strings.Contains(strings.ToUpper(ddl), "BIGINT") {
				t.Errorf("external_id holds %d but the DDL declares no BIGINT:\n%s", wide, ddl)
			}
			// The value must still appear exactly; widening the declared
			// type must not change how the literal is written.
			if !strings.Contains(out, "9007199254740994") {
				t.Errorf("the exact value is missing from the INSERT:\n%s", out)
			}
		})
	}
}
