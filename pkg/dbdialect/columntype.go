package dbdialect

import "fmt"

// A canonical column-type vocabulary, so a migration can carry a column's
// real type between backends instead of guessing one from its values
// (Tnsor-Labs/brokoli#360, #361).
//
// Type mapping used to be a single shared table from an inferred pseudo-type
// to each backend's spelling, fed by sniffing the string form of sampled
// values. That shape cannot express what a migration needs -- width,
// precision, scale, whether a timestamp carries a zone -- because its input
// had already discarded all of it. A Postgres bigint became a MySQL int, and
// the run failed on the first value above 2^31.
//
// The vocabulary here is deliberately small. It is not a type system; it is
// the set of distinctions that change whether a value survives the trip.
// Anything a backend cannot recognise stays TypeUnknown, and a destination
// that cannot render a canonical type refuses by name rather than quietly
// substituting text -- a migration that half-works is worse than one that
// says which column it cannot carry.

// TypeClass is the canonical family of a column.
type TypeClass int

const (
	// TypeUnknown is the safe answer: a type name this backend does not
	// recognise, or one with no faithful canonical form. It is never
	// rendered as DDL -- the caller is expected to refuse.
	TypeUnknown TypeClass = iota
	TypeBool
	// TypeInt carries its width in bits (16, 32, 64) so a widening
	// destination stays wide enough.
	TypeInt
	// TypeFloat carries its width in bits (32, 64). Inexact by nature.
	TypeFloat
	// TypeDecimal is exact, and carries precision and scale. Rendering it
	// as a float is a correctness change, not a formatting one.
	TypeDecimal
	TypeText
	TypeBytes
	TypeDate
	TypeTime
	// TypeTimestamp is a wall-clock instant with no zone.
	TypeTimestamp
	// TypeTimestampTZ carries a zone. Collapsing it into TypeTimestamp
	// loses information, so the two are distinct and a backend without a
	// zone-aware type must say so.
	TypeTimestampTZ
	TypeJSON
	TypeUUID
)

func (c TypeClass) String() string {
	switch c {
	case TypeBool:
		return "bool"
	case TypeInt:
		return "int"
	case TypeFloat:
		return "float"
	case TypeDecimal:
		return "decimal"
	case TypeText:
		return "text"
	case TypeBytes:
		return "bytes"
	case TypeDate:
		return "date"
	case TypeTime:
		return "time"
	case TypeTimestamp:
		return "timestamp"
	case TypeTimestampTZ:
		return "timestamptz"
	case TypeJSON:
		return "json"
	case TypeUUID:
		return "uuid"
	default:
		return "unknown"
	}
}

// ColumnType is one column's canonical type, with the detail that decides
// whether a value survives a move between backends.
type ColumnType struct {
	Class TypeClass
	// Bits is the width for TypeInt and TypeFloat: 16, 32 or 64. Zero means
	// unspecified, which a renderer must treat as the widest it supports
	// rather than the narrowest.
	Bits int
	// Precision and Scale apply to TypeDecimal. Zero precision means the
	// source did not report one, which some drivers do for unconstrained
	// numerics; a renderer then has to choose a form that cannot silently
	// truncate.
	Precision int
	Scale     int
	// Length applies to TypeText and TypeBytes, in characters or bytes.
	// Zero means unbounded.
	Length int
	// Nullable is what the source reported. Drivers are allowed to not
	// know, in which case this is true -- the permissive answer, because a
	// NOT NULL a source did not actually have would fail the load.
	Nullable bool
}

func (c ColumnType) String() string {
	switch c.Class {
	case TypeInt, TypeFloat:
		if c.Bits > 0 {
			return fmt.Sprintf("%s%d", c.Class, c.Bits)
		}
	case TypeDecimal:
		if c.Precision > 0 {
			return fmt.Sprintf("decimal(%d,%d)", c.Precision, c.Scale)
		}
	case TypeText, TypeBytes:
		if c.Length > 0 {
			return fmt.Sprintf("%s(%d)", c.Class, c.Length)
		}
	}
	return c.Class.String()
}

// TypeReader reads a backend's own type names into the canonical form. A
// backend that cannot recognise a name returns TypeUnknown rather than
// guessing: the caller refuses, which is recoverable, where a wrong guess
// writes wrong data.
type TypeReader interface {
	// CanonicalType interprets a driver's DatabaseTypeName, together with
	// whatever detail the driver could supply. precisionOK and lengthOK
	// report whether the driver actually knew those, since zero is a
	// legitimate scale.
	CanonicalType(databaseTypeName string, precision, scale int64, precisionOK bool, length int64, lengthOK bool, nullable bool) ColumnType
}

// TypeRenderer renders a canonical type as this backend's DDL. ok is false
// when the backend has no faithful equivalent -- the caller names the column
// and refuses rather than substituting something lossy.
type TypeRenderer interface {
	DDLType(ColumnType) (string, bool)
}
