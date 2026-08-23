package engine

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// COPY treats four characters as structure. Everything else, quotes
// included, is data and travels untouched — the escaping burden is much
// smaller than SQL's because this is not SQL.
func TestCopyEscapeHandlesStructuralCharacters(t *testing.T) {
	cases := map[string]string{
		"plain":            "plain",
		"tab\there":        `tab\there`,
		"new\nline":        `new\nline`,
		"carriage\rreturn": `carriage\rreturn`,
		`back\slash`:       `back\\slash`,
		"quote's fine":     "quote's fine",
		`"double" fine`:    `"double" fine`,
		"semi;colon fine":  "semi;colon fine",
		"unicode ✓ 中文":     "unicode ✓ 中文",
		"":                 "",
	}
	for in, want := range cases {
		if got := copyEscape(in); got != want {
			t.Errorf("copyEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

// nil is NULL, and an empty string is an empty field — the same
// distinction the statement path now preserves.
func TestCopyEscapeNullVersusEmpty(t *testing.T) {
	if got := copyEscape(nil); got != `\N` {
		t.Fatalf("nil = %q, want \\N", got)
	}
	if got := copyEscape(""); got != "" {
		t.Fatalf("empty string = %q, want an empty field", got)
	}
	var nilTime *time.Time
	if got := copyEscape(nilTime); got != `\N` {
		t.Fatalf("nil *time.Time = %q, want \\N", got)
	}
}

// Strings that look like other types stay strings, exactly as in the
// statement path: the server decides what they mean.
func TestCopyEscapePreservesStringsVerbatim(t *testing.T) {
	for _, s := range []string{"00123", "4111111111111111", "1.50", "true", "NULL"} {
		if got := copyEscape(s); got != s {
			t.Errorf("copyEscape(%q) = %q, want it unchanged", s, got)
		}
	}
}

// COPY text format ends its stream at a line containing only `\.`, and
// reads `\N` as NULL. Both are reachable from ordinary data, so neither
// may survive escaping as itself. Verified end to end against Postgres:
// all of these round-trip as the string they started as, while a real
// nil still arrives as NULL.
func TestCopyEscapeNeutralisesCopyFraming(t *testing.T) {
	cases := map[string]string{
		`\.`:   `\\.`,    // end-of-data marker
		`\N`:   `\\N`,    // NULL sentinel, as a literal string
		`\.\n`: `\\.\\n`, // marker followed by a literal backslash-n
		`\`:    `\\`,     // a bare backslash
	}
	for in, want := range cases {
		if got := copyEscape(in); got != want {
			t.Errorf("copyEscape(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCopyEscapeRendersTimeAndNumbers(t *testing.T) {
	ts := time.Date(2026, 8, 23, 14, 30, 5, 0, time.UTC)
	if got := copyEscape(ts); !strings.HasPrefix(got, "2026-08-23 14:30:05") {
		t.Fatalf("time = %q", got)
	}
	if got := copyEscape(42); got != "42" {
		t.Fatalf("int = %q", got)
	}
	if got := copyEscape(true); got != "true" {
		t.Fatalf("bool = %q", got)
	}
}

// The reader emits one tab-separated line per row, in column order.
func TestCopyReaderStreamsRows(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"a", "b"},
		Rows: []common.DataRow{
			{"a": "1", "b": nil},
			{"a": "x\ty", "b": "z"},
		},
	}
	sent := false
	out, err := io.ReadAll(copyReader(ds.Columns, func() (*common.DataSet, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return ds, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := "1\t\\N\nx\\ty\tz\n"
	if string(out) != want {
		t.Fatalf("stream = %q, want %q", out, want)
	}
}

// The fast path covers appends and overwrites on Postgres only.
func TestCanCopyInsertScope(t *testing.T) {
	yes := []SQLGenConfig{
		{Dialect: "postgres", Mode: ModeAppend},
		{Dialect: "postgres", Mode: ModeOverwrite},
		{Dialect: "postgres", Mode: ""},
		// "replace" is sqlgen's alias for overwrite; the COPY path has to
		// keep clearing the table for it, not silently append.
		{Dialect: "postgres", Mode: "replace"},
	}
	for _, cfg := range yes {
		if !canCopyInsert(cfg) {
			t.Errorf("expected COPY for %+v", cfg)
		}
	}
	no := []SQLGenConfig{
		{Dialect: "postgres", Mode: ModeUpsert},                    // needs ON CONFLICT
		{Dialect: "postgres", Mode: ModeAppend, CreateTable: true}, // DDL belongs on the statement path
		{Dialect: "mysql", Mode: ModeAppend},
		{Dialect: "sqlite", Mode: ModeAppend},
		{Dialect: "sqlserver", Mode: ModeAppend},
		{Dialect: "generic", Mode: ModeAppend},
	}
	for _, cfg := range no {
		if canCopyInsert(cfg) {
			t.Errorf("did not expect COPY for %+v", cfg)
		}
	}
}

// An operator can put the statement path back without a different build.
func TestCopyFastPathCanBeDisabled(t *testing.T) {
	t.Setenv("BROKOLI_SINK_COPY", "0")
	if canCopyInsert(SQLGenConfig{Dialect: "postgres", Mode: ModeAppend}) {
		t.Fatal("BROKOLI_SINK_COPY=0 should return to the statement path")
	}
	t.Setenv("BROKOLI_SINK_COPY", "1")
	if !canCopyInsert(SQLGenConfig{Dialect: "postgres", Mode: ModeAppend}) {
		t.Fatal("BROKOLI_SINK_COPY=1 should keep the fast path")
	}
}

// Batches are a transport detail, not a data one: the bytes on the wire
// must be identical whether the rows arrive as one dataset or many. This
// is what lets a sink write a table larger than memory.
func TestCopyReaderJoinsBatchesSeamlessly(t *testing.T) {
	cols := []string{"a", "b"}
	all := []common.DataRow{
		{"a": "1", "b": "x"}, {"a": "2", "b": "y"},
		{"a": "3", "b": "z"}, {"a": "4", "b": nil},
	}

	whole := &common.DataSet{Columns: cols, Rows: all}
	sent := false
	oneShot, err := io.ReadAll(copyReader(cols, func() (*common.DataSet, error) {
		if sent {
			return nil, io.EOF
		}
		sent = true
		return whole, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	i := 0
	batched, err := io.ReadAll(copyReader(cols, func() (*common.DataSet, error) {
		if i >= len(all) {
			return nil, io.EOF
		}
		batch := &common.DataSet{Columns: cols, Rows: all[i : i+1]}
		i++
		return batch, nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	if string(oneShot) != string(batched) {
		t.Fatalf("batching changed the stream:\n one-shot: %q\n batched:  %q", oneShot, batched)
	}
	if string(oneShot) != "1\tx\n2\ty\n3\tz\n4\t\\N\n" {
		t.Fatalf("unexpected stream: %q", oneShot)
	}
}

// A failure partway through the rows must reach the reader as an error
// rather than as a short stream, which Postgres would accept as a
// complete, silently truncated load.
func TestCopyReaderPropagatesProducerError(t *testing.T) {
	cols := []string{"a"}
	calls := 0
	r := copyReader(cols, func() (*common.DataSet, error) {
		calls++
		if calls == 1 {
			return &common.DataSet{Columns: cols, Rows: []common.DataRow{{"a": "1"}}}, nil
		}
		return nil, errors.New("upstream blob read failed")
	})
	_, err := io.ReadAll(r)
	if err == nil {
		t.Fatal("expected the producer error to surface, got a clean EOF")
	}
	if !strings.Contains(err.Error(), "upstream blob read failed") {
		t.Fatalf("error did not carry the cause: %v", err)
	}
}
