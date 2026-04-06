package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func makeProfileDS(cols []string, rows []common.DataRow) *common.DataSet {
	return &common.DataSet{Columns: cols, Rows: rows}
}

// ── Profile tests ────────────────────────────────────────────────────

func TestProfileDataSet_Basic(t *testing.T) {
	ds := makeProfileDS(
		[]string{"name", "age"},
		[]common.DataRow{
			{"name": "Alice", "age": 30},
			{"name": "Bob", "age": 25},
			{"name": "Carol", "age": 35},
		},
	)

	p := ProfileDataSet(ds)

	if p.RowCount != 3 {
		t.Fatalf("expected 3 rows, got %d", p.RowCount)
	}
	if p.ColumnCount != 2 {
		t.Fatalf("expected 2 columns, got %d", p.ColumnCount)
	}
	if len(p.Columns) != 2 {
		t.Fatalf("expected 2 column profiles, got %d", len(p.Columns))
	}

	nameCol := p.Columns[0]
	if nameCol.Name != "name" {
		t.Errorf("expected column name 'name', got %q", nameCol.Name)
	}
	if nameCol.Type != "string" {
		t.Errorf("expected type string, got %q", nameCol.Type)
	}
	if nameCol.UniqueCount != 3 {
		t.Errorf("expected 3 unique, got %d", nameCol.UniqueCount)
	}

	ageCol := p.Columns[1]
	if ageCol.Name != "age" {
		t.Errorf("expected column name 'age', got %q", ageCol.Name)
	}
	if !ageCol.IsNumeric {
		t.Error("expected age to be numeric")
	}
}

func TestProfileDataSet_Empty(t *testing.T) {
	ds := makeProfileDS([]string{"a", "b"}, []common.DataRow{})

	p := ProfileDataSet(ds)
	if p.RowCount != 0 {
		t.Fatalf("expected 0 rows, got %d", p.RowCount)
	}
	if p.ColumnCount != 2 {
		t.Fatalf("expected 2 columns, got %d", p.ColumnCount)
	}
	for _, col := range p.Columns {
		if col.Type != "null" {
			t.Errorf("expected null type for empty column %q, got %q", col.Name, col.Type)
		}
	}
}

func TestProfileDataSet_NilDataSet(t *testing.T) {
	p := ProfileDataSet(nil)
	if p.RowCount != 0 {
		t.Errorf("expected 0 rows, got %d", p.RowCount)
	}
	if p.ColumnCount != 0 {
		t.Errorf("expected 0 columns, got %d", p.ColumnCount)
	}
	if len(p.Columns) != 0 {
		t.Errorf("expected empty columns slice")
	}
}

func TestProfileDataSet_NumericColumns(t *testing.T) {
	ds := makeProfileDS(
		[]string{"value"},
		[]common.DataRow{
			{"value": 10.0},
			{"value": 20.0},
			{"value": 30.0},
		},
	)

	p := ProfileDataSet(ds)
	col := p.Columns[0]

	if !col.IsNumeric {
		t.Fatal("expected numeric column")
	}
	if col.MinVal != "10" {
		t.Errorf("expected min 10, got %q", col.MinVal)
	}
	if col.MaxVal != "30" {
		t.Errorf("expected max 30, got %q", col.MaxVal)
	}
	if col.MeanVal != 20.0 {
		t.Errorf("expected mean 20, got %f", col.MeanVal)
	}
	if col.NullCount != 0 {
		t.Errorf("expected 0 nulls, got %d", col.NullCount)
	}
}

func TestProfileDataSet_NullHandling(t *testing.T) {
	ds := makeProfileDS(
		[]string{"x"},
		[]common.DataRow{
			{"x": "hello"},
			{"x": nil},
			{"x": nil},
			{"x": "world"},
		},
	)

	p := ProfileDataSet(ds)
	col := p.Columns[0]

	if col.NullCount != 2 {
		t.Errorf("expected 2 nulls, got %d", col.NullCount)
	}
	if col.NullPct != 50.0 {
		t.Errorf("expected 50%% null, got %.2f%%", col.NullPct)
	}
	if col.UniqueCount != 2 {
		t.Errorf("expected 2 unique, got %d", col.UniqueCount)
	}
}

func TestProfileDataSet_MixedTypes(t *testing.T) {
	ds := makeProfileDS(
		[]string{"mixed"},
		[]common.DataRow{
			{"mixed": "hello"},
			{"mixed": 42},
			{"mixed": nil},
		},
	)

	p := ProfileDataSet(ds)
	col := p.Columns[0]

	// Mixed string and number should be detected as string.
	if col.Type != "string" {
		t.Errorf("expected string type for mixed column, got %q", col.Type)
	}
	if col.NullCount != 1 {
		t.Errorf("expected 1 null, got %d", col.NullCount)
	}
}

func TestProfileDataSet_StringNumeric(t *testing.T) {
	// String values that are parseable as numbers should be numeric.
	ds := makeProfileDS(
		[]string{"amount"},
		[]common.DataRow{
			{"amount": "100.5"},
			{"amount": "200"},
			{"amount": "50.25"},
		},
	)

	p := ProfileDataSet(ds)
	col := p.Columns[0]

	if !col.IsNumeric {
		t.Error("expected numeric detection for string-encoded numbers")
	}
}

func TestProfileDataSet_BooleanColumn(t *testing.T) {
	ds := makeProfileDS(
		[]string{"flag"},
		[]common.DataRow{
			{"flag": true},
			{"flag": false},
			{"flag": true},
		},
	)

	p := ProfileDataSet(ds)
	col := p.Columns[0]

	if col.Type != "boolean" {
		t.Errorf("expected boolean type, got %q", col.Type)
	}
}

func TestProfileDataSet_SampleValues(t *testing.T) {
	ds := makeProfileDS(
		[]string{"color"},
		[]common.DataRow{
			{"color": "red"},
			{"color": "blue"},
			{"color": "green"},
			{"color": "red"},
			{"color": "yellow"},
		},
	)

	p := ProfileDataSet(ds)
	col := p.Columns[0]

	if len(col.SampleValues) != 3 {
		t.Errorf("expected 3 sample values, got %d: %v", len(col.SampleValues), col.SampleValues)
	}
}

// ── ExtractSchema tests ──────────────────────────────────────────────

func TestExtractSchema(t *testing.T) {
	ds := makeProfileDS(
		[]string{"id", "name"},
		[]common.DataRow{
			{"id": 1, "name": "Alice"},
			{"id": 2, "name": nil},
		},
	)

	p := ProfileDataSet(ds)
	s := ExtractSchema(p)

	if len(s.Columns) != 2 {
		t.Fatalf("expected 2 schema columns, got %d", len(s.Columns))
	}
	if s.Columns[0].Name != "id" {
		t.Errorf("expected 'id', got %q", s.Columns[0].Name)
	}
	if s.Columns[0].Type != "number" {
		t.Errorf("expected number type for id, got %q", s.Columns[0].Type)
	}
	if s.Columns[1].NullPct != 50.0 {
		t.Errorf("expected 50%% null for name, got %.2f%%", s.Columns[1].NullPct)
	}
}

func TestExtractSchema_Nil(t *testing.T) {
	s := ExtractSchema(nil)
	if len(s.Columns) != 0 {
		t.Errorf("expected empty schema from nil profile")
	}
}

// ── Drift detection tests ────────────────────────────────────────────

func TestDetectDrift_NoChanges(t *testing.T) {
	s := &SchemaSnapshot{
		Columns: []SchemaColumn{
			{Name: "id", Type: "number", NullPct: 0},
			{Name: "name", Type: "string", NullPct: 5},
		},
	}

	alerts := DetectDrift(s, s)
	if len(alerts) != 0 {
		t.Errorf("expected no drift, got %d alerts", len(alerts))
	}
}

func TestDetectDrift_ColumnAdded(t *testing.T) {
	prev := &SchemaSnapshot{
		Columns: []SchemaColumn{{Name: "id", Type: "number"}},
	}
	cur := &SchemaSnapshot{
		Columns: []SchemaColumn{
			{Name: "id", Type: "number"},
			{Name: "email", Type: "string"},
		},
	}

	alerts := DetectDrift(prev, cur)
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Type != DriftColumnAdded {
		t.Errorf("expected column_added, got %s", alerts[0].Type)
	}
	if alerts[0].Column != "email" {
		t.Errorf("expected column 'email', got %q", alerts[0].Column)
	}
	if alerts[0].Severity != "warning" {
		t.Errorf("expected warning severity, got %q", alerts[0].Severity)
	}
}

func TestDetectDrift_ColumnRemoved(t *testing.T) {
	prev := &SchemaSnapshot{
		Columns: []SchemaColumn{
			{Name: "id", Type: "number"},
			{Name: "name", Type: "string"},
		},
	}
	cur := &SchemaSnapshot{
		Columns: []SchemaColumn{{Name: "id", Type: "number"}},
	}

	alerts := DetectDrift(prev, cur)
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Type != DriftColumnRemoved {
		t.Errorf("expected column_removed, got %s", alerts[0].Type)
	}
	if alerts[0].Severity != "critical" {
		t.Errorf("expected critical severity, got %q", alerts[0].Severity)
	}
}

func TestDetectDrift_TypeChanged(t *testing.T) {
	prev := &SchemaSnapshot{
		Columns: []SchemaColumn{{Name: "age", Type: "number"}},
	}
	cur := &SchemaSnapshot{
		Columns: []SchemaColumn{{Name: "age", Type: "string"}},
	}

	alerts := DetectDrift(prev, cur)
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Type != DriftTypeChanged {
		t.Errorf("expected type_changed, got %s", alerts[0].Type)
	}
	if alerts[0].Previous != "number" || alerts[0].Current != "string" {
		t.Errorf("expected number->string, got %s->%s", alerts[0].Previous, alerts[0].Current)
	}
	if alerts[0].Severity != "critical" {
		t.Errorf("expected critical severity, got %q", alerts[0].Severity)
	}
}

func TestDetectDrift_NullSpike(t *testing.T) {
	prev := &SchemaSnapshot{
		Columns: []SchemaColumn{{Name: "email", Type: "string", NullPct: 5}},
	}
	cur := &SchemaSnapshot{
		Columns: []SchemaColumn{{Name: "email", Type: "string", NullPct: 30}},
	}

	alerts := DetectDrift(prev, cur)
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Type != DriftNullSpike {
		t.Errorf("expected null_spike, got %s", alerts[0].Type)
	}
	if alerts[0].Severity != "warning" {
		t.Errorf("expected warning severity, got %q", alerts[0].Severity)
	}
}

func TestDetectDrift_NullSpike_BelowThreshold(t *testing.T) {
	prev := &SchemaSnapshot{
		Columns: []SchemaColumn{{Name: "email", Type: "string", NullPct: 5}},
	}
	cur := &SchemaSnapshot{
		Columns: []SchemaColumn{{Name: "email", Type: "string", NullPct: 24}},
	}

	alerts := DetectDrift(prev, cur)
	if len(alerts) != 0 {
		t.Errorf("expected no alerts for 19pp increase, got %d", len(alerts))
	}
}

func TestDetectDrift_MultipleDrifts(t *testing.T) {
	prev := &SchemaSnapshot{
		Columns: []SchemaColumn{
			{Name: "id", Type: "number", NullPct: 0},
			{Name: "name", Type: "string", NullPct: 2},
			{Name: "old_col", Type: "string", NullPct: 0},
		},
	}
	cur := &SchemaSnapshot{
		Columns: []SchemaColumn{
			{Name: "id", Type: "string", NullPct: 0},      // type changed
			{Name: "name", Type: "string", NullPct: 50},   // null spike
			{Name: "new_col", Type: "number", NullPct: 0}, // added
			// old_col removed
		},
	}

	alerts := DetectDrift(prev, cur)

	types := make(map[DriftType]int)
	for _, a := range alerts {
		types[a.Type]++
	}

	if types[DriftColumnRemoved] != 1 {
		t.Errorf("expected 1 column_removed, got %d", types[DriftColumnRemoved])
	}
	if types[DriftColumnAdded] != 1 {
		t.Errorf("expected 1 column_added, got %d", types[DriftColumnAdded])
	}
	if types[DriftTypeChanged] != 1 {
		t.Errorf("expected 1 type_changed, got %d", types[DriftTypeChanged])
	}
	if types[DriftNullSpike] != 1 {
		t.Errorf("expected 1 null_spike, got %d", types[DriftNullSpike])
	}
	if len(alerts) != 4 {
		t.Errorf("expected 4 total alerts, got %d", len(alerts))
	}
}

func TestDetectDrift_NilSnapshots(t *testing.T) {
	alerts := DetectDrift(nil, nil)
	if len(alerts) != 0 {
		t.Errorf("expected no alerts for nil snapshots, got %d", len(alerts))
	}

	cur := &SchemaSnapshot{
		Columns: []SchemaColumn{{Name: "id", Type: "number"}},
	}
	alerts = DetectDrift(nil, cur)
	if len(alerts) != 1 || alerts[0].Type != DriftColumnAdded {
		t.Errorf("expected 1 column_added from nil previous, got %v", alerts)
	}
}
