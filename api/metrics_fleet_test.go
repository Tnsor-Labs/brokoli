package api

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tnsor-Labs/brokoli/store"
)

type countingStore struct {
	store.Store
	counts map[string]int
	err    error
	calls  int
}

func (c *countingStore) CountRunsByStatus() (map[string]int, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return c.counts, nil
}

func resetRunCountCache() {
	runCountCache.Lock()
	runCountCache.at = time.Time{}
	runCountCache.counts = nil
	runCountCache.Unlock()
}

// Run totals must describe the deployment, not whichever process
// answered the scrape: runs execute on workers, so the API's in-process
// counters are always zero and a worker's vanish with the pod.
func TestFleetRunCountsComeFromTheStore(t *testing.T) {
	resetRunCountCache()
	s := &countingStore{counts: map[string]int{"success": 12, "failed": 3}}

	got := cachedRunCounts(s)
	if got["success"] != 12 || got["failed"] != 3 {
		t.Fatalf("unexpected counts: %v", got)
	}
}

// A burst of scrapes costs one query.
func TestRunCountsAreCachedBetweenScrapes(t *testing.T) {
	resetRunCountCache()
	s := &countingStore{counts: map[string]int{"success": 1}}

	for i := 0; i < 5; i++ {
		cachedRunCounts(s)
	}
	if s.calls != 1 {
		t.Fatalf("expected 1 query for 5 scrapes, got %d", s.calls)
	}
}

// The cache expires, so the numbers track reality.
func TestRunCountsRefreshAfterTTL(t *testing.T) {
	resetRunCountCache()
	prev := runCountCacheTTL
	runCountCacheTTL = 10 * time.Millisecond
	defer func() { runCountCacheTTL = prev }()

	s := &countingStore{counts: map[string]int{"success": 1}}
	cachedRunCounts(s)
	time.Sleep(20 * time.Millisecond)
	cachedRunCounts(s)

	if s.calls != 2 {
		t.Fatalf("expected a refresh after the TTL, got %d queries", s.calls)
	}
}

// A database blip serves the last known numbers rather than reporting
// an idle fleet.
func TestRunCountsSurviveAQueryError(t *testing.T) {
	resetRunCountCache()
	prev := runCountCacheTTL
	runCountCacheTTL = 10 * time.Millisecond
	defer func() { runCountCacheTTL = prev }()

	s := &countingStore{counts: map[string]int{"success": 7}}
	cachedRunCounts(s)

	s.err = errors.New("database unavailable")
	time.Sleep(20 * time.Millisecond)
	got := cachedRunCounts(s)

	if got["success"] != 7 {
		t.Fatalf("expected the last known counts, got %v", got)
	}
}

// With nothing cached and a failing store, the scrape still succeeds.
func TestRunCountsEmptyWhenNothingKnown(t *testing.T) {
	resetRunCountCache()
	s := &countingStore{err: errors.New("down")}
	if got := cachedRunCounts(s); len(got) != 0 {
		t.Fatalf("expected empty counts, got %v", got)
	}
}

// The exposition format is what Prometheus expects.
func TestRunStatusMetricIsLabelled(t *testing.T) {
	line := ""
	for status, count := range map[string]int{"success": 4} {
		line = strings.TrimSpace(sprintfRunStatus(status, count))
	}
	if line != `brokoli_runs_by_status{status="success"} 4` {
		t.Fatalf("unexpected exposition line: %q", line)
	}
}
