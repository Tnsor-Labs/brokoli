package engine

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/store"
)

// captureFallbackLog records every announced fallback for the test and
// forgets earlier announcements, so each test sees its own first one.
func captureFallbackLog(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var lines []string
	previous := fallbackLogf
	fallbackLogf = func(format string, args ...interface{}) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	announcedFallbacks.Range(func(k, _ interface{}) bool { announcedFallbacks.Delete(k); return true })
	t.Cleanup(func() { fallbackLogf = previous })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), lines...)
	}
}

// plainStore has only the methods of store.Store: none of the optional
// capabilities the SQLite store underneath also implements.
type plainStore struct{ store.Store }

// A store without resolved execution records still runs the task, but a
// retry may pick another payload, and that must be said (#529).
func TestResolveExecutionRecordAnnouncesAStoreThatCannotPin(t *testing.T) {
	logged := captureFallbackLog(t)
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	s := plainStore{newExpansionTestStore(t, "fallback-pin")}

	for i := 0; i < 2; i++ {
		if _, _, err := resolveExecutionRecord(s, "run-1", "task", digest, resolveTestManifest(digest)); err != nil {
			t.Fatalf("resolveExecutionRecord: %v", err)
		}
	}
	lines := logged()
	if len(lines) != 1 || !strings.Contains(lines[0], "engine.plainStore") || !strings.Contains(lines[0], "a retry may run a different payload") {
		t.Fatalf("announced %q, want one line naming the store and what is lost", lines)
	}
}

func TestResolveExecutionRecordIsQuietWithAStoreThatPins(t *testing.T) {
	skipIfNoPython3(t)
	logged := captureFallbackLog(t)
	digest := "sha256:" + strings.Repeat("0", 62) + "aa"
	if _, _, err := resolveExecutionRecord(newExpansionTestStore(t, "fallback-pin-ok"), "run-1", "task", digest, resolveTestManifest(digest)); err != nil {
		t.Fatalf("resolveExecutionRecord: %v", err)
	}
	if lines := logged(); len(lines) != 0 {
		t.Fatalf("announced %q with a store that pins", lines)
	}
}

func TestTaskJobCapabilitiesAnnouncesEachFallback(t *testing.T) {
	digest := "sha256:" + strings.Repeat("0", 62) + "bb"
	real := newExpansionTestStore(t, "fallback-caps")
	bundles := real.(store.TaskBundleV2Store)

	t.Run("store without bundles", func(t *testing.T) {
		logged := captureFallbackLog(t)
		r := &Runner{store: plainStore{real}, pipe: &models.Pipeline{}}
		for i := 0; i < 2; i++ {
			if got := r.taskJobCapabilities(digest); len(got) != 1 || got[0] != taskRuntimeCapability {
				t.Fatalf("capabilities = %v, want only the protocol tag", got)
			}
		}
		if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], "cannot read task bundles") {
			t.Fatalf("announced %q, want one line", lines)
		}
	})
	t.Run("missing bundle", func(t *testing.T) {
		logged := captureFallbackLog(t)
		r := &Runner{store: real, pipe: &models.Pipeline{}}
		r.taskJobCapabilities(digest)
		if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], "could not read task bundle "+digest) {
			t.Fatalf("announced %q, want a line naming the bundle", lines)
		}
	})
	t.Run("unreadable manifest", func(t *testing.T) {
		logged := captureFallbackLog(t)
		junk := []byte("not a gzip archive")
		junkDigest := taskbundlev2.DigestOf(junk)
		if _, err := bundles.PutTaskBundleV2("", junkDigest, junk); err != nil {
			t.Fatalf("PutTaskBundleV2: %v", err)
		}
		r := &Runner{store: real, pipe: &models.Pipeline{}}
		for i := 0; i < 2; i++ {
			r.taskJobCapabilities(junkDigest)
		}
		if lines := logged(); len(lines) != 1 || !strings.Contains(lines[0], "has no readable manifest") || !strings.Contains(lines[0], junkDigest) {
			t.Fatalf("announced %q, want one line naming the bundle", lines)
		}
	})
	t.Run("readable bundle", func(t *testing.T) {
		logged := captureFallbackLog(t)
		archive, err := taskbundlev2.Assemble(map[string]string{"fixture_task.py": "def run():\n    pass\n"}, resolveTestManifest(digest))
		if err != nil {
			t.Fatal(err)
		}
		good := taskbundlev2.DigestOf(archive)
		if _, err := bundles.PutTaskBundleV2("", good, archive); err != nil {
			t.Fatalf("PutTaskBundleV2: %v", err)
		}
		r := &Runner{store: real, pipe: &models.Pipeline{}}
		if got := r.taskJobCapabilities(good); len(got) != 2 {
			t.Fatalf("capabilities = %v, want the protocol tag and the runtime", got)
		}
		if lines := logged(); len(lines) != 0 {
			t.Fatalf("announced %q for a readable bundle", lines)
		}
	})
}
