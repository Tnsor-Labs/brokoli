package engine

import (
	"testing"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// source_api does not stream, and the reason is structural rather than
// unfinished work — the same reason a JSON source_file does not.
//
// A REST source builds its result with common.ConvertToDataSet over every
// record from every page, and that derives the column set from the union of
// keys across all of them. The columns are therefore not known until the last
// page has been fetched. A streaming version would have to either take the
// first page's columns, which changes the result whenever records differ, or
// read everything to find them, which is the thing streaming exists to avoid.
//
// This test records that constraint against the function that imposes it, so
// that if the column model ever changes — a declared schema, per-record
// columns — the blocker is re-examined rather than assumed to still hold.
func TestSourceAPIColumnsAreNotKnownUntilTheLastRecord(t *testing.T) {
	// Two records a paginated source could plausibly return on different
	// pages: the second carries a field the first does not.
	page1 := []map[string]interface{}{{"id": 1, "name": "a"}}
	page2 := []map[string]interface{}{{"id": 2, "name": "b", "nickname": "bee"}}

	firstPageOnly := common.ConvertToDataSet(page1)
	both := common.ConvertToDataSet(append(append([]map[string]interface{}{}, page1...), page2...))

	if len(firstPageOnly.Columns) == len(both.Columns) {
		t.Fatalf("this fixture no longer demonstrates the problem: first page gave %v, both gave %v",
			firstPageOnly.Columns, both.Columns)
	}

	// The point: a streamer that emitted page 1 before seeing page 2 would
	// have committed to a narrower column set than the run actually has.
	if len(both.Columns) <= len(firstPageOnly.Columns) {
		t.Errorf("expected the later page to widen the column set, got %v then %v",
			firstPageOnly.Columns, both.Columns)
	}
}
