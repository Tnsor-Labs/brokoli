package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// setupTestPluginDir creates a fresh plugin directory with the
// bundled hello-plugin copied in, and returns the directory path.
// The cleanup removes the whole tree at test end.
//
// We copy rather than symlink so plugin discovery sees a real on-disk
// directory — matches the shape an installed plugin would have.
func setupTestPluginDir(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	src := filepath.Join("testdata", "hello-plugin")
	dst := filepath.Join(tmp, "hello")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copy hello plugin: %v", err)
	}
	// Ensure the bin is executable inside the temp dir.
	if err := os.Chmod(filepath.Join(dst, "bin"), 0o755); err != nil {
		t.Fatalf("chmod bin: %v", err)
	}
	return tmp
}

// TestManager_LoadAll_Discovery locks in the plugin discovery
// contract: a directory full of manifest.json + bin pairs becomes a
// map of name → manifest and a map of node_type → owning manifest.
func TestManager_LoadAll_Discovery(t *testing.T) {
	dir := setupTestPluginDir(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	plugins := mgr.List()
	if len(plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(plugins))
	}
	if plugins[0].Name != "hello" {
		t.Errorf("plugin name: got %q, want %q", plugins[0].Name, "hello")
	}

	// Both declared node types must resolve back to the hello manifest.
	if mgr.Resolve("source_hello") == nil {
		t.Error("source_hello should resolve to a plugin manifest")
	}
	if mgr.Resolve("sink_hello") == nil {
		t.Error("sink_hello should resolve to a plugin manifest")
	}
	// Unknown types must not.
	if mgr.Resolve("source_nonexistent") != nil {
		t.Error("unknown node type should not resolve")
	}
}

// TestManager_LoadAll_MissingDir verifies that a missing plugin dir
// is not an error — fresh installs need to come up cleanly with zero
// plugins.
func TestManager_LoadAll_MissingDir(t *testing.T) {
	mgr, err := NewManager(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("NewManager on missing dir should succeed, got %v", err)
	}
	if len(mgr.List()) != 0 {
		t.Errorf("empty plugin dir should produce empty list")
	}
}

// TestManager_LoadAll_BadManifestSkipped verifies that a broken
// plugin (missing binary, bad manifest, etc.) doesn't take down the
// whole registry — the manager logs it and skips that one.
func TestManager_LoadAll_BadManifestSkipped(t *testing.T) {
	tmp := t.TempDir()
	// Good plugin.
	if err := copyDir(filepath.Join("testdata", "hello-plugin"), filepath.Join(tmp, "hello")); err != nil {
		t.Fatalf("copy good plugin: %v", err)
	}
	_ = os.Chmod(filepath.Join(tmp, "hello", "bin"), 0o755)
	// Bad plugin: manifest references a non-existent binary and an invalid protocol version.
	badDir := filepath.Join(tmp, "bad")
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badDir, "manifest.json"), []byte(`{"protocol_version":999,"name":"bad","version":"0.1.0","binary":"./missing","node_types":[{"type":"source_bad","kind":"source"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(tmp)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if len(mgr.List()) != 1 {
		t.Errorf("bad plugin should be skipped, got %d plugins loaded", len(mgr.List()))
	}
	if mgr.Get("hello") == nil {
		t.Errorf("good plugin should still load despite bad neighbor")
	}
}

// TestManager_Execute_Source_EndToEnd exercises the full spawn-→-
// -stdout-decode path: manager claims the source_hello node type,
// spawns the hello-plugin `read` command, parses 3 record messages
// and one state line from JSONL, returns a DataSet with 3 rows.
//
// This is the integration test that proves the whole protocol + runner
// + manager + NodeExecutor chain works end-to-end.
func TestManager_Execute_Source_EndToEnd(t *testing.T) {
	dir := setupTestPluginDir(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Sanity: manager should claim this node type through the
	// NodeExecutor interface — this is what engine/runner.go calls.
	if !mgr.CanHandle("source_hello") {
		t.Fatalf("manager should CanHandle source_hello")
	}
	if mgr.CanHandle("source_nonexistent") {
		t.Fatalf("manager should not CanHandle unknown types")
	}

	result, err := mgr.Execute(extensions.ExecutionContext{
		NodeType: "source_hello",
		NodeName: "Test Hello Source",
		Config: map[string]interface{}{
			"stream": "greetings",
			// config fields are plugin-specific; hello ignores them
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	ds, ok := result.OutputData.(*common.DataSet)
	if !ok {
		t.Fatalf("expected *common.DataSet, got %T", result.OutputData)
	}
	if len(ds.Rows) != 3 {
		t.Fatalf("expected 3 rows from hello source, got %d", len(ds.Rows))
	}

	// Verify columns were discovered from the record payloads.
	wantCols := map[string]bool{"id": false, "message": false}
	for _, col := range ds.Columns {
		if _, ok := wantCols[col]; ok {
			wantCols[col] = true
		}
	}
	for col, seen := range wantCols {
		if !seen {
			t.Errorf("column %q missing from output DataSet (got %v)", col, ds.Columns)
		}
	}

	// Verify actual record content for the first row.
	first := ds.Rows[0]
	if first["message"] != "hello world" {
		t.Errorf("first row message: got %v, want %q", first["message"], "hello world")
	}

	// The manager should have captured the plugin's info-level log
	// line emitted at the top of `read`.
	foundLog := false
	for _, line := range result.Logs {
		if contains(line, "emitting 3 greetings") {
			foundLog = true
			break
		}
	}
	if !foundLog {
		t.Errorf("plugin log line was not captured in ExecutionResult.Logs; got %v", result.Logs)
	}

	if result.RowCount != 3 {
		t.Errorf("RowCount: got %d, want 3", result.RowCount)
	}
	if result.DurationMs <= 0 {
		t.Errorf("DurationMs should be > 0, got %d", result.DurationMs)
	}
}

// TestManager_Execute_Sink_EndToEnd verifies the inverse path:
// records stream into the plugin's stdin, plugin counts them and
// reports via a status message, manager treats success as an ok
// ExecutionResult that passes the input through unchanged.
func TestManager_Execute_Sink_EndToEnd(t *testing.T) {
	dir := setupTestPluginDir(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	input := &common.DataSet{
		Columns: []string{"id", "name"},
		Rows: []common.DataRow{
			{"id": 1, "name": "alice"},
			{"id": 2, "name": "bob"},
			{"id": 3, "name": "carol"},
		},
	}

	result, err := mgr.Execute(extensions.ExecutionContext{
		NodeType:  "sink_hello",
		NodeName:  "Test Hello Sink",
		InputData: input,
		Config: map[string]interface{}{
			"stream": "greetings",
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.RowCount != 3 {
		t.Errorf("RowCount: got %d, want 3", result.RowCount)
	}
}

// TestManager_Runner_Check covers the simpler Check()-only path
// (used by the UI's "Test Connection" button) without going through
// the ExecutionContext wrapper.
func TestManager_Runner_Check(t *testing.T) {
	dir := setupTestPluginDir(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	man := mgr.Get("hello")
	if man == nil {
		t.Fatal("hello plugin not loaded")
	}

	runner := NewRunner(man, 10*1e9) // 10 s
	if err := runner.Check(context.Background(), Config{"whatever": "value"}); err != nil {
		t.Errorf("Check: %v", err)
	}
}

// TestManager_Runner_Discover exercises the discover path.
func TestManager_Runner_Discover(t *testing.T) {
	dir := setupTestPluginDir(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	runner := NewRunner(mgr.Get("hello"), 10*1e9)
	streams, err := runner.Discover(context.Background(), Config{})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(streams) != 1 {
		t.Fatalf("expected 1 stream, got %d", len(streams))
	}
	if streams[0].Name != "greetings" {
		t.Errorf("stream name: got %q, want %q", streams[0].Name, "greetings")
	}
	if len(streams[0].Columns) != 2 {
		t.Errorf("stream columns: got %d, want 2", len(streams[0].Columns))
	}
}

// ─── test helpers ──────────────────────────────────────────────────

// copyDir is a tiny recursive copy we use to stage the bundled
// testdata plugin into a temp dir. Testing-only; never used in
// production paths.
func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
