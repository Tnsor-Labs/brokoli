package dbdialect

import (
	"strconv"
	"strings"
)

// CanonicalType reads clickhouse-go's DatabaseTypeName spellings, which are
// ClickHouse's own type names with their wrappers still on: Nullable(Int64),
// LowCardinality(String), Decimal(12, 2). The wrappers carry information --
// Nullable is the ONLY way a ClickHouse column is nullable, since columns
// are non-nullable by default (the inverse of every other backend) -- so
// nullability is taken from the wrapper first and the driver's report is
// only a fallback.
//
// Precision, scale and width are parsed from the type name itself rather
// than trusted to the driver's DecimalSize/Length, because the name is the
// server's own statement of them and is always present, where driver
// support for the accessor methods varies by type.
//
// Mappings that need their reasons stated:
//
//   - Unsigned widths widen (UInt8 -> 16 bits, UInt16 -> 32, UInt32 -> 64),
//     the same rule the MySQL reader applies: an unsigned column holds
//     values a signed one of the same width cannot. UInt64 stays 64 -- its
//     top half has no faithful signed home, matching the MySQL precedent.
//   - DateTime and DateTime64 map to TypeTimestampTZ, not TypeTimestamp.
//     A ClickHouse DateTime is an epoch instant -- the column's zone only
//     changes how it renders, not what it identifies -- and that is
//     timestamptz's semantics. Mapping it to zoneless TypeTimestamp would
//     invite a destination to store the rendering and lose the instant.
//   - FixedString(N) is TypeBytes, not TypeText with a length: it is
//     NUL-padded to exactly N bytes and holds arbitrary bytes, which is
//     bytes semantics wearing a string's name.
//   - Enum8/Enum16 are TypeText: the driver delivers the label, not the
//     numeric code, and the label is what a destination should store.
//   - Everything else -- Array, Map, Tuple, IPv4/IPv6, geo types --
//     is TypeUnknown, and a consumer refuses by name rather than guessing.
func (clickhouse) CanonicalType(name string, precision, scale int64, precisionOK bool, length int64, lengthOK bool, nullable bool) ColumnType {
	raw := strings.TrimSpace(name)
	if strings.HasPrefix(raw, "Nullable(") || strings.Contains(raw, "(Nullable(") {
		nullable = true
	}
	unwrapped := unwrapClickHouseType(raw)

	base, args := splitClickHouseType(unwrapped)
	c := ColumnType{Nullable: nullable}

	switch base {
	case "Bool":
		c.Class = TypeBool
	case "Int8", "Int16":
		c.Class, c.Bits = TypeInt, 16
	case "Int32":
		c.Class, c.Bits = TypeInt, 32
	case "Int64":
		c.Class, c.Bits = TypeInt, 64
	case "UInt8":
		c.Class, c.Bits = TypeInt, 16
	case "UInt16":
		c.Class, c.Bits = TypeInt, 32
	case "UInt32", "UInt64":
		c.Class, c.Bits = TypeInt, 64
	case "Float32":
		c.Class, c.Bits = TypeFloat, 32
	case "Float64":
		c.Class, c.Bits = TypeFloat, 64
	case "Decimal":
		c.Class = TypeDecimal
		if len(args) == 2 {
			c.Precision, _ = strconv.Atoi(args[0])
			c.Scale, _ = strconv.Atoi(args[1])
		} else if precisionOK {
			c.Precision, c.Scale = int(precision), int(scale)
		}
	case "Decimal32", "Decimal64", "Decimal128", "Decimal256":
		// Decimal32(S) etc. fix the precision by width and declare scale.
		c.Class = TypeDecimal
		switch base {
		case "Decimal32":
			c.Precision = 9
		case "Decimal64":
			c.Precision = 18
		case "Decimal128":
			c.Precision = 38
		case "Decimal256":
			c.Precision = 76
		}
		if len(args) == 1 {
			c.Scale, _ = strconv.Atoi(args[0])
		}
	case "String":
		c.Class = TypeText
	case "Enum8", "Enum16":
		c.Class = TypeText
	case "FixedString":
		c.Class = TypeBytes
		if len(args) == 1 {
			c.Length, _ = strconv.Atoi(args[0])
		}
	case "Date", "Date32":
		c.Class = TypeDate
	case "DateTime", "DateTime64":
		c.Class = TypeTimestampTZ
	case "UUID":
		c.Class = TypeUUID
	case "JSON":
		c.Class = TypeJSON
	default:
		c.Class = TypeUnknown
	}
	return c
}

// splitClickHouseType separates "Decimal(12, 2)" into "Decimal" and
// ["12", "2"]. Arguments that are not simple comma-separated tokens -- an
// Enum's value list, a DateTime64 zone -- come back as-is and the caller
// ignores what it does not need.
func splitClickHouseType(name string) (string, []string) {
	i := strings.IndexByte(name, '(')
	if i < 0 || !strings.HasSuffix(name, ")") {
		return name, nil
	}
	base := name[:i]
	inner := name[i+1 : len(name)-1]
	parts := strings.Split(inner, ",")
	args := make([]string, 0, len(parts))
	for _, p := range parts {
		args = append(args, strings.TrimSpace(p))
	}
	return base, args
}

// DDLType renders a canonical type as ClickHouse DDL (ADR-027 phase 2 --
// this is the TypeRenderer whose absence phase 1 pinned, crossed here with
// the equivalence tests the pin demanded).
//
// Nullability is part of the type, not a suffix: ClickHouse columns are
// non-nullable by default, and Nullable(T) is the only way to hold a NULL.
// A nullable canonical type therefore renders wrapped -- returning bare T
// would make every NULL from the source either an insert error or, worse,
// the type's default value, which is the #360 corruption class.
//
// Refusals, each with its reason:
//
//   - TypeTimestamp (zoneless): a ClickHouse DateTime is an epoch instant,
//     and storing a wall-clock reading into one forces an interpretation
//     the source never made. The operator chooses -- usually by casting to
//     an instant at the source -- rather than this code guessing UTC.
//   - TypeDecimal beyond 76 digits, or with no declared precision:
//     Decimal's cap is 76, and ClickHouse has no unconstrained exact
//     numeric -- the same rule as MySQL's renderer.
//   - TypeTime: ClickHouse has no time-of-day type.
//   - TypeJSON: the JSON column type is not yet something to build on.
//
// Renderings that need their reasons stated:
//
//   - TypeDate renders Date32, not Date: Date stops at 2149 and starts at
//     1970, where Date32 spans 1900-2299 -- the wider window loses fewer
//     source values, and both cost 4 bytes... Date costs 2, which is the
//     one thing Date32 gives up.
//   - TypeTimestampTZ renders DateTime64(6, 'UTC'): microsecond precision
//     matches what the other backends carry, and the named zone makes the
//     column's rendering deterministic regardless of server timezone. The
//     zone does not change what is stored -- an epoch instant either way.
//   - TypeBytes renders String: a ClickHouse String is a byte string, so
//     this is the faithful home, not a substitution.
func (clickhouse) DDLType(c ColumnType) (string, bool) {
	var base string
	switch c.Class {
	case TypeBool:
		base = "Bool"
	case TypeInt:
		switch {
		case c.Bits <= 16 && c.Bits > 0:
			base = "Int16"
		case c.Bits <= 32 && c.Bits > 0:
			base = "Int32"
		default:
			base = "Int64"
		}
	case TypeFloat:
		if c.Bits == 32 {
			base = "Float32"
		} else {
			base = "Float64"
		}
	case TypeDecimal:
		if c.Precision <= 0 || c.Precision > 76 {
			return "", false
		}
		s := c.Scale
		if s > c.Precision {
			s = c.Precision
		}
		base = "Decimal(" + itoa(c.Precision) + ", " + itoa(s) + ")"
	case TypeText, TypeBytes:
		base = "String"
	case TypeDate:
		base = "Date32"
	case TypeTimestampTZ:
		base = "DateTime64(6, 'UTC')"
	case TypeUUID:
		base = "UUID"
	default:
		return "", false
	}
	if c.Nullable {
		return "Nullable(" + base + ")", true
	}
	return base, true
}

// itoa avoids importing strconv twice under different names in reviews;
// identical to strconv.Itoa.
func itoa(n int) string { return strconv.Itoa(n) }
