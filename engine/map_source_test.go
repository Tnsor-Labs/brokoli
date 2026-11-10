package engine

// Tests for mapping a source_db node (ADR-050 phase 1, #967): one query per
// item, ${item.column} bound and never spliced, refusals at save and at run.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// mappedSourceFixture is a SQLite database of orders and a CSV of the
// customers to read them for.
type mappedSourceFixture struct {
	dir, dbPath, customersCSV, out string
	st                             *store.SQLiteStore
	eng                            *Engine
}

func newMappedSourceFixture(t *testing.T, customers string) *mappedSourceFixture {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	f := &mappedSourceFixture{dir: dir, dbPath: filepath.Join(dir, "shop.db"), out: filepath.Join(dir, "out.csv")}
	conn, err := sql.Open("sqlite", f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		"CREATE TABLE orders (id INTEGER, customer_id INTEGER, region TEXT, total REAL)",
		"INSERT INTO orders VALUES (1, 1, 'eu', 10.5), (2, 1, 'eu', 3), (3, 2, 'us', 7), (4, 3, 'eu', 1), (5, 2, 'us', 2)",
	} {
		if _, err := conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()
	f.customersCSV = writeCSV(t, dir, "customers.csv", customers)
	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f.st = st
	f.eng = drainEngineOnCleanup(t, NewEngine(st))
	return f
}

func (f *mappedSourceFixture) pipeline(id, query string, block map[string]interface{}) *models.Pipeline {
	return &models.Pipeline{
		ID: id, Name: id, Enabled: true,
		Nodes: []models.Node{
			{ID: "customers", Type: models.NodeTypeSourceFile, Name: "Customers", Config: map[string]interface{}{"path": f.customersCSV, "format": "csv"}},
			{ID: "orders", Type: models.NodeTypeSourceDB, Name: "Orders", Config: map[string]interface{}{
				"uri": "sqlite://" + f.dbPath, "query": query, "map": block}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Out", Config: map[string]interface{}{"path": f.out, "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "customers", To: "orders"}, {From: "orders", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
}

func (f *mappedSourceFixture) run(t *testing.T, p *models.Pipeline) (*models.Run, []common.DataRow, error) {
	t.Helper()
	if err := f.st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	os.Remove(f.out)
	run, err := f.eng.RunPipeline(p.ID)
	var rows []common.DataRow
	if ds, readErr := readCSVFile(f.out); readErr == nil {
		rows = ds.Rows
	}
	return run, rows, err
}

func orderIDs(rows []common.DataRow) string {
	var ids []string
	for _, r := range rows {
		ids = append(ids, r["id"].(string))
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// Each customer's query runs on its own with its id bound, and the node's
// output is every customer's orders; each customer is its own instance,
// keyed by its id.
func TestMappedSourceDBRunsTheQueryOncePerItem(t *testing.T) {
	f := newMappedSourceFixture(t, "customer_id,region\n1,eu\n2,us\n")
	p := f.pipeline("mapped-source", "SELECT id, customer_id, total FROM orders WHERE customer_id = ${item.customer_id} AND region = '${item.region}'",
		map[string]interface{}{"over": "customers", "key": "customer_id"})
	run, rows, err := f.run(t, p)
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if got := orderIDs(rows); got != "1,2,3,5" {
		t.Fatalf("order ids = %s, want 1,2,3,5", got)
	}
	got := instanceStatuses(t, f.st, run.ID)
	if got["key:1"] != models.RunStatusSuccess || got["key:2"] != models.RunStatusSuccess || len(got) != 2 {
		t.Fatalf("instances = %v, want key:1 and key:2 succeeded", got)
	}
}

// An item value is data, so it must never become SQL. A hostile value in a
// quoted position binds as text and matches nothing; in a number position
// it is refused, because it is not a number.
func TestMappedSourceDBNeverSplicesItemValues(t *testing.T) {
	f := newMappedSourceFixture(t, "customer_id,region\n1,x' OR '1'='1\n")
	run, rows, err := f.run(t, f.pipeline("mapped-hostile-text", "SELECT id FROM orders WHERE region = '${item.region}'",
		map[string]interface{}{"over": "customers"}))
	if err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("run = %+v, err = %v", run, err)
	}
	if len(rows) != 0 {
		t.Fatalf("hostile text value returned %d rows; it was spliced into the SQL", len(rows))
	}

	g := newMappedSourceFixture(t, "customer_id,region\n1 OR 1=1,eu\n")
	_, rows, err = g.run(t, g.pipeline("mapped-hostile-number", "SELECT id FROM orders WHERE customer_id = ${item.customer_id}",
		map[string]interface{}{"over": "customers"}))
	if err == nil || !strings.Contains(err.Error(), "not a number") {
		t.Fatalf("hostile number value: err = %v, rows = %d; want it refused as not a number", err, len(rows))
	}
}

func TestMappedSourceDBRefusesAMissingOrNullItemColumn(t *testing.T) {
	f := newMappedSourceFixture(t, "customer_id,region\n1,eu\n")
	_, _, err := f.run(t, f.pipeline("mapped-missing", "SELECT id FROM orders WHERE customer_id = ${item.customer}",
		map[string]interface{}{"over": "customers"}))
	if err == nil || !strings.Contains(err.Error(), `the item has no column "customer"`) {
		t.Fatalf("missing column: err = %v", err)
	}

	item := common.DataRow{"customer_id": nil}
	r := &Runner{}
	if _, _, err := r.bindNodeSQL(withMapItem(context.Background(), item), "sqlite://x.db", "SELECT 1 WHERE x = ${item.customer_id}"); err == nil || !strings.Contains(err.Error(), "is null") {
		t.Fatalf("null value: err = %v", err)
	}
	if _, _, err := r.bindNodeSQL(context.Background(), "sqlite://x.db", "SELECT 1 WHERE x = ${item.customer_id}"); err == nil || !strings.Contains(err.Error(), "only available in a node with a 'map' block") {
		t.Fatalf("no item: err = %v", err)
	}
}

func TestMappedSourceDBValidation(t *testing.T) {
	f := newMappedSourceFixture(t, "customer_id\n1\n")
	mapped := f.pipeline("v", "SELECT id FROM orders WHERE customer_id = ${item.customer_id}", map[string]interface{}{"over": "customers"})
	if ve := ValidatePipeline(mapped); ve.HasErrors() {
		t.Fatalf("a valid mapped source was refused: %v", ve.Error())
	}

	unmapped := f.pipeline("v2", "SELECT id FROM orders WHERE customer_id = ${item.customer_id}", nil)
	delete(unmapped.Nodes[1].Config, "map")
	unmapped.Edges = unmapped.Edges[1:]
	if ve := ValidatePipeline(unmapped); !strings.Contains(ve.Error(), "only available in a node with a 'map' block") {
		t.Fatalf("${item} without map: err = %v", ve.Error())
	}

	// The map's over node is the only node that may feed a source.
	stray := f.pipeline("v3", "SELECT id FROM orders WHERE customer_id = ${item.customer_id}", map[string]interface{}{"over": "customers"})
	stray.Nodes = append(stray.Nodes, models.Node{ID: "other", Type: models.NodeTypeSourceFile, Name: "Other", Config: map[string]interface{}{"path": f.customersCSV, "format": "csv"}})
	stray.Edges = append(stray.Edges, models.Edge{From: "other", To: "orders"})
	if ve := ValidatePipeline(stray); !strings.Contains(ve.Error(), `node "orders" (type source_db) cannot receive incoming edges`) {
		t.Fatalf("an edge from outside map.over into a source: err = %v", ve.Error())
	}
}

// A dry run previews a mapped source with its first items, not all of
// them.
func TestMappedSourceDBDryRunMapsTheFirstItems(t *testing.T) {
	f := newMappedSourceFixture(t, "customer_id\n1\n2\n3\n1\n2\n")
	p := f.pipeline("mapped-dry", "SELECT id, customer_id FROM orders WHERE customer_id = ${item.customer_id}", map[string]interface{}{"over": "customers"})
	p.Nodes = p.Nodes[:2]
	p.Edges = p.Edges[:1]
	results, err := f.eng.DryRun(p, 0)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	node := results["orders"]
	if node == nil || node.Status != "success" {
		t.Fatalf("dry run node result = %+v", node)
	}
	// Items 1,2,3,1,2: the first three read 2+2+1 orders; all five
	// would read 9.
	if len(node.Rows) != 5 {
		t.Fatalf("dry run read %d orders, want 5 (the first %d items)", len(node.Rows), dryRunMappedItems)
	}
}
