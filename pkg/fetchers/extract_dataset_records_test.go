package fetchers

import "testing"

// TestExtractDatasetRecords_EmptyArrayIsZeroRecordsNotError covers the fix
// for brokoli#44: a bare empty JSON array is a normal "no more pages"
// signal for offset/numbered pagination without a records/end_flag config,
// and must not make fetchPaginated's stop condition unreachable.
func TestExtractDatasetRecords_EmptyArrayIsZeroRecordsNotError(t *testing.T) {
	records, err := extractDatasetRecords([]byte(`[]`), map[string]interface{}{})
	if err != nil {
		t.Fatalf("extractDatasetRecords([]): %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("got %d records, want 0", len(records))
	}
}

func TestExtractDatasetRecords_EmptyArrayWithSurroundingWhitespaceIsZeroRecords(t *testing.T) {
	records, err := extractDatasetRecords([]byte("  []\n"), map[string]interface{}{})
	if err != nil {
		t.Fatalf("extractDatasetRecords: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("got %d records, want 0", len(records))
	}
}

func TestExtractDatasetRecords_NonEmptyArrayUnchanged(t *testing.T) {
	records, err := extractDatasetRecords([]byte(`[{"id":1},{"id":2}]`), map[string]interface{}{})
	if err != nil {
		t.Fatalf("extractDatasetRecords: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
}

// TestExtractDatasetRecords_RecordsPathEmptyArrayUnaffected confirms the
// records-path extraction path (ExtractRecordsAtPath) already handled an
// empty array correctly before this fix and still does — the fix only adds
// a fast path ahead of the default ParseJSONData fallback.
func TestExtractDatasetRecords_RecordsPathEmptyArrayUnaffected(t *testing.T) {
	records, err := extractDatasetRecords([]byte(`{"items":[]}`), map[string]interface{}{"records": "items"})
	if err != nil {
		t.Fatalf("extractDatasetRecords: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("got %d records, want 0", len(records))
	}
}
