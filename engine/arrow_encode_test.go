package engine

// Writing Arrow is only safe if it round-trips exactly. These tests
// exist to prove the encoder either preserves a dataset or declines to
// encode it -- never a third outcome where data changes shape in
// transit (ADR-033 section 8).

import (
	"bytes"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func TestArrowEncodableSchema_AcceptsUniformlyTypedColumns(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"s", "n", "b"},
		Rows: []common.DataRow{
			{"s": "x", "n": float64(1), "b": true},
			{"s": "y", "n": float64(2), "b": false},
		},
	}
	if _, ok := arrowEncodableSchema(ds); !ok {
		t.Fatal("a uniformly typed dataset was rejected")
	}
}

// A null does not make a column mixed -- it makes it nullable.
func TestArrowEncodableSchema_NullsDoNotDisqualify(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"s"},
		Rows:    []common.DataRow{{"s": "x"}, {"s": nil}, {}},
	}
	if _, ok := arrowEncodableSchema(ds); !ok {
		t.Fatal("nulls in an otherwise typed column disqualified the dataset")
	}
}

// The cases that must fall back rather than be approximated.
func TestArrowEncodableSchema_RefusesWhatItCannotRoundTrip(t *testing.T) {
	cases := map[string]*common.DataSet{
		"mixed types in one column": {
			Columns: []string{"v"},
			Rows:    []common.DataRow{{"v": "a string"}, {"v": float64(2)}},
		},
		"all-null column has no inferable type": {
			Columns: []string{"v"},
			Rows:    []common.DataRow{{"v": nil}, {"v": nil}},
		},
		"nested value": {
			Columns: []string{"v"},
			Rows:    []common.DataRow{{"v": map[string]interface{}{"a": 1}}},
		},
		"slice value": {
			Columns: []string{"v"},
			Rows:    []common.DataRow{{"v": []interface{}{1, 2}}},
		},
		"int rather than float64": {
			Columns: []string{"v"},
			Rows:    []common.DataRow{{"v": 3}},
		},
	}
	for name, ds := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := arrowEncodableSchema(ds); ok {
				t.Error("encoded something it cannot round-trip exactly; NDJSON must take this")
			}
		})
	}
}

// The property the whole write half rests on: encode then decode
// returns the same values NDJSON would, types included.
//
// Note it is NOT raw Go identity, deliberately. A whole float64 comes
// back as int64, because that is what the NDJSON path has always done
// (normalizeJSONNumbers prefers int64 for any integer that fits) and
// ADR-033 section 8 requires the transport not to change the contract.
// Matching the incumbent beats matching Go's type system.
func TestArrowEncodeDecodeRoundTripsExactly(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"name", "amount", "active"},
		Rows: []common.DataRow{
			{"name": "alpha", "amount": float64(1.5), "active": true},
			{"name": "beta", "amount": float64(-2), "active": false},
			{"name": nil, "amount": float64(0), "active": true},
		},
	}
	// What the NDJSON contract says these become on the way back.
	want := []common.DataRow{
		{"name": "alpha", "amount": float64(1.5), "active": true},
		{"name": "beta", "amount": int64(-2), "active": false},
		{"name": nil, "amount": int64(0), "active": true},
	}
	var buf bytes.Buffer
	if err := EncodeArrowIPC(&buf, ds); err != nil {
		t.Fatalf("EncodeArrowIPC: %v", err)
	}

	r, err := NewArrowBatchReader(&buf, ds.Columns)
	if err != nil {
		t.Fatal(err)
	}
	got := drainBatches(t, r)
	if len(got) != len(ds.Rows) {
		t.Fatalf("row count = %d, want %d", len(got), len(ds.Rows))
	}
	for i, wantRow := range want {
		for _, c := range ds.Columns {
			if got[i][c] != wantRow[c] {
				t.Errorf("row %d column %q = %#v, want %#v", i, c, got[i][c], wantRow[c])
			}
		}
	}
}

// Calling the encoder on a dataset it cannot represent must fail loudly
// rather than write something that reads back wrong.
func TestArrowEncodeRefusesUnrepresentableDataset(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"v"},
		Rows:    []common.DataRow{{"v": "a string"}, {"v": float64(2)}},
	}
	if err := EncodeArrowIPC(&bytes.Buffer{}, ds); err == nil {
		t.Fatal("encoded a dataset with mixed column types")
	}
}
