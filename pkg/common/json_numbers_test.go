package common

import "testing"

// 2^53 + 1: the first integer a float64 cannot hold. Decoded as a float it
// comes back as 9007199254740992.
const bigID = int64(9007199254740993)

// Rows parsed from JSON used to go through plain json.Unmarshal, which
// turns every number into a float64: an id above 2^53 changed value, and
// the row carried a float for what the document wrote as an integer. An
// integer now decodes to an exact int64, and only a number with a fraction
// or exponent, or one too large for int64, becomes a float64.
func TestParseJSONDataKeepsIntegersExact(t *testing.T) {
	cases := map[string]string{
		"array of objects": `[{"id": 9007199254740993, "price": 1.5, "tags": {"n": 9007199254740993}}]`,
		"single object":    `{"id": 9007199254740993, "price": 1.5, "tags": {"n": 9007199254740993}}`,
		"mixed array":      `[{"page": 1}, [{"id": 9007199254740993, "price": 1.5, "tags": {"n": 9007199254740993}}]]`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			rows, err := ParseJSONData([]byte(doc))
			if err != nil {
				t.Fatalf("ParseJSONData: %v", err)
			}
			row := rows[len(rows)-1]
			if got, ok := row["id"].(int64); !ok || got != bigID {
				t.Fatalf("id = %v (%T), want int64 %d", row["id"], row["id"], bigID)
			}
			if got, ok := row["price"].(float64); !ok || got != 1.5 {
				t.Fatalf("price = %v (%T), want float64 1.5", row["price"], row["price"])
			}
			nested, _ := row["tags"].(map[string]interface{})
			if got, ok := nested["n"].(int64); !ok || got != bigID {
				t.Fatalf("nested n = %v (%T), want int64 %d", nested["n"], nested["n"], bigID)
			}
		})
	}
}

func TestExtractRecordsAtPathKeepsIntegersExact(t *testing.T) {
	rows, err := ExtractRecordsAtPath([]byte(`{"data": {"items": [{"id": 9007199254740993}]}}`), "data.items")
	if err != nil {
		t.Fatalf("ExtractRecordsAtPath: %v", err)
	}
	if got, ok := rows[0]["id"].(int64); !ok || got != bigID {
		t.Fatalf("id = %v (%T), want int64 %d", rows[0]["id"], rows[0]["id"], bigID)
	}
}

// json.Unmarshal refuses a document with anything after its value; a
// decoder stops after the first value unless told otherwise. Moving to a
// decoder must not start accepting trailing garbage.
func TestParseJSONDataStillRefusesTrailingContent(t *testing.T) {
	for _, doc := range []string{`[{"id": 1}] trailing`, `{"id": 1}{"id": 2}`} {
		if _, err := ParseJSONData([]byte(doc)); err == nil {
			t.Errorf("ParseJSONData(%q) succeeded, want an error", doc)
		}
	}
	if _, err := ExtractRecordsAtPath([]byte(`{"data": []} x`), "data"); err == nil {
		t.Error("ExtractRecordsAtPath accepted trailing content")
	}
}

func TestDecodeJSONValueNumbers(t *testing.T) {
	cases := []struct {
		in   string
		want interface{}
	}{
		{`9007199254740993`, bigID},
		{`-42`, int64(-42)},
		{`1.5`, 1.5},
		{`1e3`, 1000.0},
		{`99999999999999999999`, 1e20},
	}
	for _, c := range cases {
		got, err := DecodeJSONValue([]byte(c.in))
		if err != nil {
			t.Fatalf("DecodeJSONValue(%s): %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("DecodeJSONValue(%s) = %v (%T), want %v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}

// json.Unmarshal into []map[string]interface{} turned a null element into
// a nil row and an object array with a scalar in it into an error, which
// sent the document on to the mixed-array rule. Both are kept as they were.
func TestParseJSONDataKeepsItsShapeRules(t *testing.T) {
	rows, err := ParseJSONData([]byte(`[{"a": 1}, null]`))
	if err != nil || len(rows) != 2 || rows[1] != nil {
		t.Fatalf("[{...}, null] = %v, %v; want two rows, the second nil", rows, err)
	}
	rows, err = ParseJSONData([]byte(`[{"a": 1}, 5, [{"b": 2}]]`))
	if err != nil || len(rows) != 2 || rows[1]["b"] != int64(2) {
		t.Fatalf("mixed array = %v, %v; want the two objects", rows, err)
	}
	rows, err = ParseJSONData([]byte(`null`))
	if err != nil || len(rows) != 1 || rows[0] != nil {
		t.Fatalf("null = %v, %v; want one nil row", rows, err)
	}
}
