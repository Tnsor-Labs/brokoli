package dbdialect

import "testing"

// ADR-027 phase 1. The live proofs are in engine/clickhouse_read_test.go;
// these pin the SQL shapes and the type table.

func TestClickHouseQuoting(t *testing.T) {
	d, ok := For("clickhouse")
	if !ok {
		t.Fatal("clickhouse dialect not registered")
	}
	if got := d.QuoteIdent("sel`ect"); got != "`sel``ect`" {
		t.Errorf("QuoteIdent = %q", got)
	}
	if got := d.QuoteQualifiedIdent("db.orders"); got != "`db`.`orders`" {
		t.Errorf("QuoteQualifiedIdent = %q", got)
	}
	// Backslash escaped before the quote is doubled -- the MySQL rule,
	// because ClickHouse also treats backslash as an escape.
	if got := d.QuoteLiteral(`x\'; DROP TABLE t; --`); got != `'x\\''; DROP TABLE t; --'` {
		t.Errorf("QuoteLiteral = %q", got)
	}
}

// Registration claims read-and-write but never pushdown: no Addresser
// means sameServer can never answer yes, so no segment can form. Since
// phase 4 this is a MEASURED refusal, not only a structural one --
// TestClickHouseInsertSelectReportsNoCount (engine) pins that clickhouse-go
// reports no row count for INSERT ... SELECT, so a ClickHouse segment can
// never satisfy ADR-023's free-count-source rule and its executor would
// fail every segment the planner formed. Behind that stand two more
// blockers: no transaction (the commit-is-the-artifact resume rule cannot
// hold) and no lock primitive (an overwrite segment cannot serialize).
// Adding Addresser is correct only after the count measurement flips AND
// the differential corpus passes against a live server.
func TestClickHouseClaimsNoAddresser(t *testing.T) {
	d, _ := For("clickhouse")
	if _, ok := d.(Addresser); ok {
		t.Fatal("clickhouse implements Addresser; the pushdown refusal is measured (see " +
			"TestClickHouseInsertSelectReportsNoCount) and may only be lifted when that " +
			"measurement flips and the differential corpus passes (ADR-024, ADR-027)")
	}
}

func TestClickHouseClassifyType(t *testing.T) {
	d, _ := For("clickhouse")
	for name, want := range map[string]ColumnKind{
		"Int64":                            KindNumeric,
		"UInt32":                           KindNumeric,
		"Float64":                          KindNumeric,
		"Nullable(Int64)":                  KindNumeric,
		"LowCardinality(String)":           KindText,
		"Nullable(LowCardinality(String))": KindText,
		"String":                           KindText,
		// Each with its reason in ClassifyType's comment.
		"Decimal(12, 2)":  KindUnclassified,
		"FixedString(16)": KindUnclassified,
		"DateTime":        KindUnclassified,
		"UUID":            KindUnclassified,
		"Array(String)":   KindUnclassified,
	} {
		if got := d.ClassifyType(name); got != want {
			t.Errorf("ClassifyType(%q) = %v, want %v", name, got, want)
		}
	}
}

// The canonical-type table, wrappers included. What the migrate read side
// stands on (#360): a wrong row here becomes a wrong destination column.
func TestClickHouseCanonicalType(t *testing.T) {
	d, _ := For("clickhouse")
	r, ok := d.(TypeReader)
	if !ok {
		t.Fatal("clickhouse must be a TypeReader; the migrate read side depends on it")
	}
	read := func(name string) ColumnType {
		return r.CanonicalType(name, 0, 0, false, 0, false, false)
	}

	cases := map[string]ColumnType{
		"Bool":  {Class: TypeBool},
		"Int8":  {Class: TypeInt, Bits: 16},
		"Int32": {Class: TypeInt, Bits: 32},
		"Int64": {Class: TypeInt, Bits: 64},
		// Unsigned widens, the MySQL rule; UInt64 stays 64.
		"UInt8":           {Class: TypeInt, Bits: 16},
		"UInt16":          {Class: TypeInt, Bits: 32},
		"UInt32":          {Class: TypeInt, Bits: 64},
		"UInt64":          {Class: TypeInt, Bits: 64},
		"Float32":         {Class: TypeFloat, Bits: 32},
		"Float64":         {Class: TypeFloat, Bits: 64},
		"Decimal(12, 2)":  {Class: TypeDecimal, Precision: 12, Scale: 2},
		"Decimal64(4)":    {Class: TypeDecimal, Precision: 18, Scale: 4},
		"String":          {Class: TypeText},
		"Enum8('a' = 1)":  {Class: TypeText},
		"FixedString(16)": {Class: TypeBytes, Length: 16},
		"Date":            {Class: TypeDate},
		// An epoch instant: timestamptz semantics, not a wall clock.
		"DateTime":             {Class: TypeTimestampTZ},
		"DateTime64(3)":        {Class: TypeTimestampTZ},
		"DateTime64(3, 'UTC')": {Class: TypeTimestampTZ},
		"UUID":                 {Class: TypeUUID},
		"Array(String)":        {Class: TypeUnknown},
		"Map(String, Int64)":   {Class: TypeUnknown},
		"Tuple(Int64, String)": {Class: TypeUnknown},
	}
	for name, want := range cases {
		if got := read(name); got != want {
			t.Errorf("CanonicalType(%q) = %+v, want %+v", name, got, want)
		}
	}

	// Nullability comes from the wrapper -- the only way a ClickHouse
	// column is nullable -- regardless of what the driver reported.
	got := read("Nullable(Int64)")
	if got.Class != TypeInt || got.Bits != 64 || !got.Nullable {
		t.Errorf("Nullable(Int64) = %+v, want nullable int64", got)
	}
	if got := read("LowCardinality(Nullable(String))"); !got.Nullable || got.Class != TypeText {
		t.Errorf("LowCardinality(Nullable(String)) = %+v, want nullable text", got)
	}

	// Phase 2 crossed the phase-1 pin deliberately: the TypeRenderer now
	// exists, with the equivalence tests in engine/clickhouse_append_test.go
	// as the proof the pin demanded. Its own table is TestClickHouseDDLType.
	if _, ok := d.(TypeRenderer); !ok {
		t.Fatal("clickhouse must render DDL now; the write path depends on it")
	}
}

// The renderer's table, refusals included.
func TestClickHouseDDLType(t *testing.T) {
	d, _ := For("clickhouse")
	r := d.(TypeRenderer)

	for _, tc := range []struct {
		ct   ColumnType
		want string
		ok   bool
	}{
		{ColumnType{Class: TypeBool}, "Bool", true},
		{ColumnType{Class: TypeInt, Bits: 16}, "Int16", true},
		{ColumnType{Class: TypeInt, Bits: 64}, "Int64", true},
		{ColumnType{Class: TypeInt}, "Int64", true}, // unspecified widens, never narrows
		{ColumnType{Class: TypeFloat, Bits: 32}, "Float32", true},
		{ColumnType{Class: TypeDecimal, Precision: 12, Scale: 2}, "Decimal(12, 2)", true},
		{ColumnType{Class: TypeText}, "String", true},
		{ColumnType{Class: TypeBytes}, "String", true},
		{ColumnType{Class: TypeDate}, "Date32", true},
		{ColumnType{Class: TypeTimestampTZ}, "DateTime64(6, 'UTC')", true},
		{ColumnType{Class: TypeUUID}, "UUID", true},
		// Nullable is part of the type: bare T would turn NULLs into the
		// type's default value, the #360 corruption class.
		{ColumnType{Class: TypeInt, Bits: 64, Nullable: true}, "Nullable(Int64)", true},
		{ColumnType{Class: TypeText, Nullable: true}, "Nullable(String)", true},
		// Refusals, each named in DDLType's comment.
		{ColumnType{Class: TypeTimestamp}, "", false},
		{ColumnType{Class: TypeTime}, "", false},
		{ColumnType{Class: TypeJSON}, "", false},
		{ColumnType{Class: TypeDecimal}, "", false},                // no declared precision
		{ColumnType{Class: TypeDecimal, Precision: 77}, "", false}, // past the 76 cap
		{ColumnType{Class: TypeUnknown}, "", false},
	} {
		got, ok := r.DDLType(tc.ct)
		if got != tc.want || ok != tc.ok {
			t.Errorf("DDLType(%+v) = %q,%v want %q,%v", tc.ct, got, ok, tc.want, tc.ok)
		}
	}
}
