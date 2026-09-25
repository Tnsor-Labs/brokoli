package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// A draft skips executable validation when it is saved, so it must be
// unable to run by any route. If even one route can run one, the flag
// advertises a safety property it does not hold (#107).
//
// The table is the point: a seventh way to start a run added later fails
// this test rather than silently bypassing the flag.
func TestDraftPipelineCannotRunByAnyRoute(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "draft.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	// A draft that is otherwise complete and would run happily if it were
	// published: the refusal must come from the flag, not from the graph
	// being unfinished.
	src := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(src, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &models.Pipeline{
		ID: "draft-1", Name: "draft-1", Enabled: true, Draft: true,
		Schedule: "0 9 * * *",
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
				Config: map[string]interface{}{"path": src, "format": "csv"}},
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatalf("create: %v", err)
	}

	routes := map[string]func() error{
		"RunPipeline": func() error {
			_, err := eng.RunPipeline(p.ID)
			return err
		},
		"RunPipelineOpts": func() error {
			_, err := eng.RunPipelineOpts(p.ID, RunOptions{})
			return err
		},
		"RunPipelineAsync": func() error {
			_, err := eng.RunPipelineAsync(p.ID)
			return err
		},
		"RunPipelineAsyncLocal": func() error {
			_, err := eng.RunPipelineAsyncLocal(p.ID)
			return err
		},
		"RunPipelineAsyncWithParameters": func() error {
			_, err := eng.RunPipelineAsyncWithParameters(p.ID, nil, nil)
			return err
		},
		"Backfill": func() error {
			_, err := eng.Backfill(p.ID, BackfillRequest{
				Start: time.Now().Add(-48 * time.Hour),
				End:   time.Now().Add(-24 * time.Hour),
			})
			return err
		},
	}

	for name, run := range routes {
		t.Run(name, func(t *testing.T) {
			err := run()
			if err == nil {
				t.Fatalf("%s ran a draft pipeline", name)
			}
			if !errors.Is(err, ErrPipelineIsDraft) {
				t.Fatalf("%s refused with %v, want ErrPipelineIsDraft; a refusal for another "+
					"reason would stop being a refusal once that reason went away", name, err)
			}
		})
	}

	// Nothing was created behind any of those refusals.
	runs, err := st.ListRunsByPipeline(p.ID, 50)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("a draft produced %d runs", len(runs))
	}
}

// The other half: publishing makes it run. Without this, a test suite
// that only proves drafts are refused would also pass if everything were
// refused.
func TestPublishedPipelineRuns(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "pub.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	src := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(src, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &models.Pipeline{
		ID: "pub-1", Name: "pub-1", Enabled: true, Draft: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
				Config: map[string]interface{}{"path": src, "format": "csv"}},
		},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := eng.RunPipeline(p.ID); !errors.Is(err, ErrPipelineIsDraft) {
		t.Fatalf("draft ran or failed for another reason: %v", err)
	}

	p.Draft = false
	if err := st.UpdatePipeline(p); err != nil {
		t.Fatalf("publish: %v", err)
	}
	run, err := eng.RunPipeline(p.ID)
	if err != nil {
		t.Fatalf("published pipeline did not run: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Errorf("run status = %s, error = %s", run.Status, run.Error)
	}
}

// The flag has to survive a round trip, or a draft becomes a runnable
// pipeline the next time it is read.
func TestDraftSurvivesStorage(t *testing.T) {
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "rt.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	for _, draft := range []bool{true, false} {
		t.Run(fmt.Sprintf("draft=%v", draft), func(t *testing.T) {
			id := fmt.Sprintf("rt-%v", draft)
			p := &models.Pipeline{
				ID: id, Name: id, Enabled: true, Draft: draft,
				Nodes:     []models.Node{{ID: "n", Type: models.NodeTypeSourceFile, Name: "S"}},
				CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			}
			if err := st.CreatePipeline(p); err != nil {
				t.Fatalf("create: %v", err)
			}
			got, err := st.GetPipeline(id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Draft != draft {
				t.Fatalf("after create, Draft = %v, want %v", got.Draft, draft)
			}

			// And through an update, which writes a separate column list.
			got.Draft = !draft
			if err := st.UpdatePipeline(got); err != nil {
				t.Fatalf("update: %v", err)
			}
			again, err := st.GetPipeline(id)
			if err != nil {
				t.Fatalf("get after update: %v", err)
			}
			if again.Draft != !draft {
				t.Fatalf("after update, Draft = %v, want %v", again.Draft, !draft)
			}
		})
	}
}

// A draft with a schedule is allowed, so you can build the whole thing
// and publish once. It must not reach the scheduler in the meantime.
func TestDraftWithScheduleIsNotRegistered(t *testing.T) {
	st, err := store.NewSQLiteStore(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	p := &models.Pipeline{
		ID: "sched-draft", Name: "sched-draft", Enabled: true, Draft: true,
		Schedule:  "*/1 * * * *",
		Nodes:     []models.Node{{ID: "n", Type: models.NodeTypeSourceFile, Name: "S"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatalf("create: %v", err)
	}

	sched := NewScheduler(eng, st, nil)
	if err := sched.Start(); err != nil {
		t.Fatalf("start scheduler: %v", err)
	}
	t.Cleanup(sched.Stop)
	if next := sched.NextRun(p.ID); !next.IsZero() {
		t.Errorf("a draft was registered with the scheduler, next run %s", next)
	}
}
