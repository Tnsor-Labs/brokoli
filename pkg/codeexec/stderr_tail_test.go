package codeexec

import (
	"strings"
	"sync"
	"testing"
)

func TestStderrTailBoundsAndResets(t *testing.T) {
	tail := &stderrTail{}
	_, _ = tail.Write([]byte("prefix\n" + strings.Repeat("x", workerStderrTailBytes+100) + "suffix"))
	got := tail.String()
	if !strings.Contains(got, "truncated") || !strings.HasSuffix(got, "suffix") || strings.Contains(got, "prefix") {
		t.Fatalf("unexpected bounded tail: %q", got)
	}
	tail.Reset()
	if got := tail.String(); got != "" {
		t.Fatalf("tail after reset = %q", got)
	}
}

func TestStderrTailConcurrentWrites(t *testing.T) {
	tail := &stderrTail{}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_, _ = tail.Write([]byte("worker output\n"))
			}
		}()
	}
	wg.Wait()
	if len(tail.String()) == 0 {
		t.Fatal("concurrent writes produced no tail")
	}
}
