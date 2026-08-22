package cmd

import (
	"testing"
	"time"
)

// The default matches Kubernetes' own 30s terminationGracePeriodSeconds
// with headroom for the SIGTERM-to-SIGKILL race.
func TestWorkerDrainDefault(t *testing.T) {
	t.Setenv("BROKOLI_WORKER_DRAIN_TIMEOUT", "")
	if got := defaultWorkerDrainTimeout(); got != 25*time.Second {
		t.Fatalf("expected 25s, got %s", got)
	}
}

// A duration string is the documented form.
func TestWorkerDrainAcceptsDuration(t *testing.T) {
	t.Setenv("BROKOLI_WORKER_DRAIN_TIMEOUT", "5m")
	if got := defaultWorkerDrainTimeout(); got != 5*time.Minute {
		t.Fatalf("expected 5m, got %s", got)
	}
}

// Bare seconds are accepted too, since that is what the Helm value and
// terminationGracePeriodSeconds are expressed in.
func TestWorkerDrainAcceptsBareSeconds(t *testing.T) {
	t.Setenv("BROKOLI_WORKER_DRAIN_TIMEOUT", "600")
	if got := defaultWorkerDrainTimeout(); got != 600*time.Second {
		t.Fatalf("expected 600s, got %s", got)
	}
}

// Nonsense falls back rather than disabling the drain entirely, which
// would abandon in-flight work on every shutdown.
func TestWorkerDrainIgnoresInvalid(t *testing.T) {
	for _, bad := range []string{"0", "-1", "soon", "5x"} {
		t.Setenv("BROKOLI_WORKER_DRAIN_TIMEOUT", bad)
		if got := defaultWorkerDrainTimeout(); got != 25*time.Second {
			t.Fatalf("%q produced %s, expected the 25s default", bad, got)
		}
	}
}

// drainWorkerSlots returns true only when every slot came back.
func TestDrainWaitsForInFlightSlots(t *testing.T) {
	slots := make(chan struct{}, 2)
	slots <- struct{}{} // one job in flight

	done := make(chan bool, 1)
	go func() { done <- drainWorkerSlots(slots, 2, 2*time.Second) }()

	select {
	case <-done:
		t.Fatal("drain returned while a job was still in flight")
	case <-time.After(150 * time.Millisecond):
	}

	<-slots // the job finishes
	if got := <-done; !got {
		t.Fatal("expected drain to report success once the slot was released")
	}
}

// ...and gives up at the timeout rather than blocking shutdown forever.
func TestDrainGivesUpAtTimeout(t *testing.T) {
	slots := make(chan struct{}, 1)
	slots <- struct{}{} // never released

	start := time.Now()
	if drainWorkerSlots(slots, 1, 200*time.Millisecond) {
		t.Fatal("expected drain to time out")
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("returned too early: %s", elapsed)
	}
}
