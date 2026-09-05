package taskinterface

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"regexp"
	"time"
)

// ValidateValue checks that raw -- an already JSON-decoded value (so a
// JSON object is map[string]interface{}, a JSON number is float64, and so
// on, exactly what encoding/json produces) -- conforms to t, per ADR-032
// section 4's canonical value encoding. This is a different question from
// AssignType/AssignPort: those compare two *type descriptors*; this
// checks one *concrete value* against one type descriptor, the check a
// run-trigger request's submitted parameter values need.
//
// path is a diagnostic prefix (see Result.Path in assignability.go);
// pass "$" at the top level.
func ValidateValue(raw interface{}, t Type, path string) error {
	if raw == nil {
		if t.Nullable {
			return nil
		}
		return fmt.Errorf("%s: null is not allowed (type is not nullable)", path)
	}

	switch t.Kind {
	case KindUnknown:
		return nil // ADR-032 section 4: unknown accepts any canonical json value
	case KindJSON:
		return nil // any JSON value is valid for kind json
	case KindBoolean:
		if _, ok := raw.(bool); !ok {
			return fmt.Errorf("%s: expected boolean, got %T", path, raw)
		}
		return nil
	case KindString:
		return validateStringValue(raw, t, path)
	case KindInt64:
		return validateInt64Value(raw, t, path)
	case KindDecimal:
		return validateDecimalValue(raw, t, path)
	case KindFloat64:
		return validateFloat64Value(raw, t, path)
	case KindBytes:
		return validateBytesValue(raw, path)
	case KindDate:
		return validateDateValue(raw, path)
	case KindTimestamp:
		return validateTimestampValue(raw, path)
	case KindDuration:
		return validateDurationValue(raw, path)
	case KindArray:
		return validateArrayValue(raw, t, path)
	case KindMap:
		return validateMapValue(raw, t, path)
	case KindRecord:
		return validateRecordValue(raw, t, path)
	case KindEnum:
		return validateEnumValue(raw, t, path)
	case KindUnion:
		return validateUnionValue(raw, t, path)
	default:
		return fmt.Errorf("%s: unrecognized type kind %q", path, t.Kind)
	}
}

// taggedValue recognizes ADR-032 section 4's {"$bptd": "<kind>", ...}
// wire encoding for values JSON cannot represent losslessly on its own.
// An ordinary map that happens to contain a "$bptd" key is only
// reinterpreted this way when the *declared descriptor* requires that
// tagged type (ADR-032 section 4) -- callers here already know the
// declared kind from t.Kind, so that condition is satisfied by construction.
func taggedValue(raw interface{}) (kind string, rest map[string]interface{}, ok bool) {
	m, isMap := raw.(map[string]interface{})
	if !isMap {
		return "", nil, false
	}
	k, hasTag := m["$bptd"].(string)
	if !hasTag {
		return "", nil, false
	}
	return k, m, true
}

func validateStringValue(raw interface{}, t Type, path string) error {
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%s: expected string, got %T", path, raw)
	}
	if t.MinLength != nil && len(s) < *t.MinLength {
		return fmt.Errorf("%s: string length %d is below min_length %d", path, len(s), *t.MinLength)
	}
	if t.MaxLength != nil && len(s) > *t.MaxLength {
		return fmt.Errorf("%s: string length %d exceeds max_length %d", path, len(s), *t.MaxLength)
	}
	if t.Pattern != nil {
		re, err := regexp.Compile(*t.Pattern)
		if err != nil {
			return fmt.Errorf("%s: declared pattern %q does not compile: %w", path, *t.Pattern, err)
		}
		if !re.MatchString(s) {
			return fmt.Errorf("%s: value does not match pattern %q", path, *t.Pattern)
		}
	}
	return nil
}

var canonicalInt64Pattern = regexp.MustCompile(`^(0|-?[1-9][0-9]*)$`)

func validateInt64Value(raw interface{}, t Type, path string) error {
	var s string
	if kind, tv, ok := taggedValue(raw); ok {
		if kind != "int64" {
			return fmt.Errorf("%s: tagged value kind %q does not match declared type int64", path, kind)
		}
		sv, ok := tv["value"].(string)
		if !ok {
			return fmt.Errorf("%s: tagged int64 value must carry a string 'value'", path)
		}
		s = sv
	} else if f, ok := raw.(float64); ok {
		// Plain JSON number convenience form, valid only when it is a
		// whole number (a fractional int64 value makes no sense either
		// way, and a tagged string is required beyond 2^53 -- ADR-032
		// section 4's own JS bigint guidance implies exactly this limit
		// for the untagged convenience form).
		if f != float64(int64(f)) {
			return fmt.Errorf("%s: %v is not a whole number", path, f)
		}
		s = fmt.Sprintf("%d", int64(f))
	} else {
		return fmt.Errorf("%s: expected an int64 (a whole JSON number or a tagged {\"$bptd\":\"int64\",...} value), got %T", path, raw)
	}
	if !canonicalInt64Pattern.MatchString(s) {
		return fmt.Errorf("%s: %q is not a canonical int64 string", path, s)
	}
	n, err := parseCanonicalNumber(s)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return checkNumericBounds(path, n, t.Minimum, t.Maximum, t.ExclusiveMinimum, t.ExclusiveMaximum)
}

var canonicalDecimalPattern = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?$`)

func validateDecimalValue(raw interface{}, t Type, path string) error {
	kind, tv, ok := taggedValue(raw)
	if !ok {
		return fmt.Errorf("%s: decimal values must use the tagged {\"$bptd\":\"decimal\",\"value\":\"...\"} form", path)
	}
	if kind != "decimal" {
		return fmt.Errorf("%s: tagged value kind %q does not match declared type decimal", path, kind)
	}
	s, ok := tv["value"].(string)
	if !ok {
		return fmt.Errorf("%s: tagged decimal value must carry a string 'value'", path)
	}
	if !canonicalDecimalPattern.MatchString(s) {
		return fmt.Errorf("%s: %q is not a canonical decimal string", path, s)
	}
	n, err := parseCanonicalNumber(s)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return checkNumericBounds(path, n, t.Minimum, t.Maximum, t.ExclusiveMinimum, t.ExclusiveMaximum)
}

func checkNumericBounds(path string, n *big.Float, minimum, maximum, exclMin, exclMax *string) error {
	check := func(label string, bound *string, cmp func(n, b *big.Float) bool) error {
		if bound == nil {
			return nil
		}
		b, err := parseCanonicalNumber(*bound)
		if err != nil {
			return fmt.Errorf("%s: declared %s %q is not a parseable canonical number", path, label, *bound)
		}
		if !cmp(n, b) {
			return fmt.Errorf("%s: value does not satisfy %s %s", path, label, *bound)
		}
		return nil
	}
	if err := check("minimum", minimum, func(n, b *big.Float) bool { return n.Cmp(b) >= 0 }); err != nil {
		return err
	}
	if err := check("maximum", maximum, func(n, b *big.Float) bool { return n.Cmp(b) <= 0 }); err != nil {
		return err
	}
	if err := check("exclusive_minimum", exclMin, func(n, b *big.Float) bool { return n.Cmp(b) > 0 }); err != nil {
		return err
	}
	if err := check("exclusive_maximum", exclMax, func(n, b *big.Float) bool { return n.Cmp(b) < 0 }); err != nil {
		return err
	}
	return nil
}

func validateFloat64Value(raw interface{}, t Type, path string) error {
	f, ok := raw.(float64)
	if !ok {
		return fmt.Errorf("%s: expected float64, got %T", path, raw)
	}
	check := func(label string, bound *float64, cmp func(f, b float64) bool) error {
		if bound == nil {
			return nil
		}
		if !cmp(f, *bound) {
			return fmt.Errorf("%s: value %v does not satisfy %s %v", path, f, label, *bound)
		}
		return nil
	}
	if err := check("minimum", t.MinimumF, func(f, b float64) bool { return f >= b }); err != nil {
		return err
	}
	if err := check("maximum", t.MaximumF, func(f, b float64) bool { return f <= b }); err != nil {
		return err
	}
	if err := check("exclusive_minimum", t.ExclusiveMinimumF, func(f, b float64) bool { return f > b }); err != nil {
		return err
	}
	if err := check("exclusive_maximum", t.ExclusiveMaximumF, func(f, b float64) bool { return f < b }); err != nil {
		return err
	}
	return nil
}

func validateBytesValue(raw interface{}, path string) error {
	kind, tv, ok := taggedValue(raw)
	if !ok || kind != "bytes" {
		return fmt.Errorf("%s: bytes values must use the tagged {\"$bptd\":\"bytes\",\"value\":\"<base64>\"} form", path)
	}
	s, ok := tv["value"].(string)
	if !ok {
		return fmt.Errorf("%s: tagged bytes value must carry a string 'value'", path)
	}
	if _, err := base64.StdEncoding.DecodeString(s); err != nil {
		return fmt.Errorf("%s: %q is not valid padded standard base64: %w", path, s, err)
	}
	return nil
}

func validateDateValue(raw interface{}, path string) error {
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%s: expected a date string, got %T", path, raw)
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return fmt.Errorf("%s: %q is not an RFC 3339 full-date string: %w", path, s, err)
	}
	return nil
}

func validateTimestampValue(raw interface{}, path string) error {
	kind, tv, ok := taggedValue(raw)
	if !ok || kind != "timestamp" {
		return fmt.Errorf("%s: timestamp values must use the tagged {\"$bptd\":\"timestamp\",\"value\":\"...\"} form", path)
	}
	s, ok := tv["value"].(string)
	if !ok {
		return fmt.Errorf("%s: tagged timestamp value must carry a string 'value'", path)
	}
	if !regexp.MustCompile(`Z$`).MatchString(s) {
		return fmt.Errorf("%s: timestamp %q must use UTC 'Z' (ADR-032 section 4 rule 7)", path, s)
	}
	if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
		return fmt.Errorf("%s: %q is not a valid RFC 3339 timestamp: %w", path, s, err)
	}
	return nil
}

func validateDurationValue(raw interface{}, path string) error {
	kind, tv, ok := taggedValue(raw)
	if !ok || kind != "duration" {
		return fmt.Errorf("%s: duration values must use the tagged {\"$bptd\":\"duration\",\"seconds\":...,\"nanos\":...} form", path)
	}
	if _, ok := tv["seconds"].(string); !ok {
		return fmt.Errorf("%s: tagged duration value must carry a string 'seconds'", path)
	}
	nanos, ok := tv["nanos"].(float64)
	if !ok {
		return fmt.Errorf("%s: tagged duration value must carry a numeric 'nanos'", path)
	}
	if nanos < -999999999 || nanos > 999999999 {
		return fmt.Errorf("%s: nanos %v is outside [-999999999, 999999999] (ADR-032 section 4 rule 8)", path, nanos)
	}
	return nil
}

func validateArrayValue(raw interface{}, t Type, path string) error {
	arr, ok := raw.([]interface{})
	if !ok {
		return fmt.Errorf("%s: expected an array, got %T", path, raw)
	}
	if t.MinItems != nil && len(arr) < *t.MinItems {
		return fmt.Errorf("%s: array has %d items, below min_items %d", path, len(arr), *t.MinItems)
	}
	if t.MaxItems != nil && len(arr) > *t.MaxItems {
		return fmt.Errorf("%s: array has %d items, exceeds max_items %d", path, len(arr), *t.MaxItems)
	}
	if t.UniqueItems {
		seen := map[string]bool{}
		for i, item := range arr {
			key := fmt.Sprintf("%v", item)
			if seen[key] {
				return fmt.Errorf("%s[%d]: duplicate item, but unique_items is required", path, i)
			}
			seen[key] = true
		}
	}
	for i, item := range arr {
		if err := ValidateValue(item, *t.Items, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func validateMapValue(raw interface{}, t Type, path string) error {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return fmt.Errorf("%s: expected an object (map keys are always string, ADR-032 section 4), got %T", path, raw)
	}
	for k, v := range m {
		if err := ValidateValue(v, *t.Items, fmt.Sprintf("%s.%s", path, k)); err != nil {
			return err
		}
	}
	return nil
}

func validateRecordValue(raw interface{}, t Type, path string) error {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return fmt.Errorf("%s: expected an object, got %T", path, raw)
	}
	declared := map[string]Field{}
	for _, f := range t.Fields {
		declared[f.Name] = f
	}
	for _, f := range t.Fields {
		v, present := m[f.Name]
		fieldPath := path + "." + f.Name
		if !present {
			if f.Required {
				return fmt.Errorf("%s: required field is missing", fieldPath)
			}
			continue
		}
		if err := ValidateValue(v, f.Type, fieldPath); err != nil {
			return err
		}
	}
	if !t.AdditionalFields {
		for k := range m {
			if _, ok := declared[k]; !ok {
				return fmt.Errorf("%s.%s: field is not declared, and this record is closed (additional_fields: false)", path, k)
			}
		}
	}
	return nil
}

func validateEnumValue(raw interface{}, t Type, path string) error {
	s, ok := raw.(string)
	if !ok {
		return fmt.Errorf("%s: expected an enum string value, got %T", path, raw)
	}
	for _, v := range t.Values {
		if v == s {
			return nil
		}
	}
	return fmt.Errorf("%s: %q is not one of the declared enum values %v", path, s, t.Values)
}

func validateUnionValue(raw interface{}, t Type, path string) error {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return fmt.Errorf("%s: expected a tagged union object, got %T", path, raw)
	}
	tag, ok := m[t.TagField].(string)
	if !ok {
		return fmt.Errorf("%s: missing string tag field %q", path, t.TagField)
	}
	for _, variant := range t.Variants {
		if variant.Tag == tag {
			payload, ok := m[t.ValueField]
			if !ok {
				return fmt.Errorf("%s: missing value field %q for tag %q", path, t.ValueField, tag)
			}
			return ValidateValue(payload, variant.Type, path+"["+t.TagField+"="+tag+"]")
		}
	}
	return fmt.Errorf("%s: tag %q does not match any declared union variant", path, tag)
}
