package taskinterface

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func mustType(t *testing.T, doc string) Type {
	t.Helper()
	var raw interface{}
	if err := json.Unmarshal([]byte(doc), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ty, err := ParseType(raw)
	if err != nil {
		t.Fatalf("ParseType: %v", err)
	}
	return ty
}

func mustPortValue(t *testing.T, doc string) PortValue {
	t.Helper()
	var raw interface{}
	if err := json.Unmarshal([]byte(doc), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	pv, err := ParsePortValue(raw)
	if err != nil {
		t.Fatalf("ParsePortValue: %v", err)
	}
	return pv
}

// --- rule 1 / port-level kind mismatch -------------------------------------

func TestAssignPort_ValueKindMismatch(t *testing.T) {
	producer := mustPortValue(t, `{"kind": "dataset"}`)
	consumer := mustPortValue(t, `{"kind": "scalar", "type": {"kind": "string"}}`)
	res := AssignPort(producer, consumer)
	if res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignPort_Control_AlwaysAssignable(t *testing.T) {
	producer := mustPortValue(t, `{"kind": "control"}`)
	consumer := mustPortValue(t, `{"kind": "control"}`)
	if res := AssignPort(producer, consumer); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

// --- rule 12: unknown -------------------------------------------------------

func TestAssignType_BothUnknown_Assignable(t *testing.T) {
	u := mustType(t, `{"kind": "unknown"}`)
	if res := AssignType(u, u, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignType_OneSideUnknown_Unverified(t *testing.T) {
	u := mustType(t, `{"kind": "unknown"}`)
	s := mustType(t, `{"kind": "string"}`)
	for _, tc := range []struct{ p, c Type }{{u, s}, {s, u}} {
		res := AssignType(tc.p, tc.c, "$")
		if res.Verdict != Unverified {
			t.Fatalf("expected Unverified, got %s: %s", res.Verdict, res.Reason)
		}
	}
}

// --- rule 1 / rule 5: kind mismatch, no implicit numeric widening -----------

func TestAssignType_Int64ToFloat64_Incompatible(t *testing.T) {
	i := mustType(t, `{"kind": "int64"}`)
	f := mustType(t, `{"kind": "float64"}`)
	res := AssignType(i, f, "$")
	if res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible (rule 5: no implicit widening), got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignType_MatchingPrimitives_Assignable(t *testing.T) {
	for _, kind := range []string{"boolean", "string", "bytes", "date", "timestamp", "duration", "json"} {
		ty := mustType(t, `{"kind": "`+kind+`"}`)
		if res := AssignType(ty, ty, "$"); res.Verdict != Assignable {
			t.Fatalf("%s: expected Assignable, got %s: %s", kind, res.Verdict, res.Reason)
		}
	}
}

// --- rule 4: nullable --------------------------------------------------------

func TestAssignType_Nullable(t *testing.T) {
	nullableStr := mustType(t, `{"kind": "string", "nullable": true}`)
	plainStr := mustType(t, `{"kind": "string"}`)

	if res := AssignType(nullableStr, plainStr, "$"); res.Verdict != Incompatible {
		t.Fatalf("nullable producer -> non-nullable consumer: expected Incompatible, got %s", res.Verdict)
	}
	if res := AssignType(plainStr, nullableStr, "$"); res.Verdict != Assignable {
		t.Fatalf("non-nullable producer -> nullable consumer: expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
	if res := AssignType(nullableStr, nullableStr, "$"); res.Verdict != Assignable {
		t.Fatalf("both nullable: expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

// --- rule 2: records ---------------------------------------------------------

func TestAssignRecord_RequiredFieldMissing(t *testing.T) {
	producer := mustType(t, `{"kind": "record", "fields": [{"name": "id", "type": {"kind": "int64"}, "required": true}]}`)
	consumer := mustType(t, `{"kind": "record", "fields": [
		{"name": "id", "type": {"kind": "int64"}, "required": true},
		{"name": "score", "type": {"kind": "float64"}, "required": true}
	]}`)
	res := AssignType(producer, consumer, "$")
	if res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible, got %s: %s", res.Verdict, res.Reason)
	}
	if !strings.Contains(res.Path, "score") {
		t.Errorf("expected path to name the missing field, got %q", res.Path)
	}
}

func TestAssignRecord_OptionalProducerFieldCannotSatisfyRequiredConsumer(t *testing.T) {
	producer := mustType(t, `{"kind": "record", "fields": [{"name": "email", "type": {"kind": "string"}, "required": false}]}`)
	consumer := mustType(t, `{"kind": "record", "fields": [{"name": "email", "type": {"kind": "string"}, "required": true}]}`)
	res := AssignType(producer, consumer, "$")
	if res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignRecord_OptionalConsumerFieldAbsentFromProducer_Fine(t *testing.T) {
	producer := mustType(t, `{"kind": "record", "fields": [{"name": "id", "type": {"kind": "int64"}, "required": true}]}`)
	consumer := mustType(t, `{"kind": "record", "fields": [
		{"name": "id", "type": {"kind": "int64"}, "required": true},
		{"name": "nickname", "type": {"kind": "string"}, "required": false}
	]}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignRecord_ProducerExtrasRejectedWhenConsumerClosed(t *testing.T) {
	producer := mustType(t, `{"kind": "record", "fields": [
		{"name": "id", "type": {"kind": "int64"}, "required": true},
		{"name": "debug", "type": {"kind": "string"}, "required": false}
	]}`)
	consumer := mustType(t, `{"kind": "record", "fields": [{"name": "id", "type": {"kind": "int64"}, "required": true}], "additional_fields": false}`)
	res := AssignType(producer, consumer, "$")
	if res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible (closed consumer, producer extra 'debug'), got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignRecord_ProducerExtrasAllowedWhenConsumerOpen(t *testing.T) {
	producer := mustType(t, `{"kind": "record", "fields": [
		{"name": "id", "type": {"kind": "int64"}, "required": true},
		{"name": "debug", "type": {"kind": "string"}, "required": false}
	]}`)
	consumer := mustType(t, `{"kind": "record", "fields": [{"name": "id", "type": {"kind": "int64"}, "required": true}], "additional_fields": true}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

// TestADR032WorkedExample reproduces ADR-032 section 9's own diagnostic:
//
//	publish.orders <- score_orders.result: required field $.score expects
//	float64 but producer declares string
func TestADR032WorkedExample(t *testing.T) {
	producer := mustType(t, `{"kind": "record", "fields": [{"name": "score", "type": {"kind": "string"}, "required": true}]}`)
	consumer := mustType(t, `{"kind": "record", "fields": [{"name": "score", "type": {"kind": "float64"}, "required": true}]}`)
	res := AssignType(producer, consumer, "$")
	if res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible, got %s", res.Verdict)
	}
	if res.Path != "$.score" {
		t.Errorf("expected path $.score, got %q", res.Path)
	}
	if !strings.Contains(res.Reason, "float64") || !strings.Contains(res.Reason, "string") {
		t.Errorf("expected reason to name both types, got %q", res.Reason)
	}
}

// --- rule 6: enum -------------------------------------------------------------

func TestAssignEnum_Subset_Assignable(t *testing.T) {
	producer := mustType(t, `{"kind": "enum", "values": ["us-east"]}`)
	consumer := mustType(t, `{"kind": "enum", "values": ["us-east", "eu-west"]}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignEnum_NotSubset_Incompatible(t *testing.T) {
	producer := mustType(t, `{"kind": "enum", "values": ["us-east", "ap-south"]}`)
	consumer := mustType(t, `{"kind": "enum", "values": ["us-east", "eu-west"]}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible, got %s", res.Verdict)
	}
}

// --- rule 10: union -----------------------------------------------------------

func TestAssignUnion_MatchingVariants_Assignable(t *testing.T) {
	producer := mustType(t, `{"kind": "union", "tag_field": "kind", "value_field": "value", "variants": [
		{"tag": "card", "type": {"kind": "record", "fields": [{"name": "last4", "type": {"kind": "string"}, "required": true}]}}
	]}`)
	consumer := mustType(t, `{"kind": "union", "tag_field": "kind", "value_field": "value", "variants": [
		{"tag": "card", "type": {"kind": "record", "fields": [{"name": "last4", "type": {"kind": "string"}, "required": true}], "additional_fields": true}},
		{"tag": "bank", "type": {"kind": "record", "fields": [{"name": "iban", "type": {"kind": "string"}, "required": true}]}}
	]}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignUnion_MissingConsumerVariant_Incompatible(t *testing.T) {
	producer := mustType(t, `{"kind": "union", "tag_field": "kind", "value_field": "value", "variants": [
		{"tag": "crypto", "type": {"kind": "record", "fields": []}}
	]}`)
	consumer := mustType(t, `{"kind": "union", "tag_field": "kind", "value_field": "value", "variants": [
		{"tag": "card", "type": {"kind": "record", "fields": []}}
	]}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible, got %s", res.Verdict)
	}
}

// --- rule 8/9: array, map -------------------------------------------------------

func TestAssignArray_ItemRecursion(t *testing.T) {
	producer := mustType(t, `{"kind": "array", "items": {"kind": "int64"}}`)
	consumer := mustType(t, `{"kind": "array", "items": {"kind": "string"}}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible (item type mismatch), got %s", res.Verdict)
	}
}

func TestAssignArray_MinMaxItemsSubset(t *testing.T) {
	producer := mustType(t, `{"kind": "array", "items": {"kind": "string"}, "min_items": 2, "max_items": 5}`)
	consumer := mustType(t, `{"kind": "array", "items": {"kind": "string"}, "min_items": 1, "max_items": 10}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable (producer's range is inside consumer's), got %s: %s", res.Verdict, res.Reason)
	}
	// reversed: producer's range is WIDER than consumer's -> incompatible.
	if res := AssignType(consumer, producer, "$"); res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible (producer's range is wider than consumer's), got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignMap_ValueRecursion(t *testing.T) {
	producer := mustType(t, `{"kind": "map", "keys": "string", "values": {"kind": "int64"}}`)
	consumer := mustType(t, `{"kind": "map", "keys": "string", "values": {"kind": "int64"}}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

// --- rule 11: string constraints -----------------------------------------------

func TestAssignString_LengthSubset(t *testing.T) {
	producer := mustType(t, `{"kind": "string", "min_length": 2, "max_length": 8}`)
	consumer := mustType(t, `{"kind": "string", "min_length": 1, "max_length": 10}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
	if res := AssignType(consumer, producer, "$"); res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible (wider producer range), got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignString_PatternIdentical_Assignable(t *testing.T) {
	producer := mustType(t, `{"kind": "string", "pattern": "^[a-z]+$"}`)
	consumer := mustType(t, `{"kind": "string", "pattern": "^[a-z]+$"}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignString_PatternDifferent_Unverified(t *testing.T) {
	producer := mustType(t, `{"kind": "string", "pattern": "^[a-z]+$"}`)
	consumer := mustType(t, `{"kind": "string", "pattern": "^[a-zA-Z]+$"}`)
	res := AssignType(producer, consumer, "$")
	if res.Verdict != Unverified {
		t.Fatalf("expected Unverified (rule 11: patterns assignable only when identical), got %s: %s", res.Verdict, res.Reason)
	}
}

// --- rule 11: numeric constraints -----------------------------------------------

func TestAssignInt64_RangeSubset(t *testing.T) {
	producer := mustType(t, `{"kind": "int64", "minimum": "0", "maximum": "100"}`)
	consumer := mustType(t, `{"kind": "int64", "minimum": "-10", "maximum": "1000"}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
	if res := AssignType(consumer, producer, "$"); res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible (wider producer range), got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignInt64_LargeBoundary(t *testing.T) {
	// 2^62, well past float64's 2^53 exact-integer boundary -- proves
	// this isn't silently going through a lossy float64 conversion.
	producer := mustType(t, `{"kind": "int64", "minimum": "0", "maximum": "4611686018427387904"}`)
	consumer := mustType(t, `{"kind": "int64", "minimum": "0", "maximum": "4611686018427387904"}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
	tighter := mustType(t, `{"kind": "int64", "minimum": "0", "maximum": "4611686018427387903"}`)
	if res := AssignType(producer, tighter, "$"); res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible (producer's max exceeds consumer's by 1), got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignDecimal_RangeSubset(t *testing.T) {
	producer := mustType(t, `{"kind": "decimal", "minimum": "0.00", "maximum": "99.99"}`)
	consumer := mustType(t, `{"kind": "decimal", "minimum": "-100.00", "maximum": "100.00"}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignFloat64_RangeSubset(t *testing.T) {
	producer := mustType(t, `{"kind": "float64", "minimum": 0.5, "maximum": 0.9}`)
	consumer := mustType(t, `{"kind": "float64", "minimum": 0, "maximum": 1}`)
	if res := AssignType(producer, consumer, "$"); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
	if res := AssignType(consumer, producer, "$"); res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignNumeric_ConsumerBoundButProducerUnset_Unverified(t *testing.T) {
	producer := mustType(t, `{"kind": "int64"}`)
	consumer := mustType(t, `{"kind": "int64", "minimum": "0"}`)
	res := AssignType(producer, consumer, "$")
	if res.Verdict != Unverified {
		t.Fatalf("expected Unverified (can't prove an undeclared producer range), got %s: %s", res.Verdict, res.Reason)
	}
}

// --- dataset row / rule 3 --------------------------------------------------------

func TestAssignPort_Dataset_BothRowsUnknown_Assignable(t *testing.T) {
	producer := mustPortValue(t, `{"kind": "dataset", "row": {"kind": "unknown"}}`)
	consumer := mustPortValue(t, `{"kind": "dataset", "row": {"kind": "unknown"}}`)
	if res := AssignPort(producer, consumer); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignPort_Dataset_AbsentRowConsumer_Assignable(t *testing.T) {
	producer := mustPortValue(t, `{"kind": "dataset"}`) // no row at all
	consumer := mustPortValue(t, `{"kind": "dataset"}`) // no row at all
	if res := AssignPort(producer, consumer); res.Verdict != Assignable {
		t.Fatalf("expected Assignable (consumer declares no row shape), got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignPort_Dataset_AbsentProducerRow_ClosedConsumer_Unverified(t *testing.T) {
	producer := mustPortValue(t, `{"kind": "dataset"}`)
	consumer := mustPortValue(t, `{"kind": "dataset", "row": {"kind": "record", "fields": [{"name": "id", "type": {"kind": "int64"}, "required": true}]}}`)
	res := AssignPort(producer, consumer)
	if res.Verdict != Unverified {
		t.Fatalf("expected Unverified (rule 3: unknown producer cannot prove a closed consumer), got %s: %s", res.Verdict, res.Reason)
	}
}

// --- rule 7: artifact media types -------------------------------------------------

func TestAssignArtifact_MediaTypeSubset(t *testing.T) {
	producer := mustPortValue(t, `{"kind": "artifact", "media_types": ["application/pdf"]}`)
	consumer := mustPortValue(t, `{"kind": "artifact", "media_types": ["application/pdf", "text/csv"]}`)
	if res := AssignPort(producer, consumer); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

func TestAssignArtifact_MediaTypeNotAccepted_Incompatible(t *testing.T) {
	producer := mustPortValue(t, `{"kind": "artifact", "media_types": ["image/png"]}`)
	consumer := mustPortValue(t, `{"kind": "artifact", "media_types": ["application/pdf"]}`)
	if res := AssignPort(producer, consumer); res.Verdict != Incompatible {
		t.Fatalf("expected Incompatible, got %s", res.Verdict)
	}
}

func TestAssignArtifact_ConsumerAcceptsAny(t *testing.T) {
	producer := mustPortValue(t, `{"kind": "artifact", "media_types": ["image/png"]}`)
	consumer := mustPortValue(t, `{"kind": "artifact"}`)
	if res := AssignPort(producer, consumer); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

// --- collection --------------------------------------------------------------------

func TestAssignPort_Collection_ItemRecursion(t *testing.T) {
	producer := mustPortValue(t, `{"kind": "collection", "items": {"kind": "scalar", "type": {"kind": "int64"}}, "ordered": true, "item_key": {"kind": "string"}}`)
	consumer := mustPortValue(t, `{"kind": "collection", "items": {"kind": "scalar", "type": {"kind": "int64"}}, "ordered": true, "item_key": {"kind": "string"}}`)
	if res := AssignPort(producer, consumer); res.Verdict != Assignable {
		t.Fatalf("expected Assignable, got %s: %s", res.Verdict, res.Reason)
	}
}

// --- parsing real fixtures from step 1 ----------------------------------------------

func TestParsePortValue_AgainstRealFixtures(t *testing.T) {
	fixtures := []string{
		"../../docs/schema/fixtures/task-interface/positive/dataset-record.json",
		"../../docs/schema/fixtures/task-interface/positive/enum-and-union.json",
		"../../docs/schema/fixtures/task-interface/positive/collection-artifact-control.json",
		"../../docs/schema/fixtures/task-interface/positive/nullable-scalar.json",
	}
	for _, path := range fixtures {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var doc map[string]interface{}
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatalf("unmarshal %s: %v", path, err)
			}
			for portGroup, ports := range map[string]interface{}{"inputs": doc["inputs"], "outputs": doc["outputs"]} {
				portsMap, ok := ports.(map[string]interface{})
				if !ok {
					continue
				}
				for name, p := range portsMap {
					pm := p.(map[string]interface{})
					if _, err := ParsePortValue(pm["value"]); err != nil {
						t.Errorf("%s.%s.%s: ParsePortValue: %v", path, portGroup, name, err)
					}
				}
			}
		})
	}
}
