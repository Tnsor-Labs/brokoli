package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

// currentLeaderSnapshot is a small test helper returning the raw
// scheduler_leader row state, independent of any single elector's
// in-memory view — used to assert on ground truth rather than just what an
// elector believes about itself.
type currentLeaderSnapshot struct {
	Holder            string
	FencingGeneration int64
	LeaseExpiresAt    time.Time
}

func fetchLeaderSnapshot(ctx context.Context, db *sql.DB) (*currentLeaderSnapshot, error) {
	var snap currentLeaderSnapshot
	err := db.QueryRowContext(ctx,
		`SELECT holder, fencing_generation, lease_expires_at FROM scheduler_leader WHERE id=$1`,
		schedulerLeaderRowID,
	).Scan(&snap.Holder, &snap.FencingGeneration, &snap.LeaseExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

// openTestPostgres connects to a real local Postgres instance for
// PostgresLeaderElector tests (Tnsor-Labs/brokoli#10).
//
// There is no Postgres service in CI today (.github/workflows/ci.yml's
// `test` job runs `go test ./...` with no database container/service, and
// no docker-compose/testcontainers setup exists anywhere in this repo — see
// the PR description for the full audit). These tests therefore connect to
// BROKOLI_TEST_POSTGRES_URL if set, or fall back to a conventional local
// default (postgres superuser, trust/peer auth, no password — the same
// setup this change was developed and verified against), and skip cleanly
// via t.Skip if nothing is reachable within a short timeout. That means:
//   - Locally, with Postgres running, these tests exercise the real
//     acquire/release/reacquire cycle, concurrent-acquisition mutual
//     exclusion, and a simulated failover against actual SQL.
//   - In CI (or any environment without local Postgres), they skip rather
//     than fail — this is the "no live-Postgres test infra exists" case the
//     task called out explicitly; see the PR body for what that means for
//     confidence level.
func openTestPostgres(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if dsn == "" {
		dsn = "postgres://postgres@localhost:5432/brokoli_leader_test?sslmode=disable"
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("skipping live-Postgres leader election test: sql.Open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		t.Skipf("skipping live-Postgres leader election test: no reachable Postgres at %s: %v", dsn, err)
	}

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scheduler_leader (
		id TEXT PRIMARY KEY,
		holder TEXT NOT NULL DEFAULT '',
		fencing_generation BIGINT NOT NULL DEFAULT 0,
		lease_expires_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
		acquired_at TIMESTAMPTZ,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW())`); err != nil {
		db.Close()
		t.Fatalf("create scheduler_leader table: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM scheduler_leader`); err != nil {
		db.Close()
		t.Fatalf("reset scheduler_leader table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestPostgresLeaderElectorAcquireReleaseReacquire is the core lifecycle
// test the task asked for: acquire, release, and reacquire against a real
// Postgres database, asserting the fencing generation is monotonically
// increasing across the cycle (it must increment on the reacquire, since
// that's a new tenure of leadership even though it's the same holder).
func TestPostgresLeaderElectorAcquireReleaseReacquire(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()

	e := NewPostgresLeaderElector(db, "holder-1", 2*time.Second, 500*time.Millisecond)

	if err := e.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if !e.IsLeader() {
		t.Fatal("expected IsLeader() true after first Acquire")
	}
	if got := e.FencingGeneration(); got != 1 {
		t.Fatalf("FencingGeneration after first acquire = %d, want 1", got)
	}
	if got := e.Acquisitions(); got != 1 {
		t.Fatalf("Acquisitions = %d, want 1", got)
	}

	if err := e.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if e.IsLeader() {
		t.Fatal("expected IsLeader() false after Release")
	}
	if got := e.FencingGeneration(); got != 0 {
		t.Fatalf("FencingGeneration after Release = %d, want 0 (never a stale non-zero value once not leader)", got)
	}
	if got := e.Releases(); got != 1 {
		t.Fatalf("Releases = %d, want 1", got)
	}

	if err := e.Acquire(ctx); err != nil {
		t.Fatalf("Acquire (reacquire): %v", err)
	}
	if !e.IsLeader() {
		t.Fatal("expected IsLeader() true after reacquire")
	}
	if got := e.FencingGeneration(); got != 2 {
		t.Fatalf("FencingGeneration after reacquire = %d, want 2 (must strictly increase across a release/reacquire cycle)", got)
	}
	if got := e.Acquisitions(); got != 2 {
		t.Fatalf("Acquisitions = %d, want 2", got)
	}

	snap, err := fetchLeaderSnapshot(ctx, db)
	if err != nil {
		t.Fatalf("fetchLeaderSnapshot: %v", err)
	}
	if snap == nil || snap.Holder != "holder-1" || snap.FencingGeneration != 2 {
		t.Fatalf("unexpected row state: %+v", snap)
	}
}

// TestPostgresLeaderElectorRenewExtendsLeaseWithoutBumpingGeneration checks
// that a plain renewal (still-leader Acquire call) does NOT bump the
// fencing generation — only an actual change of holder should, per the
// documented contract in store/leader.go. A renewal that bumped the
// generation on every tick would defeat the point of fencing (a downstream
// consumer comparing "did the generation I was handed change" would see
// spurious churn even though the same instance has held the lease the
// whole time).
func TestPostgresLeaderElectorRenewExtendsLeaseWithoutBumpingGeneration(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()

	e := NewPostgresLeaderElector(db, "holder-1", 2*time.Second, 200*time.Millisecond)
	if err := e.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	gen1 := e.FencingGeneration()

	for i := 0; i < 3; i++ {
		if err := e.Acquire(ctx); err != nil { // renew path, since already leader
			t.Fatalf("renew attempt %d: %v", i, err)
		}
	}

	if got := e.FencingGeneration(); got != gen1 {
		t.Fatalf("FencingGeneration changed across renewals: got %d, want unchanged %d", got, gen1)
	}
	if !e.IsLeader() {
		t.Fatal("expected still leader after renewals")
	}
}

// TestPostgresLeaderElectorOnlyOneWinnerAmongConcurrentAcquire fires many
// electors at the same fresh (never-held) lease concurrently and asserts
// exactly one wins — the mutual-exclusion property the whole mechanism
// exists for. Relies on Postgres's row-level locking of the conflicting row
// during INSERT ... ON CONFLICT DO UPDATE ... WHERE to serialize the race
// (see the design note in store/postgres_leader.go).
func TestPostgresLeaderElectorOnlyOneWinnerAmongConcurrentAcquire(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()

	const n = 8
	electors := make([]*PostgresLeaderElector, n)
	for i := 0; i < n; i++ {
		electors[i] = NewPostgresLeaderElector(db, "racer-"+string(rune('a'+i)), 5*time.Second, time.Second)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for _, e := range electors {
		wg.Add(1)
		go func(e *PostgresLeaderElector) {
			defer wg.Done()
			if err := e.Acquire(ctx); err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			if e.IsLeader() {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}(e)
	}
	wg.Wait()

	if winners != 1 {
		t.Fatalf("expected exactly 1 winner among %d concurrent acquirers, got %d", n, winners)
	}
	for _, e := range electors {
		if e.IsLeader() && e.FencingGeneration() != 1 {
			t.Fatalf("sole winner's fencing generation = %d, want 1 (first-ever acquisition)", e.FencingGeneration())
		}
	}
}

// TestPostgresLeaderElectorLiveLeaseNotStolen confirms a standby's Acquire
// calls never succeed while the current leader keeps renewing in time —
// i.e. this isn't just "whoever polls last wins," a live lease is
// genuinely held.
func TestPostgresLeaderElectorLiveLeaseNotStolen(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()

	leader := NewPostgresLeaderElector(db, "leader", time.Second, 200*time.Millisecond)
	standby := NewPostgresLeaderElector(db, "standby", time.Second, 200*time.Millisecond)

	if err := leader.Acquire(ctx); err != nil {
		t.Fatalf("leader Acquire: %v", err)
	}

	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if err := leader.Acquire(ctx); err != nil { // renew
			t.Fatalf("leader renew: %v", err)
		}
		if err := standby.Acquire(ctx); err != nil {
			t.Fatalf("standby Acquire: %v", err)
		}
		if standby.IsLeader() {
			t.Fatal("standby acquired leadership while leader was still renewing its lease in time")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !leader.IsLeader() {
		t.Fatal("leader unexpectedly lost leadership despite renewing throughout")
	}
}

// TestPostgresLeaderElectorFailoverAfterLeaseExpiry simulates a leader that
// stops renewing (as if the process died) and measures how long a standby
// takes to detect the expired lease and take over.
//
// IMPORTANT CAVEAT (see PR description): this is two PostgresLeaderElector
// instances inside one Go test process sharing one Postgres connection
// pool, not two separate OS processes against a real multi-node cluster —
// it validates the SQL-level election mechanism and gives one real,
// measured latency data point, but it is not the acceptance criterion's
// "kill a leader process in a test cluster" scenario, which needs
// multi-process infrastructure this environment doesn't have.
func TestPostgresLeaderElectorFailoverAfterLeaseExpiry(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()

	leaseDuration := 500 * time.Millisecond
	renewInterval := 100 * time.Millisecond

	leader := NewPostgresLeaderElector(db, "dead-leader", leaseDuration, renewInterval)
	if err := leader.Acquire(ctx); err != nil {
		t.Fatalf("leader Acquire: %v", err)
	}
	if !leader.IsLeader() {
		t.Fatal("expected leader to hold leadership before simulated crash")
	}
	leaderGen := leader.FencingGeneration()
	// Simulate a crash: leader stops calling Acquire/Run entirely from this
	// point on — no graceful Release, exactly like a killed process leaves
	// its lease to simply expire.

	standby := NewPostgresLeaderElector(db, "standby-takeover", leaseDuration, renewInterval)
	start := time.Now()
	timeout := time.After(5 * leaseDuration)
	tick := time.NewTicker(renewInterval)
	defer tick.Stop()

	for !standby.IsLeader() {
		select {
		case <-timeout:
			t.Fatalf("standby did not take over leadership within %v of the leader going silent", 5*leaseDuration)
		case <-tick.C:
			if err := standby.Acquire(ctx); err != nil {
				t.Fatalf("standby Acquire: %v", err)
			}
		}
	}
	failoverLatency := time.Since(start)

	t.Logf("measured failover latency (single-process simulation, leaseDuration=%v, renewInterval=%v): %v", leaseDuration, renewInterval, failoverLatency)

	if got := standby.FencingGeneration(); got != leaderGen+1 {
		t.Fatalf("standby fencing generation = %d, want %d (must strictly exceed the dead leader's)", got, leaderGen+1)
	}
	// Sanity bound: failover should never take dramatically longer than
	// lease+renew, generous margin for a loaded CI/dev machine.
	if maxExpected := leaseDuration + 4*renewInterval + 2*time.Second; failoverLatency > maxExpected {
		t.Fatalf("failover took %v, expected under %v (leaseDuration=%v + renewInterval margin)", failoverLatency, maxExpected, leaseDuration)
	}
}

// TestPostgresLeaderElectorFencingGenerationResetsOnLoss verifies that a
// former leader's FencingGeneration() drops back to 0 the moment it
// discovers (via a failed renew) that it has been fenced out — not just
// IsLeader() flipping to false while FencingGeneration() keeps reporting
// the stale, no-longer-valid number from the ended tenure. This is the
// contract documented on store.LeaderElector.FencingGeneration.
func TestPostgresLeaderElectorFencingGenerationResetsOnLoss(t *testing.T) {
	db := openTestPostgres(t)
	ctx := context.Background()

	leaseDuration := 300 * time.Millisecond
	oldLeader := NewPostgresLeaderElector(db, "old-leader", leaseDuration, time.Second)
	if err := oldLeader.Acquire(ctx); err != nil {
		t.Fatalf("oldLeader Acquire: %v", err)
	}
	if got := oldLeader.FencingGeneration(); got != 1 {
		t.Fatalf("FencingGeneration after first acquire = %d, want 1", got)
	}

	// Let the lease expire without renewing, then let a different instance
	// take over — simulating oldLeader falling behind (GC pause, network
	// partition) long enough for a standby to claim the lease.
	time.Sleep(leaseDuration + 100*time.Millisecond)
	newLeader := NewPostgresLeaderElector(db, "new-leader", leaseDuration, time.Second)
	if err := newLeader.Acquire(ctx); err != nil {
		t.Fatalf("newLeader Acquire: %v", err)
	}
	if !newLeader.IsLeader() {
		t.Fatal("expected newLeader to acquire the expired lease")
	}

	// oldLeader still believes it's leader (nothing has told it otherwise
	// yet) until its next Acquire call discovers the renew fails.
	if err := oldLeader.Acquire(ctx); err != nil {
		t.Fatalf("oldLeader Acquire (discovers loss): %v", err)
	}
	if oldLeader.IsLeader() {
		t.Fatal("expected oldLeader to discover it lost leadership")
	}
	if got := oldLeader.FencingGeneration(); got != 0 {
		t.Fatalf("oldLeader FencingGeneration after discovering loss = %d, want 0 (must not keep reporting the stale generation from the ended tenure)", got)
	}
}
