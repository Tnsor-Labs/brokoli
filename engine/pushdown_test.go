package engine

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// A pipeline must produce the same rows whether the engine moved them or the
// database did. This runs the same pipeline both ways against a live Postgres
// and compares the destination table, because that is the artifact anyone
// actually cares about — and it checks which path each run took, since
// otherwise it passes just as happily when both fall back.
func TestPushdownWritesTheSameRowsAsTheEngine(t *testing.T) {
	uri := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if uri == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	db, err := sql.Open("pgx", uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	setup := []string{
		`DROP TABLE IF EXISTS brokoli_pd_src`,
		`CREATE TABLE brokoli_pd_src (id bigint, city text, amount numeric, note text)`,
		`INSERT INTO brokoli_pd_src
		 SELECT g, 'city' || (g % 7), (g % 1000)::numeric / 4,
		        CASE WHEN g % 11 = 0 THEN NULL ELSE 'note ' || g END
		 FROM generate_series(1, 5000) g`,
	}
	for _, s := range setup {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	t.Cleanup(func() { _, _ = db.Exec(`DROP TABLE IF EXISTS brokoli_pd_src`) })

	cases := []struct {
		name        string
		rules       []TransformRule
		wantPushed  bool
		destColumns string
	}{
		{
			name:        "straight copy, no transform",
			wantPushed:  true,
			destColumns: "id bigint, city text, amount numeric, note text",
		},
		{
			name:        "with a compilable filter",
			rules:       []TransformRule{{Type: "filter_rows", Condition: "city != city3"}},
			wantPushed:  true,
			destColumns: "id bigint, city text, amount numeric, note text",
		},
		{
			name: "with a drop and a rename",
			rules: []TransformRule{
				{Type: "drop_columns", Columns: []string{"note"}},
				{Type: "rename_columns", Mapping: map[string]string{"city": "town"}},
			},
			wantPushed:  true,
			destColumns: "id bigint, town text, amount numeric",
		},
		{
			name: "with an aggregate",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"city"},
				AggFields: []AggField{{Function: "count", Column: "id", Alias: "n"}}}},
			wantPushed:  true,
			destColumns: "city text, n bigint",
		},
		{
			// sum is refused by the compiler, so the segment must fall back
			// to the engine and still be correct.
			name: "an uncompilable transform falls back and still works",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"city"},
				AggFields: []AggField{{Function: "sum", Column: "amount", Alias: "total"}}}},
			wantPushed:  false,
			destColumns: "city text, total double precision",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Distinct destinations per case: the cases have different column
			// sets, and a reused name leaves the driver holding a cached plan
			// for the previous shape ("cached plan must not change result
			// type").
			engineDest := fmt.Sprintf("brokoli_pd_engine_%d", i)
			pushedDest := fmt.Sprintf("brokoli_pd_pushed_%d", i)
			run := func(pushdown bool, dest string) (rows []string, pushed bool) {
				t.Helper()
				if pushdown {
					t.Setenv("BROKOLI_DATA_PLANE", "")
				} else {
					t.Setenv("BROKOLI_DATA_PLANE", "interpreted")
				}
				if _, err := db.Exec(`DROP TABLE IF EXISTS ` + dest); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`CREATE TABLE ` + dest + ` (` + tc.destColumns + `)`); err != nil {
					t.Fatal(err)
				}

				eng, st := newExecCtxTestEngine(t)
				eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))

				nodes := []models.Node{
					{ID: "src", Type: models.NodeTypeSourceDB, Name: "Src", Config: map[string]interface{}{
						"uri": uri, "query": "SELECT id, city, amount, note FROM brokoli_pd_src"}},
				}
				edges := []models.Edge{}
				last := "src"
				if len(tc.rules) > 0 {
					raw := make([]interface{}, 0, len(tc.rules))
					for _, r := range tc.rules {
						raw = append(raw, transformRuleToMap(r))
					}
					nodes = append(nodes, models.Node{ID: "xf", Type: models.NodeTypeTransform, Name: "Xf",
						Config: map[string]interface{}{"rules": raw}})
					edges = append(edges, models.Edge{From: "src", To: "xf"})
					last = "xf"
				}
				nodes = append(nodes, models.Node{ID: "dst", Type: models.NodeTypeSinkDB, Name: "Dst",
					Config: map[string]interface{}{"uri": uri, "table": dest, "mode": "append"}})
				edges = append(edges, models.Edge{From: last, To: "dst"})

				p := &models.Pipeline{ID: "p-pd-" + dest, Name: dest, Enabled: true, Nodes: nodes, Edges: edges}
				if err := st.CreatePipeline(p); err != nil {
					t.Fatal(err)
				}
				r, err := eng.RunPipeline(p.ID)
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				if r.Status != models.RunStatusSuccess {
					t.Fatalf("run status %s: %s", r.Status, r.Error)
				}

				logs, err := st.GetLogs(r.ID)
				if err != nil {
					t.Fatal(err)
				}
				for _, l := range logs {
					if strings.Contains(l.Message, "the engine moved no rows") {
						pushed = true
					}
				}

				out, err := db.Query(`SELECT * FROM ` + dest + ` ORDER BY 1, 2`)
				if err != nil {
					t.Fatal(err)
				}
				defer out.Close()
				cols, _ := out.Columns()
				for out.Next() {
					vals := make([]interface{}, len(cols))
					ptrs := make([]interface{}, len(cols))
					for i := range vals {
						ptrs[i] = &vals[i]
					}
					if err := out.Scan(ptrs...); err != nil {
						t.Fatal(err)
					}
					parts := make([]string, len(cols))
					for i, v := range vals {
						parts[i] = fmt.Sprintf("%v", v)
					}
					rows = append(rows, strings.Join(parts, "|"))
				}
				return rows, pushed
			}

			engineRows, enginePushed := run(false, engineDest)
			pushRows, didPush := run(true, pushedDest)
			t.Cleanup(func() {
				_, _ = db.Exec(`DROP TABLE IF EXISTS ` + engineDest)
				_, _ = db.Exec(`DROP TABLE IF EXISTS ` + pushedDest)
			})

			if enginePushed {
				t.Fatal("the control run pushed down; BROKOLI_DATA_PLANE=interpreted must force the engine path")
			}
			if didPush != tc.wantPushed {
				if tc.wantPushed {
					t.Fatalf("expected this shape to push down and it did not, so the comparison proves nothing")
				}
				t.Fatalf("this shape is not proven equivalent and must not push down")
			}
			if len(engineRows) == 0 {
				t.Fatal("the control run wrote nothing; the comparison would be vacuous")
			}
			if len(pushRows) != len(engineRows) {
				t.Fatalf("row count differs: pushed %d, engine %d", len(pushRows), len(engineRows))
			}
			for i := range engineRows {
				if pushRows[i] != engineRows[i] {
					t.Fatalf("row %d differs:\n  pushed %s\n  engine %s", i, pushRows[i], engineRows[i])
				}
			}
		})
	}
}

func transformRuleToMap(r TransformRule) map[string]interface{} {
	m := map[string]interface{}{"type": r.Type}
	if r.Column != "" {
		m["column"] = r.Column
	}
	if r.Condition != "" {
		m["condition"] = r.Condition
	}
	if len(r.Columns) > 0 {
		m["columns"] = toIfaceSlice(r.Columns)
	}
	if len(r.Mapping) > 0 {
		mm := map[string]interface{}{}
		for k, v := range r.Mapping {
			mm[k] = v
		}
		m["mapping"] = mm
	}
	if len(r.GroupBy) > 0 {
		m["group_by"] = toIfaceSlice(r.GroupBy)
	}
	if len(r.AggFields) > 0 {
		af := make([]interface{}, 0, len(r.AggFields))
		for _, a := range r.AggFields {
			af = append(af, map[string]interface{}{
				"column": a.Column, "function": a.Function, "alias": a.Alias})
		}
		m["agg_fields"] = af
	}
	return m
}

func toIfaceSlice(in []string) []interface{} {
	out := make([]interface{}, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}
