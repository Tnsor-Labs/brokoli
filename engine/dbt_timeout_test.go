package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// dbt ran under the run's context, which a node's timeout never reaches,
// so a dbt run that timed out kept building models while the run went on
// (and a retry would have started a second dbt beside it). A fake dbt that
// takes 10s, a 2s timeout, and a sibling node keeping the run alive: the
// dbt process is now stopped at the deadline.
func TestDBTIsStoppedWhenItsAttemptTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake dbt is a shell script")
	}
	dir := t.TempDir()
	marks := filepath.Join(dir, "marks")
	script := "#!/bin/sh\necho started >> " + marks + "\nsleep 10\necho finished >> " + marks + "\n"
	if err := os.WriteFile(filepath.Join(dir, "dbt"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	s, err := store.NewSQLiteStore(filepath.Join(dir, "meta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UTC()
	pipe := &models.Pipeline{ID: "dbt-timeout", Name: "dbt timeout", CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "dbt", Type: models.NodeTypeDBT, Name: "dbt", Config: map[string]interface{}{
				"command": "run", "project_dir": dir, "timeout": float64(2),
			}},
			{ID: "busy", Type: sleeperNode, Name: "busy", Config: map[string]interface{}{}},
		},
	}
	if err := s.CreatePipeline(pipe); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRunner(s, nil, pipe, nil, nil, []extensions.NodeExecutor{sleeperExecutor{12 * time.Second}}, nil, "", nil).Execute(); err == nil {
		t.Fatal("the dbt node did not time out")
	}
	data, _ := os.ReadFile(marks)
	if string(data) != "started\n" {
		t.Fatalf("dbt marks = %q, want it started and stopped before finishing", data)
	}
}
