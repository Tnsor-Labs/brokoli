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
	"time"
)

// TestCrossLanguage_SodpClient starts a real Go SODP server, then runs the
// @sodp/client TypeScript library against it from Node.js.
// This proves the wire format is compatible end-to-end AND validates the
// baseline tracking pattern used by ws.ts.
func TestCrossLanguage_SodpClient(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found in PATH")
	}

	_, thisFile, _, _ := runtime.Caller(0)
	pkgDir := filepath.Dir(thisFile)
	uiDir := filepath.Join(pkgDir, "..", "..", "ui")
	testdataDir := filepath.Join(pkgDir, "testdata")
	scriptPath := filepath.Join(testdataDir, "sodp_client_test.mjs")
	uiNodeModules := filepath.Join(uiDir, "node_modules")

	if _, err := os.Stat(scriptPath); err != nil {
		t.Skipf("test script not found: %v", err)
	}
	if _, err := os.Stat(filepath.Join(uiNodeModules, "@sodp", "client")); err != nil {
		t.Skipf("@sodp/client not installed in ui/ — run `cd ui && npm install`: %v", err)
	}

	// Node ESM resolves packages relative to the importing file's location.
	// Create a node_modules symlink next to the test script so `import { ... }
	// from "@sodp/client"` resolves to ui/node_modules. The symlink is in
	// .gitignore via the global node_modules/ rule.
	symlinkPath := filepath.Join(testdataDir, "node_modules")
	relTarget, err := filepath.Rel(testdataDir, uiNodeModules)
	if err != nil {
		t.Fatalf("compute relative symlink target: %v", err)
	}
	_ = os.Remove(symlinkPath)
	if err := os.Symlink(relTarget, symlinkPath); err != nil {
		t.Fatalf("create node_modules symlink: %v", err)
	}
	defer os.Remove(symlinkPath)

	// Start SODP server
	sodpSrv := NewServer()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ws", sodpSrv.HandleWS)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	t.Logf("SODP server at %s", wsURL)

	// Inject events on a schedule matching what the JS test expects.
	// First injection is delayed to 1500ms to give Node.js startup, the
	// SODP handshake, the WATCH, and the JS test's 200ms verification sleep
	// time to all complete before any events arrive — even when the rest
	// of the Go test suite is running and the system is busy.
	//
	// Schedule:
	//   t=1500ms: _events[0] (run.started)
	//   t=2000ms: _events[1] (node.completed)
	//   t=2500ms: _events[2] (run.completed)
	//   t=3700ms: _events2[0]
	//   t=4000ms: _events2[1]
	go func() {
		time.Sleep(1500 * time.Millisecond)
		sodpSrv.MutateAppend("_events", map[string]any{
			"type": "run.started", "run_id": "cross-lang-1", "timestamp": time.Now().Format(time.RFC3339),
		}, 100)
		t.Log("injected _events[0]: run.started")

		time.Sleep(500 * time.Millisecond)
		sodpSrv.MutateAppend("_events", map[string]any{
			"type": "node.completed", "run_id": "cross-lang-1", "node_id": "n1",
			"row_count": 100, "duration_ms": 50,
		}, 100)
		t.Log("injected _events[1]: node.completed")

		time.Sleep(500 * time.Millisecond)
		sodpSrv.MutateAppend("_events", map[string]any{
			"type": "run.completed", "run_id": "cross-lang-1",
		}, 100)
		t.Log("injected _events[2]: run.completed")

		// _events2 for baseline tracking test. JS test starts watching
		// _events2 around t=3200ms (after the events sleep). We inject the
		// first event at t=3700ms so STATE_INIT lands first, baseline starts
		// at 0, then the deltas arrive and the forwarder picks them up.
		time.Sleep(1200 * time.Millisecond)
		sodpSrv.MutateAppend("_events2", map[string]any{
			"type": "test.event.1",
		}, 100)
		t.Log("injected _events2[0]")

		time.Sleep(300 * time.Millisecond)
		sodpSrv.MutateAppend("_events2", map[string]any{
			"type": "test.event.2",
		}, 100)
		t.Log("injected _events2[1]")
	}()

	// Run the Node.js test
	cmd := exec.Command("node", scriptPath, wsURL)
	cmd.Dir = uiDir
	output, err := cmd.CombinedOutput()
	t.Logf("--- node output ---\n%s", string(output))

	if err != nil {
		t.Fatalf("cross-language test failed: %v", err)
	}
}
