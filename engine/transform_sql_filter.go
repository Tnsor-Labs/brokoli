package engine

import (
	"fmt"
	"strconv"
	"strings"
)

// sqlColumnRef is how the prefix compiler describes one visible column to the
// filter compiler: the identifier to emit in SQL, and what kind of comparison
// it supports. The two can differ in name, because a rename earlier in the
// prefix changes what the condition calls a column without changing what the
// subquery exposes.
type sqlColumnRef struct {
	Ident string
	Kind  sqlColumnKind
}

// compileFilterToSQL renders a filter condition as a SQL boolean expression
// that evaluates exactly as the Go matcher does, or reports that it cannot.
//
// "Exactly as the Go matcher does" is the whole specification, and it is not
// the same as "correctly". The Go matcher compares the fmt.Sprintf("%v")
// rendering of a value, treats a missing value as the literal string
// "<nil>", and compares numerically only when both sides parse as float64.
// Reproducing that means deliberately importing its quirks:
//
//   - Numeric comparison casts to double precision, so a bigint beyond 2^53
//     loses the same precision it loses going through Go's ParseFloat. Casting
//     to numeric would be more accurate and would therefore disagree.
//   - Text comparison uses the C collation, because Go compares bytes and the
//     database's default collation does not.
//   - A NULL is not left to SQL's three-valued logic. Go compares "<nil>"
//     against the target like any other string, so the outcome for a NULL row
//     is a constant, computed here at compile time and emitted as one.
//
// Where the two cannot be shown to agree, ok is false and the filter runs in
// the engine.
func compileFilterToSQL(cond string, cols map[string]sqlColumnRef) (string, bool) {
	pc, err := parseCondition(cond)
	if err != nil {
		return "", false
	}
	ref, known := cols[pc.Column]
	if !known || ref.Kind == kindUnclassified {
		return "", false
	}
	// The emitted identifier is the SOURCE column, not the name the condition
	// uses. A rule can rename a column earlier in the prefix, and SQL cannot
	// refer to a SELECT alias from the WHERE of the same query level -- the
	// filter has to name what the subquery actually exposes.
	col, kind := ref.Ident, ref.Kind

	// What the Go matcher decides for a row whose value is absent or NULL. It
	// depends only on the target, so it is known now.
	nullOutcome, ok := goOutcomeForNil(pc)
	if !ok {
		return "", false
	}
	nullBranch := "FALSE"
	if nullOutcome {
		nullBranch = "TRUE"
	}

	var live string
	switch pc.Op {
	case "in":
		// Set membership compares rendered text. Reproducible, but the
		// rendering of a numeric column has to match %v exactly for every
		// member; not yet demonstrated, so it waits for its own comparison.
		return "", false

	case "=", "==", "!=":
		// String equality on the rendered value.
		lhs := col + ` COLLATE "C"`
		if kind == kindNumeric {
			lhs = "CAST(" + col + " AS TEXT)"
		}
		op := "="
		if pc.Op == "!=" {
			op = "<>"
		}
		live = fmt.Sprintf("%s %s %s", lhs, op, quoteLiteralPG(pc.Target))

	case ">", "<", ">=", "<=":
		targetNum, targetIsNum := parseGoFloat(pc.Target)
		switch {
		case kind == kindNumeric && targetIsNum:
			// Both sides parse, so Go compares numerically -- through
			// float64, which the cast reproduces.
			live = fmt.Sprintf("CAST(%s AS DOUBLE PRECISION) %s %s",
				col, pc.Op, strconv.FormatFloat(targetNum, 'g', -1, 64))
		case kind == kindText && !targetIsNum:
			// Neither side can parse, so Go compares bytes.
			live = fmt.Sprintf(`%s COLLATE "C" %s %s`, col, pc.Op, quoteLiteralPG(pc.Target))
		default:
			// A text column against a numeric-looking target is the case that
			// cannot be compiled: Go decides per row, comparing numerically
			// for values that happen to parse and byte-wise for the rest, so
			// '9' and '10' order differently depending on the row's content.
			return "", false
		}

	default:
		return "", false
	}

	return fmt.Sprintf("CASE WHEN %s IS NULL THEN %s ELSE (%s) END", col, nullBranch, live), true
}

// goOutcomeForNil computes what the Go matcher returns for a row whose value
// is absent, where it renders as the string "<nil>" and is compared like any
// other value. ok is false for a form this compiler does not handle.
func goOutcomeForNil(pc parsedCondition) (bool, bool) {
	const nilRendering = "<nil>"
	switch pc.Op {
	case "=", "==":
		return nilRendering == pc.Target, true
	case "!=":
		return nilRendering != pc.Target, true
	case ">", "<", ">=", "<=":
		cmp := strings.Compare(nilRendering, pc.Target)
		if lf, lok := parseGoFloat(nilRendering); lok {
			if rf, rok := parseGoFloat(pc.Target); rok {
				switch {
				case lf < rf:
					cmp = -1
				case lf > rf:
					cmp = 1
				default:
					cmp = 0
				}
			}
		}
		switch pc.Op {
		case ">":
			return cmp > 0, true
		case "<":
			return cmp < 0, true
		case ">=":
			return cmp >= 0, true
		case "<=":
			return cmp <= 0, true
		}
	}
	return false, false
}

// parseGoFloat mirrors the matcher's strconv.ParseFloat check, so both
// backends agree about which values count as numbers.
func parseGoFloat(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// quoteLiteralPG renders a string as a SQL literal, doubling embedded quotes
// so a target value cannot close the literal and inject SQL.
func quoteLiteralPG(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
