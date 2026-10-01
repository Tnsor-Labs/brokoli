package engine

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/identity"
	"github.com/Tnsor-Labs/brokoli/pkg/netguard"
)

func TestBigQueryEmulatorPhase1(t *testing.T) {
	if os.Getenv("BROKOLI_BIGQUERY_ENDPOINT") == "" {
		t.Skip("set BROKOLI_BIGQUERY_ENDPOINT to run the BigQuery emulator test")
	}
	// The emulator is on loopback, which the default outbound policy
	// refuses. The test opts in itself rather than the CI job loosening
	// the policy for everything it runs.
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	uri := "bigquery://test/brokoli_test"
	table := fmt.Sprintf("phase1_rows_%d", time.Now().UnixNano())
	config := map[string]interface{}{}
	if err := CheckBigQueryConnection(ctx, uri, config, "", nil, identity.TokenRequest{}); err != nil {
		t.Fatal(err)
	}

	data := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows:    []common.DataRow{{"id": int64(1), "name": "one"}},
	}
	auth := googleAuth{}
	if _, err := QueryBigQuery(ctx, uri, fmt.Sprintf("CREATE TABLE %s (id INT64, name STRING)", table), config, auth); err != nil {
		t.Fatal(err)
	}
	if err := LoadBigQuery(ctx, uri, table, ModeAppend, data, config, auth); err != nil {
		t.Fatal(err)
	}
	got, err := QueryBigQuery(ctx, uri, fmt.Sprintf("SELECT id, name FROM %s", table), config, auth)
	if err != nil {
		t.Fatal(err)
	}
	var name interface{}
	if len(got.Rows) > 0 {
		name = got.Rows[0]["name"]
	}
	if name == nil && len(got.Rows) > 0 {
		// The emulator does not return the dry-run schema for this optimized
		// query path, so the client uses positional fallback names locally.
		name = got.Rows[0]["column_2"]
	}
	if len(got.Rows) != 1 || name != "one" {
		t.Fatalf("rows = %#v, want one row named one", got.Rows)
	}
	if _, _, err := DryRunBigQuery(ctx, uri, fmt.Sprintf("SELECT id FROM %s", table), config, auth); err != nil {
		t.Fatal(err)
	}
	if err := LoadBigQuery(ctx, uri, table, ModeUpsert, data, config, auth); err == nil {
		t.Fatal("upsert must be refused by name")
	}
}

// Run parameters reach BigQuery as positional query parameters, not SQL
// text (#774): a hostile value is compared, and matches nothing.
func TestBigQueryBindsRunParameters(t *testing.T) {
	if os.Getenv("BROKOLI_BIGQUERY_ENDPOINT") == "" {
		t.Skip("set BROKOLI_BIGQUERY_ENDPOINT to run the BigQuery emulator test")
	}
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	uri := "bigquery://test/brokoli_test"
	table := fmt.Sprintf("params_rows_%d", time.Now().UnixNano())
	config := map[string]interface{}{}
	auth := googleAuth{}
	if _, err := QueryBigQuery(ctx, uri, fmt.Sprintf("CREATE TABLE %s (id INT64, name STRING)", table), config, auth); err != nil {
		t.Fatal(err)
	}
	data := &common.DataSet{Columns: []string{"id", "name"}, Rows: []common.DataRow{
		{"id": int64(1), "name": "one"}, {"id": int64(2), "name": "two"}}}
	if err := LoadBigQuery(ctx, uri, table, ModeAppend, data, config, auth); err != nil {
		t.Fatal(err)
	}
	count := func(query string, params map[string]string) int {
		t.Helper()
		bound, args, err := bindSQLParams(query, sqlParamDialectFor(uri), paramResolver(params))
		if err != nil {
			t.Fatal(err)
		}
		got, err := QueryBigQuery(ctx, uri, bound, config, auth, args...)
		if err != nil {
			t.Fatalf("query %q %v: %v", bound, args, err)
		}
		return len(got.Rows)
	}
	byName := fmt.Sprintf("SELECT id FROM %s WHERE name = '${param.name}'", table)
	if n := count(byName, map[string]string{"name": "x' OR '1'='1"}); n != 0 {
		t.Fatalf("hostile value matched %d rows", n)
	}
	if n := count(byName, map[string]string{"name": "two"}); n != 1 {
		t.Fatalf("honest value matched %d rows, want 1", n)
	}
	if n := count(fmt.Sprintf("SELECT id FROM %s WHERE id <= ${param.n}", table), map[string]string{"n": "1"}); n != 1 {
		t.Fatalf("bare number matched %d rows, want 1", n)
	}
}

// The same through a run: the runner hands BigQuery the bound values.
func TestBigQueryRunBindsRunParameters(t *testing.T) {
	if os.Getenv("BROKOLI_BIGQUERY_ENDPOINT") == "" {
		t.Skip("set BROKOLI_BIGQUERY_ENDPOINT to run the BigQuery emulator test")
	}
	t.Cleanup(netguard.SetOutboundForTesting(netguard.Policy{AllowLoopback: true}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	uri := "bigquery://test/brokoli_test"
	table := fmt.Sprintf("params_run_%d", time.Now().UnixNano())
	auth := googleAuth{}
	if _, err := QueryBigQuery(ctx, uri, fmt.Sprintf("CREATE TABLE %s (id INT64, name STRING)", table), map[string]interface{}{}, auth); err != nil {
		t.Fatal(err)
	}
	data := &common.DataSet{Columns: []string{"id", "name"}, Rows: []common.DataRow{
		{"id": int64(1), "name": "one"}, {"id": int64(2), "name": "two"}}}
	if err := LoadBigQuery(ctx, uri, table, ModeAppend, data, map[string]interface{}{}, auth); err != nil {
		t.Fatal(err)
	}
	r := newUnitTestRunner(t)
	r.varCtx = NewVariableContext(map[string]string{"name": "x' OR '1'='1"}, r.run.ID, time.Now())
	r.ctx = ctx
	node := models.Node{ID: "bq", Type: models.NodeTypeSourceDB, Config: map[string]interface{}{
		"uri": uri, "query": fmt.Sprintf("SELECT id FROM %s WHERE name = '${param.name}'", table)}}
	result, err := r.runSourceDB(node, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.output == nil || len(result.output.Rows) != 0 {
		t.Fatalf("hostile value: %#v; the parameter was not bound", result.output)
	}
	r.varCtx = NewVariableContext(map[string]string{"name": "two"}, r.run.ID, time.Now())
	result, err = r.runSourceDB(node, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.output.Rows) != 1 {
		t.Fatalf("honest value matched %d rows, want 1", len(result.output.Rows))
	}
}
