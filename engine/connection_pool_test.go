package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// Connection pools (#398): the budget primitive, the loud-failure rule for
// explicit pools, and the live-database acceptance check -- a pool of N
// never observed at N+1 FROM THE SERVER'S SIDE (pg_stat_activity), not
// from our own bookkeeping.

func TestConnectionPoolsBoundsInUse(t *testing.T) {
	p := newConnectionPools()
	ctx := context.Background()

	// Fill a 2-slot pool.
	for i := 0; i < 2; i++ {
		if err := p.Acquire(ctx, "wh", 2, nil); err != nil {
			t.Fatal(err)
		}
	}

	// The third acquire blocks, reports the wait once, and completes only
	// after a release.
	waits := int32(0)
	done := make(chan error, 1)
	go func() {
		done <- p.Acquire(ctx, "wh", 2, func(inUse, limit int) {
			atomic.AddInt32(&waits, 1)
			if inUse != 2 || limit != 2 {
				t.Errorf("onWait(%d, %d), want (2, 2)", inUse, limit)
			}
		})
	}()
	select {
	case err := <-done:
		t.Fatalf("acquire on a full pool returned early: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	p.Release("wh")
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("release did not unblock the waiter")
	}
	if got := atomic.LoadInt32(&waits); got != 1 {
		t.Errorf("onWait fired %d times, want exactly once", got)
	}

	// Limit 0 is unlimited and touches nothing.
	if err := p.Acquire(ctx, "wh", 0, nil); err != nil {
		t.Fatal(err)
	}

	// A cancelled context abandons the wait with its error.
	ctx2, cancel := context.WithCancel(context.Background())
	done2 := make(chan error, 1)
	go func() { done2 <- p.Acquire(ctx2, "wh", 2, nil) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done2:
		if err == nil {
			t.Fatal("cancelled acquire returned nil")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not unblock the waiter")
	}
}

// An in-process stress: many goroutines through a 3-slot pool, the
// observed high-water mark never exceeds the limit.
func TestConnectionPoolsHighWaterMark(t *testing.T) {
	p := newConnectionPools()
	var inUse, peak int32
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.Acquire(context.Background(), "hw", 3, nil); err != nil {
				t.Error(err)
				return
			}
			n := atomic.AddInt32(&inUse, 1)
			for {
				old := atomic.LoadInt32(&peak)
				if n <= old || atomic.CompareAndSwapInt32(&peak, old, n) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			atomic.AddInt32(&inUse, -1)
			p.Release("hw")
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&peak); got > 3 {
		t.Fatalf("high-water mark %d exceeded the 3-slot budget", got)
	}
}

// An explicit pool: naming no connection fails the node loudly -- a budget
// that silently doesn't limit is a disabled control.
func TestExplicitPoolNamingNothingFailsTheRun(t *testing.T) {
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))

	csv := filepath.Join(dir, "in.csv")
	if err := os.WriteFile(csv, []byte("id\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &models.Pipeline{
		ID: "poolless", Name: "poolless", Enabled: true,
		Nodes: []models.Node{{ID: "src", Type: models.NodeTypeSourceFile, Name: "S",
			Config: map[string]interface{}{"path": csv, "format": "csv", "pool": "no-such-budget"}}},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreatePipeline(p); err != nil {
		t.Fatal(err)
	}
	run, err := eng.RunPipeline("poolless")
	if err == nil && (run == nil || run.Status != models.RunStatusFailed) {
		t.Fatalf("run = %+v, err = %v; an explicit pool naming no connection must fail", run, err)
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	} else if run != nil {
		msg = run.Error
	}
	if !strings.Contains(msg, "no-such-budget") {
		t.Errorf("failure %q does not name the missing pool", msg)
	}
}

// The acceptance check from #398, verbatim: a pool of N against a live
// database never observes N+1 concurrent node executions, asserted from
// the SERVER side by sampling pg_stat_activity while four runs race
// through a 2-slot connection. Also: the wait is visible in a run log.
func TestConnectionPoolNeverExceedsBudgetOnLivePostgres(t *testing.T) {
	dsn := os.Getenv("BROKOLI_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("BROKOLI_TEST_POSTGRES_URL not set")
	}
	dir := t.TempDir()
	st, err := store.NewSQLiteStore(filepath.Join(dir, "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	eng := drainEngineOnCleanup(t, NewEngine(st))
	eng.SetMaxConcurrentRuns(8) // the pool must do the limiting, not runSem

	if err := st.CreateConnection(&models.Connection{
		ID: "cp-conn", ConnID: "pool-pg", Type: models.ConnTypePostgres,
		Host:      "unused", // the node's own uri is injected below via extra fields
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		MaxConcurrent: 2,
	}); err != nil {
		t.Fatal(err)
	}

	// Four pipelines, each one source_db node running a sleepy query on
	// the live server through its own uri, drawing from pool-pg via the
	// explicit pool: escape hatch (so the query needs no conn_id
	// rewriting -- the budget is the thing under test, not resolution).
	marker := fmt.Sprintf("poolmark_%d", time.Now().UnixNano())
	for i := 0; i < 4; i++ {
		p := &models.Pipeline{
			ID: fmt.Sprintf("cp-%d", i), Name: fmt.Sprintf("cp-%d", i), Enabled: true,
			Nodes: []models.Node{{ID: "q", Type: models.NodeTypeSourceDB, Name: "Q",
				Config: map[string]interface{}{
					"uri":   dsn,
					"query": fmt.Sprintf("SELECT pg_sleep(0.7) AS %s", marker),
					"pool":  "pool-pg",
				}}},
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}
		if err := st.CreatePipeline(p); err != nil {
			t.Fatal(err)
		}
	}

	// Sample the server while the runs race.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stopSampling := make(chan struct{})
	var peak int32
	go func() {
		for {
			select {
			case <-stopSampling:
				return
			case <-time.After(40 * time.Millisecond):
			}
			var n int32
			if err := db.QueryRow(
				`SELECT count(*) FROM pg_stat_activity WHERE state = 'active' AND query LIKE '%'||$1||'%' AND query NOT LIKE '%pg_stat_activity%'`,
				marker).Scan(&n); err != nil {
				continue
			}
			for {
				old := atomic.LoadInt32(&peak)
				if n <= old || atomic.CompareAndSwapInt32(&peak, old, n) {
					break
				}
			}
		}
	}()

	var wg sync.WaitGroup
	runIDs := make([]string, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			run, err := eng.RunPipeline(fmt.Sprintf("cp-%d", i))
			if err != nil {
				t.Errorf("run cp-%d: %v", i, err)
				return
			}
			if run.Status != models.RunStatusSuccess {
				t.Errorf("run cp-%d failed: %s", i, run.Error)
			}
			runIDs[i] = run.ID
		}(i)
	}
	wg.Wait()
	close(stopSampling)

	if got := atomic.LoadInt32(&peak); got > 2 {
		t.Fatalf("the SERVER saw %d concurrent executions through a 2-slot pool", got)
	}
	if atomic.LoadInt32(&peak) == 0 {
		t.Fatal("sampling saw nothing; the assertion proved nothing")
	}

	// Four 0.7s queries through 2 slots: at least one run waited, and
	// said so in its log.
	waitLogged := false
	for _, id := range runIDs {
		if id == "" {
			continue
		}
		logs, err := st.GetLogs(id)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range logs {
			if strings.Contains(l.Message, `waiting for connection pool "pool-pg"`) {
				waitLogged = true
			}
		}
	}
	if !waitLogged {
		t.Error("no run log shows the pool wait; a silent wait is how operators conclude the system is hung")
	}
}
