package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// TestManager_DeclaredCapabilities locks in the extensions.NodeKindDeclarer
// contract (Tnsor-Labs/brokoli#62): a manifest's declared Kind for
// source_hello/sink_hello must translate into the matching capability
// tag, and an unknown node type must report ok=false rather than an
// empty-but-found result.
func TestManager_DeclaredCapabilities(t *testing.T) {
	dir := setupTestPluginDir(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	var _ extensions.NodeKindDeclarer = mgr // compile-time interface check

	caps, ok := mgr.DeclaredCapabilities("source_hello")
	if !ok {
		t.Fatal("expected source_hello to be recognized")
	}
	if len(caps) != 1 || caps[0] != "source" {
		t.Errorf("source_hello capabilities: got %v, want [source]", caps)
	}

	caps, ok = mgr.DeclaredCapabilities("sink_hello")
	if !ok {
		t.Fatal("expected sink_hello to be recognized")
	}
	if len(caps) != 1 || caps[0] != "sink" {
		t.Errorf("sink_hello capabilities: got %v, want [sink]", caps)
	}

	if _, ok := mgr.DeclaredCapabilities("source_nonexistent"); ok {
		t.Error("expected an unregistered node type to report ok=false")
	}
}

func TestManager_CanHandleOnlyExecutableKinds(t *testing.T) {
	dir := setupTestPluginDir(t)
	manifestPath := filepath.Join(dir, "hello", "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.NodeTypes = append(manifest.NodeTypes, NodeTypeDecl{Type: "transform_hello", Kind: KindTransform})
	data, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !mgr.CanHandle("source_hello") || !mgr.CanHandle("sink_hello") {
		t.Fatal("manager must execute source and sink plugin kinds")
	}
	if mgr.CanHandle("transform_hello") {
		t.Fatal("manager claimed unsupported transform plugin kind")
	}
	if caps, ok := mgr.DeclaredCapabilities("transform_hello"); !ok || len(caps) != 1 || caps[0] != "compute" {
		t.Fatalf("transform structural declaration = %v, %v; want [compute], true", caps, ok)
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

// ─── MsgProgress (Tnsor-Labs/brokoli#39 M1) ───────────────────────

// TestManager_Runner_Progress covers the handler path directly on
// Runner, without the Manager wrapper: every MsgProgress line the
// plugin emits reaches ProgressHandler with its fields intact, in
// order, and the last one is retained on RunResult.
func TestManager_Runner_Progress(t *testing.T) {
	dir := setupTestPluginDir(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	man := mgr.Get("hello")
	if man == nil {
		t.Fatal("hello plugin not loaded")
	}

	runner := NewRunner(man, 30*time.Second)
	var got []Progress
	runner.ProgressHandler = func(p Progress) { got = append(got, p) }

	res, err := runner.Read(context.Background(), Config{}, "greetings", nil)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 progress reports, got %d (%+v)", len(got), got)
	}
	// Reports must arrive in emission order, not reordered or coalesced.
	for i, p := range got {
		if p.Current == nil || *p.Current != int64(i+1) {
			t.Errorf("progress[%d].Current: got %v, want %d", i, p.Current, i+1)
		}
		if p.Total == nil || *p.Total != 3 {
			t.Errorf("progress[%d].Total: got %v, want 3", i, p.Total)
		}
		if p.Unit != "records" {
			t.Errorf("progress[%d].Unit: got %q, want %q", i, p.Unit, "records")
		}
	}

	// RunResult keeps the last report, mirroring State's last-one-wins.
	if res.LastProgress == nil {
		t.Fatal("RunResult.LastProgress is nil, want the final progress report")
	}
	if res.LastProgress.Current == nil || *res.LastProgress.Current != 3 {
		t.Errorf("LastProgress.Current: got %v, want 3", res.LastProgress.Current)
	}
	if res.LastProgress.RowsOut != 3 {
		t.Errorf("LastProgress.RowsOut: got %d, want 3", res.LastProgress.RowsOut)
	}

	// Progress must not disturb the record stream it is interleaved with.
	if len(res.Records) != 3 {
		t.Errorf("expected 3 records alongside progress, got %d", len(res.Records))
	}
}

// TestManager_Runner_Progress_NilHandlerIsSafe is the compatibility
// guarantee ADR-013 claims for M1: a host that does not care about
// progress (nil ProgressHandler — the state every host was in before
// this change) still receives every record, with no panic. If this ever
// fails, MsgProgress stopped being purely additive.
func TestManager_Runner_Progress_NilHandlerIsSafe(t *testing.T) {
	dir := setupTestPluginDir(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	runner := NewRunner(mgr.Get("hello"), 30*time.Second)
	// ProgressHandler deliberately left nil.

	res, err := runner.Read(context.Background(), Config{}, "greetings", nil)
	if err != nil {
		t.Fatalf("Read with nil ProgressHandler: %v", err)
	}
	if len(res.Records) != 3 {
		t.Errorf("expected 3 records, got %d", len(res.Records))
	}
	// Still recorded on the result even with no handler wired.
	if res.LastProgress == nil {
		t.Error("LastProgress should be populated even when ProgressHandler is nil")
	}
}

// TestManager_Execute_ProgressInLogs covers the Manager wrapper: plugin
// progress is surfaced into ExecutionResult.Logs, which is the path
// engine/runner.go replays into the run log. Note this is not live —
// Logs is delivered after the plugin exits (see ADR-013's Update).
func TestManager_Execute_ProgressInLogs(t *testing.T) {
	dir := setupTestPluginDir(t)
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	result, err := mgr.Execute(extensions.ExecutionContext{
		NodeType: "source_hello",
		NodeName: "Test Hello Source",
		Config:   map[string]interface{}{"stream": "greetings"},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var progressLines int
	var sawFinal bool
	for _, line := range result.Logs {
		if !contains(line, "[progress]") {
			continue
		}
		progressLines++
		if contains(line, "3/3 records (100%)") {
			sawFinal = true
		}
	}
	if progressLines != 3 {
		t.Errorf("expected 3 progress log lines, got %d (%v)", progressLines, result.Logs)
	}
	if !sawFinal {
		t.Errorf("final progress line missing or misformatted; got %v", result.Logs)
	}

	// The plugin's ordinary MsgLog output must still come through
	// alongside progress — regression guard for the two handlers
	// competing over the same slice.
	var sawLog bool
	for _, line := range result.Logs {
		if contains(line, "emitting 3 greetings") {
			sawLog = true
		}
	}
	if !sawLog {
		t.Errorf("MsgLog line lost when progress was added; got %v", result.Logs)
	}
}

// TestFormatProgress covers the log rendering, including the degraded
// cases a connector will actually produce: no total (cursor pagination),
// no counts at all, message-only.
func TestFormatProgress(t *testing.T) {
	i64 := func(v int64) *int64 { return &v }

	tests := []struct {
		name string
		in   Progress
		want string
	}{
		{
			name: "current and total",
			in:   Progress{Current: i64(1), Total: i64(3), Unit: "records"},
			want: "1/3 records (33%)",
		},
		{
			name: "complete",
			in:   Progress{Current: i64(3), Total: i64(3), Unit: "records"},
			want: "3/3 records (100%)",
		},
		{
			name: "indeterminate total",
			in:   Progress{Current: i64(7), Unit: "pages"},
			want: "7 pages",
		},
		{
			name: "zero total is treated as unknown, not divided by",
			in:   Progress{Current: i64(2), Total: i64(0), Unit: "pages"},
			want: "2 pages",
		},
		{
			name: "no counts at all",
			in:   Progress{Message: "warming up"},
			want: "progress · warming up",
		},
		{
			name: "rows and rate appended",
			in:   Progress{Current: i64(2), Total: i64(4), Unit: "pages", RowsOut: 500, Rate: 1.5},
			want: "2/4 pages (50%) · 500 rows out · 1.5/s",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatProgress(tc.in); got != tc.want {
				t.Errorf("formatProgress()\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
