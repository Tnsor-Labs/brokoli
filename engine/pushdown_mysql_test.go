package engine

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
)

// The MySQL counterpart of TestPushdownWritesTheSameRowsAsTheEngine: every
// segment shape runs both ways -- forced through the engine, then with
// pushdown enabled -- and the destination tables must match row for row,
// with the anti-vacuity assertions that check which path each run took.
func TestPushdownWritesTheSameRowsAsTheEngineMySQL(t *testing.T) {
	uri := mysqlTestURI(t)
	db := openMySQLForTest(t, uri)

	setup := []string{
		`DROP TABLE IF EXISTS brokoli_pd_src`,
		`CREATE TABLE brokoli_pd_src (id BIGINT, city TEXT, amount DECIMAL(13,2), note TEXT)`,
	}
	for _, s := range setup {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	// The same shape of seed as the Postgres run builds with
	// generate_series, produced in Go because MySQL has no equivalent.
	var sb strings.Builder
	sb.WriteString("INSERT INTO brokoli_pd_src VALUES ")
	for g := 1; g <= 5000; g++ {
		if g > 1 {
			sb.WriteString(",")
		}
		note := fmt.Sprintf("'note %d'", g)
		if g%11 == 0 {
			note = "NULL"
		}
		fmt.Fprintf(&sb, "(%d, 'city%d', %.2f, %s)", g, g%7, float64(g%1000)/4, note)
	}
	if _, err := db.Exec(sb.String()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { db.Exec(`DROP TABLE IF EXISTS brokoli_pd_src`) })

	cases := []struct {
		name        string
		rules       []TransformRule
		wantPushed  bool
		destColumns string
	}{
		{
			name:        "straight copy, no transform",
			wantPushed:  true,
			destColumns: "id BIGINT, city TEXT, amount DECIMAL(13,2), note TEXT",
		},
		{
			name:        "with a compilable filter",
			rules:       []TransformRule{{Type: "filter_rows", Condition: "city != city3"}},
			wantPushed:  true,
			destColumns: "id BIGINT, city TEXT, amount DECIMAL(13,2), note TEXT",
		},
		{
			name: "with a drop and a rename",
			rules: []TransformRule{
				{Type: "drop_columns", Columns: []string{"note"}},
				{Type: "rename_columns", Mapping: map[string]string{"city": "town"}},
			},
			wantPushed:  true,
			destColumns: "id BIGINT, town TEXT, amount DECIMAL(13,2)",
		},
		{
			name: "with an aggregate",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"city"},
				AggFields: []AggField{{Function: "count", Column: "id", Alias: "n"}}}},
			wantPushed:  true,
			destColumns: "city TEXT, n BIGINT",
		},
		{
			// sum is refused by the compiler, so the segment must fall back
			// to the engine and still be correct.
			name: "an uncompilable transform falls back and still works",
			rules: []TransformRule{{Type: "aggregate", GroupBy: []string{"city"},
				AggFields: []AggField{{Function: "sum", Column: "amount", Alias: "total"}}}},
			wantPushed:  false,
			destColumns: "city TEXT, total DOUBLE",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engineDest := fmt.Sprintf("brokoli_pd_my_engine_%d", i)
			pushedDest := fmt.Sprintf("brokoli_pd_my_pushed_%d", i)
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
						if b, ok := v.([]byte); ok {
							v = string(b)
						}
						parts[i] = fmt.Sprintf("%v", v)
					}
					rows = append(rows, strings.Join(parts, "|"))
				}
				return rows, pushed
			}

			engineRows, enginePushed := run(false, engineDest)
			pushRows, didPush := run(true, pushedDest)
			t.Cleanup(func() {
				db.Exec(`DROP TABLE IF EXISTS ` + engineDest)
				db.Exec(`DROP TABLE IF EXISTS ` + pushedDest)
			})

			if enginePushed {
				t.Fatal("the control run pushed down; BROKOLI_DATA_PLANE=interpreted must force the engine path")
			}
			if didPush != tc.wantPushed {
				if tc.wantPushed {
					t.Fatalf("expected this shape to push down on MySQL and it did not, so the comparison proves nothing")
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
