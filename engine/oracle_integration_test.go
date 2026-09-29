package engine

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/dbdialect"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Tests against a real Oracle server. They skip unless
// BROKOLI_TEST_ORACLE_URL is set, which docker-compose.test.yml's oracle
// service and the "Test (Oracle / oracle-free)" CI job provide:
//
//	docker compose -f docker-compose.test.yml up -d oracle
//	BROKOLI_TEST_ORACLE_URL='oracle://brokoli:p%40ss%2Fw%3Ard%231@127.0.0.1:55544/FREEPDB1' \
//	  go test ./engine -run '^TestOracle' -v
//
// The password carries @, /, : and # on purpose: the connection record's
// URI has to survive them on its way to the driver.
func oracleTestURL(t *testing.T) string {
	t.Helper()
	uri := os.Getenv("BROKOLI_TEST_ORACLE_URL")
	if uri == "" {
		t.Skip("set BROKOLI_TEST_ORACLE_URL to run the Oracle integration tests")
	}
	return uri
}

// One row of every type a NUMBER decoding decision covers.
const oracleTypesQuery = `SELECT
	1 AS small_int,
	-7 AS negative,
	9223372036854775807 AS max_int64,
	12345678901234567890123 AS past_int64,
	CAST(1.5 AS NUMBER(10,2)) AS fixed_point,
	0.1 AS decimal_fraction,
	TO_BINARY_DOUBLE(2.5) AS binary_double,
	'text' AS label,
	DATE '2024-01-02' AS day,
	CAST(NULL AS NUMBER) AS missing
FROM dual`

func assertOracleTypes(t *testing.T, row common.DataRow) {
	t.Helper()
	want := map[string]interface{}{
		"SMALL_INT":        int64(1),
		"NEGATIVE":         int64(-7),
		"MAX_INT64":        int64(9223372036854775807),
		"PAST_INT64":       "12345678901234567890123",
		"FIXED_POINT":      1.5,
		"DECIMAL_FRACTION": 0.1,
		"BINARY_DOUBLE":    2.5,
		"LABEL":            "text",
		"MISSING":          nil,
	}
	for column, value := range want {
		if got := row[column]; got != value {
			t.Errorf("%s = %#v (%T), want %#v (%T)", column, got, got, value, value)
		}
	}
	if day, ok := row["DAY"].(time.Time); !ok || day.Format("2006-01-02") != "2024-01-02" {
		t.Errorf("DAY = %#v, want the date 2024-01-02", row["DAY"])
	}
}

// A NUMBER comes back as the number it is -- int64 for whole numbers, every
// digit kept -- not as the driver's text.
func TestOracleQueryDecodesNumbers(t *testing.T) {
	uri := oracleTestURL(t)
	ds, err := QueryDatabase(uri, oracleTypesQuery)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(ds.Rows))
	}
	assertOracleTypes(t, ds.Rows[0])
}

// Streaming reads work against Oracle, across several batches, and decode
// exactly as the materializing path does.
func TestOracleStreamingQuery(t *testing.T) {
	uri := oracleTestURL(t)
	var first common.DataRow
	_, total, err := StreamQueryDatabase(context.Background(), uri, oracleTypesQuery, 10, func(b *common.DataSet) error {
		first = common.DataRow{}
		for k, v := range b.Rows[0] {
			first[k] = v
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("total = %d, want 1", total)
	}
	assertOracleTypes(t, first)

	batches, sum := 0, int64(0)
	_, total, err = StreamQueryDatabase(context.Background(), uri,
		"SELECT LEVEL AS id FROM dual CONNECT BY LEVEL <= 2500", 1000, func(b *common.DataSet) error {
			batches++
			for _, row := range b.Rows {
				id, ok := row["ID"].(int64)
				if !ok {
					t.Fatalf("ID = %#v (%T), want int64", row["ID"], row["ID"])
				}
				sum += id
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if total != 2500 || batches != 3 || sum != 2500*2501/2 {
		t.Fatalf("total %d in %d batches summing to %d; want 2500 in 3 summing to %d", total, batches, sum, 2500*2501/2)
	}
}

// The column probe is Oracle SQL (no AS before an alias, no LIMIT) and
// describes a query without running it.
func TestOracleProbeDescribesColumns(t *testing.T) {
	uri := oracleTestURL(t)
	d, _ := dbdialect.For("oracle")
	cols, err := describeQueryColumns(context.Background(), uri, "SELECT 1 AS a, 'x' AS s FROM dual", d)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if _, ok := cols["A"]; !ok || len(cols) != 2 {
		t.Fatalf("probe columns = %v, want A and S", cols)
	}
}

// A saved connection -- host, port, service, login, and a password full of
// URL syntax -- reaches the driver intact, and source_db reads through it
// in a run. The output keeps a 64-bit value exactly.
func TestOracleConnectionReadsThroughRunner(t *testing.T) {
	u, err := url.Parse(oracleTestURL(t))
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	password, _ := u.User.Password()

	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.CreateConnection(&models.Connection{
		ConnID: "ora", Type: models.ConnTypeOracle,
		Host: u.Hostname(), Port: port, Schema: strings.TrimPrefix(u.Path, "/"),
		Login: u.User.Username(), Password: password,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.ConnResolver = NewConnectionResolver(st, nil)

	out := filepath.Join(dir, "out.json")
	p := &models.Pipeline{
		ID: "ora-read", Name: "oracle read", Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceDB, Name: "Read", Config: map[string]interface{}{
				"conn_id": "ora", "query": "SELECT 9223372036854775807 AS id, 'lisbon' AS city FROM dual"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "Write", Config: map[string]interface{}{
				"path": out, "format": "json"}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run failed: %s", run.Error)
	}
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "9223372036854775807") || !strings.Contains(string(body), "lisbon") {
		t.Fatalf("output lost the exact value or the row: %s", body)
	}
}
