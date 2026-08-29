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
	if after := runtime.NumGoroutine(); after > before+20 {
		t.Fatalf("goroutines %d -> %d; a thousand parks must not cost a thousand goroutines", before, after)
	}
}
