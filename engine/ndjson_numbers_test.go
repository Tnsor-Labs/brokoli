package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// A streamed pipeline and a materialising one must agree about what a number
// is. They did not: encoding/json turns every JSON number into a float64, and
// %v on a float64 is not the text that went in. A bigint id crossing a
// streamed boundary arrived at the write as "1e+06".
func TestNDJSONPreservesIntegers(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string // what fmt.Sprintf("%v") must produce
	}{
		{"the value that broke a COPY", `{"v":1000000}`, "1000000"},
		{"a small integer", `{"v":42}`, "42"},
		{"zero", `{"v":0}`, "0"},
		{"negative", `{"v":-1000000}`, "-1000000"},
		{"beyond 2^53, where float64 loses digits", `{"v":9007199254740993}`, "9007199254740993"},
		{"max int64", `{"v":9223372036854775807}`, "9223372036854775807"},
		{"a real float stays a float", `{"v":1.5}`, "1.5"},
		{"a float that is integral", `{"v":2.0}`, "2"},
		{"beyond int64 falls back to float", `{"v":92233720368547758070}`, "9.223372036854776e+19"},
		{"a string is untouched", `{"v":"1e+06"}`, "1e+06"},
		{"null is untouched", `{"v":null}`, "<nil>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec := json.NewDecoder(bytes.NewReader([]byte(tc.json)))
			var row common.DataRow
			if err := decodeRow(dec, &row); err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%v", row["v"]); got != tc.want {
				t.Errorf("rendered as %q, want %q (decoded as %T)", got, tc.want, row["v"])
			}
		})
	}
}

// A number inside a JSON column must not be left as a json.Number, which no
// existing type switch matches — it would turn a rendering bug into a
// silently-skipped value.
func TestNDJSONNormalizesNestedNumbers(t *testing.T) {
	dec := json.NewDecoder(bytes.NewReader([]byte(
		`{"obj":{"n":1000000,"deep":{"m":2000000}},"arr":[1000000,{"k":3000000}]}`)))
	var row common.DataRow
	if err := decodeRow(dec, &row); err != nil {
		t.Fatal(err)
	}

	obj := row["obj"].(map[string]interface{})
	if _, ok := obj["n"].(int64); !ok {
		t.Errorf("nested number is %T, want int64", obj["n"])
	}
	deep := obj["deep"].(map[string]interface{})
	if _, ok := deep["m"].(int64); !ok {
		t.Errorf("doubly nested number is %T, want int64", deep["m"])
	}
	arr := row["arr"].([]interface{})
	if _, ok := arr[0].(int64); !ok {
		t.Errorf("number in an array is %T, want int64", arr[0])
	}
	inArr := arr[1].(map[string]interface{})
	if _, ok := inArr["k"].(int64); !ok {
		t.Errorf("number in an object in an array is %T, want int64", inArr["k"])
	}
}

// The property that matters is that the two paths agree, not that either one
// renders a particular way. This asserts a round trip through the streamed
// encoding leaves the value identical to what went in.
func TestNDJSONRoundTripIsIdentity(t *testing.T) {
	in := &common.DataSet{
		Columns: []string{"id", "big", "f", "s"},
		Rows: []common.DataRow{
			{"id": int64(1000000), "big": int64(9007199254740993), "f": 1.5, "s": "text"},
			{"id": int64(0), "big": int64(-9007199254740993), "f": 0.1, "s": ""},
		},
	}
	var buf bytes.Buffer
	if err := EncodeArrowJSON(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := DecodeArrowJSON(bytes.NewReader(buf.Bytes()), in.Columns)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != len(in.Rows) {
		t.Fatalf("row count: got %d, want %d", len(out.Rows), len(in.Rows))
	}
	for i := range in.Rows {
		for _, c := range in.Columns {
			want := fmt.Sprintf("%v", in.Rows[i][c])
			got := fmt.Sprintf("%v", out.Rows[i][c])
			if got != want {
				t.Errorf("row %d column %q: %q survived the round trip as %q (%T)",
					i, c, want, got, out.Rows[i][c])
			}
		}
	}
}

// The round trip resolves an ambiguity JSON cannot express, and the resolution
// should be recorded rather than discovered. An integer stays an integer,
// which is the point; a float that happens to be integral becomes one too,
// which is the cost.
//
// The cost is affordable because nothing downstream can tell them apart: both
// render identically through the CSV and COPY writers, toAggFloat accepts
// both, and a "number" type assertion matches both. If any of that stops being
// true, this test is where to come back to.
func TestNDJSONRoundTripResolvesIntegralFloatsToIntegers(t *testing.T) {
	var buf bytes.Buffer
	in := &common.DataSet{
		Columns: []string{"integral", "fractional"},
		Rows:    []common.DataRow{{"integral": 150.0, "fractional": 1.5}},
	}
	if err := EncodeArrowJSON(&buf, in); err != nil {
		t.Fatal(err)
	}
	out, err := DecodeArrowJSON(bytes.NewReader(buf.Bytes()), in.Columns)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Rows[0]["integral"].(int64); !ok {
		t.Errorf("an integral float came back as %T, want int64", out.Rows[0]["integral"])
	}
	if _, ok := out.Rows[0]["fractional"].(float64); !ok {
		t.Errorf("a fractional float came back as %T, want float64", out.Rows[0]["fractional"])
	}
	// The property that matters: the rendered value is unchanged either way.
	if got := fmt.Sprintf("%v", out.Rows[0]["integral"]); got != "150" {
		t.Errorf("integral value rendered as %q, want %q", got, "150")
	}
}
