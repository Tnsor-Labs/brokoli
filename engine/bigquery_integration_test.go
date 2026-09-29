package engine

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

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
