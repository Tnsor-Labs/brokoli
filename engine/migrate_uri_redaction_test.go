package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

func TestRedactURI(t *testing.T) {
	cases := []struct {
		name string
		uri  string
		want string
	}{
		{
			name: "postgres URI with password is redacted",
			uri:  "postgres://admin:S3cretPass1@db.internal:5432/orders",
			want: "postgres://admin:xxxxx@db.internal:5432/orders",
		},
		{
			name: "URI with no userinfo is unchanged",
			uri:  "postgres://db.internal:5432/orders",
			want: "postgres://db.internal:5432/orders",
		},
		{
			name: "sqlite bare file path is unchanged (no userinfo to find)",
			uri:  "/data/orders.db",
			want: "/data/orders.db",
		},
		{
			name: "empty string is unchanged",
			uri:  "",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactURI(tc.uri)
			if got != tc.want {
				t.Errorf("redactURI(%q) = %q, want %q", tc.uri, got, tc.want)
			}
			if strings.Contains(got, "S3cretPass1") {
				t.Errorf("redactURI(%q) = %q still contains the plaintext password", tc.uri, got)
			}
		})
	}
}

// TestRunMigrate_LogDoesNotLeakSourcePassword pins the actual fix end to
// end: the migrate node's own "Migration: ..." log line, as it's really
// persisted and exposed through the run-logs API/export, must not
// contain the plaintext source connection password -- regardless of
// whether the migration itself succeeds. The log line is written before
// the connection is attempted, so an unreachable host (used here to
// avoid needing a real Postgres server) still exercises the exact code
// path that leaked the password before this fix.
func TestRunMigrate_LogDoesNotLeakSourcePassword(t *testing.T) {
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const password = "S3cretPass1"
	sourceURI := "postgres://admin:" + password + "@unreachable-host.invalid:5432/orders"

	pipeline := &models.Pipeline{
		ID:   "mig-redact",
		Name: "mig redact",
		Nodes: []models.Node{{
			ID: "mig", Type: models.NodeTypeMigrate, Name: "Mig",
			Config: map[string]interface{}{
				"source_uri":   sourceURI,
				"source_query": "SELECT 1",
				"dest_uri":     filepath.Join(dir, "dst.db"),
				"dest_table":   "dst",
			},
		}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatal(err)
	}

	eng := NewEngine(s)
	defer eng.Close(context.Background())
	// The run is expected to fail (unreachable host) -- that's fine and
	// expected; RunPipeline still returns the run alongside the error.
	// What matters here is what got logged before the failure.
	run, _ := eng.RunPipeline(pipeline.ID)
	if run == nil {
		t.Fatal("RunPipeline returned a nil run")
	}

	logs, err := s.GetLogs(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) == 0 {
		t.Fatal("expected at least the Migration log line, got none")
	}
	for _, l := range logs {
		if strings.Contains(l.Message, password) {
			t.Fatalf("run log leaked the plaintext source password: %q", l.Message)
		}
	}
	found := false
	for _, l := range logs {
		if strings.HasPrefix(l.Message, "Migration:") {
			found = true
			if !strings.Contains(l.Message, "xxxxx") {
				t.Errorf("Migration log line = %q, want it to contain the redacted placeholder", l.Message)
			}
		}
	}
	if !found {
		t.Fatal("no \"Migration: ...\" log line found")
	}
}
