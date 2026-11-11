package engine

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func oneColumn(vals ...interface{}) *common.DataSet {
	ds := &common.DataSet{Columns: []string{"c"}}
	for _, v := range vals {
		ds.Rows = append(ds.Rows, common.DataRow{"c": v})
	}
	return ds
}

// Every way a dataset misses Arrow said nothing about why (#529). The
// reason names the column and what disqualified it.
func TestArrowSchemaForSaysWhyItDeclined(t *testing.T) {
	for _, tc := range []struct {
		name string
		ds   *common.DataSet
		want []string
	}{
		{"nested object", oneColumn(map[string]interface{}{"a": 1.0}), []string{`column "c"`, "nested object"}},
		{"list", oneColumn([]interface{}{1.0}), []string{`column "c"`, "list"}},
		{"mixed types", oneColumn("x", true), []string{`column "c"`, "utf8", "bool"}},
		{"all null", oneColumn(nil, nil), []string{`column "c"`, "null in every row"}},
		{"lossy widening", oneColumn(1.5, int64(1)<<60+1), []string{`column "c"`, "2^53"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, reason := arrowSchemaFor(tc.ds)
			if schema != nil {
				t.Fatalf("schema %v, want a refusal", schema)
			}
			for _, w := range tc.want {
				if !strings.Contains(reason, w) {
					t.Errorf("reason %q, want it to mention %q", reason, w)
				}
			}
			if _, ok := arrowEncodableSchema(tc.ds); ok {
				t.Error("arrowEncodableSchema accepted what arrowSchemaFor refused")
			}
		})
	}
	// Accepted, and nothing to report.
	if schema, reason := arrowSchemaFor(oneColumn(int64(1), 2.5, nil)); schema == nil || reason != "" {
		t.Errorf("a nullable numeric column: schema %v, reason %q", schema, reason)
	}
	// Nothing to infer from is not a refusal worth reporting.
	if schema, reason := arrowSchemaFor(&common.DataSet{Columns: []string{"c"}}); schema != nil || reason != "" {
		t.Errorf("an empty dataset: schema %v, reason %q, want neither", schema, reason)
	}
}

// A spilled dataset that misses Arrow is still written exactly, as NDJSON,
// and the log says why, once per cause.
func TestSpillAnnouncesWhyADatasetMissedArrow(t *testing.T) {
	logged := captureFallbackLog(t)
	outputs := newNodeOutputs(newSQLArtifactTestStore(t).Blobs(), "run-fallback", 1)
	nested := oneColumn(map[string]interface{}{"a": 1.0})
	for i := 0; i < 3; i++ {
		ref, err := outputs.spill(nested, "")
		if err != nil {
			t.Fatal(err)
		}
		if ref.Format != artifact.FormatNDJSON {
			t.Fatalf("format %q, want ndjson", ref.Format)
		}
	}
	lines := logged()
	if len(lines) != 1 || !strings.Contains(lines[0], `column "c" holds a nested object`) || !strings.Contains(lines[0], "NDJSON instead of Arrow") {
		t.Fatalf("logged %q, want one line naming the column and the cause", lines)
	}
	// A different cause is announced too.
	if _, err := outputs.spill(oneColumn(nil, nil), ""); err != nil {
		t.Fatal(err)
	}
	// Asking for NDJSON is a choice, not a fallback, and an Arrow dataset
	// has nothing to report.
	if _, err := outputs.spill(oneColumn(map[string]interface{}{"b": 2.0}), artifact.FormatNDJSON); err != nil {
		t.Fatal(err)
	}
	if _, err := outputs.spill(oneColumn(int64(1)), ""); err != nil {
		t.Fatal(err)
	}
	// Nor does an empty dataset: nothing was refused.
	if _, err := outputs.spill(&common.DataSet{Columns: []string{"c"}}, ""); err != nil {
		t.Fatal(err)
	}
	if lines := logged(); len(lines) != 2 || !strings.Contains(lines[1], "null in every row") {
		t.Errorf("logged %q, want a second line for the all-null column and nothing else", lines)
	}
}

// The streamed path decides from the first batch: in auto mode it falls
// back and says why; asked for Arrow, its error says why.
func TestStreamedDatasetsSayWhyTheyMissedArrow(t *testing.T) {
	logged := captureFallbackLog(t)
	format, _, err := newDatasetStreamWriter(&bytes.Buffer{}, streamCodecAuto).Decide(oneColumn("x", 1.0))
	if err != nil || format != artifact.FormatNDJSON {
		t.Fatalf("auto: %q, %v, want ndjson", format, err)
	}
	if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], `column "c" holds both`) {
		t.Errorf("auto logged %q, want the mixed column named", lines)
	}
	_, _, err = newDatasetStreamWriter(&bytes.Buffer{}, streamCodecArrow).Decide(oneColumn("x", 1.0))
	if err == nil || !strings.Contains(err.Error(), `column "c" holds both`) {
		t.Errorf("arrow: %v, want the refusal to name the column", err)
	}
	if _, _, err := newDatasetStreamWriter(&bytes.Buffer{}, streamCodecNDJSON).Decide(oneColumn("x", 1.0)); err != nil {
		t.Fatal(err)
	}
	if lines := logged(); len(lines) != 1 {
		t.Errorf("logged %q; asking for ndjson or for arrow is not a silent fallback", lines)
	}
}
