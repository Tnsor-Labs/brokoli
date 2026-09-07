package taskinterface

import "fmt"

// Direction identifies which side of a task's declared contract a value
// crossed (ADR-032 section 10).
type Direction string

const (
	DirectionInput     Direction = "input"
	DirectionOutput    Direction = "output"
	DirectionParameter Direction = "parameter"
)

// ValidationMode is the enforcement level a boundary checked a value
// under (ADR-032 section 10's none/sample/full). ModeFull is the only
// mode this package ever produces today: ModeSample has no reachable
// target yet anywhere in this codebase (nothing produces or consumes a
// dataset-kind task value), and ModeNone is a deployment policy choice
// this package never makes for itself. The type exists now so a future
// sample-mode implementation is an additive value, not another breaking
// field change.
type ValidationMode string

const (
	ModeFull ValidationMode = "full"
)

// ValidationFailure is ADR-032 section 10's "first-class task failure"
// shape: a structured runtime contract violation, not a bare string.
//
// It deliberately wraps ValidateValue's existing plain error rather than
// changing that function's signature -- ValidateValue recurses through
// itself dozens of times per call (one per record field, array item,
// union variant...), and only its own top-level caller knows the
// direction/name/mode context a violation needs to be reported inside;
// ValidateValue itself never does.
//
// ContractPath is the path passed to the top-level ValidateValue call
// (conventionally "$"), not a per-field path within a nested failure --
// the field/array-index/variant-tag that actually failed inside a
// record/array/union is already named in Cause's own message (every
// validateXValue helper prepends its own path segment there), just not
// exposed as a second structured field yet. Splitting that out would
// mean restructuring value.go's internal error construction, deferred as
// a named gap rather than approximated by parsing the message text.
type ValidationFailure struct {
	Direction    Direction
	Name         string // port or parameter name
	ContractPath string
	Expected     string // short descriptor of the declared type, e.g. "int64" -- never a full schema dump
	Observed     string // redacted: a kind/shape summary, never the value itself
	Redacted     bool   // true when Observed (and Cause's detail) were suppressed for a sensitive declaration
	CheckedRows  int
	Mode         ValidationMode

	cause error
}

func (f *ValidationFailure) Error() string {
	base := fmt.Sprintf("%s %q: expected %s, got %s", f.Direction, f.Name, f.Expected, f.Observed)
	if f.Redacted {
		return base + " (detail redacted: declaration is sensitive)"
	}
	if f.cause != nil {
		return fmt.Sprintf("%s: %s", base, f.cause.Error())
	}
	return base
}

func (f *ValidationFailure) Unwrap() error { return f.cause }

// NewValidationFailure builds a ValidationFailure from ValidateValue's
// plain error, given the boundary context only a caller knows: which
// direction, which port or parameter, and whether that declaration is
// sensitive (ADR-032 section 4 rule 7 / docs/schema/task-interface-v1.json's
// parameter_declaration.sensitive -- this package has no per-Type
// sensitivity flag, only ParameterDeclaration does, so callers pass it
// explicitly rather than this function guessing).
//
// cause must be non-nil (the error ValidateValue(raw, t, path) returned);
// returns nil if cause is nil, so a caller can write
// `if err := ValidateValue(...); err != nil { return NewValidationFailure(...) }`
// without a redundant nil check.
func NewValidationFailure(direction Direction, name string, t Type, raw interface{}, cause error, path string, sensitive bool) *ValidationFailure {
	if cause == nil {
		return nil
	}
	f := &ValidationFailure{
		Direction:    direction,
		Name:         name,
		ContractPath: path,
		Expected:     describeType(t),
		CheckedRows:  1,
		Mode:         ModeFull,
	}
	if sensitive {
		f.Redacted = true
		return f
	}
	f.Observed = describeValueKind(raw)
	f.cause = cause
	return f
}

// describeType renders a short, safe descriptor of a declared type --
// its kind plus a small structural hint, never a full schema dump (this
// is a diagnostic summary, not a serialization of the contract).
func describeType(t Type) string {
	s := string(t.Kind)
	switch t.Kind {
	case KindRecord:
		s = fmt.Sprintf("record{%d field(s)}", len(t.Fields))
	case KindArray:
		if t.Items != nil {
			s = fmt.Sprintf("array<%s>", describeType(*t.Items))
		}
	case KindMap:
		if t.Items != nil {
			s = fmt.Sprintf("map<string,%s>", describeType(*t.Items))
		}
	case KindEnum:
		s = fmt.Sprintf("enum{%d value(s)}", len(t.Values))
	case KindUnion:
		s = fmt.Sprintf("union{%d variant(s)}", len(t.Variants))
	}
	if t.Nullable {
		s += " (nullable)"
	}
	return s
}

// describeValueKind reports the observed value's shape -- never its
// content. Sizes are safe to report (they say nothing about what the
// data contains) and are exactly what a caller needs to tell "empty
// array" from "wrong element type" apart without seeing the array.
func describeValueKind(raw interface{}) string {
	switch v := raw.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return fmt.Sprintf("string(len=%d)", len(v))
	case float64:
		return "number"
	case []interface{}:
		return fmt.Sprintf("array(len=%d)", len(v))
	case map[string]interface{}:
		return fmt.Sprintf("object(%d field(s))", len(v))
	default:
		return fmt.Sprintf("%T", v)
	}
}
