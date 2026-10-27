package engine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A code expansion ran each local instance with no context at all, so
// cancelling the run did not stop an instance's script (which may write
// anywhere): it ran on to the end after the run was cancelled. A script
// that takes 10s is cancelled once it has started; it no longer finishes.
func TestCodeExpansionInstanceStopsWhenTheRunIsCancelled(t *testing.T) {
	dir := t.TempDir()
	marks := filepath.Join(dir, "marks")
	items := writeCSV(t, dir, "items.csv", "path\nonly\n")
	s, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	pipe := &models.Pipeline{ID: "expand-cancel", Name: "expand cancel", CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "items", Type: models.NodeTypeSourceFile, Name: "items", Config: map[string]interface{}{"path": items, "format": "csv"}},
			{ID: "slow", Type: models.NodeTypeCode, Name: "slow", Config: map[string]interface{}{
				"script": "import time\n" +
					"open(" + pyQuote(marks) + ", 'a').write('started\\n')\n" +
					"time.sleep(10)\n" +
					"open(" + pyQuote(marks) + ", 'a').write('finished\\n')\n" +
					"output_data = {'columns': columns, 'rows': rows}\n",
				"expansion": map[string]interface{}{"over": map[string]interface{}{"file": "items"}},
			}},
		},
		Edges: []models.Edge{{From: "items", To: "slow"}},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(s, nil, pipe, nil, nil, nil, nil, "", nil)
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = runner.Execute()
	}()
	for {
		if data, _ := os.ReadFile(marks); len(data) > 0 {
			break
		}
		if time.Since(start) > 15*time.Second {
			t.Fatal("the script never started")
		}
		time.Sleep(50 * time.Millisecond)
	}
	runner.Cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the cancelled run never returned")
	}
	// The script would finish 10s after it started; wait past that.
	if wait := 11*time.Second - time.Since(start); wait > 0 {
		time.Sleep(wait)
	}
	data, _ := os.ReadFile(marks)
	if string(data) != "started\n" {
		t.Fatalf("script marks = %q, want it stopped before finishing", data)
	}
}

func pyQuote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' || r == '\\' {
			out += "\\"
		}
		out += string(r)
	}
	return out + "'"
}
