package models

import "time"

// AttemptStatus is the lifecycle state of a durable execution attempt row
// (store.ExecutionAttemptStore). Unlike RunStatus, which describes a whole
// run or node_run, AttemptStatus describes the claim/lease state of the
// outbox/intent record that gates dispatch of one (run, node, attempt) unit
// of work — see Tnsor-Labs/brokoli#7.
type AttemptStatus string

const (
	// AttemptStatusQueued is the initial state: the durable intent to
	// dispatch this attempt has been recorded, but no worker has claimed it
	// yet.
	AttemptStatusQueued AttemptStatus = "queued"

	// AttemptStatusClaimed means a worker atomically claimed the attempt
	// (via ClaimAttempt) and holds a live lease, but has not yet
	// acknowledged starting work.
	AttemptStatusClaimed AttemptStatus = "claimed"

	// AttemptStatusStarted means the claiming worker acknowledged (via
	// AckAttempt) that it has begun executing the attempt.
	AttemptStatusStarted AttemptStatus = "started"

	// AttemptStatusCompleted is terminal: the attempt finished successfully.
	AttemptStatusCompleted AttemptStatus = "completed"

	// AttemptStatusFailed is terminal: the attempt finished with an error.
	AttemptStatusFailed AttemptStatus = "failed"
)

// ExecutionAttempt is the durable outbox/intent + claim/lease record for one
// (run_id, node_id, instance_key, attempt) unit of dispatchable work. It is
// the crash-safe side-effect boundary described by Tnsor-Labs/brokoli#7: no
// external dispatch (enqueue, plugin/process spawn) is considered committed
// until its ExecutionAttempt row exists, and no worker may act on an
// attempt without first winning ClaimAttempt's compare-and-swap.
//
// For pipeline-level (not yet node-level) dispatch — the outbox record
// engine.Engine.RunPipelineAsync writes atomically alongside the run-created
// event — NodeID and InstanceKey are empty and Attempt is 0, mirroring how
// models.RunEvent already leaves NodeID empty for run-scoped events.
// Node-level attempt records (one per retry, keyed the same way
// models.NodeRun.Attempt already is, InstanceKey empty) are wired in
// engine/runner.go's per-node execution loop (Tnsor-Labs/brokoli#90 M3).
//
// InstanceKey (added for ADR-017, worker protocol v2) is the physical-
// instance identity within one node — a pagination page or a dynamic-
// expansion item, matching models.PhysicalPlan's InstanceKeyTemplate and
// engine.deriveInstanceKey's "idx:N" scheme, empty for whole-node/pipeline
// claims. Attempt is scoped WITHIN (node_id, instance_key): an instance's
// own retry counter, independent of any other instance of the same node —
// retrying one failed page or expansion item does not touch its siblings'
// rows or bump their own attempt numbers. Adding this widened an existing
// table's primary key (see the SQLite/Postgres migration comments at the
// table's creation site) rather than introducing a parallel table, per
// ADR-017's stated default lean toward extending this existing contract.
type ExecutionAttempt struct {
	RunID  string `json:"run_id"`
	NodeID string `json:"node_id,omitempty"`
	// InstanceKey identifies which physical instance of NodeID this
	// attempt is for — empty for a whole-node (or whole-pipeline, when
	// NodeID is also empty) claim. See the type doc comment.
	InstanceKey string `json:"instance_key,omitempty"`
	// Attempt mirrors models.NodeRun.Attempt: 0 for the first try, 1+ for
	// retries — scoped within (node_id, instance_key), not shared across
	// every instance of a node. Always 0 for pipeline-level (NodeID-empty)
	// outbox records.
	Attempt int `json:"attempt"`

	Status AttemptStatus `json:"status"`

	// ClaimedBy identifies the worker/process holding the current lease
	// (empty until first claimed).
	ClaimedBy string `json:"claimed_by,omitempty"`

	// LeaseExpiresAt is when the current claim's lease lapses, after which
	// ClaimAttempt permits a different worker to reclaim it (fencing
	// generation is bumped on every successful claim so a worker that wakes
	// up after losing its lease can detect it via a stale generation).
	LeaseExpiresAt *time.Time `json:"lease_expires_at,omitempty"`

	// FencingGeneration increments on every successful ClaimAttempt. Callers
	// present the generation they were granted back to RenewLease/
	// AckAttempt/CompleteAttempt/FailAttempt; a mismatch means the caller's
	// lease was reclaimed by someone else and its work must not be trusted.
	FencingGeneration int64 `json:"fencing_generation"`

	// IdempotencyKey lets a re-delivered dispatch (e.g. a JobQueue redelivery
	// or a plugin runner retry) recognize it is re-processing the same
	// logical attempt rather than starting a new one.
	IdempotencyKey string `json:"idempotency_key,omitempty"`

	// Error carries the failure reason when Status is AttemptStatusFailed.
	Error string `json:"error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
