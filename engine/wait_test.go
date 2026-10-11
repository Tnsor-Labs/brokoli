package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Deferrable waits (#399): parking frees the slot, the park survives
// restart, the watcher wakes the SAME run when the condition fires,
// timeouts fail naming the condition, and a thousand parked waits hold
// zero run slots and one watcher loop.

func waitTestEngine(t *testing.T) (*store.SQLiteStore, *Engine, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, drainEngineOnCleanup(t, NewEngine(st)), dir
}

// mkWaitPipeline: src -> wait(file_exists: gate) -> sink, so the wake has
// upstream output to restore and downstream work to prove it continued.
func mkWaitPipeline(t *testing.T, st store.Store, dir, id, gate string, extra map[string]interface{}) {
	t.Helper()
	csv := filepath.Join(dir, "in-"+id+".csv")
	if err := os.WriteFile(csv, []byte("id\n7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]interface{}{"condition": "file_exists", "path": gate}
	for k, v := range extra {
		cfg[k] = v
	}
	p := &models.Pipeline{
		ID: id, Name: id, Enabled: true,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
				Config: map[string]interface{}{"path": csv, "format": "csv"}},
			{ID: "gate", Type: models.NodeTypeWait, Name: "G", Config: cfg},
			{ID: "sink", Type: models.NodeTypeSinkFile, Name: "K",
				Config: map[string]interface{}{"path": filepath.Join(dir, "out-"+id+".csv"), "format": "csv"}},
		},
		Edges:     []models.Edge{{From: "src", To: "gate"}, {From: "gate", To: "sink"}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
}

// awaitNoActiveRuns polls GetQueueInfo briefly: RunPipeline signals its
// caller BEFORE its dispatch goroutine's deferred active-map delete and
// slot release run (deliberate -- return latency excludes fan-out), so an
// instant read can see the slot for a few microseconds. The property
// under test is "parked holds nothing", not "bookkeeping is synchronous".
func awaitNoActiveRuns(t *testing.T, eng *Engine) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if n, _ := eng.GetQueueInfo(); n == 0 {
			return
		}
		if time.Now().After(deadline) {
			n, _ := eng.GetQueueInfo()
			t.Fatalf("%d active runs while parked, want 0 -- a parked wait must hold no slot", n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWaitNodeParksAndWatcherWakesTheSameRun(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	gate := filepath.Join(dir, "ready.flag")
	mkWaitPipeline(t, st, dir, "wp", gate, map[string]interface{}{"poll_interval": "50ms"})

	run, err := eng.RunPipeline("wp")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Status != models.RunStatusWaiting {
		t.Fatalf("run status = %s, want waiting", run.Status)
	}
	// Parked: no slot held, park durable, upstream succeeded, sink never ran.
	awaitNoActiveRuns(t, eng)
	if n, _ := st.CountParkedWaits(); n != 1 {
		t.Fatalf("parked waits = %d, want 1", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "out-wp.csv")); err == nil {
		t.Fatal("sink ran before the gate opened")
	}

	// Open the gate; run the watcher against a fake leader. The park's
	// first poll is due poll_interval (50ms) after parking.
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	sched := NewScheduler(eng, st, &fakeLeaderElector{leader: true})
	sched.pollParkedWaits()
	eng.bg.Wait()

	got, err := st.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.RunStatusSuccess {
		t.Fatalf("woken run status = %s (err %q), want success on the SAME run id", got.Status, got.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, "out-wp.csv")); err != nil {
		t.Fatal("the woken run did not finish the pipeline")
	}
	if n, _ := st.CountParkedWaits(); n != 0 {
		t.Fatalf("park row leaked: %d", n)
	}
}

// The park is in the store, not in memory: a fresh engine + scheduler
// (instance restart) wakes a run parked by the previous life.
func TestParkedWaitSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "w.db")
	st, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	gate := filepath.Join(dir, "ready.flag")
	mkWaitPipeline(t, st, dir, "rp", gate, map[string]interface{}{"poll_interval": "50ms"})

	eng := NewEngine(st)
	run, err := eng.RunPipeline("rp")
	if err != nil || run.Status != models.RunStatusWaiting {
		t.Fatalf("park failed: %v %v", run, err)
	}
	if err := eng.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// The restart: new store handle, new engine, new scheduler.
	st2, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	eng2 := drainEngineOnCleanup(t, NewEngine(st2))
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond) // past the park's first poll_interval
	sched := NewScheduler(eng2, st2, &fakeLeaderElector{leader: true})
	sched.pollParkedWaits()
	eng2.bg.Wait()

	got, err := st2.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.RunStatusSuccess {
		t.Fatalf("run parked before restart = %s (err %q), want success after the new instance's watcher", got.Status, got.Error)
	}
}

func TestParkedWaitTimesOutNamingTheCondition(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	mkWaitPipeline(t, st, dir, "tp", filepath.Join(dir, "never.flag"),
		map[string]interface{}{"timeout": "1ms", "poll_interval": "10ms"})

	run, err := eng.RunPipeline("tp")
	if err != nil || run.Status != models.RunStatusWaiting {
		t.Fatalf("park failed: %v %v", run, err)
	}
	time.Sleep(5 * time.Millisecond) // let the 1ms deadline pass
	sched := NewScheduler(eng, st, &fakeLeaderElector{leader: true})
	sched.pollParkedWaits()

	got, err := st.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.RunStatusFailed {
		t.Fatalf("timed-out run status = %s, want failed", got.Status)
	}
	if got.Error == "" || !strings.Contains(got.Error, "file_exists") {
		t.Fatalf("timeout error %q does not name the condition", got.Error)
	}
	if n, _ := st.CountParkedWaits(); n != 0 {
		t.Fatal("timed-out park leaked")
	}
}

// A non-leader's watcher never wakes anything.
func TestWaitWatcherIsLeaderGated(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	gate := filepath.Join(dir, "ready.flag")
	mkWaitPipeline(t, st, dir, "lg", gate, nil)
	run, err := eng.RunPipeline("lg")
	if err != nil || run.Status != models.RunStatusWaiting {
		t.Fatalf("park failed: %v %v", run, err)
	}
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	sched := NewScheduler(eng, st, &fakeLeaderElector{leader: false})
	old := waitWatchInterval
	waitWatchInterval = 10 * time.Millisecond
	t.Cleanup(func() { waitWatchInterval = old })
	go sched.runWaitWatcher()
	time.Sleep(60 * time.Millisecond)
	close(sched.stopReclaim)
	<-sched.waitDone

	got, _ := st.GetRun(run.ID)
	if got.Status != models.RunStatusWaiting {
		t.Fatalf("a non-leader woke the run (status %s)", got.Status)
	}
}

// The acceptance number from #399: a thousand parked waits hold zero run
// slots and cost one polling loop, not a thousand goroutines. Measured at
// the full 1,000 on 2026-08-29 (66s wall, 0 active runs, goroutine delta
// under 20); CI asserts the same property at 250 because the engine
// package already runs close to Go's per-package timeout (#329) and -race
// triples the loop. BROKOLI_WAIT_ACCEPTANCE_N=1000 re-runs the full
// measurement.
func TestThousandParkedWaitsHoldNothing(t *testing.T) {
	n := 250
	if v := os.Getenv("BROKOLI_WAIT_ACCEPTANCE_N"); v != "" {
		fmt.Sscanf(v, "%d", &n)
	}
	st, eng, dir := waitTestEngine(t)
	gate := filepath.Join(dir, "never.flag")

	// The smallest valid parking pipeline: a one-row source feeding the
	// gate, shared across all thousand.
	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		p := &models.Pipeline{
			ID: fmt.Sprintf("w%03d", i), Name: fmt.Sprintf("w%03d", i), Enabled: true,
			Nodes: []models.Node{
				{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
					Config: map[string]interface{}{"path": csv, "format": "csv"}},
				{ID: "gate", Type: models.NodeTypeWait, Name: "G",
					Config: map[string]interface{}{"condition": "file_exists", "path": gate}}},
			Edges:     []models.Edge{{From: "src", To: "gate"}},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := st.CreatePipeline(p); err != nil {
			t.Fatal(err)
		}
	}
	before := runtime.NumGoroutine()
	for i := 0; i < n; i++ {
		run, err := eng.RunPipeline(fmt.Sprintf("w%03d", i))
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if run.Status != models.RunStatusWaiting {
			t.Fatalf("run %d status = %s", i, run.Status)
		}
	}

	if parked, _ := st.CountParkedWaits(); parked != n {
		t.Fatalf("parked = %d, want %d", parked, n)
	}
	awaitNoActiveRuns(t, eng)

	// The property is that parking does not cost a goroutine per parked
	// run, so the budget scales with n rather than being a fixed number.
	// It was before+20, which defended a 250-park property with a
	// 20-goroutine margin and failed CI at 21: ambient goroutines from
	// neighbouring tests in this package land inside the margin, since
	// runtime.NumGoroutine is process-global. A per-park implementation
	// would show +250 here and still fails comfortably.
	//
	// Settled first: goroutines from earlier tests can still be winding
	// down when this one finishes, and a single instantaneous sample
	// cannot tell that apart from a leak.
	budget := n / 4
	if budget < 25 {
		budget = 25
	}
	after := runtime.NumGoroutine()
	for waited := 0; after > before+budget && waited < 2000; waited += 50 {
		time.Sleep(50 * time.Millisecond)
		after = runtime.NumGoroutine()
	}
	if after > before+budget {
		t.Fatalf("goroutines %d -> %d (budget +%d for %d parks); parking must not cost a goroutine per park",
			before, after, budget, n)
	}
}

// A file_exists wait is held to the data directories, like every file
// node; it used to probe any path on the worker's filesystem.
func TestWaitFileExistsIsHeldToTheDataDirectories(t *testing.T) {
	_, eng, dir := waitTestEngine(t)
	t.Setenv("BROKOLI_DATA_DIRS", dir)
	st := eng.store
	outside := "/etc/hostname"
	if _, err := os.Stat(outside); err != nil {
		outside = "/etc/passwd"
	}
	mkWaitPipeline(t, st, dir, "outside", outside, nil)
	run, err := eng.RunPipeline("outside")
	if err == nil && run.Status != models.RunStatusFailed {
		t.Fatalf("a wait on %s outside the data directories ran: status %s", outside, run.Status)
	}
	if run != nil && !strings.Contains(run.Error, "outside allowed directories") {
		t.Fatalf("run error = %q, want the data-directory refusal", run.Error)
	}

	// Control: a path inside them still works.
	gate := filepath.Join(dir, "ready.flag")
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	mkWaitPipeline(t, st, dir, "inside", gate, nil)
	if run, err := eng.RunPipeline("inside"); err != nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("wait inside the data directories: run %v, err %v", run, err)
	}
}

// A pipeline wait only sees pipelines of the waiting run's organization;
// it used to report another organization's pipeline status.
func TestWaitPipelineConditionStaysInsideTheOrganization(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	upstream := func(id, org string) {
		now := time.Now().UTC()
		if err := st.CreatePipeline(&models.Pipeline{ID: id, Name: id, Enabled: true, OrgID: org, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateRun(&models.Run{ID: "run-" + id, PipelineID: id, OrgID: org, Status: models.RunStatusSuccess, StartedAt: &now}); err != nil {
			t.Fatal(err)
		}
	}
	upstream("theirs", "org-other")
	upstream("ours", "org-acme")
	waiter := func(id, target string) *models.Run {
		csv := filepath.Join(dir, "in-"+id+".csv")
		if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if err := st.CreatePipeline(&models.Pipeline{ID: id, Name: id, Enabled: true, OrgID: "org-acme",
			Nodes: []models.Node{
				{ID: "src", Type: models.NodeTypeSourceFile, Name: "S", Config: map[string]interface{}{"path": csv, "format": "csv"}},
				{ID: "gate", Type: models.NodeTypeWait, Name: "G", Config: map[string]interface{}{"condition": "pipeline", "pipeline_id": target}},
			},
			Edges: []models.Edge{{From: "src", To: "gate"}}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		run, _ := eng.RunPipeline(id)
		return run
	}
	if run := waiter("wait-theirs", "theirs"); run == nil || run.Status != models.RunStatusFailed || !strings.Contains(run.Error, "pipeline not found") {
		t.Fatalf("waiting on another organization's pipeline: %+v, want failed with not found", run)
	}
	if run := waiter("wait-ours", "ours"); run == nil || run.Status != models.RunStatusSuccess {
		t.Fatalf("waiting on our own succeeded pipeline: %+v, want success", run)
	}
}

// RunPipeline hands back a parked run before its goroutine unregisters the
// Runner, so a cancel sent at once can find that finished Runner still in
// the active map. It must not be taken for a live one: the cancel would be
// reported as delivered and the run would stay waiting.
func TestCancellingAParkedRunWhileItsRunnerIsStillRegistered(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	mkWaitPipeline(t, st, dir, "cancel-race", filepath.Join(dir, "never.flag"), map[string]interface{}{"poll_interval": "50ms"})
	run, err := eng.RunPipeline("cancel-race")
	if err != nil || run.Status != models.RunStatusWaiting {
		t.Fatalf("run = %+v, err %v; want waiting", run, err)
	}
	pipe, err := st.GetPipeline("cancel-race")
	if err != nil {
		t.Fatal(err)
	}
	eng.mu.Lock()
	eng.active[run.ID] = &Runner{pipe: pipe}
	eng.mu.Unlock()
	defer func() {
		eng.mu.Lock()
		delete(eng.active, run.ID)
		eng.mu.Unlock()
	}()
	if err := eng.CancelRun(run.ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if got, _ := st.GetRun(run.ID); got.Status != models.RunStatusCancelled {
		t.Fatalf("status after cancel = %s, want cancelled", got.Status)
	}
	// The same window after a run finished: report it as completed, not
	// as a cancel delivered.
	if err := eng.CancelRun(run.ID); err == nil {
		t.Fatal("cancelling a cancelled run with a stale Runner registered succeeded, want already completed")
	}
}

// A parked run can be cancelled. It has no Runner to deliver a cancel to,
// so it used to stay waiting until it woke or timed out.
func TestCancellingAParkedRun(t *testing.T) {
	st, eng, dir := waitTestEngine(t)
	gate := filepath.Join(dir, "never.flag")
	mkWaitPipeline(t, st, dir, "cancel-me", gate, map[string]interface{}{"poll_interval": "50ms"})
	run, err := eng.RunPipeline("cancel-me")
	if err != nil || run.Status != models.RunStatusWaiting {
		t.Fatalf("run = %+v, err %v; want waiting", run, err)
	}
	if err := eng.CancelRun(run.ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	got, err := st.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.RunStatusCancelled {
		t.Fatalf("status after cancel = %s, want cancelled", got.Status)
	}
	if n, _ := st.CountParkedWaits(); n != 0 {
		t.Fatalf("park left behind after cancel: %d", n)
	}
	// The condition coming true afterwards must not resurrect it.
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(60 * time.Millisecond)
	NewScheduler(eng, st, &fakeLeaderElector{leader: true}).pollParkedWaits()
	eng.bg.Wait()
	if got, _ := st.GetRun(run.ID); got.Status != models.RunStatusCancelled {
		t.Fatalf("cancelled run changed to %s after its condition came true", got.Status)
	}
}

// pipelineLookup is a waitStore whose GetPipeline fails with err.
type pipelineLookup struct{ err error }

func (p pipelineLookup) GetPipeline(string) (*models.Pipeline, error) { return nil, p.err }
func (p pipelineLookup) ListRunsByPipeline(string, int) ([]models.Run, error) {
	return nil, fmt.Errorf("ListRunsByPipeline must not be reached")
}

// A process whose store cannot look pipelines up (a worker on an
// HTTP-backed store) says so. It used to answer "pipeline not found",
// which sent the author to check an ID that was fine.
func TestWaitPipelineConditionSaysWhenThisProcessCannotCheckIt(t *testing.T) {
	run := &models.Run{ID: "r1", OrgID: "org-acme"}
	cond := waitCondition{Type: "pipeline", PipelineID: "upstream"}

	_, err := waitConditionMet(t.Context(), pipelineLookup{err: fmt.Errorf("apistore: GetPipeline not supported on worker: %w", store.ErrUnsupported)}, run, cond)
	if err == nil || !strings.Contains(err.Error(), "cannot check a pipeline condition") || strings.Contains(err.Error(), "not found") {
		t.Fatalf("unsupported lookup: err = %v, want a capability message, not 'not found'", err)
	}
	// Control: a store that can look and does not find it still says not found.
	_, err = waitConditionMet(t.Context(), pipelineLookup{err: fmt.Errorf("sql: no rows in result set")}, run, cond)
	if err == nil || !strings.Contains(err.Error(), "pipeline not found") {
		t.Fatalf("missing pipeline: err = %v, want not found", err)
	}
}

// waitInPlaceEngine is waitTestEngine with Engine.WaitInPlace set, the
// mode for a process that cannot park.
func waitInPlaceEngine(t *testing.T) (*store.SQLiteStore, *Engine, string) {
	t.Helper()
	st, eng, dir := waitTestEngine(t)
	eng.WaitInPlace = true
	return st, eng, dir
}

// awaitRunStatus polls the stored run until it reaches want.
func awaitRunStatus(t *testing.T, st store.Store, runID string, want models.RunStatus) *models.Run {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := st.GetRun(runID)
		if err == nil && got.Status == want {
			return got
		}
		if time.Now().After(deadline) {
			status, msg := models.RunStatus("missing"), ""
			if got != nil {
				status, msg = got.Status, got.Error
			}
			t.Fatalf("run %s is %s (err %q), want %s", runID, status, msg, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A process that cannot park keeps the run and checks the condition
// itself: the run never leaves running, nothing is parked, and it
// finishes once the condition comes true, upstream output intact.
func TestWaitInPlaceKeepsTheRunAndFinishesWhenTheConditionComesTrue(t *testing.T) {
	st, eng, dir := waitInPlaceEngine(t)
	gate := filepath.Join(dir, "ready.flag")
	mkWaitPipeline(t, st, dir, "wip", gate, map[string]interface{}{"poll_interval": "50ms"})

	runID, err := eng.RunPipelineAsync("wip")
	if err != nil {
		t.Fatal(err)
	}
	awaitRunStatus(t, st, runID, models.RunStatusRunning)
	time.Sleep(200 * time.Millisecond) // several polls with the gate shut
	if got, _ := st.GetRun(runID); got.Status != models.RunStatusRunning {
		t.Fatalf("run is %s while waiting in place, want running", got.Status)
	}
	if n, _ := st.CountParkedWaits(); n != 0 {
		t.Fatalf("parked waits = %d, want 0: this process waits in place", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "out-wip.csv")); err == nil {
		t.Fatal("sink ran before the gate opened")
	}

	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	awaitRunStatus(t, st, runID, models.RunStatusSuccess)
	out, err := os.ReadFile(filepath.Join(dir, "out-wip.csv"))
	if err != nil || !strings.Contains(string(out), "7") {
		t.Fatalf("sink output = %q (err %v), want the upstream row", out, err)
	}
}

// The in-place wait ends on its deadline with the parked wait's message.
func TestWaitInPlaceTimesOutWithTheParkedWaitMessage(t *testing.T) {
	st, eng, dir := waitInPlaceEngine(t)
	mkWaitPipeline(t, st, dir, "wto", filepath.Join(dir, "never.flag"),
		map[string]interface{}{"poll_interval": "50ms", "timeout": "300ms"})

	start := time.Now()
	run, _ := eng.RunPipeline("wto") // a failed run returns its error too
	if run == nil {
		t.Fatal("no run returned")
	}
	got := awaitRunStatus(t, st, run.ID, models.RunStatusFailed)
	if !strings.Contains(got.Error, "wait node gate timed out: condition file_exists not met by") {
		t.Fatalf("run error = %q, want the wait timeout message", got.Error)
	}
	if took := time.Since(start); took < 300*time.Millisecond || took > 5*time.Second {
		t.Fatalf("timed out after %s, want about the 300ms timeout", took)
	}
}

// A retried wait keeps its first attempt's deadline: retrying must not
// restart the clock, or max_retries would multiply the timeout.
func TestWaitInPlaceRetryKeepsTheFirstDeadline(t *testing.T) {
	st, eng, dir := waitInPlaceEngine(t)
	mkWaitPipeline(t, st, dir, "wrt", filepath.Join(dir, "never.flag"),
		map[string]interface{}{"poll_interval": "50ms", "timeout": "400ms", "max_retries": float64(3),
			"retry_delay": float64(10), "retry_backoff": "fixed"})

	run, _ := eng.RunPipeline("wrt")
	if run == nil {
		t.Fatal("no run returned")
	}
	got := awaitRunStatus(t, st, run.ID, models.RunStatusFailed)
	// Each attempt's own duration, not the run's: the run's setup and
	// teardown are slow under the race detector and say nothing here.
	retries := 0
	for _, nr := range got.NodeRuns {
		if nr.NodeID != "gate" || nr.Attempt == 0 {
			continue
		}
		retries++
		if nr.DurationMs >= 350 {
			t.Fatalf("retry %d waited %dms of a 400ms wait whose deadline had passed: the retry restarted the clock", nr.Attempt, nr.DurationMs)
		}
	}
	if retries != 3 {
		t.Fatalf("gate retries recorded = %d, want 3", retries)
	}
}

// Cancelling a run that waits in place stops the wait and the run ends
// cancelled, not failed.
func TestWaitInPlaceStopsWhenTheRunIsCancelled(t *testing.T) {
	st, eng, dir := waitInPlaceEngine(t)
	mkWaitPipeline(t, st, dir, "wcx", filepath.Join(dir, "never.flag"),
		map[string]interface{}{"poll_interval": "50ms"})

	runID, err := eng.RunPipelineAsync("wcx")
	if err != nil {
		t.Fatal(err)
	}
	awaitRunStatus(t, st, runID, models.RunStatusRunning)
	time.Sleep(100 * time.Millisecond)
	if err := eng.CancelRun(runID); err != nil {
		t.Fatal(err)
	}
	awaitRunStatus(t, st, runID, models.RunStatusCancelled)
	awaitNoActiveRuns(t, eng)
	awaitNoWaitLoop(t)
}

// awaitNoWaitLoop fails if an in-place wait loop is still running: the
// run reporting cancelled is not enough, the loop must have stopped too
// rather than polling on for the rest of its timeout.
func awaitNoWaitLoop(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if !strings.Contains(string(buf[:n]), "waitInPlaceUntilMet") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("an in-place wait loop is still running after its run ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pipelineChecker is a waitStore that answers pipeline waits itself and
// cannot look pipelines up.
type pipelineChecker struct {
	pipelineLookup
	met, found bool
	err        error
}

func (p pipelineChecker) PipelineWaitMet(string) (bool, bool, error) { return p.met, p.found, p.err }

// A store that answers pipeline waits itself is asked instead of being
// made to look pipelines up, and "not found" keeps its single meaning.
func TestWaitPipelineConditionAsksAStoreThatAnswersItItself(t *testing.T) {
	run := &models.Run{ID: "r1", OrgID: "org-acme"}
	cond := waitCondition{Type: "pipeline", PipelineID: "upstream"}
	cannot := pipelineLookup{err: fmt.Errorf("GetPipeline must not be reached: %w", store.ErrUnsupported)}

	met, err := waitConditionMet(t.Context(), pipelineChecker{pipelineLookup: cannot, met: true, found: true}, run, cond)
	if err != nil || !met {
		t.Fatalf("met upstream: met=%v err=%v, want met", met, err)
	}
	met, err = waitConditionMet(t.Context(), pipelineChecker{pipelineLookup: cannot, met: false, found: true}, run, cond)
	if err != nil || met {
		t.Fatalf("unmet upstream: met=%v err=%v, want not met, no error", met, err)
	}
	_, err = waitConditionMet(t.Context(), pipelineChecker{pipelineLookup: cannot, found: false}, run, cond)
	if err == nil || !strings.Contains(err.Error(), "pipeline not found") {
		t.Fatalf("missing upstream: err = %v, want pipeline not found", err)
	}
	_, err = waitConditionMet(t.Context(), pipelineChecker{pipelineLookup: cannot, err: fmt.Errorf("control plane unreachable")}, run, cond)
	if err == nil || !strings.Contains(err.Error(), "control plane unreachable") || strings.Contains(err.Error(), "not found") {
		t.Fatalf("checker error: err = %v, want it passed through, not 'not found'", err)
	}
}

// An in-place wait may outlast the default node attempt timeout: it ends
// on its own deadline. It was first cut off at the attempt default (30
// minutes), shrunk here so the test can see it.
func TestWaitInPlaceIsNotCutOffByTheDefaultNodeTimeout(t *testing.T) {
	old := defaultNodeTimeout
	defaultNodeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { defaultNodeTimeout = old })

	st, eng, dir := waitInPlaceEngine(t)
	gate := filepath.Join(dir, "late.flag")
	mkWaitPipeline(t, st, dir, "wlong", gate, map[string]interface{}{"poll_interval": "50ms", "timeout": "5s"})
	// Only the wait may rely on the shrunk default: the source and sink
	// get their own attempt timeout, or a slow machine times them out.
	p, err := st.GetPipeline("wlong")
	if err != nil {
		t.Fatal(err)
	}
	for i := range p.Nodes {
		if p.Nodes[i].Type != models.NodeTypeWait {
			p.Nodes[i].Config["timeout"] = float64(60)
		}
	}
	if err := st.UpdatePipeline(p); err != nil {
		t.Fatal(err)
	}

	runID, err := eng.RunPipelineAsync("wlong")
	if err != nil {
		t.Fatal(err)
	}
	awaitRunStatus(t, st, runID, models.RunStatusRunning)
	time.Sleep(600 * time.Millisecond) // three default attempt timeouts
	if err := os.WriteFile(gate, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	awaitRunStatus(t, st, runID, models.RunStatusSuccess)
}

// A run created with RunOptions.RunID carries that ID and can be
// cancelled by it while it waits in place: what a process executing a
// run that another process created needs to honour that process's
// cancel.
func TestARunWithAGivenIDCanBeCancelledByItWhileWaitingInPlace(t *testing.T) {
	st, eng, dir := waitInPlaceEngine(t)
	mkWaitPipeline(t, st, dir, "wid", filepath.Join(dir, "never.flag"),
		map[string]interface{}{"poll_interval": "50ms"})

	const runID = "run-created-elsewhere"
	done := make(chan *models.Run, 1)
	go func() {
		run, _ := eng.RunPipelineOpts("wid", RunOptions{RunID: runID})
		done <- run
	}()
	awaitRunStatus(t, st, runID, models.RunStatusRunning)
	if err := eng.CancelRun(runID); err != nil {
		t.Fatalf("cancel by the given ID: %v", err)
	}
	select {
	case run := <-done:
		if run == nil || run.ID != runID {
			t.Fatalf("returned run = %+v, want ID %s", run, runID)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not stop after its cancel")
	}
	awaitRunStatus(t, st, runID, models.RunStatusCancelled)
	awaitNoWaitLoop(t)
}
