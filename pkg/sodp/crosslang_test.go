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

	// Run the Node.js test
	cmd := exec.Command("node", scriptPath, wsURL)
	cmd.Dir = uiDir
	output, err := cmd.CombinedOutput()
	t.Logf("--- node output ---\n%s", string(output))

	if err != nil {
		t.Fatalf("cross-language test failed: %v", err)
	}
}
