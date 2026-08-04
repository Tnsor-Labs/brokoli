package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Design note (Tnsor-Labs/brokoli#10): why a leased row, not
// pg_advisory_lock.
//
// The issue offers two options: a session-scoped pg_try_advisory_lock, or a
// dedicated single-row table with a monotonic fencing generation. This
// implements the latter.
//
// PostgresStore opens its connection via database/sql's sql.Open("pgx", uri)
// (store/postgres.go) — a pooled *sql.DB with no dedicated-connection
// lifecycle management anywhere else in this codebase. Session-scoped
// advisory locks are tied to the physical backend connection that took
// them: releasing correctly requires pinning one *sql.Conn out of the pool
// for the entire time leadership is held (via DB.Conn(ctx)) and very
// carefully ensuring that exact connection — never a different one from the
// pool — is used for both the lock and unlock calls, that it is never
// silently recycled or closed by pool health-checking, and that a Read
// timeout/deadline/context-cancellation doesn't drop the connection out
// from under the lock without our knowledge (in which case Postgres itself
// releases the advisory lock the moment the backend connection closes, with
// no application-visible error until the next call fails). Getting that
// lifecycle right is exactly the failure mode the issue calls out as "the
// trickiest part."
//
// A leased row sidesteps all of that: every operation is a single
// self-contained, idempotent SQL statement (INSERT/UPDATE ... WHERE ...
// RETURNING) that works correctly through the connection pool exactly like
// every other store method in this package (see ClaimAttempt/RenewLease in
// store/postgres.go, which this mirrors almost exactly for
// execution_attempts leasing). Leadership is expressed as data — a
// (holder, fencing_generation, lease_expires_at) row — rather than session
// state tied to a specific TCP connection, so it survives connection
// recycling, load balancer/pgbouncer transaction pooling, and driver
// retries without any special handling. The tradeoff is a real but bounded
// failover latency window (up to leaseDuration + renewInterval, see
// DefaultLeaseDuration/DefaultRenewInterval below) instead of near-instant
// release-on-disconnect; given brokoli's cron-scheduled dispatch (minute
// granularity at best) rather than sub-second coordination, that tradeoff
// is the right one here.

// DefaultLeaseDuration is how long a held leadership lease remains valid
// without renewal. If the leader dies or is partitioned, another instance
// can take over once this elapses.
const DefaultLeaseDuration = 15 * time.Second

// DefaultRenewInterval is how often the leader renews its lease (and how
// often standbys retry acquisition). Set well below DefaultLeaseDuration so
// a single missed tick (GC pause, transient network blip) doesn't cost
// leadership.
const DefaultRenewInterval = 5 * time.Second

// schedulerLeaderRowID is the fixed primary key of the single row in
// scheduler_leader — see PostgresStore.migrate's inline DDL. There is
// exactly one leadership lease per cluster, so the table only ever has one
// row.
const schedulerLeaderRowID = "scheduler"

// PostgresLeaderElector implements LeaderElector using a leased-row +
// fencing-generation pattern over the scheduler_leader table (see the
// design note above). Safe for concurrent use — IsLeader/FencingGeneration
// may be called from any goroutine while Run's election loop is active.
type PostgresLeaderElector struct {
	db            *sql.DB
	holderID      string
	leaseDuration time.Duration
	renewInterval time.Duration

	// OnAcquire/OnRelease/OnLoss are optional observability hooks invoked
	// synchronously from the election loop. cmd/serve.go does not wire
	// these today — every acquire/release/loss is already unconditionally
	// logged via common.SLog() inside attempt()/release() below, which is
	// sufficient for this PR's structured-log-lines requirement — but they
	// exist as an extension point for a future external metrics/alerting
	// integration without needing to touch the election logic itself. Left
	// nil (the default), they are simply skipped. The cumulative counters
	// below (acquisitions/releases/electionFailures) are the mechanism
	// api.PrometheusHandler actually pulls from today, via
	// engine.Scheduler's passthrough methods.
	OnAcquire func(generation int64)
	OnRelease func(generation int64)
	OnLoss    func(generation int64)

	mu                sync.RWMutex
	isLeader          bool
	fencingGeneration int64
	acquisitions      atomic.Int64
	releases          atomic.Int64
	electionFailures  atomic.Int64
}

// NewPostgresLeaderElector creates an elector that will campaign for
// leadership over db using holderID to identify this instance in the
// scheduler_leader row. holderID should be stable for the process's
// lifetime and unique per instance — see DefaultHolderID.
func NewPostgresLeaderElector(db *sql.DB, holderID string, leaseDuration, renewInterval time.Duration) *PostgresLeaderElector {
	if leaseDuration <= 0 {
		leaseDuration = DefaultLeaseDuration
	}
	if renewInterval <= 0 {
		renewInterval = DefaultRenewInterval
	}
	return &PostgresLeaderElector{
		db:            db,
		holderID:      holderID,
		leaseDuration: leaseDuration,
		renewInterval: renewInterval,
	}
}

// DefaultHolderID builds a holder identity from hostname, PID, and a short
// random suffix — unique enough to distinguish instances (including two
// replicas on the same host, or PID reuse across restarts) in logs and in
// the scheduler_leader.holder column.
func DefaultHolderID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	return fmt.Sprintf("%s:%d:%s", host, os.Getpid(), common.NewID()[:8])
}

// HolderID returns this elector's identity string.
func (e *PostgresLeaderElector) HolderID() string { return e.holderID }

// Acquire performs one synchronous election attempt: renew if currently
// leader, otherwise try to take over an expired (or never-held) lease.
func (e *PostgresLeaderElector) Acquire(ctx context.Context) error {
	return e.attempt(ctx)
}

// Run starts the periodic election loop and blocks until ctx is cancelled.
// On cancellation, it releases leadership (best-effort, if held) before
// returning — a graceful shutdown should not leave a lease other instances
// must wait out.
func (e *PostgresLeaderElector) Run(ctx context.Context) {
	ticker := time.NewTicker(e.renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := e.release(releaseCtx); err != nil {
				common.SLog().Warn("leader: release on shutdown failed", common.HolderAttr(e.holderID), "error", err)
			}
			return
		case <-ticker.C:
			if err := e.attempt(ctx); err != nil {
				common.SLog().Warn("leader: election attempt failed", common.HolderAttr(e.holderID), "error", err)
			}
		}
	}
}

// attempt runs one renew-or-acquire cycle against Postgres.
func (e *PostgresLeaderElector) attempt(ctx context.Context) error {
	if e.IsLeader() {
		ok, gen, err := e.renew(ctx)
		if err != nil {
			e.electionFailures.Add(1)
			return err
		}
		if ok {
			e.mu.Lock()
			e.fencingGeneration = gen
			e.mu.Unlock()
			return nil
		}
		// Renewal failed with no error: our lease lapsed and someone else
		// already claimed it. We have been fenced out — this is the
		// failover event from the *outgoing* leader's perspective.
		//
		// fencingGeneration is reset to 0 here (not left at its last-held
		// value): FencingGeneration()'s contract is "the generation while
		// currently leader, 0 otherwise," so a monitor reading it after
		// IsLeader() has already gone false never sees a stale
		// still-looks-valid number for a tenure that has actually ended.
		e.mu.Lock()
		e.isLeader = false
		lostGen := e.fencingGeneration
		e.fencingGeneration = 0
		e.mu.Unlock()
		common.SLog().Warn("leader: lost leadership — lease not renewed in time, reclaimed by another instance", common.HolderAttr(e.holderID), common.GenerationAttr(lostGen))
		if e.OnLoss != nil {
			e.OnLoss(lostGen)
		}
		return nil
	}

	gen, acquired, err := e.tryAcquire(ctx)
	if err != nil {
		e.electionFailures.Add(1)
		return err
	}
	if !acquired {
		return nil
	}
	e.mu.Lock()
	e.isLeader = true
	e.fencingGeneration = gen
	e.mu.Unlock()
	e.acquisitions.Add(1)
	common.SLog().Info("leader: acquired leadership", common.HolderAttr(e.holderID), common.GenerationAttr(gen))
	if e.OnAcquire != nil {
		e.OnAcquire(gen)
	}
	return nil
}

// tryAcquire attempts to take over the leadership row. It only succeeds if
// the row doesn't exist yet (first-ever election) or the current lease has
// expired — a live leader's lease is never disturbed by this call, which is
// what makes it safe to call unconditionally from a standby on every tick.
func (e *PostgresLeaderElector) tryAcquire(ctx context.Context) (generation int64, acquired bool, err error) {
	now := time.Now().UTC()
	leaseExpires := now.Add(e.leaseDuration)
	row := e.db.QueryRowContext(ctx,
		`INSERT INTO scheduler_leader (id, holder, fencing_generation, lease_expires_at, acquired_at, updated_at)
		 VALUES ($1, $2, 1, $3, $4, $4)
		 ON CONFLICT (id) DO UPDATE SET
		   holder = EXCLUDED.holder,
		   fencing_generation = scheduler_leader.fencing_generation + 1,
		   lease_expires_at = EXCLUDED.lease_expires_at,
		   acquired_at = EXCLUDED.acquired_at,
		   updated_at = EXCLUDED.updated_at
		 WHERE scheduler_leader.lease_expires_at < $4
		 RETURNING fencing_generation`,
		schedulerLeaderRowID, e.holderID, leaseExpires, now,
	)
	if scanErr := row.Scan(&generation); scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, false, nil // another instance holds a still-live lease
		}
		return 0, false, fmt.Errorf("try acquire leadership: %w", scanErr)
	}
	return generation, true, nil
}

// renew extends the lease for a holder that believes it is still leader,
// verified by holder identity and fencing generation (so a leader that was
// fenced out and somehow tries to renew a stale generation cannot
// resurrect it). ok=false with no error means the lease was not renewed —
// the caller is no longer leader.
func (e *PostgresLeaderElector) renew(ctx context.Context) (ok bool, generation int64, err error) {
	e.mu.RLock()
	currentGen := e.fencingGeneration
	e.mu.RUnlock()

	now := time.Now().UTC()
	leaseExpires := now.Add(e.leaseDuration)
	result, err := e.db.ExecContext(ctx,
		`UPDATE scheduler_leader SET lease_expires_at=$1, updated_at=$2
		 WHERE id=$3 AND holder=$4 AND fencing_generation=$5`,
		leaseExpires, now, schedulerLeaderRowID, e.holderID, currentGen,
	)
	if err != nil {
		return false, currentGen, fmt.Errorf("renew leadership lease: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, currentGen, fmt.Errorf("renew leadership lease: %w", err)
	}
	return n == 1, currentGen, nil
}

// release voluntarily gives up leadership by clearing the lease
// immediately (backdating lease_expires_at), so the very next standby
// tick can acquire without waiting out the full lease duration. A no-op if
// this instance isn't currently leader.
func (e *PostgresLeaderElector) release(ctx context.Context) error {
	e.mu.RLock()
	wasLeader := e.isLeader
	currentGen := e.fencingGeneration
	e.mu.RUnlock()
	if !wasLeader {
		return nil
	}

	now := time.Now().UTC()
	result, err := e.db.ExecContext(ctx,
		`UPDATE scheduler_leader SET lease_expires_at=$1, updated_at=$2
		 WHERE id=$3 AND holder=$4 AND fencing_generation=$5`,
		now.Add(-time.Second), now, schedulerLeaderRowID, e.holderID, currentGen,
	)
	if err != nil {
		return fmt.Errorf("release leadership: %w", err)
	}
	n, _ := result.RowsAffected()

	e.mu.Lock()
	e.isLeader = false
	e.fencingGeneration = 0 // see the matching comment in attempt()'s lost-leadership branch
	e.mu.Unlock()
	if n == 1 {
		e.releases.Add(1)
		common.SLog().Info("leader: released leadership", common.HolderAttr(e.holderID), common.GenerationAttr(currentGen))
		if e.OnRelease != nil {
			e.OnRelease(currentGen)
		}
	}
	return nil
}

// Release is the exported, context-checked form of release for callers
// (e.g. tests) that want to force a voluntary release outside the Run loop.
func (e *PostgresLeaderElector) Release(ctx context.Context) error {
	return e.release(ctx)
}

func (e *PostgresLeaderElector) IsLeader() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.isLeader
}

func (e *PostgresLeaderElector) FencingGeneration() int64 {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.fencingGeneration
}

func (e *PostgresLeaderElector) Acquisitions() int64     { return e.acquisitions.Load() }
func (e *PostgresLeaderElector) Releases() int64         { return e.releases.Load() }
func (e *PostgresLeaderElector) ElectionFailures() int64 { return e.electionFailures.Load() }
