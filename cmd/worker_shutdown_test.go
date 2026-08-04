package cmd

import (
	"testing"
	"time"
)

// TestDrainWorkerSlotsWaitsForInFlightJobs covers the common graceful-
// shutdown case for --mode worker (Tnsor-Labs/brokoli-ee#9): jobs that are
// still running when SIGTERM arrives get a chance to finish before the
// dequeue loop returns, rather than being hard-killed mid-execution.
//
// OS signals themselves aren't exercised here (that's a job for an
// integration/e2e harness, not a unit test) — this targets the extracted
// drain/wait logic directly, simulating "in-flight job" by holding a slot
// until the test releases it.
func TestDrainWorkerSlotsWaitsForInFlightJobs(t *testing.T) {
	capacity := 3
	slots := make(chan struct{}, capacity)

	// Simulate one in-flight job holding a slot.
	slots <- struct{}{}

	release := make(chan struct{})
	go func() {
		<-release
		<-slots // the job's own goroutine releasing its slot on completion
	}()

	drained := make(chan bool, 1)
	go func() {
		drained <- drainWorkerSlots(slots, capacity, 2*time.Second)
	}()

	// Give drainWorkerSlots a moment to start blocking on the occupied slot,
	// then let the simulated job finish.
	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case ok := <-drained:
		if !ok {
			t.Fatal("expected drainWorkerSlots to report fully drained")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drainWorkerSlots did not return after the in-flight job finished")
	}
}

// TestDrainWorkerSlotsTimesOutWithJobsStillRunning covers the bounded-wait
// half of the contract: a job that won't finish in time must not block
// shutdown indefinitely — it's abandoned to the execution-attempt lease/
// recovery system instead (see cmd/serve.go's drainWorkerSlots doc comment).
func TestDrainWorkerSlotsTimesOutWithJobsStillRunning(t *testing.T) {
	capacity := 2
	slots := make(chan struct{}, capacity)

	// One slot occupied by a job that never finishes within the test.
	slots <- struct{}{}

	start := time.Now()
	ok := drainWorkerSlots(slots, capacity, 100*time.Millisecond)
	elapsed := time.Since(start)

	if ok {
		t.Fatal("expected drainWorkerSlots to report NOT fully drained")
	}
	if elapsed < 100*time.Millisecond {
		t.Fatalf("expected drainWorkerSlots to wait out the timeout, returned after %s", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("drainWorkerSlots took too long to time out: %s", elapsed)
	}
}

// TestDrainWorkerSlotsNoInFlightJobsReturnsImmediately covers the trivial
// case (no jobs running at shutdown time, or workerCount == 0): draining
// must not wait out the full timeout when there's nothing to wait for.
func TestDrainWorkerSlotsNoInFlightJobsReturnsImmediately(t *testing.T) {
	capacity := 4
	slots := make(chan struct{}, capacity)

	start := time.Now()
	ok := drainWorkerSlots(slots, capacity, 5*time.Second)
	elapsed := time.Since(start)

	if !ok {
		t.Fatal("expected drainWorkerSlots to report fully drained with no in-flight jobs")
	}
	if elapsed > time.Second {
		t.Fatalf("expected drainWorkerSlots to return promptly with no in-flight jobs, took %s", elapsed)
	}
}
