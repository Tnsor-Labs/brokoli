package taskinterface

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, doc string) interface{} {
	t.Helper()
	var raw interface{}
	if err := json.Unmarshal([]byte(doc), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return raw
}

func TestValidateValue_Primitives(t *testing.T) {
	cases := []struct {
		name    string
		typ     string
		value   string
		wantErr bool
	}{
		{"boolean ok", `{"kind":"boolean"}`, `true`, false},
		{"boolean wrong type", `{"kind":"boolean"}`, `"true"`, true},
		{"string ok", `{"kind":"string"}`, `"hello"`, false},
		{"string wrong type", `{"kind":"string"}`, `42`, true},
		{"float64 ok", `{"kind":"float64"}`, `0.5`, false},
		{"float64 wrong type", `{"kind":"float64"}`, `"0.5"`, true},
		{"json accepts anything", `{"kind":"json"}`, `{"a":[1,2,"x"]}`, false},
		{"unknown accepts anything", `{"kind":"unknown"}`, `{"a":1}`, false},
		{"date ok", `{"kind":"date"}`, `"2026-09-05"`, false},
		{"date bad format", `{"kind":"date"}`, `"09/05/2026"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ty := mustType(t, tc.typ)
			err := ValidateValue(mustJSON(t, tc.value), ty, "$")
			if tc.wantErr && err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
		})
	}
}

func TestValidateValue_Nullable(t *testing.T) {
	nullableStr := mustType(t, `{"kind": "string", "nullable": true}`)
	plainStr := mustType(t, `{"kind": "string"}`)
	if err := ValidateValue(nil, nullableStr, "$"); err != nil {
		t.Errorf("nullable type should accept null, got: %v", err)
	}
	if err := ValidateValue(nil, plainStr, "$"); err == nil {
		t.Error("non-nullable type should reject null")
	}
}

func TestValidateValue_Int64_PlainNumberConvenienceForm(t *testing.T) {
	ty := mustType(t, `{"kind": "int64"}`)
	if err := ValidateValue(mustJSON(t, `42`), ty, "$"); err != nil {
		t.Errorf("expected whole number to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `42.5`), ty, "$"); err == nil {
		t.Error("expected a fractional JSON number to be rejected for int64")
	}
}

func TestValidateValue_Int64_TaggedForm(t *testing.T) {
	ty := mustType(t, `{"kind": "int64"}`)
	if err := ValidateValue(mustJSON(t, `{"$bptd": "int64", "value": "9223372036854775807"}`), ty, "$"); err != nil {
		t.Errorf("expected tagged int64 max to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `{"$bptd": "int64", "value": "01"}`), ty, "$"); err == nil {
		t.Error("expected a leading-zero canonical string to be rejected")
	}
}

func TestValidateValue_Int64_RangeConstraint(t *testing.T) {
	ty := mustType(t, `{"kind": "int64", "minimum": "0", "maximum": "100"}`)
	if err := ValidateValue(mustJSON(t, `50`), ty, "$"); err != nil {
		t.Errorf("expected 50 to satisfy [0,100], got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `101`), ty, "$"); err == nil {
		t.Error("expected 101 to violate maximum 100")
	}
	if err := ValidateValue(mustJSON(t, `-1`), ty, "$"); err == nil {
		t.Error("expected -1 to violate minimum 0")
	}
}

func TestValidateValue_Decimal_RequiresTaggedForm(t *testing.T) {
	ty := mustType(t, `{"kind": "decimal"}`)
	if err := ValidateValue(mustJSON(t, `1.5`), ty, "$"); err == nil {
		t.Error("expected a plain JSON number to be rejected for decimal (tagged form required)")
	}
	if err := ValidateValue(mustJSON(t, `{"$bptd": "decimal", "value": "1200.50"}`), ty, "$"); err != nil {
		t.Errorf("expected tagged decimal to validate, got: %v", err)
	}
}

func TestValidateValue_Float64_RangeConstraint(t *testing.T) {
	ty := mustType(t, `{"kind": "float64", "minimum": 0, "maximum": 1}`)
	if err := ValidateValue(mustJSON(t, `0.5`), ty, "$"); err != nil {
		t.Errorf("expected 0.5 to satisfy [0,1], got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `1.5`), ty, "$"); err == nil {
		t.Error("expected 1.5 to violate maximum 1")
	}
}

func TestValidateValue_Bytes(t *testing.T) {
	ty := mustType(t, `{"kind": "bytes"}`)
	if err := ValidateValue(mustJSON(t, `{"$bptd": "bytes", "value": "SGVsbG8="}`), ty, "$"); err != nil {
		t.Errorf("expected valid base64 to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `{"$bptd": "bytes", "value": "not-base64!!"}`), ty, "$"); err == nil {
		t.Error("expected invalid base64 to be rejected")
	}
}

func TestValidateValue_Timestamp(t *testing.T) {
	ty := mustType(t, `{"kind": "timestamp"}`)
	if err := ValidateValue(mustJSON(t, `{"$bptd": "timestamp", "value": "2026-09-05T12:34:56.123456789Z"}`), ty, "$"); err != nil {
		t.Errorf("expected valid UTC timestamp to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `{"$bptd": "timestamp", "value": "2026-09-05T12:34:56+02:00"}`), ty, "$"); err == nil {
		t.Error("expected an offset (non-Z) timestamp to be rejected (rule 7: UTC Z required)")
	}
}

func TestValidateValue_Duration(t *testing.T) {
	ty := mustType(t, `{"kind": "duration"}`)
	if err := ValidateValue(mustJSON(t, `{"$bptd": "duration", "seconds": "90", "nanos": 500000000}`), ty, "$"); err != nil {
		t.Errorf("expected valid duration to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `{"$bptd": "duration", "seconds": "90", "nanos": 1000000000}`), ty, "$"); err == nil {
		t.Error("expected nanos out of range to be rejected")
	}
}

func TestValidateValue_Array(t *testing.T) {
	ty := mustType(t, `{"kind": "array", "items": {"kind": "int64"}, "min_items": 1, "max_items": 3}`)
	if err := ValidateValue(mustJSON(t, `[1,2]`), ty, "$"); err != nil {
		t.Errorf("expected [1,2] to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `[]`), ty, "$"); err == nil {
		t.Error("expected empty array to violate min_items")
	}
	if err := ValidateValue(mustJSON(t, `[1,2,3,4]`), ty, "$"); err == nil {
		t.Error("expected 4 items to violate max_items")
	}
	if err := ValidateValue(mustJSON(t, `[1,"two"]`), ty, "$"); err == nil {
		t.Error("expected a non-int64 item to be rejected")
	}
}

func TestValidateValue_Array_UniqueItems(t *testing.T) {
	ty := mustType(t, `{"kind": "array", "items": {"kind": "int64"}, "unique_items": true}`)
	if err := ValidateValue(mustJSON(t, `[1,2,3]`), ty, "$"); err != nil {
		t.Errorf("expected unique items to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `[1,2,1]`), ty, "$"); err == nil {
		t.Error("expected a duplicate to be rejected")
	}
}

func TestValidateValue_Map(t *testing.T) {
	ty := mustType(t, `{"kind": "map", "keys": "string", "values": {"kind": "int64"}}`)
	if err := ValidateValue(mustJSON(t, `{"a": 1, "b": 2}`), ty, "$"); err != nil {
		t.Errorf("expected valid map to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `{"a": "not an int"}`), ty, "$"); err == nil {
		t.Error("expected a non-int64 value to be rejected")
	}
}

func TestValidateValue_Record(t *testing.T) {
	ty := mustType(t, `{"kind": "record", "fields": [
		{"name": "id", "type": {"kind": "int64"}, "required": true},
		{"name": "email", "type": {"kind": "string"}, "required": false}
	], "additional_fields": false}`)
	if err := ValidateValue(mustJSON(t, `{"id": 1}`), ty, "$"); err != nil {
		t.Errorf("expected valid record (optional field omitted) to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `{}`), ty, "$"); err == nil {
		t.Error("expected missing required field 'id' to be rejected")
	}
	if err := ValidateValue(mustJSON(t, `{"id": 1, "extra": true}`), ty, "$"); err == nil {
		t.Error("expected an undeclared field to be rejected (closed record)")
	}
}

func TestValidateValue_Enum(t *testing.T) {
	ty := mustType(t, `{"kind": "enum", "values": ["us-east", "eu-west"]}`)
	if err := ValidateValue(mustJSON(t, `"us-east"`), ty, "$"); err != nil {
		t.Errorf("expected a declared value to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `"ap-south"`), ty, "$"); err == nil {
		t.Error("expected an undeclared enum value to be rejected")
	}
}

func TestValidateValue_Union(t *testing.T) {
	ty := mustType(t, `{"kind": "union", "tag_field": "kind", "value_field": "value", "variants": [
		{"tag": "card", "type": {"kind": "record", "fields": [{"name": "last4", "type": {"kind": "string"}, "required": true}]}},
		{"tag": "bank", "type": {"kind": "record", "fields": [{"name": "iban", "type": {"kind": "string"}, "required": true}]}}
	]}`)
	if err := ValidateValue(mustJSON(t, `{"kind": "card", "value": {"last4": "1234"}}`), ty, "$"); err != nil {
		t.Errorf("expected a valid tagged union value to validate, got: %v", err)
	}
	if err := ValidateValue(mustJSON(t, `{"kind": "crypto", "value": {}}`), ty, "$"); err == nil {
		t.Error("expected an unrecognized tag to be rejected")
	}
	if err := ValidateValue(mustJSON(t, `{"kind": "card", "value": {}}`), ty, "$"); err == nil {
		t.Error("expected a payload missing the variant's required field to be rejected")
	}
}

func TestValidateValue_ErrorNamesPath(t *testing.T) {
	ty := mustType(t, `{"kind": "record", "fields": [{"name": "score", "type": {"kind": "int64"}, "required": true}]}`)
	err := ValidateValue(mustJSON(t, `{"score": "not a number"}`), ty, "$")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "$.score") {
		t.Errorf("expected error to name the field path $.score, got: %v", err)
	}
}
