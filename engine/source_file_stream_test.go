package engine

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/loaders"
)

// streamEligible is what decides whether a node takes the streaming path, so
// it is what decides whether a large CSV OOMs the worker. A source_file node
// pointed at a csv must be eligible; one pointed at a format whose column set
// is not known until the file has been read must not be, because "stream it
// anyway" there means reading the whole file to find the columns.
func TestSourceFileStreamEligibility(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		want bool
	}{
		{"csv streams", "/data/big.csv", true},
		{"uppercase extension still streams", "/data/BIG.CSV", true},
		{"json cannot: columns are the union of all objects", "/data/big.json", false},
		{"xlsx cannot", "/data/book.xlsx", false},
		{"xml cannot", "/data/feed.xml", false},
		{"a node with no path is not eligible", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := loaders.SupportsStreaming(tc.path); tc.path != "" && got != tc.want {
				t.Errorf("SupportsStreaming(%q) = %v, want %v", tc.path, got, tc.want)
			}
			node := models.Node{Type: models.NodeTypeSourceFile, Config: map[string]interface{}{}}
			if tc.path != "" {
				node.Config["path"] = tc.path
			}
			eligible := func() bool {
				p, _ := node.Config["path"].(string)
				return p != "" && loaders.SupportsStreaming(p)
			}()
			if eligible != tc.want {
				t.Errorf("eligibility = %v, want %v", eligible, tc.want)
			}
		})
	}
}

// The streamed node path and the materialising node path must produce the
// same rows. This runs a real pipeline both ways over the same file rather
// than comparing the loaders directly, because the thing that has to agree is
// what lands downstream -- a reference and a materialised dataset are
// different objects and the equivalence has to survive that difference.
func TestSourceFileStreamedRunMatchesMaterialized(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "rows.csv")

	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	w := csv.NewWriter(f)
	if err := w.Write([]string{"id", "name", "amount", "note"}); err != nil {
		t.Fatal(err)
	}
	const rows = 12000 // more than one default batch
	for i := 0; i < rows; i++ {
		note := "note text"
		if i%11 == 0 {
			note = ""
		}
		if err := w.Write([]string{fmt.Sprint(i), fmt.Sprintf("n%d", i), fmt.Sprint(i * 3), note}); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Returns the sink output and whether the source actually took the
	// streaming path. Without that second value this test would pass just as
	// happily if both runs materialised, which is the failure mode it exists
	// to catch.
	run := func(t *testing.T, streaming bool) (string, bool) {
		t.Helper()
		out := filepath.Join(dir, fmt.Sprintf("out-%v.csv", streaming))
		t.Setenv("BROKOLI_DATA_DIRS", dir)

		eng, s := newResumeTestEngine(t)
		eng.ArtifactStore = NewLocalDiskArtifactStore(filepath.Join(t.TempDir(), "artifacts"))
		if streaming {
			eng.SpillThresholdBytes = 1
			eng.StreamThresholdBytes = 1
		} else {
			// A negative threshold disables streaming eligibility, which is
			// how the same pipeline reaches the materialising path.
			eng.StreamThresholdBytes = -1
		}

		pipeline := &models.Pipeline{
			ID:      fmt.Sprintf("p-src-file-%v", streaming),
			Name:    "source_file equivalence",
			Enabled: true,
			Nodes: []models.Node{
				{ID: "src", Type: models.NodeTypeSourceFile, Name: "read",
					Config: map[string]interface{}{"path": src}},
				{ID: "dst", Type: models.NodeTypeSinkFile, Name: "write",
					Config: map[string]interface{}{"path": out, "format": "csv"}},
			},
			Edges: []models.Edge{{From: "src", To: "dst"}},
		}
		if err := s.CreatePipeline(pipeline); err != nil {
			t.Fatal(err)
		}
		r, err := eng.RunPipeline(pipeline.ID)
		if err != nil {
			t.Fatal(err)
		}
		if r.Status != models.RunStatusSuccess {
			t.Fatalf("streaming=%v: run status = %s (error: %s)", streaming, r.Status, r.Error)
		}

		logs, err := s.GetLogs(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		didStream := false
		for _, l := range logs {
			if l.NodeID == "src" && strings.Contains(l.Message, "never materialized") {
				didStream = true
				break
			}
		}

		got, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("reading %s: %v", out, err)
		}
		return string(got), didStream
	}

	materialized, materializedStreamed := run(t, false)
	streamed, streamedStreamed := run(t, true)

	if materialized == "" {
		t.Fatal("the materialising run produced nothing; the comparison would be vacuous")
	}
	if !streamedStreamed {
		t.Fatal("the streaming run did not take the streaming path, so this proves nothing")
	}
	if materializedStreamed {
		t.Fatal("the control run streamed too, so there is no materialising side to compare against")
	}
	if streamed != materialized {
		t.Errorf("streamed and materialised runs disagree (%d vs %d bytes)",
			len(streamed), len(materialized))
	}
}
