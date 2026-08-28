package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

func streamTestOutputs(t *testing.T) *nodeOutputs {
	t.Helper()
	o := newStreamTestOutputs(t)
	o.streamThreshold = 1
	return o
}

// The whole point of the streamed source: what comes back out is exactly
// what went in, no matter how many batches it took.
func TestPutStreamRoundTripsEveryBatch(t *testing.T) {
	o := streamTestOutputs(t)
	const total = 4321
	ref, err := o.PutStream(func(emit func(*common.DataSet) error) error {
		for i := 0; i < total; i += 100 {
			rows := []common.DataRow{}
			for j := i; j < i+100 && j < total; j++ {
				rows = append(rows, common.DataRow{"n": float64(j), "s": fmt.Sprintf("v%d", j)})
			}
			if err := emit(&common.DataSet{Columns: []string{"n", "s"}, Rows: rows}); err != nil {
				return err
			}
		}
		return nil
	}, func() []string { return []string{"n", "s"} })
	if err != nil {
		t.Fatal(err)
	}
	if ref.RowCount != total {
		t.Fatalf("ref row count = %d, want %d", ref.RowCount, total)
	}
	if strings.Join(ref.Columns, ",") != "n,s" {
		t.Fatalf("ref columns = %v", ref.Columns)
	}

	batches, closer, err := o.OpenBatches(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	seen := 0
	for {
		b, err := batches.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range b.Rows {
			want := fmt.Sprintf("v%d", seen)
			if row["s"] != want {
				t.Fatalf("row %d = %v, want s=%q", seen, row, want)
			}
			seen++
		}
	}
	if seen != total {
		t.Fatalf("read back %d rows, want %d", seen, total)
	}
}

// An empty result must decode identically to an empty batch-written
// output, or a downstream consumer sees a corrupt blob rather than no
// rows.
func TestPutStreamEmptyResultUsesTheSentinel(t *testing.T) {
	o := streamTestOutputs(t)
	ref, err := o.PutStream(func(emit func(*common.DataSet) error) error { return nil },
		func() []string { return []string{"a"} })
	if err != nil {
		t.Fatal(err)
	}
	if ref.RowCount != 0 {
		t.Fatalf("row count = %d, want 0", ref.RowCount)
	}
	batches, closer, err := o.OpenBatches(ref)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	if _, err := batches.Next(); err != io.EOF {
		t.Fatalf("expected an immediate EOF, got %v", err)
	}
}

// A producer that fails partway must not leave a short blob behind that
// reads as a complete, smaller dataset.
func TestPutStreamProducerErrorIsNotSilentTruncation(t *testing.T) {
	o := streamTestOutputs(t)
	sentinel := errors.New("query died at row 50")
	_, err := o.PutStream(func(emit func(*common.DataSet) error) error {
		if err := emit(&common.DataSet{Columns: []string{"a"}, Rows: []common.DataRow{{"a": 1.0}}}); err != nil {
			return err
		}
		return sentinel
	}, func() []string { return []string{"a"} })
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected the producer error, got %v", err)
	}
}

func TestPutStreamRequiresSpilling(t *testing.T) {
	o := newNodeOutputs(nil, "", 0) // no blob store
	_, err := o.PutStream(func(emit func(*common.DataSet) error) error { return nil }, nil)
	if err == nil {
		t.Fatal("expected an error when spilling is disabled")
	}
}

// sinkStreamConfig has to build the same config runSinkDB builds, or the
// streamed and batch paths write differently for the same node.
func TestSinkStreamConfigMatchesRunSinkDB(t *testing.T) {
	node := models.Node{
		ID:   "sink",
		Type: models.NodeTypeSinkDB,
		Config: map[string]interface{}{
			"uri":          "postgres://u:p@h:5432/db",
			"table":        "staging.t",
			"mode":         "overwrite",
			"key_columns":  []interface{}{"id", "region"},
			"create_table": false,
		},
	}
	uri, got, ok := sinkStreamConfig(node)
	if !ok {
		t.Fatal("expected this sink to be streamable")
	}
	if uri != "postgres://u:p@h:5432/db" {
		t.Fatalf("uri = %q", uri)
	}
	// Built exactly as runSinkDB builds it.
	want := SQLGenConfig{
		Dialect:     dialectForURI(uri),
		Table:       "staging.t",
		Mode:        "overwrite",
		KeyColumns:  configStringSlice(node.Config["key_columns"]),
		CreateTable: configBool(node.Config["create_table"]),
	}
	if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
		t.Fatalf("config drift between the streamed and batch paths:\n got %+v\nwant %+v", got, want)
	}
}

// Only sinks whose write path can actually consume batches may claim
// eligibility. Anything else would be decoded back into memory to build
// statements — the ceiling moved rather than lifted.
func TestSinkCanStreamScope(t *testing.T) {
	sink := func(cfg map[string]interface{}) models.Node {
		return models.Node{Type: models.NodeTypeSinkDB, Config: cfg}
	}
	pg := "postgres://u:p@h/db"

	my := "mysql://u:p@h/db"

	yes := []models.Node{
		sink(map[string]interface{}{"uri": pg, "table": "t", "mode": "append"}),
		sink(map[string]interface{}{"uri": pg, "table": "t", "mode": "overwrite"}),
		sink(map[string]interface{}{"uri": pg, "table": "t"}),
		// MySQL earned the bulk-write capability in ADR-024's sense --
		// the equivalence tests in mysql_bulk_test.go, with the server's
		// Com_load counter as the anti-vacuity check, are what let these
		// rows move out of the "no" list below.
		sink(map[string]interface{}{"uri": my, "table": "t", "mode": "append"}),
		sink(map[string]interface{}{"uri": my, "table": "t", "mode": "overwrite"}),
		sink(map[string]interface{}{"uri": my, "table": "t"}),
		// A Postgres upsert stages through the bulk writer (#377), so it
		// streams too -- which is what gives an upsert of a table larger
		// than worker memory a bounded resident set.
		sink(map[string]interface{}{"uri": pg, "table": "t", "mode": "upsert", "key_columns": []interface{}{"id"}}),
	}
	for _, n := range yes {
		if !sinkCanStream(n) {
			t.Errorf("expected streamable: %v", n.Config)
		}
	}

	no := []models.Node{
		// An upsert without key_columns has no conflict target to merge
		// on; it keeps the statement path, whose per-batch statements
		// carry the same refusal the server would give.
		sink(map[string]interface{}{"uri": pg, "table": "t", "mode": "upsert"}),
		sink(map[string]interface{}{"uri": pg, "table": "t", "create_table": true}),
		sink(map[string]interface{}{"uri": my, "table": "t", "mode": "upsert", "key_columns": []interface{}{"id"}}),
		sink(map[string]interface{}{"uri": my, "table": "t", "create_table": true}),
		// SQLite has no bulk protocol; it keeps the statement path.
		sink(map[string]interface{}{"uri": "sqlite:///tmp/x.db", "table": "t", "mode": "append"}),
		// No table: the sql_generate hand-off path, which has no config
		// to inspect and must keep the batch path.
		sink(map[string]interface{}{"uri": pg}),
		sink(map[string]interface{}{"table": "t"}),
	}
	for _, n := range no {
		if sinkCanStream(n) {
			t.Errorf("did not expect streamable: %v", n.Config)
		}
	}
}

// The node types that stream. Sources and sinks were the gap #304 closed;
// everything not listed keeps the batch path it always had.
func TestStreamEligibleCoversSourceAndSink(t *testing.T) {
	r := &Runner{}
	o := streamTestOutputs(t)
	pg := "postgres://u:p@h/db"

	if !r.streamEligible(models.Node{Type: models.NodeTypeSourceDB}, o) {
		t.Error("source_db should be stream-eligible")
	}
	if !r.streamEligible(models.Node{Type: models.NodeTypeSinkDB,
		Config: map[string]interface{}{"uri": pg, "table": "t", "mode": "append"}}, o) {
		t.Error("a COPY-capable sink_db should be stream-eligible")
	}
	if r.streamEligible(models.Node{Type: models.NodeTypeSinkDB,
		Config: map[string]interface{}{"uri": pg, "table": "t", "mode": "upsert"}}, o) {
		t.Error("an upsert sink_db without key_columns must not be stream-eligible")
	}
	if !r.streamEligible(models.Node{Type: models.NodeTypeSinkDB,
		Config: map[string]interface{}{"uri": pg, "table": "t", "mode": "upsert",
			"key_columns": []interface{}{"id"}}}, o) {
		t.Error("a keyed Postgres upsert stages through the bulk writer (#377) and should stream")
	}
	// A file sink streams for the formats that can be written
	// incrementally, and keeps the batch path for the one that cannot.
	if !r.streamEligible(models.Node{Type: models.NodeTypeSinkFile,
		Config: map[string]interface{}{"path": "/tmp/o.csv"}}, o) {
		t.Error("a csv sink_file should be stream-eligible")
	}
	if !r.streamEligible(models.Node{Type: models.NodeTypeSinkFile,
		Config: map[string]interface{}{"path": "/tmp/o.json"}}, o) {
		t.Error("a json sink_file should be stream-eligible")
	}
	if r.streamEligible(models.Node{Type: models.NodeTypeSinkFile,
		Config: map[string]interface{}{"path": "/tmp/o.sql"}}, o) {
		t.Error("a sql sink_file must keep the batch path")
	}

	// An API sink posts rows in batches either way; streaming changes where
	// the rows come from, not what is sent.
	if !r.streamEligible(models.Node{Type: models.NodeTypeSinkAPI,
		Config: map[string]interface{}{"url": "https://example.com/ingest"}}, o) {
		t.Error("a sink_api should be stream-eligible")
	}

	// A file source streams the format whose shape is known before the data
	// is read, and keeps the batch path for the one whose columns are the
	// union of every object in the file.
	if !r.streamEligible(models.Node{Type: models.NodeTypeSourceFile,
		Config: map[string]interface{}{"path": "/tmp/in.csv"}}, o) {
		t.Error("a csv source_file should be stream-eligible")
	}
	if r.streamEligible(models.Node{Type: models.NodeTypeSourceFile,
		Config: map[string]interface{}{"path": "/tmp/in.json"}}, o) {
		t.Error("a json source_file must keep the batch path")
	}

	for _, typ := range []models.NodeType{
		models.NodeTypeSourceAPI, models.NodeTypeJoin,
	} {
		if r.streamEligible(models.Node{Type: typ}, o) {
			t.Errorf("%s must keep the batch path", typ)
		}
	}

	// Spilling off means there is nowhere to put a ref, so nothing streams.
	off := newNodeOutputs(nil, "", 0)
	if r.streamEligible(models.Node{Type: models.NodeTypeSourceDB}, off) {
		t.Error("no blob store means no streaming")
	}
	// A dry run previews in memory and never streams.
	dry := &Runner{dryRun: true}
	if dry.streamEligible(models.Node{Type: models.NodeTypeSourceDB}, o) {
		t.Error("dry runs must not stream")
	}
}

// A cancelled context stops the scan rather than reading the rest of the
// result set first.
func TestStreamQueryDatabaseHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := StreamQueryDatabase(ctx, "postgres://u:p@127.0.0.1:1/db", "SELECT 1", 0,
		func(*common.DataSet) error { return nil })
	if err == nil {
		t.Fatal("expected a cancelled context to fail the scan")
	}
}
