package engine

import (
	"encoding/json"
	"os"
	"testing"
)

// TestGCSLive is opt-in because CI has no shared customer bucket.
// BROKOLI_TEST_GCS_EXTRA must contain the complete secret-bearing connection
// extra JSON for a disposable bucket.
func TestGCSLive(t *testing.T) {
	extra := os.Getenv("BROKOLI_TEST_GCS_EXTRA")
	if extra == "" {
		t.Skip("set BROKOLI_TEST_GCS_EXTRA to run the GCS integration test")
	}
	var settings map[string]interface{}
	if err := json.Unmarshal([]byte(extra), &settings); err != nil {
		t.Fatalf("BROKOLI_TEST_GCS_EXTRA: %v", err)
	}
	if err := TestGCSConnection(t.Context(), settings); err != nil {
		t.Fatalf("GCS connection test: %v", err)
	}
}
