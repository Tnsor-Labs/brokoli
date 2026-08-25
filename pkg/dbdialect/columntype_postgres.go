package dbdialect

import (
	"fmt"
	"strings"
)

// CanonicalType reads pgx's DatabaseTypeName spellings. pgx reports the
// Postgres type name, so INT8/BIGINT, FLOAT8/DOUBLE PRECISION and so on both
// appear depending on how the column was declared.
func (postgres) CanonicalType(name string, precision, scale int64, precisionOK bool, length int64, lengthOK bool, nullable bool) ColumnType {
	c := ColumnType{Nullable: nullable}
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "BOOL", "BOOLEAN":
		c.Class = TypeBool
	case "INT2", "SMALLINT":
		c.Class, c.Bits = TypeInt, 16
	case "INT4", "INTEGER", "INT", "SERIAL":
		c.Class, c.Bits = TypeInt, 32
	case "INT8", "BIGINT", "BIGSERIAL":
		c.Class, c.Bits = TypeInt, 64
	case "FLOAT4", "REAL":
		c.Class, c.Bits = TypeFloat, 32
	case "FLOAT8", "DOUBLE PRECISION":
		c.Class, c.Bits = TypeFloat, 64
	case "NUMERIC", "DECIMAL", "MONEY":
		c.Class = TypeDecimal
		if precisionOK {
			c.Precision, c.Scale = int(precision), int(scale)
		}
	case "TEXT", "VARCHAR", "CHAR", "BPCHAR", "NAME", "CITEXT":
		c.Class = TypeText
		if lengthOK {
			c.Length = int(length)
		}
	case "BYTEA":
		c.Class = TypeBytes
	case "DATE":
		c.Class = TypeDate
	case "TIME", "TIMETZ":
		c.Class = TypeTime
	case "TIMESTAMP":
		c.Class = TypeTimestamp
	case "TIMESTAMPTZ":
		c.Class = TypeTimestampTZ
	case "JSON", "JSONB":
		c.Class = TypeJSON
	case "UUID":
		c.Class = TypeUUID
	default:
		c.Class = TypeUnknown
	}
	return c
}

// DDLType renders a canonical type as Postgres DDL.
func (postgres) DDLType(c ColumnType) (string, bool) {
	switch c.Class {
	case TypeBool:
		return "BOOLEAN", true
	case TypeInt:
		switch {
		case c.Bits <= 16 && c.Bits > 0:
			return "SMALLINT", true
		case c.Bits <= 32 && c.Bits > 0:
			return "INTEGER", true
		default:
			// Unspecified width renders as the widest, never the
			// narrowest: a too-wide column costs storage, a too-narrow
			// one costs the run.
			return "BIGINT", true
		}
	case TypeFloat:
		if c.Bits == 32 {
			return "REAL", true
		}
		return "DOUBLE PRECISION", true
	case TypeDecimal:
		if c.Precision > 0 {
			return fmt.Sprintf("NUMERIC(%d,%d)", c.Precision, c.Scale), true
		}
		// Unconstrained NUMERIC is exact and arbitrary-precision in
		// Postgres, so an unknown precision has a faithful home here.
		return "NUMERIC", true
	case TypeText:
		if c.Length > 0 {
			return fmt.Sprintf("VARCHAR(%d)", c.Length), true
		}
		return "TEXT", true
	case TypeBytes:
		return "BYTEA", true
	case TypeDate:
		return "DATE", true
	case TypeTime:
		return "TIME", true
	case TypeTimestamp:
		return "TIMESTAMP", true
	case TypeTimestampTZ:
		return "TIMESTAMPTZ", true
	case TypeJSON:
		return "JSONB", true
	case TypeUUID:
		return "UUID", true
	default:
		return "", false
	}
}
