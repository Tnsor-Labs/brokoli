package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// runMutationPipeline runs gen -> script -> sink with everything streaming, so
// the middle node always receives its input by reference.
func runMutationPipeline(t *testing.T, id, script string) (string, error) {
	t.Helper()
	eng, s := newExecCtxTestEngine(t)
	eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
	eng.SpillThresholdBytes = 1
	eng.StreamThresholdBytes = 1

	out := filepath.Join(t.TempDir(), "out.csv")
	pipeline := &models.Pipeline{
		ID: id, Name: id, Enabled: true,
		Nodes: []models.Node{
			{ID: "gen", Type: models.NodeTypeCode, Name: "Generate",
				Capabilities: []string{models.CapabilitySource, models.CapabilityDatasetOutput},
				Config: map[string]interface{}{"script": `
output_data = {"columns": ["name", "n"], "rows": [{"name": "alice", "n": 1}, {"name": "bob", "n": 2}, {"name": "carol", "n": 3}]}
`}},
			{ID: "mid", Type: models.NodeTypeCode, Name: "Mid",
				Config: map[string]interface{}{"script": script}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out",
				Config: map[string]interface{}{"path": out, "format": "csv"}},
		},
		Edges: []models.Edge{{From: "gen", To: "mid"}, {From: "mid", To: "sink"}},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(pipeline.ID)
	if err != nil {
		return "", err
	}
	if run.Status != models.RunStatusSuccess {
		return "", &runFailure{run.Error}
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read sink output: %v", err)
	}
	return string(b), nil
}

type runFailure struct{ msg string }

func (e *runFailure) Error() string { return e.msg }

// The compat suite covered four ways to READ a by-reference input and none to
// write to one. That gap shipped: in v0.10.68 the most ordinary Python idiom
// there is silently produced the original rows, because each iteration handed
// the script fresh dicts parsed from the file and the edits were dropped when
// the output re-read it. The run reported success.
func TestLazyRowsMutationIdioms(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		want   []string
	}{
		{
			name: "edit in the loop body and pass rows through",
			script: `
for r in rows:
    r["name"] = r["name"].upper()
output_data = {"columns": columns, "rows": rows}
`,
			want: []string{"ALICE", "BOB", "CAROL"},
		},
		{
			name: "edit only some rows",
			script: `
for r in rows:
    if r["name"] == "bob":
        r["name"] = "ROBERT"
output_data = {"columns": columns, "rows": rows}
`,
			want: []string{"alice", "ROBERT", "carol"},
		},
		{
			name: "add a column",
			script: `
for r in rows:
    r["greeting"] = "hi " + r["name"]
output_data = {"columns": columns + ["greeting"], "rows": rows}
`,
			want: []string{"hi alice", "hi bob", "hi carol"},
		},
		{
			name: "delete a key",
			script: `
for r in rows:
    del r["n"]
output_data = {"columns": ["name"], "rows": rows}
`,
			want: []string{"alice", "bob", "carol"},
		},
		{
			name: "update()",
			script: `
for r in rows:
    r.update({"name": r["name"].title()})
output_data = {"columns": columns, "rows": rows}
`,
			want: []string{"Alice", "Bob", "Carol"},
		},
		{
			name: "edits survive a second pass",
			script: `
for r in rows:
    r["name"] = r["name"].upper()
seen = [r["name"] for r in rows]
output_data = {"columns": ["name"], "rows": [{"name": v} for v in seen]}
`,
			want: []string{"ALICE", "BOB", "CAROL"},
		},
		{
			name: "edit after len() materialized the rows",
			script: `
total = len(rows)
for r in rows:
    r["name"] = r["name"].upper() + str(total)
output_data = {"columns": columns, "rows": rows}
`,
			want: []string{"ALICE3", "BOB3", "CAROL3"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := runMutationPipeline(t, "p-mut-"+strings.ReplaceAll(tc.name, " ", "-"), tc.script)
			if err != nil {
				t.Fatalf("run failed: %v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("output is missing %q — the edit did not survive.\ngot:\n%s", want, got)
				}
			}
		})
	}
}

// A read-only pass must not pay for the journal, and nested iteration must
// give each loop its own cursor rather than sharing one.
func TestLazyRowsReadOnlyAndNestedIteration(t *testing.T) {
	got, err := runMutationPipeline(t, "p-nested", `
pairs = 0
for a in rows:
    for b in rows:
        pairs += 1
output_data = {"columns": ["pairs"], "rows": [{"pairs": pairs}]}
`)
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if !strings.Contains(got, "9") {
		t.Errorf("nested iteration over 3 rows should visit 9 pairs; got:\n%s", got)
	}
}

// Editing a row the pass has already moved past cannot be placed correctly, so
// it is refused. Silently dropping it is what this whole change exists to
// stop, and silently guessing an order would be worse.
func TestLazyRowsRefusesMutationAfterThePass(t *testing.T) {
	_, err := runMutationPipeline(t, "p-late-mutation", `
held = []
for r in rows:
    held.append(r)
held[0]["name"] = "TOO LATE"
output_data = {"columns": columns, "rows": rows}
`)
	if err == nil {
		t.Fatal("editing a row after the pass moved on was accepted; it cannot be applied correctly and must not pass silently")
	}
	if !strings.Contains(err.Error(), "already moved on") {
		t.Errorf("the failure does not explain what happened: %v", err)
	}
}
