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

// Many goroutines logging at once: every line is stored exactly once and
// is stored by the time append returns, each goroutine's lines keep their
// order, and lines that waited were stored together.
func TestLogBatcherStoresEveryLineOnceInOrderAndBatches(t *testing.T) {
	var b logBatcher
	s := &fakeLogStore{}
	const loggers, lines = 32, 40
	var wg sync.WaitGroup
	notStored := make(chan string, loggers*lines)
	for g := 0; g < loggers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < lines; i++ {
				e := &models.LogEntry{NodeID: fmt.Sprint(g), Message: fmt.Sprint(i)}
				if err := b.append(s, e); err != nil {
					notStored <- err.Error()
					continue
				}
				if !s.has(e) {
					notStored <- fmt.Sprintf("logger %d line %d returned before it was stored", g, i)
				}
			}
		}(g)
	}
	wg.Wait()
	close(notStored)
	for msg := range notStored {
		t.Error(msg)
	}

	if len(s.stored) != loggers*lines {
		t.Fatalf("%d lines stored, want %d", len(s.stored), loggers*lines)
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

// A line that cannot be stored fails its own caller only.
func TestLogBatcherIsolatesALineThatFails(t *testing.T) {
	var b logBatcher
	s := &fakeLogStore{}
	var wg sync.WaitGroup
	errs := make([]error, 16)
	entries := make([]*models.LogEntry, 16)
	for i := range entries {
		msg := fmt.Sprint(i)
		if i == 7 {
			msg = "bad"
		}
		entries[i] = &models.LogEntry{Message: msg}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = b.append(s, entries[i])
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if (err != nil) != (i == 7) {
			t.Errorf("line %d: err = %v", i, err)
		}
		if i != 7 && !s.has(entries[i]) {
			t.Errorf("line %d was lost with the bad one", i)
		}
	}
}
