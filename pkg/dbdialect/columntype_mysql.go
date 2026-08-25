package dbdialect

import (
	"fmt"
	"strings"
)

// CanonicalType reads go-sql-driver's DatabaseTypeName spellings (fields.go).
// The driver reports UNSIGNED variants as a prefix, which widens the class
// rather than changing it.
func (mysqld) CanonicalType(name string, precision, scale int64, precisionOK bool, length int64, lengthOK bool, nullable bool) ColumnType {
	c := ColumnType{Nullable: nullable}
	n := strings.ToUpper(strings.TrimSpace(name))
	unsigned := strings.HasPrefix(n, "UNSIGNED ")
	n = strings.TrimPrefix(n, "UNSIGNED ")

	switch n {
	case "TINYINT":
		// The driver cannot distinguish TINYINT(1) -- MySQL's BOOLEAN --
		// from a genuine one-byte integer, so this stays an integer.
		// Treating it as a bool would rewrite real numeric data.
		c.Class, c.Bits = TypeInt, 16
	case "SMALLINT", "YEAR":
		c.Class, c.Bits = TypeInt, 16
	case "MEDIUMINT", "INT":
		c.Class, c.Bits = TypeInt, 32
	case "BIGINT":
		c.Class, c.Bits = TypeInt, 64
	case "FLOAT":
		c.Class, c.Bits = TypeFloat, 32
	case "DOUBLE":
		c.Class, c.Bits = TypeFloat, 64
	case "DECIMAL", "NUMERIC":
		c.Class = TypeDecimal
		if precisionOK {
			c.Precision, c.Scale = int(precision), int(scale)
		}
	case "CHAR", "VARCHAR", "TEXT", "TINYTEXT", "MEDIUMTEXT", "LONGTEXT", "ENUM", "SET":
		c.Class = TypeText
		if lengthOK {
			c.Length = int(length)
		}
	case "BINARY", "VARBINARY", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB":
		c.Class = TypeBytes
	case "DATE":
		c.Class = TypeDate
	case "TIME":
		c.Class = TypeTime
	case "DATETIME", "TIMESTAMP":
		// Both are wall-clock as far as the wire is concerned: MySQL's
		// TIMESTAMP converts to the session zone on the way in and out,
		// so what the driver hands over carries no zone of its own.
		c.Class = TypeTimestamp
	case "JSON":
		c.Class = TypeJSON
	default:
		c.Class = TypeUnknown
	}
	if unsigned && c.Class == TypeInt && c.Bits < 64 {
		// An unsigned column holds values a signed one of the same width
		// cannot, so widen rather than risk the top half of the range.
		c.Bits *= 2
	}
	return c
}

// DDLType renders a canonical type as MySQL DDL.
func (mysqld) DDLType(c ColumnType) (string, bool) {
	switch c.Class {
	case TypeBool:
		return "BOOLEAN", true
	case TypeInt:
		switch {
		case c.Bits <= 16 && c.Bits > 0:
			return "SMALLINT", true
		case c.Bits <= 32 && c.Bits > 0:
			return "INT", true
		default:
			return "BIGINT", true
		}
	case TypeFloat:
		if c.Bits == 32 {
			return "FLOAT", true
		}
		return "DOUBLE", true
	case TypeDecimal:
		if c.Precision > 0 {
			// MySQL caps DECIMAL at 65 digits total and 30 of scale.
			p, s := c.Precision, c.Scale
			if p > 65 {
				p = 65
			}
			if s > 30 {
				s = 30
			}
			if s > p {
				s = p
			}
			return fmt.Sprintf("DECIMAL(%d,%d)", p, s), true
		}
		// MySQL has no unconstrained exact numeric: DECIMAL without
		// precision means DECIMAL(10,0), which silently truncates a scale
		// the source had. Refuse instead -- the caller names the column,
		// and the operator picks a precision rather than discovering the
		// truncation in the data.
		return "", false
	case TypeText:
		if c.Length > 0 && c.Length <= 65535 {
			return fmt.Sprintf("VARCHAR(%d)", c.Length), true
		}
		return "TEXT", true
	case TypeBytes:
		return "LONGBLOB", true
	case TypeDate:
		return "DATE", true
	case TypeTime:
		return "TIME", true
	case TypeTimestamp:
		// Microsecond precision, matching what the engine renders.
		return "DATETIME(6)", true
	case TypeTimestampTZ:
		// MySQL has no zone-carrying type. DATETIME would store the
		// instant and drop the zone, which is exactly the silent loss
		// this vocabulary exists to prevent.
		return "", false
	case TypeJSON:
		return "JSON", true
	case TypeUUID:
		// No native UUID type; CHAR(36) is the conventional form and is
		// lossless for the canonical text rendering.
		return "CHAR(36)", true
	default:
		return "", false
	}
}
