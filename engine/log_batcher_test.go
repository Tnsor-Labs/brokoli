package engine

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// fakeLogStore records each AppendLogs call. Every call takes a moment, so
// concurrent loggers overlap the way they do on a real disk, and a batch
// containing a "bad" line fails whole.
type fakeLogStore struct {
	mu      sync.Mutex
	stored  []*models.LogEntry
	calls   int
	maxSeen int
}

func (s *fakeLogStore) AppendLogs(entries []*models.LogEntry) error {
	time.Sleep(200 * time.Microsecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if len(entries) > s.maxSeen {
		s.maxSeen = len(entries)
	}
	for _, e := range entries {
		if e.Message == "bad" {
			return errors.New("cannot store this line")
		}
	}
	s.stored = append(s.stored, entries...)
	return nil
}

func (s *fakeLogStore) has(e *models.LogEntry) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.stored {
		if x == e {
			return true
		}
	}
	return false
}

// Many goroutines logging at once: once flush returns, every line is
// stored exactly once, each goroutine's lines keep their order, and lines
// that queued together were stored together.
func TestLogBatcherStoresEveryLineOnceInOrderAndBatches(t *testing.T) {
	var b logBatcher
	s := &fakeLogStore{}
	const loggers, lines = 32, 40
	var wg sync.WaitGroup
	for g := 0; g < loggers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < lines; i++ {
				b.append(s, &models.LogEntry{NodeID: fmt.Sprint(g), Message: fmt.Sprint(i)})
			}
		}(g)
	}
	wg.Wait()
	b.flush()

	if len(s.stored) != loggers*lines {
		t.Fatalf("%d lines stored after flush, want %d", len(s.stored), loggers*lines)
	}
	next := map[string]int{}
	for _, e := range s.stored {
		if want := fmt.Sprint(next[e.NodeID]); e.Message != want {
			t.Fatalf("logger %s: line %s stored where line %s was due", e.NodeID, e.Message, want)
		}
		next[e.NodeID]++
	}
	if s.calls >= loggers*lines {
		t.Errorf("%d writes for %d lines: nothing was batched", s.calls, loggers*lines)
	}
	t.Logf("%d lines in %d writes, largest batch %d", len(s.stored), s.calls, s.maxSeen)
}

// A caller does not wait for the store: append returns while a slow write
// is still in progress, which is what let a mapped node's items stop
// queueing behind each other's log lines (#967 benchmark).
func TestLogBatcherAppendDoesNotWaitForTheStore(t *testing.T) {
	var b logBatcher
	s := &slowLogStore{release: make(chan struct{})}
	start := time.Now()
	for i := 0; i < 100; i++ {
		b.append(s, &models.LogEntry{Message: fmt.Sprint(i)})
	}
	if waited := time.Since(start); waited > time.Second {
		t.Fatalf("100 appends took %s with the store blocked", waited)
	}
	if n := s.count(); n != 0 {
		t.Fatalf("%d lines stored while the store was blocked", n)
	}
	close(s.release)
	b.flush()
	if n := s.count(); n != 100 {
		t.Fatalf("%d lines stored after flush, want 100", n)
	}
}

// A line that cannot be stored is reported, and the lines batched with it
// are still stored.
func TestLogBatcherIsolatesALineThatFails(t *testing.T) {
	var failed []string
	var mu sync.Mutex
	b := logBatcher{onFailed: func(e *models.LogEntry, err error) {
		mu.Lock()
		defer mu.Unlock()
		failed = append(failed, e.Message)
	}}
	s := &fakeLogStore{}
	entries := make([]*models.LogEntry, 16)
	for i := range entries {
		msg := fmt.Sprint(i)
		if i == 7 {
			msg = "bad"
		}
		entries[i] = &models.LogEntry{Message: msg}
		b.append(s, entries[i])
	}
	b.flush()
	if len(failed) != 1 || failed[0] != "bad" {
		t.Fatalf("reported %v, want only the bad line", failed)
	}
	for i, e := range entries {
		if i != 7 && !s.has(e) {
			t.Errorf("line %d was lost with the bad one", i)
		}
	}
}

// When the store falls behind by maxPendingLogLines, a caller waits: the
// queue is bounded.
func TestLogBatcherQueueIsBounded(t *testing.T) {
	var b logBatcher
	s := &slowLogStore{release: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < maxPendingLogLines+10; i++ {
			b.append(s, &models.LogEntry{Message: "x"})
		}
	}()
	select {
	case <-done:
		t.Fatal("appended past the bound while nothing was being stored")
	case <-time.After(200 * time.Millisecond):
	}
	close(s.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("appends did not resume once the store caught up")
	}
	b.flush()
	if n := s.count(); n != maxPendingLogLines+10 {
		t.Fatalf("%d lines stored, want %d", n, maxPendingLogLines+10)
	}
}

// slowLogStore blocks every write until release is closed.
type slowLogStore struct {
	release chan struct{}
	mu      sync.Mutex
	n       int
}

func (s *slowLogStore) AppendLogs(entries []*models.LogEntry) error {
	<-s.release
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n += len(entries)
	return nil
}

func (s *slowLogStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}
