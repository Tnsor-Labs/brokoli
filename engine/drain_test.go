package engine

import (
	"context"
	"testing"
	"time"
)

// drainEngineOnCleanup registers a t.Cleanup that drains the engine's
// background goroutines before the test's store closes and TempDir is
// removed. Every RunPipeline — including the synchronous path — fires
// fireTriggerModeDependents on an engine-owned goroutine AFTER returning
// its result, so a test that runs a pipeline and immediately tears down
// races that goroutine's SQLite access against store.Close/RemoveAll. The
// losing interleaving recreates WAL files mid-removal and fails teardown
// with "TempDir RemoveAll cleanup: directory not empty" (the #94-family
// flake, always preceded by a "trigger-mode: ... sql: database is closed"
// log line from the orphaned goroutine).
//
// Call this immediately after NewEngine, AFTER the store's own cleanup is
// registered: t.Cleanup is LIFO, so the engine then drains first.
func drainEngineOnCleanup(t *testing.T, eng *Engine) *Engine {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := eng.Close(ctx); err != nil {
			t.Errorf("engine close: %v", err)
		}
	})
	return eng
}
