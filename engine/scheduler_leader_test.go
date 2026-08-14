package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// fakeLeaderElector is a minimal in-memory store.LeaderElector for testing
// Scheduler's leadership gate (Tnsor-Labs/brokoli#10) without any database
// at all — exactly the "gate is just a boolean check" unit-testability the
// task called out explicitly. leader is exported so tests can flip it
// directly between calls.
type fakeLeaderElector struct {
	leader     bool
	generation int64
}

func (f *fakeLeaderElector) Acquire(context.Context) error { return nil }
func (f *fakeLeaderElector) Run(ctx context.Context)       { <-ctx.Done() }
func (f *fakeLeaderElector) IsLeader() bool                { return f.leader }
func (f *fakeLeaderElector) FencingGeneration() int64      { return f.generation }
func (f *fakeLeaderElector) Acquisitions() int64           { return 0 }
func (f *fakeLeaderElector) Releases() int64               { return 0 }
func (f *fakeLeaderElector) ElectionFailures() int64       { return 0 }

// newLeaderTestStore is a SQLite-backed store for the leadership-gate
// tests — plain in-process SQLite, no Postgres involved, matching the
// task's expectation that this part of the test suite needs no live
// database coordination at all.
func newLeaderTestStore(t *testing.T) store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "scheduler-leader.db")
	s, err := store.NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// blockedPipeline builds a pipeline whose RunPipeline call synchronously
// creates a "blocked" run (via an unsatisfiable gate dependency on a
// nonexistent upstream pipeline) and returns immediately, without
// executing any real node. This is the same trick
// TestRunPipeline_BlockedByUnsatisfiedDep (engine/engine_deps_test.go)
// uses — it makes RunPipeline fast, deterministic, and free of filesystem/
// executor dependencies, while still exercising the real dispatch path
// these tests are gating.
func blockedPipeline(id string) *models.Pipeline {
	now := time.Now().UTC()
	return &models.Pipeline{
		ID:   id,
		Name: "Blocked Test Pipeline " + id,
		Nodes: []models.Node{
			{ID: "src", Type: models.NodeTypeSourceFile, Name: "csv", Config: map[string]interface{}{"path": "/tmp/nonexistent.csv"}},
		},
		Edges:           []models.Edge{},
		DependencyRules: []models.DependencyRule{{PipelineID: "nonexistent-upstream", State: models.DepStateSucceeded, Mode: models.DepModeGate}},
		CreatedAt:       now, UpdatedAt: now, Enabled: true,
	}
}

// TestSchedulerRegisterSkipsDispatchWhenNotLeader is the core regression
// test for point 3 of Tnsor-Labs/brokoli#10: the Register() cron closure
// must not call engine.RunPipeline at all when this instance isn't leader.
// It fires the registered cron job directly (bypassing real cron timing)
// via the same cron.Job interface the cron library itself calls, so the
// test is deterministic rather than waiting on a real schedule tick.
func TestSchedulerRegisterSkipsDispatchWhenNotLeader(t *testing.T) {
	s := newLeaderTestStore(t)
	p := blockedPipeline("gated-pipeline")
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(s))
	fake := &fakeLeaderElector{leader: false}
	sched := NewScheduler(eng, s, fake)

	if err := sched.Register(p.ID, p.Name, "0 0 1 1 *", ""); err != nil { // schedule irrelevant, fired manually below
		t.Fatalf("Register: %v", err)
	}

	fireScheduledJob(t, sched, p.ID)

	runs, err := s.ListRunsByPipeline(p.ID, 10)
	if err != nil {
		t.Fatalf("ListRunsByPipeline: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs while not leader, got %d", len(runs))
	}

	// Flip to leader and fire again: now RunPipeline must be called.
	fake.leader = true
	fireScheduledJob(t, sched, p.ID)

	runs, err = s.ListRunsByPipeline(p.ID, 10)
	if err != nil {
		t.Fatalf("ListRunsByPipeline: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected 1 run after becoming leader, got %d", len(runs))
	}
	if runs[0].Status != models.RunStatusBlocked {
		t.Fatalf("expected the dispatched run to reach blocked status (proving RunPipeline actually ran), got %s", runs[0].Status)
	}
}

// fireScheduledJob triggers pipelineID's registered cron entry synchronously,
// the same way the cron library's own ticker would — via the cron.Job
// interface's Run() method — without waiting on real wall-clock scheduling.
func fireScheduledJob(t *testing.T, sched *Scheduler, pipelineID string) {
	t.Helper()
	sched.mu.Lock()
	entryID, ok := sched.entries[pipelineID]
	sched.mu.Unlock()
	if !ok {
		t.Fatalf("no cron entry registered for pipeline %s", pipelineID)
	}
	sched.cron.Entry(entryID).Job.Run()
}

// TestSchedulerCatchUpMissedRunsSkipsWhenNotLeader is the second regression
// test for point 3: catchUpMissedRuns must not dispatch a missed run on a
// standby.
func TestSchedulerCatchUpMissedRunsSkipsWhenNotLeader(t *testing.T) {
	s := newLeaderTestStore(t)
	p := blockedPipeline("catchup-pipeline")
	p.Schedule = "* * * * *" // every minute, so "missed since last run" is trivially true below
	if err := s.CreatePipeline(p); err != nil {
		t.Fatalf("CreatePipeline: %v", err)
	}

	// Seed a prior run 2 hours ago, so catch-up sees a missed fire well in
	// the past but within the 24h catch-up window.
	lastRun := time.Now().UTC().Add(-2 * time.Hour)
	if err := s.CreateRun(&models.Run{
		ID: "seed-run", PipelineID: p.ID, Status: models.RunStatusSuccess, StartedAt: &lastRun,
	}); err != nil {
		t.Fatalf("seed CreateRun: %v", err)
	}

	eng := drainEngineOnCleanup(t, NewEngine(s))
	fake := &fakeLeaderElector{leader: false}
	sched := NewScheduler(eng, s, fake)

	sched.catchUpMissedRuns([]models.Pipeline{*p})

	runs, err := s.ListRunsByPipeline(p.ID, 10)
	if err != nil {
		t.Fatalf("ListRunsByPipeline: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("expected only the seeded run while not leader (no catch-up dispatch), got %d runs", len(runs))
	}

	// Flip to leader: catch-up should now dispatch (asynchronously — see
	// catchUpMissedRuns' `go func(){ RunPipeline }()` — so poll briefly).
	fake.leader = true
	sched.catchUpMissedRuns([]models.Pipeline{*p})

	deadline := time.Now().Add(2 * time.Second)
	for {
		runs, err = s.ListRunsByPipeline(p.ID, 10)
		if err != nil {
			t.Fatalf("ListRunsByPipeline: %v", err)
		}
		if len(runs) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected a catch-up run to be dispatched after becoming leader, still only %d runs after timeout", len(runs))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSchedulerReclaimSweepReclaimsExpiredLeaseWhenLeader is the direct
// regression test for the gap found live: a worker OOMKilled mid-node
// execution left a run's execution attempt with an expired lease,
// permanently orphaned — every other already-running process's own
// one-shot startup recovery pass had already executed and correctly
// deferred it (the lease still looked live at that exact moment, per
// reconcileExecutionAttempts' own documented safety rule), and nothing
// ever checked again since RecoverNonTerminalRuns only ever ran once, at
// process boot. Proves the scheduler leader's periodic sweep
// (runReclaimSweep) picks up exactly that case on a later pass.
func TestSchedulerReclaimSweepReclaimsExpiredLeaseWhenLeader(t *testing.T) {
	s := newLeaderTestStore(t).(*store.SQLiteStore)
	eng := drainEngineOnCleanup(t, NewEngine(s))
	seedRecoveryPipeline(t, s, "pipe-sweep-reclaim")
	run := seedOrphanedRun(t, s, "pipe-sweep-reclaim", "run-sweep-reclaim", models.RunStatusRunning)
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})
	createTestAttempt(t, s, run.ID, "", 0)
	if _, ok, err := s.ClaimAttempt(run.ID, "", "", 0, "dead-process", -time.Second); err != nil || !ok {
		t.Fatalf("ClaimAttempt: ok=%v err=%v", ok, err)
	}

	fake := &fakeLeaderElector{leader: true}
	sched := NewScheduler(eng, s, fake)
	prevInterval := reclaimSweepInterval
	reclaimSweepInterval = 20 * time.Millisecond
	defer func() { reclaimSweepInterval = prevInterval }()

	if err := sched.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for {
		attempt, err := s.GetExecutionAttempt(run.ID, "", "", 0)
		if err != nil {
			t.Fatalf("GetExecutionAttempt: %v", err)
		}
		if attempt.Status == models.AttemptStatusFailed {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected the periodic sweep to reclaim the expired lease, attempt status still %s after timeout", attempt.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSchedulerReclaimSweepSkipsWhenNotLeader proves the periodic sweep is
// gated on leadership the same way catchUpMissedRuns already is — only
// one instance should ever be reclaiming expired leases at a time, and a
// standby must leave them exactly as found for the real leader (or its
// own promotion) to handle.
func TestSchedulerReclaimSweepSkipsWhenNotLeader(t *testing.T) {
	s := newLeaderTestStore(t).(*store.SQLiteStore)
	eng := drainEngineOnCleanup(t, NewEngine(s))
	seedRecoveryPipeline(t, s, "pipe-sweep-skip")
	run := seedOrphanedRun(t, s, "pipe-sweep-skip", "run-sweep-skip", models.RunStatusRunning)
	appendRecoveryEvent(t, s, &models.RunEvent{
		RunID: run.ID, EventType: models.RunEventCreated,
		Payload: models.RunEventPayload{Status: models.RunStatusRunning, PipelineID: run.PipelineID, StartedAt: run.StartedAt},
	})
	createTestAttempt(t, s, run.ID, "", 0)
	gen, ok, err := s.ClaimAttempt(run.ID, "", "", 0, "dead-process", -time.Second)
	if err != nil || !ok {
		t.Fatalf("ClaimAttempt: ok=%v err=%v", ok, err)
	}
	_ = gen

	fake := &fakeLeaderElector{leader: false}
	sched := NewScheduler(eng, s, fake)
	prevInterval := reclaimSweepInterval
	reclaimSweepInterval = 20 * time.Millisecond
	defer func() { reclaimSweepInterval = prevInterval }()

	if err := sched.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer sched.Stop()

	time.Sleep(200 * time.Millisecond) // several sweep ticks' worth
	attempt, err := s.GetExecutionAttempt(run.ID, "", "", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if attempt.Status != models.AttemptStatusClaimed {
		t.Fatalf("attempt status = %s, want unchanged (claimed) — a non-leader must not reclaim", attempt.Status)
	}
}
