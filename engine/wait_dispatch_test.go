package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Parking before dispatch: the waits with nothing upstream park on this
// process before a run is handed to another, and WakeHandoff hands the
// woken run on.

// mkDispatchPipeline: src -> sink, plus root waits gating the sink.
func mkDispatchPipeline(t *testing.T, st store.Store, dir, id string, waits ...models.Node) *models.Pipeline {
	t.Helper()
	csv := filepath.Join(dir, "in-"+id+".csv")
	if err := os.WriteFile(csv, []byte("id\n7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &models.Pipeline{
		ID: id, Name: id, Enabled: true,
		Params: map[string]string{"upstream": "upstream"},
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
				Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "K",
				Config: map[string]interface{}{"path": filepath.Join(dir, "out-"+id+".csv"), "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	for _, w := range waits {
		w.Type = models.NodeTypeWait
		w.Name = w.ID
		p.Nodes = append(p.Nodes, w)
		p.Edges = append(p.Edges, models.Edge{From: w.ID, To: "sink"})
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// mkUpstream creates pipeline "upstream" whose latest run has status.
func mkUpstream(t *testing.T, st store.Store, status models.RunStatus) {
	t.Helper()
	if _, err := st.GetPipeline("upstream"); err != nil {
		if err := st.CreatePipeline(&models.Pipeline{ID: "upstream", Name: "upstream", Enabled: true,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now().UTC()
	if err := st.CreateRun(&models.Run{ID: common.NewID(), PipelineID: "upstream", Status: status, StartedAt: &started}); err != nil {
		t.Fatal(err)
	}
}

// pendingRun creates the run row a dispatcher creates before handing a
// run over.
func pendingRun(t *testing.T, st store.Store, pipelineID string) *models.Run {
	t.Helper()
	now := time.Now().UTC()
	run := &models.Run{ID: common.NewID(), PipelineID: pipelineID, Status: models.RunStatusPending, StartedAt: &now}
	if err := st.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	return run
}

func pipelineWait(id, pipelineID string) models.Node {
	return models.Node{ID: id, Config: map[string]interface{}{
		"condition": "pipeline", "pipeline_id": pipelineID, "poll_interval": "50ms"}}
}

// An unmet wait with nothing upstream parks the run here, before it is
// handed over: the run is waiting, the park is stored, nothing ran.
func TestParkBeforeDispatchParksOnAnUnmetRootWait(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	mkUpstream(t, st, models.RunStatusFailed)
	p := mkDispatchPipeline(t, st, dir, "pbd", pipelineWait("gate", "upstream"))
	run := pendingRun(t, st, p.ID)

	parked, err := eng.ParkBeforeDispatch(run, p, nil)
	if err != nil || !parked {
		t.Fatalf("parked=%v err=%v, want parked", parked, err)
	}
	got, _ := st.GetRun(run.ID)
	if got.Status != models.RunStatusWaiting {
		t.Fatalf("run is %s, want waiting", got.Status)
	}
	if n, _ := st.CountParkedWaits(); n != 1 {
		t.Fatalf("parked waits = %d, want 1", n)
	}
	if len(got.NodeRuns) != 0 {
		t.Fatalf("%d node runs recorded, want none: nothing has run", len(got.NodeRuns))
	}
}

// Every wait met: the run is left as it was, to be dispatched.
func TestParkBeforeDispatchLeavesARunWhoseWaitsAreMet(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	mkUpstream(t, st, models.RunStatusSuccess)
	// pipeline_id comes from a parameter: config is resolved as a runner
	// resolves it, params over the pipeline's defaults.
	p := mkDispatchPipeline(t, st, dir, "pbm", pipelineWait("gate", "${param.upstream}"))
	run := pendingRun(t, st, p.ID)

	parked, err := eng.ParkBeforeDispatch(run, p, map[string]string{"unrelated": "x"})
	if err != nil || parked {
		t.Fatalf("parked=%v err=%v, want not parked", parked, err)
	}
	if got, _ := st.GetRun(run.ID); got.Status != models.RunStatusPending {
		t.Fatalf("run is %s, want it left pending", got.Status)
	}
	if n, _ := st.CountParkedWaits(); n != 0 {
		t.Fatalf("parked waits = %d, want 0", n)
	}
}

// file_exists waits and waits with something upstream are left to the
// process that runs the pipeline, however unmet.
func TestParkBeforeDispatchLeavesFileWaitsAndDownstreamWaitsAlone(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	mkUpstream(t, st, models.RunStatusFailed)
	p := mkDispatchPipeline(t, st, dir, "pbl", models.Node{ID: "file", Config: map[string]interface{}{
		"condition": "file_exists", "path": filepath.Join(dir, "never.flag")}})
	// A pipeline wait downstream of src, unmet.
	p.Nodes = append(p.Nodes, models.Node{ID: "later", Type: models.NodeTypeWait, Name: "later",
		Config: pipelineWait("later", "upstream").Config})
	p.Edges = append(p.Edges, models.Edge{From: "src", To: "later"})
	run := pendingRun(t, st, p.ID)

	parked, err := eng.ParkBeforeDispatch(run, p, nil)
	if err != nil || parked {
		t.Fatalf("parked=%v err=%v, want not parked", parked, err)
	}
	if n, _ := st.CountParkedWaits(); n != 0 {
		t.Fatalf("parked waits = %d, want 0", n)
	}
}

// A check that fails fails the run, as a runner's first check does.
func TestParkBeforeDispatchFailsTheRunWhenACheckFails(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	p := mkDispatchPipeline(t, st, dir, "pbf", pipelineWait("gate", "no-such-pipeline"))
	run := pendingRun(t, st, p.ID)

	parked, err := eng.ParkBeforeDispatch(run, p, nil)
	if err == nil || parked || !strings.Contains(err.Error(), "pipeline not found") {
		t.Fatalf("parked=%v err=%v, want a not-found failure", parked, err)
	}
	got, _ := st.GetRun(run.ID)
	if got.Status != models.RunStatusFailed || !strings.Contains(got.Error, "pipeline not found") {
		t.Fatalf("run is %s (%q), want failed naming the cause", got.Status, got.Error)
	}
}

// parkAndOpen parks a run before dispatch, then makes its condition true
// and runs one watcher pass.
func parkAndOpen(t *testing.T, st *store.SQLiteStore, eng *Engine, dir, id string) *models.Run {
	t.Helper()
	mkUpstream(t, st, models.RunStatusFailed)
	p := mkDispatchPipeline(t, st, dir, id, pipelineWait("gate", "upstream"))
	run := pendingRun(t, st, p.ID)
	if parked, err := eng.ParkBeforeDispatch(run, p, nil); err != nil || !parked {
		t.Fatalf("parked=%v err=%v, want parked", parked, err)
	}
	mkUpstream(t, st, models.RunStatusSuccess)
	time.Sleep(60 * time.Millisecond) // the park's first poll is due after 50ms
	NewScheduler(eng, st, &fakeLeaderElector{leader: true}).pollParkedWaits()
	eng.bg.Wait()
	return run
}

// The watcher wakes a run parked before dispatch and offers it to
// WakeHandoff, claimed; taken, it does not run here.
func TestWakeHandoffTakesTheWokenRun(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	var mu sync.Mutex
	var offered []*models.Run
	eng.WakeHandoff = func(run *models.Run) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		offered = append(offered, run)
		return true, nil
	}
	run := parkAndOpen(t, st, eng, dir, "wht")

	mu.Lock()
	defer mu.Unlock()
	if len(offered) != 1 || offered[0].ID != run.ID || offered[0].Status != models.RunStatusRunning {
		t.Fatalf("offered %d runs (%+v), want this run once, claimed (running)", len(offered), offered)
	}
	got, _ := st.GetRun(run.ID)
	if got.Status != models.RunStatusRunning {
		t.Fatalf("run is %s, want running: handed off, executing elsewhere", got.Status)
	}
	if _, err := os.Stat(filepath.Join(dir, "out-wht.csv")); err == nil {
		t.Fatal("the pipeline ran here although the hook took the run")
	}
	if n, _ := st.CountParkedWaits(); n != 0 {
		t.Fatalf("parked waits = %d, want 0 after the wake", n)
	}
}

// A hook that declines leaves the run to run here, as without one.
func TestWakeHandoffDeclinedRunsHere(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	eng.WakeHandoff = func(*models.Run) (bool, error) { return false, nil }
	run := parkAndOpen(t, st, eng, dir, "whd")

	if got, _ := st.GetRun(run.ID); got.Status != models.RunStatusSuccess {
		t.Fatalf("run is %s (%q), want success, run here", got.Status, got.Error)
	}
}

// A hook that fails fails the run rather than leaving it running with
// nothing executing it.
func TestWakeHandoffErrorFailsTheRun(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	eng.WakeHandoff = func(*models.Run) (bool, error) { return false, errors.New("queue unavailable") }
	run := parkAndOpen(t, st, eng, dir, "whe")

	got, _ := st.GetRun(run.ID)
	if got.Status != models.RunStatusFailed || !strings.Contains(got.Error, "queue unavailable") {
		t.Fatalf("run is %s (%q), want failed naming the hook's error", got.Status, got.Error)
	}
}
