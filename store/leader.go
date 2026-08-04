package store

import "context"

// LeaderElector coordinates which of possibly-many running instances is
// currently allowed to dispatch scheduled work (Tnsor-Labs/brokoli#10).
//
// Exactly one concrete implementation exists per backend:
//   - PostgresLeaderElector (postgres_leader.go) runs a real lease/fencing
//     election over a dedicated `scheduler_leader` table, for multi-instance
//     Postgres deployments.
//   - NoopLeaderElector (below) is used for SQLite, which is documented as
//     single-instance-only — see cmd/serve.go's startup warning. A single
//     process is trivially "the leader," so there is nothing to coordinate.
//
// engine.Scheduler holds one of these and consults IsLeader() before firing
// any scheduled dispatch (cron ticks and missed-run catch-up); it never
// distinguishes which concrete implementation it was given.
type LeaderElector interface {
	// Acquire performs one synchronous election attempt (try to become
	// leader if not already, or renew the existing lease if already
	// leader) and returns once it completes. Callers use this to block
	// startup on the outcome of the first election round before deciding
	// whether to run missed-run catch-up, rather than racing a background
	// goroutine's first tick.
	//
	// A non-nil error indicates a genuine failure to reach the coordination
	// backend (e.g. a database error) — losing an election to another
	// instance that currently holds the lease is not an error, it just
	// leaves IsLeader() false.
	Acquire(ctx context.Context) error

	// Run starts the background renew/re-acquire loop and blocks until ctx
	// is cancelled, at which point it releases leadership (if held) and
	// returns. Callers run this in its own goroutine, typically after an
	// initial Acquire call.
	Run(ctx context.Context)

	// IsLeader reports whether this instance currently believes it holds
	// leadership. Safe for concurrent use; called on every scheduler tick.
	IsLeader() bool

	// FencingGeneration returns the fencing generation for this instance's
	// *current* leadership tenure — 0 whenever IsLeader() is false, whether
	// because leadership was never held, was voluntarily released, or was
	// lost to another instance. It never reports a stale value left over
	// from a tenure that has already ended, so callers never need to check
	// IsLeader() and FencingGeneration() together to avoid misreading a
	// leftover number as still valid. Downstream consumers can use a
	// nonzero generation to reject writes from a since-superseded leader,
	// the same pattern models.ExecutionAttempt.FencingGeneration already
	// establishes for execution-attempt claims (Tnsor-Labs/brokoli#7).
	FencingGeneration() int64

	// Acquisitions, Releases, and ElectionFailures are cumulative counters
	// for observability (Tnsor-Labs/brokoli#10 point 6) — surfaced via
	// engine.Scheduler's passthrough methods and rendered by
	// api.PrometheusHandler, mirroring how engine.Engine already exposes
	// RunsTotal/RunsSucceeded/RunsFailed as plain counters for that same
	// handler to read at scrape time.
	Acquisitions() int64
	Releases() int64
	ElectionFailures() int64
}

// NoopLeaderElector is the LeaderElector for single-instance deployments
// (SQLite, or a lone Postgres instance that doesn't need coordination). It
// is always leader and never contacts a database — every method is a
// constant-time no-op. This is what preserves existing single-binary
// behavior: engine.NewScheduler defaults to this when no elector is given.
type NoopLeaderElector struct{}

// NewNoopLeaderElector returns a LeaderElector that is always the leader.
func NewNoopLeaderElector() *NoopLeaderElector { return &NoopLeaderElector{} }

func (*NoopLeaderElector) Acquire(context.Context) error { return nil }
func (*NoopLeaderElector) Run(ctx context.Context)       { <-ctx.Done() }
func (*NoopLeaderElector) IsLeader() bool                { return true }

// FencingGeneration is always 0: with a single implicit leader there is
// nothing to fence out, so no consumer should ever need to compare
// generations against this elector.
func (*NoopLeaderElector) FencingGeneration() int64 { return 0 }
func (*NoopLeaderElector) Acquisitions() int64      { return 0 }
func (*NoopLeaderElector) Releases() int64          { return 0 }
func (*NoopLeaderElector) ElectionFailures() int64  { return 0 }
