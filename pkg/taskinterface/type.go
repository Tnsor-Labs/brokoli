// Package taskinterface implements ADR-032's Brokoli Portable Type
// Descriptor (BPTD) as real Go types, and the directional assignability
// check ADR-032 section 9 defines over them.
//
// This package is deliberately NOT wired into engine/validate.go or any
// deploy/run path yet (ADR-032 rollout step 4, issue #439) -- it is a
// pure, standalone function library, tested against the schema this
// project already ships at docs/schema/task-interface-v1.json. Wiring it
// into graph validation and typed run-parameter snapshotting is the next
// slice.
//
// docs/schema/task-interface-v1.json is the normative structural
// contract (what shapes are well-formed); this package additionally
// implements the semantic assignability rules that schema alone cannot
// express (ADR-032 section 9's twelve directional rules).
package taskinterface

import (
	"fmt"
	"math/big"
	"strconv"
)

// Kind is a BPTD type-descriptor discriminant (ADR-032 section 4).
type Kind string

const (
	KindBoolean   Kind = "boolean"
	KindInt64     Kind = "int64"
	KindFloat64   Kind = "float64"
	KindDecimal   Kind = "decimal"
	KindString    Kind = "string"
	KindBytes     Kind = "bytes"
	KindDate      Kind = "date"
	KindTimestamp Kind = "timestamp"
	KindDuration  Kind = "duration"
	KindJSON      Kind = "json"
	KindUnknown   Kind = "unknown"
	KindArray     Kind = "array"
	KindMap       Kind = "map"
	KindRecord    Kind = "record"
	KindEnum      Kind = "enum"
	KindUnion     Kind = "union"
)

// Type is a parsed Brokoli Portable Type Descriptor
// (docs/schema/task-interface-v1.json#/$defs/bptd_type).
//
// Numeric/length bound fields are pointers because "declared" and
// "declared as zero" are different states (ADR-032 section 6: absence is
// honest). int64/decimal bounds are the canonical string encodings
// docs/schema/task-interface-canonicalization.md defines (rules 3-4);
// float64 bounds are plain float64 (rule 5). Parsing them into a
// comparable numeric form happens in assignability.go, at comparison
// time, not here -- this type is a faithful, uninterpreted structural
// parse of the JSON descriptor.
type Type struct {
	Kind     Kind
	Nullable bool

	// array / map
	Items *Type // array items, or map values (map keys are always string, ADR-032 section 4)

	// record
	Fields           []Field
	AdditionalFields bool

	// enum
	Values []string

	// union
	TagField, ValueField string
	Variants             []Variant

	// constraints (ADR-032 section 5)
	Minimum, Maximum                     *string // int64/decimal, canonical string form
	ExclusiveMinimum, ExclusiveMaximum   *string
	MinimumF, MaximumF                   *float64 // float64
	ExclusiveMinimumF, ExclusiveMaximumF *float64
	MinLength, MaxLength                 *int
	Pattern                              *string
	MinItems, MaxItems                   *int
	UniqueItems                          bool
}

// Field is one record field (docs/schema/task-interface-v1.json#/$defs/record_field).
type Field struct {
	Name     string
	Type     Type
	Required bool
}

// Variant is one union variant (docs/schema/task-interface-v1.json#/$defs/union_variant).
type Variant struct {
	Tag  string
	Type Type
}

// ValueKind is a port's value-contract discriminant (ADR-032 section 6).
type ValueKind string

const (
	ValueDataset    ValueKind = "dataset"
	ValueScalar     ValueKind = "scalar"
	ValueArtifact   ValueKind = "artifact"
	ValueCollection ValueKind = "collection"
	ValueControl    ValueKind = "control"
)

// PortValue is a parsed value contract
// (docs/schema/task-interface-v1.json#/$defs/value_contract).
type PortValue struct {
	Kind ValueKind

	Row *Type // dataset; nil means unknown row shape (ADR-032 section 6)

	ScalarType *Type // scalar

	MediaTypes  []string // artifact; empty means unrestricted
	LogicalType string   // artifact; informational only, not part of assignability (ADR-032 section 9 does not name it)

	Items   *PortValue // collection
	Ordered bool       // collection; informational only, not part of assignability
	ItemKey *Type      // collection; informational only in this package -- see assignability.go's doc note
}

// ParseType decodes a bptd_type JSON object (already schema-validated
// elsewhere) into a Type. It does not re-validate structural well-formedness
// -- that is docs/schema/task-interface-v1.json's job -- it assumes a
// document that already passed that schema and returns an error only on
// a shape this package cannot make sense of (defensive, not a second
// validator).
func ParseType(raw interface{}) (Type, error) {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return Type{}, fmt.Errorf("taskinterface: type descriptor is not an object: %T", raw)
	}
	kindRaw, _ := m["kind"].(string)
	if kindRaw == "" {
		return Type{}, fmt.Errorf("taskinterface: type descriptor missing 'kind'")
	}
	t := Type{Kind: Kind(kindRaw)}
	if nullable, ok := m["nullable"].(bool); ok {
		t.Nullable = nullable
	}

	switch t.Kind {
	case KindArray, KindMap:
		itemsKey := "items"
		if t.Kind == KindMap {
			itemsKey = "values"
		}
		itemsRaw, ok := m[itemsKey]
		if !ok {
			return Type{}, fmt.Errorf("taskinterface: %s type missing '%s'", t.Kind, itemsKey)
		}
		items, err := ParseType(itemsRaw)
		if err != nil {
			return Type{}, err
		}
		t.Items = &items
		if t.Kind == KindArray {
			t.MinItems = parseIntPtr(m["min_items"])
			t.MaxItems = parseIntPtr(m["max_items"])
			if unique, ok := m["unique_items"].(bool); ok {
				t.UniqueItems = unique
			}
		}
	case KindRecord:
		fieldsRaw, _ := m["fields"].([]interface{})
		for _, fr := range fieldsRaw {
			fm, ok := fr.(map[string]interface{})
			if !ok {
				return Type{}, fmt.Errorf("taskinterface: record field is not an object")
			}
			name, _ := fm["name"].(string)
			ft, err := ParseType(fm["type"])
			if err != nil {
				return Type{}, fmt.Errorf("taskinterface: record field %q: %w", name, err)
			}
			required, _ := fm["required"].(bool)
			t.Fields = append(t.Fields, Field{Name: name, Type: ft, Required: required})
		}
		if additional, ok := m["additional_fields"].(bool); ok {
			t.AdditionalFields = additional
		}
	case KindEnum:
		valuesRaw, _ := m["values"].([]interface{})
		for _, v := range valuesRaw {
			if s, ok := v.(string); ok {
				t.Values = append(t.Values, s)
			}
		}
	case KindUnion:
		t.TagField, _ = m["tag_field"].(string)
		t.ValueField, _ = m["value_field"].(string)
		variantsRaw, _ := m["variants"].([]interface{})
		for _, vr := range variantsRaw {
			vm, ok := vr.(map[string]interface{})
			if !ok {
				return Type{}, fmt.Errorf("taskinterface: union variant is not an object")
			}
			tag, _ := vm["tag"].(string)
			vt, err := ParseType(vm["type"])
			if err != nil {
				return Type{}, fmt.Errorf("taskinterface: union variant %q: %w", tag, err)
			}
			t.Variants = append(t.Variants, Variant{Tag: tag, Type: vt})
		}
	case KindString:
		t.MinLength = parseIntPtr(m["min_length"])
		t.MaxLength = parseIntPtr(m["max_length"])
		if pattern, ok := m["pattern"].(string); ok {
			t.Pattern = &pattern
		}
	case KindInt64, KindDecimal:
		t.Minimum = parseStringPtr(m["minimum"])
		t.Maximum = parseStringPtr(m["maximum"])
		t.ExclusiveMinimum = parseStringPtr(m["exclusive_minimum"])
		t.ExclusiveMaximum = parseStringPtr(m["exclusive_maximum"])
	case KindFloat64:
		t.MinimumF = parseFloatPtr(m["minimum"])
		t.MaximumF = parseFloatPtr(m["maximum"])
		t.ExclusiveMinimumF = parseFloatPtr(m["exclusive_minimum"])
		t.ExclusiveMaximumF = parseFloatPtr(m["exclusive_maximum"])
	}
	return t, nil
}

// ParsePortValue decodes a value_contract JSON object into a PortValue.
func ParsePortValue(raw interface{}) (PortValue, error) {
	m, ok := raw.(map[string]interface{})
	if !ok {
		return PortValue{}, fmt.Errorf("taskinterface: port value is not an object: %T", raw)
	}
	kindRaw, _ := m["kind"].(string)
	if kindRaw == "" {
		return PortValue{}, fmt.Errorf("taskinterface: port value missing 'kind'")
	}
	pv := PortValue{Kind: ValueKind(kindRaw)}

	switch pv.Kind {
	case ValueDataset:
		if rowRaw, ok := m["row"]; ok {
			row, err := ParseType(rowRaw)
			if err != nil {
				return PortValue{}, fmt.Errorf("taskinterface: dataset row: %w", err)
			}
			pv.Row = &row
		}
	case ValueScalar:
		scalarType, err := ParseType(m["type"])
		if err != nil {
			return PortValue{}, fmt.Errorf("taskinterface: scalar type: %w", err)
		}
		pv.ScalarType = &scalarType
	case ValueArtifact:
		mediaTypesRaw, _ := m["media_types"].([]interface{})
		for _, mt := range mediaTypesRaw {
			if s, ok := mt.(string); ok {
				pv.MediaTypes = append(pv.MediaTypes, s)
			}
		}
		pv.LogicalType, _ = m["logical_type"].(string)
	case ValueCollection:
		itemsRaw, ok := m["items"]
		if !ok {
			return PortValue{}, fmt.Errorf("taskinterface: collection missing 'items'")
		}
		items, err := ParsePortValue(itemsRaw)
		if err != nil {
			return PortValue{}, fmt.Errorf("taskinterface: collection items: %w", err)
		}
		pv.Items = &items
		if ordered, ok := m["ordered"].(bool); ok {
			pv.Ordered = ordered
		}
		if itemKeyRaw, ok := m["item_key"]; ok {
			itemKey, err := ParseType(itemKeyRaw)
			if err != nil {
				return PortValue{}, fmt.Errorf("taskinterface: collection item_key: %w", err)
			}
			pv.ItemKey = &itemKey
		}
	case ValueControl:
		// no payload
	default:
		return PortValue{}, fmt.Errorf("taskinterface: unknown value kind %q", kindRaw)
	}
	return pv, nil
}

func parseIntPtr(v interface{}) *int {
	f, ok := v.(float64) // encoding/json decodes JSON numbers as float64
	if !ok {
		return nil
	}
	i := int(f)
	return &i
}

func parseStringPtr(v interface{}) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

func parseFloatPtr(v interface{}) *float64 {
	f, ok := v.(float64)
	if !ok {
		return nil
	}
	return &f
}

// parseCanonicalNumber parses an int64/decimal canonical string
// (docs/schema/task-interface-canonicalization.md rules 3-4) into a
// big.Float for comparison. int64 fits losslessly; decimal keeps
// whatever precision big.Float's default (53-bit mantissa, like
// float64) preserves -- adequate for a range-subset comparison, not
// claimed to be exact arbitrary-precision arithmetic.
func parseCanonicalNumber(s string) (*big.Float, error) {
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		f := new(big.Float)
		if _, ok := f.SetString(s); ok {
			return f, nil
		}
	}
	f := new(big.Float)
	if _, ok := f.SetString(s); !ok {
		return nil, fmt.Errorf("taskinterface: not a canonical number: %q", s)
	}
	return f, nil
}
