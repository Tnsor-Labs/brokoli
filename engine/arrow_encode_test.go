package engine

// Writing Arrow is only safe if it round-trips exactly. These tests
// exist to prove the encoder either preserves a dataset or declines to
// encode it -- never a third outcome where data changes shape in
// transit (ADR-033 section 8).

import (
	"bytes"
	"io"
	"math"
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
		// "int rather than float64" used to be refused here. It is
		// accepted now (#521): Arrow Int64 holds it exactly and the
		// decoder returns int64, which is what the NDJSON path yields
		// for a whole number too. See
		// TestArrowEncodableSchema_AcceptsIntegerColumns.
		"an integer mixed with a float in one column": {
			Columns: []string{"v"},
			Rows:    []common.DataRow{{"v": int64(1)}, {"v": float64(1.5)}},
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

// #521: an integer column disqualified the entire dataset, so Arrow
// declined on essentially every real table, silently, and the measured
// 6.8x decode never applied to anything with an id in it.
func TestArrowEncodableSchema_AcceptsIntegerColumns(t *testing.T) {
	for name, ds := range map[string]*common.DataSet{
		"int64": {
			Columns: []string{"id"},
			Rows:    []common.DataRow{{"id": int64(1)}, {"id": int64(2)}},
		},
		"int": {
			Columns: []string{"id"},
			Rows:    []common.DataRow{{"id": 1}, {"id": 2}},
		},
		"an id beside the types that already worked": {
			Columns: []string{"id", "name", "amount", "active"},
			Rows: []common.DataRow{
				{"id": int64(1), "name": "a", "amount": 1.5, "active": true},
				{"id": int64(2), "name": "b", "amount": 2.5, "active": false},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, ok := arrowEncodableSchema(ds); !ok {
				t.Error("declined a dataset it can hold exactly, so this spills as NDJSON for no reason")
			}
		})
	}
}

// The point of accepting integers is worthless if the round trip is not
// exact, which is the whole reason they were excluded. These are the
// values float64 cannot hold.
func TestArrowRoundTripsLargeIntegersExactly(t *testing.T) {
	ds := &common.DataSet{
		Columns: []string{"id", "label"},
		Rows: []common.DataRow{
			{"id": int64(9007199254740993), "label": "2^53+1"},
			{"id": int64(math.MaxInt64), "label": "max"},
			{"id": int64(math.MinInt64), "label": "min"},
			{"id": nil, "label": "null id"},
		},
	}

	var buf bytes.Buffer
	if err := EncodeArrowIPC(&buf, ds); err != nil {
		t.Fatalf("encode: %v", err)
	}
	rows, cols, err := decodeArrowIPCRows(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(cols) != 2 {
		t.Fatalf("columns = %v", cols)
	}
	for i, want := range []interface{}{
		int64(9007199254740993), int64(math.MaxInt64), int64(math.MinInt64), nil,
	} {
		if got := rows[i]["id"]; got != want {
			t.Errorf("row %d id = %v (%T), want %v", i, got, got, want)
		}
	}
}

// The reader's memory contract is "one Arrow record batch becomes one
// batch of rows". That was true of the reader and false of the stream:
// the encoder wrote a single record for the whole dataset, so an Arrow
// ref materialised on read however carefully the reader was written.
func TestArrowWritesBoundedRecordBatches(t *testing.T) {
	const rows = arrowRecordRows*2 + 137 // deliberately not a multiple
	ds := &common.DataSet{Columns: []string{"id", "name"}}
	for i := 0; i < rows; i++ {
		ds.Rows = append(ds.Rows, common.DataRow{"id": int64(i), "name": "r"})
	}

	var buf bytes.Buffer
	if err := EncodeArrowIPC(&buf, ds); err != nil {
		t.Fatalf("encode: %v", err)
	}

	r, err := NewArrowBatchReader(&buf, ds.Columns)
	if err != nil {
		t.Fatal(err)
	}
	var batches, total int
	var largest int
	for {
		b, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		batches++
		total += len(b.Rows)
		if len(b.Rows) > largest {
			largest = len(b.Rows)
		}
	}

	if total != rows {
		t.Errorf("read %d rows, want %d", total, rows)
	}
	if batches < 3 {
		t.Errorf("read the dataset in %d batch(es); %d rows at %d per record should be 3",
			batches, rows, arrowRecordRows)
	}
	// The point of the whole change: no single batch holds everything.
	if largest > arrowRecordRows {
		t.Errorf("largest batch was %d rows, over the %d-row record size", largest, arrowRecordRows)
	}
}

// Bounding the batches must not change what comes back.
func TestArrowBoundedBatchesRoundTripExactly(t *testing.T) {
	const rows = arrowRecordRows + 5
	ds := &common.DataSet{Columns: []string{"id", "score", "ok", "name"}}
	for i := 0; i < rows; i++ {
		ds.Rows = append(ds.Rows, common.DataRow{
			"id":    int64(9007199254740993 + int64(i)), // past float64
			"score": float64(i) + 0.5,
			"ok":    i%2 == 0,
			"name":  "row",
		})
	}

	var buf bytes.Buffer
	if err := EncodeArrowIPC(&buf, ds); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, _, err := decodeArrowIPCRows(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != rows {
		t.Fatalf("decoded %d rows, want %d", len(got), rows)
	}
	for _, i := range []int{0, arrowRecordRows - 1, arrowRecordRows, rows - 1} {
		want := int64(9007199254740993 + int64(i))
		if got[i]["id"] != want {
			t.Errorf("row %d id = %v (%T), want %d", i, got[i]["id"], got[i]["id"], want)
		}
	}
}
