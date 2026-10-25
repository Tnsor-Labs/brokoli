package engine

import (
	"context"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
)

// A cancelled run ended with the same plain "pipeline cancelled" error as
// a failure, and the engine counted it in RunsFailed: cancelling runs
// raised the failure metric operators alert on. It is now counted as
// cancelled, on the async path and on the synchronous one.
func TestACancelledRunIsCountedAsCancelledNotFailed(t *testing.T) {
	for _, path := range []string{"async", "sync"} {
		t.Run(path, func(t *testing.T) {
			eng, s := newExecCtxTestEngine(t)
			exec := &blockingUntilCancelExecutor{nodeType: "blocking", startedCh: make(chan struct{}), observed: make(chan error, 1)}
			eng.Executors = []extensions.NodeExecutor{exec}
			pipe := &models.Pipeline{
				ID: "p-cancel-count-" + path, Name: "Cancel count", Enabled: true,
				Nodes: []models.Node{
					{ID: "source", Type: models.NodeTypeSourceFile, Name: "Source", Config: map[string]interface{}{"path": execCtxSourceCSV(t), "format": "csv"}},
					{ID: "blocking", Type: models.NodeType("blocking"), Name: "Blocking"},
				},
				Edges: []models.Edge{{From: "source", To: "blocking"}},
			}
			if err := s.CreatePipeline(pipe); err != nil {
				t.Fatal(err)
			}

			syncDone := make(chan error, 1)
			if path == "async" {
				if _, err := eng.RunPipelineAsync(pipe.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				go func() {
					_, err := eng.RunPipeline(pipe.ID)
					syncDone <- err
				}()
			}
			select {
			case <-exec.startedCh:
			case <-time.After(10 * time.Second):
				t.Fatal("the blocking node never started")
			}
			runs, err := s.ListRunsByPipeline(pipe.ID, 10)
			if err != nil || len(runs) != 1 {
				t.Fatalf("runs = %v, %v", runs, err)
			}
			if err := eng.CancelRun(runs[0].ID); err != nil {
				t.Fatal(err)
			}
			if path == "sync" {
				select {
				case <-syncDone:
				case <-time.After(15 * time.Second):
					t.Fatal("the cancelled run never returned")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_ = eng.Close(ctx)

			m := eng.Metrics()
			if m.RunsFailed != 0 || m.RunsCancelled != 1 || m.RunsSucceeded != 0 {
				t.Fatalf("failed=%d cancelled=%d succeeded=%d, want 0, 1, 0", m.RunsFailed, m.RunsCancelled, m.RunsSucceeded)
			}
		})
	}
}
