package sodp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// crosslangSkip reports the outcome of a missing cross-language test
// dependency (node, or @sodp/client under ui/node_modules). Locally, a
// missing dependency is a normal, expected state — most contributors won't
// have run `cd ui && npm install` — so it degrades to Skip.
//
// In CI, though, ui/node_modules is always populated by an explicit `npm
// ci` step before `go test` ever runs (see .github/workflows/ci.yml and
// release.yml); a missing dependency there means that setup step silently
// failed or was skipped, not that the environment legitimately lacks it.
// Tnsor-Labs/brokoli#11 flags exactly this: t.Skip reads as green in CI
// dashboards, masking a broken pipeline instead of failing it loudly. CI
// is detected via the CI env var GitHub Actions (and most other CI
// systems) set automatically — see
// https://docs.github.com/en/actions/learn-github-actions/variables#default-environment-variables.
func crosslangSkip(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

// TestCrossLanguage_SodpClient starts a real Go SODP server, then runs the
// @sodp/client TypeScript library against it from Node.js.
// This proves the wire format is compatible end-to-end AND validates the
// baseline tracking pattern used by ws.ts.
func TestCrossLanguage_SodpClient(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		crosslangSkip(t, "node not found in PATH: %v", err)
	}

	_, thisFile, _, _ := runtime.Caller(0)
	pkgDir := filepath.Dir(thisFile)
	uiDir := filepath.Join(pkgDir, "..", "..", "ui")
	testdataDir := filepath.Join(pkgDir, "testdata")
	scriptPath := filepath.Join(testdataDir, "sodp_client_test.mjs")
	uiNodeModules := filepath.Join(uiDir, "node_modules")

	if _, err := os.Stat(scriptPath); err != nil {
		t.Fatalf("test script not found: %v", err) // part of the repo, never legitimately missing
	}
	if _, err := os.Stat(filepath.Join(uiNodeModules, "@sodp", "client")); err != nil {
		crosslangSkip(t, "@sodp/client not installed in ui/ — run `cd ui && npm install`: %v", err)
	}

	// Node ESM resolves packages relative to the importing file's location,
	// and Node's module resolution algorithm only recognizes directories
	// literally named "node_modules" — so the symlink providing
	// ui/node_modules to the test script must sit next to whatever file
	// does the importing.
	//
	// Run the whole thing from a fresh t.TempDir() rather than the shared,
	// fixed pkg/sodp/testdata/node_modules path the previous version of
	// this test used: that path was created and removed with no locking,
	// so two concurrent test runs sharing a checkout (e.g. two CI jobs on
	// the same self-hosted runner workspace, or `go test -count=2`) could
	// race on it — one run's defer os.Remove() firing while the other run
	// is mid-import (Tnsor-Labs/brokoli#11). A per-test temp directory has
	// no shared mutable state to race on at all.
	tmpDir := t.TempDir()
	scriptDest := filepath.Join(tmpDir, "sodp_client_test.mjs")
	if err := copyFile(scriptPath, scriptDest); err != nil {
		t.Fatalf("copy test script into isolated temp dir: %v", err)
	}
	relTarget, err := filepath.Rel(tmpDir, uiNodeModules)
	if err != nil {
		t.Fatalf("compute relative symlink target: %v", err)
	}
	if err := os.Symlink(relTarget, filepath.Join(tmpDir, "node_modules")); err != nil {
		t.Fatalf("create node_modules symlink: %v", err)
	}

	// Start SODP server
	sodpSrv := NewServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	mux.HandleFunc("POST /inject/events", func(w http.ResponseWriter, _ *http.Request) {
		for _, event := range []map[string]any{
			{"type": "run.started", "run_id": "cross-lang-1"},
			{"type": "node.completed", "run_id": "cross-lang-1", "node_id": "n1", "row_count": 100, "duration_ms": 50},
			{"type": "run.completed", "run_id": "cross-lang-1"},
		} {
			sodpSrv.MutateAppend("_events", event, 100)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /inject/events2", func(w http.ResponseWriter, _ *http.Request) {
		for _, eventType := range []string{"test.event.1", "test.event.2"} {
			sodpSrv.MutateAppend("_events2", map[string]any{"type": eventType}, 100)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	t.Logf("SODP server at %s", wsURL)

	// Run the Node.js test from the isolated temp dir so `import "@sodp/client"`
	// resolves via the symlink created above, not the shared testdata path.
	cmd := exec.Command("node", scriptDest, wsURL)
	cmd.Dir = uiDir
	output, err := cmd.CombinedOutput()
	t.Logf("--- node output ---\n%s", string(output))

	if err != nil {
		t.Fatalf("cross-language test failed: %v", err)
	}
}

// copyFile copies src to dst, creating dst (or truncating it if it already
// exists) with the source file's permission bits.
func copyFile(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, info.Mode().Perm())
}
