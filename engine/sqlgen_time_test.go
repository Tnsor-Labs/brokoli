package engine

import (
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func timeDS(v interface{}) *common.DataSet {
	return &common.DataSet{Columns: []string{"when"}, Rows: []common.DataRow{{"when": v}}}
}

// Go's default rendering of a time.Time ("2026-08-22 00:00:00 +0000
// UTC") is not valid input for any of these dialects, so a date column
// carried from a database source to a database sink failed on write.
func TestFormatValueRendersTimeForPostgres(t *testing.T) {
	ts := time.Date(2026, 8, 22, 14, 30, 5, 0, time.UTC)
	sql, err := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "t"}, timeDS(ts))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if strings.Contains(sql, "+0000 UTC") {
		t.Fatalf("Go's time formatting leaked into SQL:\n%s", sql)
	}
	if !strings.Contains(sql, "'2026-08-22 14:30:05+00:00'") {
		t.Fatalf("expected an ISO timestamp with offset, got:\n%s", sql)
	}
}

// Dialects whose literal carries no zone get the instant in UTC, so it
// is not silently reinterpreted in the session timezone.
func TestFormatValueNormalisesToUTCForZonelessDialects(t *testing.T) {
	zone := time.FixedZone("UTC+3", 3*60*60)
	ts := time.Date(2026, 8, 22, 17, 30, 5, 0, zone) // 14:30:05 UTC
	for _, d := range []string{"mysql", "sqlserver"} {
		sql, err := GenerateSQL(SQLGenConfig{Dialect: d, Table: "t"}, timeDS(ts))
		if err != nil {
			t.Fatalf("%s generate: %v", d, err)
		}
		if !strings.Contains(sql, "2026-08-22 14:30:05") {
			t.Fatalf("%s: expected the UTC instant, got:\n%s", d, sql)
		}
	}
}

// SQLite's documented storage format is ISO-8601.
func TestFormatValueUsesISOForSQLite(t *testing.T) {
	ts := time.Date(2026, 8, 22, 14, 30, 5, 0, time.UTC)
	sql, err := GenerateSQL(SQLGenConfig{Dialect: "sqlite", Table: "t"}, timeDS(ts))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(sql, "'2026-08-22T14:30:05Z'") {
		t.Fatalf("expected ISO-8601, got:\n%s", sql)
	}
}

// A pointer is followed; a nil pointer is NULL, not the string "<nil>".
func TestFormatValueHandlesTimePointers(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	sql, _ := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "t"}, timeDS(&ts))
	if !strings.Contains(sql, "'2026-01-02 03:04:05+00:00'") {
		t.Fatalf("expected the pointed-to time, got:\n%s", sql)
	}
	var nilTime *time.Time
	sql, _ = GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "t"}, timeDS(nilTime))
	if !strings.Contains(sql, "NULL") {
		t.Fatalf("expected NULL for a nil time pointer, got:\n%s", sql)
	}
}

// Drivers hand back text columns as []byte; %v would render the numbers.
func TestFormatValueRendersByteSlicesAsText(t *testing.T) {
	sql, err := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "t"}, timeDS([]byte("hello")))
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(sql, "'hello'") {
		t.Fatalf("expected the text value, got:\n%s", sql)
	}
}

// Sub-second precision is preserved where the dialect supports it.
func TestFormatValueKeepsFractionalSeconds(t *testing.T) {
	ts := time.Date(2026, 8, 22, 14, 30, 5, 123456000, time.UTC)
	sql, _ := GenerateSQL(SQLGenConfig{Dialect: "postgres", Table: "t"}, timeDS(ts))
	if !strings.Contains(sql, ".123456") {
		t.Fatalf("expected fractional seconds, got:\n%s", sql)
	}
}
