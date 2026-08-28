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
