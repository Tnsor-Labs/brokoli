package engine

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// storeWithoutAttempts embeds the store.Store INTERFACE (not a concrete
// type), so only store.Store's own declared methods are promoted — a type
// assertion for store.ExecutionAttemptStore against a storeWithoutAttempts
// value fails even though the underlying concrete store fully implements
// it. This stands in for an EE APIStore (or any store.Store implementation)
// that has not yet adopted store.ExecutionAttemptStore, proving the
// Tnsor-Labs/brokoli#90 M3 claim/lease wiring in engine/runner.go is
// genuinely optional-capability and does not regress node execution when
// absent.
type storeWithoutAttempts struct {
	store.Store
}

const attemptWiringInstanceID = "test-instance"

// newAttemptWiringTestPipeline mirrors runRoutingPipeline
// (conditional_routing_test.go): NewRunner + Execute() directly, bypassing
// Engine.RunPipeline's pre-flight "must have a real source node type"
// validation, which routingTestNode intentionally isn't — the same reason
// that existing helper uses this lower-level path instead of RunPipeline.
func newAttemptWiringTestPipeline(t *testing.T, config map[string]interface{}, executor extensions.NodeExecutor) (*models.Run, *store.SQLiteStore) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.NewSQLiteStore(filepath.Join(dir, "attempts.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	now := time.Now().UTC()
	pipeline := &models.Pipeline{
		ID: "attempt-wiring", Name: "Attempt Wiring", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "n", Type: routingTestNode, Name: "N", Config: config},
		},
	}
	if err := s.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	runner := NewRunner(s, nil, pipeline, nil, nil, []extensions.NodeExecutor{executor}, nil, attemptWiringInstanceID)
	run, err := runner.Execute()
	if err != nil {
		t.Fatalf("execute pipeline: %v", err)
	}
	return run, s
}

// TestExecuteNode_ClaimsAndCompletesExecutionAttempt proves the happy path
// of Tnsor-Labs/brokoli#90 M3's claim/lease wiring end to end: a successful
// node attempt produces a durable execution_attempts row, claimed by the
// Runner's instanceID, and settled to AttemptStatusCompleted by the time
// the run finishes.
func TestExecuteNode_ClaimsAndCompletesExecutionAttempt(t *testing.T) {
	run, s := newAttemptWiringTestPipeline(t, map[string]interface{}{"source": true}, newRoutingTestExecutor())
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}

	attempt, err := s.GetExecutionAttempt(run.ID, "n", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt: %v", err)
	}
	if attempt.Status != models.AttemptStatusCompleted {
		t.Errorf("attempt status = %s, want completed", attempt.Status)
	}
	if attempt.ClaimedBy != attemptWiringInstanceID {
		t.Errorf("attempt claimed_by = %q, want %q", attempt.ClaimedBy, attemptWiringInstanceID)
	}
	if attempt.FencingGeneration < 1 {
		t.Errorf("attempt fencing_generation = %d, want >= 1 (bumped by ClaimAttempt)", attempt.FencingGeneration)
	}
}

// TestExecuteNode_FailedThenRetried_TwoExecutionAttempts proves each retry
// gets its own independent execution_attempts row, keyed by attempt number
// exactly like models.NodeRun.Attempt already is — the failed first try
// settles to AttemptStatusFailed rather than being overwritten or left
// dangling once the retry claims attempt 1.
func TestExecuteNode_FailedThenRetried_TwoExecutionAttempts(t *testing.T) {
	config := map[string]interface{}{
		"source":      true,
		"fail_once":   true,
		"max_retries": float64(1),
		"retry_delay": float64(1),
	}
	run, s := newAttemptWiringTestPipeline(t, config, newRoutingTestExecutor())
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success (after one retry)", run.Status)
	}

	failed, err := s.GetExecutionAttempt(run.ID, "n", 0)
	if err != nil {
		t.Fatalf("GetExecutionAttempt(attempt 0): %v", err)
	}
	if failed.Status != models.AttemptStatusFailed {
		t.Errorf("attempt 0 status = %s, want failed", failed.Status)
	}

	completed, err := s.GetExecutionAttempt(run.ID, "n", 1)
	if err != nil {
		t.Fatalf("GetExecutionAttempt(attempt 1): %v", err)
	}
	if completed.Status != models.AttemptStatusCompleted {
		t.Errorf("attempt 1 status = %s, want completed", completed.Status)
	}
	// attempt is part of (run_id, node_id, attempt)'s composite key, so
	// each attempt number gets its own row with its own independent
	// fencing_generation sequence starting at 1 — not a shared counter
	// across retries. What must hold is that attempt 1's row exists at
	// all, is distinct from attempt 0's, and was itself validly claimed
	// (generation >= 1), which the two GetExecutionAttempt calls above
	// already establish by succeeding on two different attempt numbers.
	if completed.FencingGeneration < 1 {
		t.Errorf("attempt 1 fencing_generation = %d, want >= 1 (bumped by its own ClaimAttempt)", completed.FencingGeneration)
	}
}

// TestExecuteNode_StoreWithoutExecutionAttemptStore_Unaffected proves node
// execution is byte-for-byte unaffected when the store doesn't implement
// store.ExecutionAttemptStore — the state every EE APIStore is in today
// until it adopts the capability. No execution_attempts rows are written
// (there is nowhere to write them), and the run still succeeds through the
// pre-#90-M3 CreateNodeRun/UpdateNodeRun path.
func TestExecuteNode_StoreWithoutExecutionAttemptStore_Unaffected(t *testing.T) {
	dir := t.TempDir()
	real, err := store.NewSQLiteStore(filepath.Join(dir, "attempts.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { real.Close() })

	now := time.Now().UTC()
	pipeline := &models.Pipeline{
		ID: "attempt-wiring", Name: "Attempt Wiring", Enabled: true,
		CreatedAt: now, UpdatedAt: now,
		Nodes: []models.Node{
			{ID: "n", Type: routingTestNode, Name: "N", Config: map[string]interface{}{"source": true}},
		},
	}
	if err := real.CreatePipeline(pipeline); err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	wrapped := storeWithoutAttempts{Store: real}
	runner := NewRunner(wrapped, nil, pipeline, nil, nil, []extensions.NodeExecutor{newRoutingTestExecutor()}, nil, attemptWiringInstanceID)
	run, err := runner.Execute()
	if err != nil {
		t.Fatalf("execute pipeline: %v", err)
	}
	if run.Status != models.RunStatusSuccess {
		t.Fatalf("run status = %s, want success", run.Status)
	}

	nodeRuns, err := real.ListNodeRunsByRun(run.ID)
	if err != nil {
		t.Fatalf("ListNodeRunsByRun: %v", err)
	}
	if len(nodeRuns) != 1 || nodeRuns[0].Status != models.RunStatusSuccess {
		t.Fatalf("node_runs = %+v, want one successful row (plain CreateNodeRun path)", nodeRuns)
	}

	if _, err := real.GetExecutionAttempt(run.ID, "n", 0); err == nil {
		t.Error("GetExecutionAttempt found a row, want none — nothing should be written when the store lacks ExecutionAttemptStore")
	}
}
