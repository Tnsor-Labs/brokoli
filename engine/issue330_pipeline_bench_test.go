package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// #330's own measurement, re-run per codec.
//
// The issue reports 1,000,000 rows of CSV through source_file to
// sink_file and asks for "a before and after on those numbers ... not
// the elegance of the format". The microbenchmarks beside this file
// measure the codec in isolation; this measures the pipeline, which is
// what the issue is about and where CSV parsing, the per-row map and the
// sink's own encoding dilute the codec's share.
//
// Skipped unless BROKOLI_BENCH_330 is set, because generating and
// processing a million rows twice takes minutes and CI has no reason to
// pay it. Run it with:
//
//	BROKOLI_BENCH_330=1 go test ./engine/ -run TestIssue330 -v -timeout 30m
func TestIssue330SourceFileToSinkFile(t *testing.T) {
	if os.Getenv("BROKOLI_BENCH_330") == "" {
		t.Skip("set BROKOLI_BENCH_330=1 to run the million-row pipeline measurement")
	}
	const rows = 1_000_000

	dir := t.TempDir()
	input := filepath.Join(dir, "input.csv")
	writeBenchCSV(t, input, rows)
	if fi, err := os.Stat(input); err == nil {
		t.Logf("input: %d rows, %.1f MB", rows, float64(fi.Size())/1024/1024)
	}

	for _, codec := range []string{"ndjson", "arrow"} {
		t.Run(codec, func(t *testing.T) {
			t.Setenv(streamCodecEnv, codec)
			out := filepath.Join(dir, "out-"+codec+".csv")

			eng, s := newResumeTestEngine(t)
			eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(dir, "artifacts-"+codec))
			// Both thresholds at 1 so the streaming path engages, which
			// is the path a dataset this size takes in production.
			eng.SpillThresholdBytes = 1
			eng.StreamThresholdBytes = 1

			pipe := &models.Pipeline{
				ID: "p-330-" + codec, Name: "issue 330 " + codec, Enabled: true,
				Nodes: []models.Node{
					{ID: "src", Type: models.NodeTypeSourceFile, Name: "Read",
						Config: map[string]interface{}{"path": input}},
					{ID: "dst", Type: models.NodeTypeSinkFile, Name: "Write",
						Config: map[string]interface{}{"path": out, "format": "csv"}},
				},
				Edges: []models.Edge{{From: "src", To: "dst"}},
			}
			if err := s.CreatePipeline(pipe); err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			run, err := eng.RunPipeline(pipe.ID)
			elapsed := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != models.RunStatusSuccess {
				t.Fatalf("run status = %s (error: %s)", run.Status, run.Error)
			}

			// The output is checked, not assumed: a codec that wrote
			// fewer rows faster would otherwise look like a win.
			got := countCSVDataRows(t, out)
			if got != rows {
				t.Fatalf("wrote %d rows, want %d", got, rows)
			}
			t.Logf("%s: %s for %d rows (%.0f rows/sec)",
				codec, elapsed.Round(time.Millisecond), rows,
				float64(rows)/elapsed.Seconds())
		})
	}
}

func writeBenchCSV(t *testing.T, path string, rows int) {
	t.Helper()
	f, err := os.Create(path) // #nosec G304 -- test-local temp path
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cats := []string{"alpha", "beta", "gamma", "delta"}
	if _, err := fmt.Fprintln(f, "id,name,amount,active,category,note"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < rows; i++ {
		active := "false"
		if i%3 == 0 {
			active = "true"
		}
		if _, err := fmt.Fprintf(f, "%d,customer-%d,%d.5,%s,%s,a short note representative of a text column\n",
			i, i, i%997, active, cats[i%4]); err != nil {
			t.Fatal(err)
		}
	}
}

func countCSVDataRows(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path) // #nosec G304 -- test-local temp path
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	if n > 0 {
		n-- // header
	}
	return n
}
