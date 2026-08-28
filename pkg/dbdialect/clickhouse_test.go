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

// Registration claims read-side only: no Addresser means sameServer can
// never answer yes, so no pushdown segment can form. This is ADR-027's
// "pushdown starts fully refused" as a structural fact -- if someone adds
// Addresser without the corpus, this fails and points at the rule.
func TestClickHouseClaimsNoAddresser(t *testing.T) {
	d, _ := For("clickhouse")
	if _, ok := d.(Addresser); ok {
		t.Fatal("clickhouse implements Addresser; pushdown may only be claimed with the " +
			"differential corpus passing against a live server (ADR-024, ADR-027 phase 3+)")
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

	// And clickhouse deliberately does NOT render DDL yet: the write path
	// is phase 2, and a TypeRenderer here without its equivalence tests
	// would be a capability claimed without proof.
	if _, ok := d.(TypeRenderer); ok {
		t.Fatal("clickhouse implements TypeRenderer; DDL rendering is phase 2 and needs its tests first")
	}
}
