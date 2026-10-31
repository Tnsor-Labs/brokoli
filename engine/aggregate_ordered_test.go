package engine

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
)

// min and max used to fold only values that parse as numbers, so over a
// column of dates none counted and every group's max came out as 0, with
// no error: a silent wrong answer.

func aggregateRows(t *testing.T, rows []common.DataRow, fields ...AggField) map[string]common.DataRow {
	t.Helper()
	ds := &common.DataSet{Columns: []string{"customer", "value"}, Rows: rows}
	rule := TransformRule{Type: "aggregate", GroupBy: []string{"customer"}, AggFields: fields}
	if err := ApplyTransforms([]TransformRule{rule}, ds); err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	out := map[string]common.DataRow{}
	for _, r := range ds.Rows {
		out[r["customer"].(string)] = r
	}
	return out
}

func TestAggregateMinMaxOfDatesAreDates(t *testing.T) {
	got := aggregateRows(t, []common.DataRow{
		{"customer": "a", "value": "2026-03-14"},
		{"customer": "a", "value": "2026-10-27"},
		{"customer": "a", "value": nil},
		{"customer": "a", "value": "2025-12-31"},
		{"customer": "b", "value": "2026-01-02"},
	}, AggField{Column: "value", Function: "max", Alias: "last"}, AggField{Column: "value", Function: "min", Alias: "first"})

	if got["a"]["last"] != "2026-10-27" || got["a"]["first"] != "2025-12-31" {
		t.Errorf("customer a: first=%v last=%v, want 2025-12-31 and 2026-10-27", got["a"]["first"], got["a"]["last"])
	}
	if got["b"]["last"] != "2026-01-02" || got["b"]["first"] != "2026-01-02" {
		t.Errorf("customer b: first=%v last=%v, want 2026-01-02 for both", got["b"]["first"], got["b"]["last"])
	}
}

// Instants compare by time, not by their text: 10:00+02:00 is earlier than
// 09:00Z although its text sorts later.
func TestAggregateMinMaxOfTimestampsCompareInstants(t *testing.T) {
	plus2 := time.FixedZone("+02", 2*60*60)
	early := time.Date(2026, 10, 27, 10, 0, 0, 0, plus2) // 08:00Z
	late := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	got := aggregateRows(t, []common.DataRow{
		{"customer": "a", "value": early},
		{"customer": "a", "value": late},
	}, AggField{Column: "value", Function: "max", Alias: "last"}, AggField{Column: "value", Function: "min", Alias: "first"})

	if last, _ := got["a"]["last"].(time.Time); !last.Equal(late) {
		t.Errorf("max = %v, want the later instant %v", got["a"]["last"], late)
	}
	if first, _ := got["a"]["first"].(time.Time); !first.Equal(early) {
		t.Errorf("min = %v, want the earlier instant %v", got["a"]["first"], early)
	}
}

// Numbers keep the numeric answer they always had, including a column of
// numbers with junk in it, and numeric text compares as numbers (10 > 9).
func TestAggregateMinMaxStayNumeric(t *testing.T) {
	got := aggregateRows(t, []common.DataRow{
		{"customer": "junk", "value": 12.5},
		{"customer": "junk", "value": "N/A"},
		{"customer": "junk", "value": 40.0},
		{"customer": "text", "value": "9"},
		{"customer": "text", "value": "10"},
	}, AggField{Column: "value", Function: "max", Alias: "hi"}, AggField{Column: "value", Function: "min", Alias: "lo"})

	if got["junk"]["hi"] != 40.0 || got["junk"]["lo"] != 12.5 {
		t.Errorf("junk: lo=%v hi=%v, want 12.5 and 40 (the junk skipped)", got["junk"]["lo"], got["junk"]["hi"])
	}
	if got["text"]["hi"] != 10.0 || got["text"]["lo"] != 9.0 {
		t.Errorf("numeric text: lo=%v hi=%v, want 9 and 10 as numbers", got["text"]["lo"], got["text"]["hi"])
	}
}

func TestAggregateMinMaxOfNoValueIsZero(t *testing.T) {
	got := aggregateRows(t, []common.DataRow{{"customer": "a", "value": nil}},
		AggField{Column: "value", Function: "max", Alias: "hi"})
	if got["a"]["hi"] != 0.0 {
		t.Errorf("max over nulls = %v (%T), want 0.0 as before", got["a"]["hi"], got["a"]["hi"])
	}
}

func TestMinMaxSchemaFollowsTheColumn(t *testing.T) {
	in := columnSchema{
		"day":   {Class: dbdialect.TypeDate, Nullable: false},
		"at":    {Class: dbdialect.TypeTimestampTZ, Nullable: false},
		"name":  {Class: dbdialect.TypeText, Nullable: false},
		"count": {Class: dbdialect.TypeInt, Bits: 64, Nullable: false},
	}
	schema := aggregateSchema(TransformRule{GroupBy: []string{"name"}, AggFields: []AggField{
		{Column: "day", Function: "max"}, {Column: "at", Function: "min"},
		{Column: "name", Function: "max", Alias: "top_name"}, {Column: "count", Function: "max"},
		{Column: "unknown", Function: "max"},
	}}, in)

	want := map[string]dbdialect.ColumnType{
		"max_day":   {Class: dbdialect.TypeDate, Nullable: true},
		"min_at":    {Class: dbdialect.TypeTimestampTZ, Nullable: true},
		"top_name":  {Class: dbdialect.TypeText, Nullable: true},
		"max_count": {Class: dbdialect.TypeFloat, Bits: 64, Nullable: true},
	}
	for col, w := range want {
		if schema[col] != w {
			t.Errorf("%s declared %v, want %v", col, schema[col], w)
		}
	}
	if _, known := schema.known("max_unknown"); known {
		t.Error("max over a column of unknown type must not be declared: it may be a number or a value")
	}
}

// The case that showed it: a CSV of orders, the latest order date per
// customer, written to a file. Run in memory and streamed.
func TestPipelineMaxOfADateColumn(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		name := "in-memory"
		if streamed {
			name = "streamed"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			in := filepath.Join(dir, "orders.csv")
			out := filepath.Join(dir, "ltv.csv")
			csvText := "customer,ordered_on\nC1,2026-03-14\nC1,2026-10-27\nC2,2026-01-02\nC1,2025-12-31\nC2,2026-02-28\n"
			if err := os.WriteFile(in, []byte(csvText), 0o600); err != nil {
				t.Fatal(err)
			}
			eng, s := newResumeTestEngine(t)
			if streamed {
				eng.SpillThresholdBytes = 1
				eng.StreamThresholdBytes = 1
			} else {
				// Nothing is stored, so the transform gets its input in
				// memory and aggregates with computeAgg.
				eng.SpillThresholdBytes = -1
				eng.StreamThresholdBytes = 1 << 40
			}
			pipe := &models.Pipeline{
				ID: "p-max-date", Name: "p-max-date", Enabled: true,
				Nodes: []models.Node{
					{ID: "src", Type: models.NodeTypeSourceFile, Config: map[string]interface{}{"path": in, "format": "csv"}},
					{ID: "agg", Type: models.NodeTypeTransform, Config: map[string]interface{}{"rules": []interface{}{
						map[string]interface{}{"type": "aggregate", "group_by": []interface{}{"customer"}, "agg_fields": []interface{}{
							map[string]interface{}{"column": "ordered_on", "function": "max", "alias": "last_order_on"},
						}},
					}}},
					{ID: "dst", Type: models.NodeTypeSinkFile, Config: map[string]interface{}{"path": out, "format": "csv"}},
				},
				Edges:     []models.Edge{{From: "src", To: "agg"}, {From: "agg", To: "dst"}},
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := s.CreatePipeline(pipe); err != nil {
				t.Fatal(err)
			}
			run, err := eng.RunPipeline(pipe.ID)
			if err != nil {
				t.Fatal(err)
			}
			if finished := waitForRun(t, s, run.ID); finished.Status != models.RunStatusSuccess {
				t.Fatalf("run status = %s (error: %s)", finished.Status, finished.Error)
			}

			f, err := os.Open(out)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			records, err := csv.NewReader(f).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			col := -1
			for i, h := range records[0] {
				if h == "last_order_on" {
					col = i
				}
			}
			if col < 0 {
				t.Fatalf("no last_order_on column in %v", records[0])
			}
			for _, r := range records[1:] {
				got[r[0]] = strings.TrimSpace(r[col])
			}
			if got["C1"] != "2026-10-27" || got["C2"] != "2026-02-28" {
				t.Errorf("last_order_on = %v, want C1 2026-10-27 and C2 2026-02-28", got)
			}
		})
	}
}
