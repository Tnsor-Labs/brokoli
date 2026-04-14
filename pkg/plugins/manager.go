package plugins

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Manager is the host-side plugin registry. It scans a directory of
// installed plugin manifests at startup, maintains a map from node
// type to manifest, and implements the extensions.NodeExecutor
// interface so the existing runner's executor loop in
// engine/runner.go picks up plugin-registered node types without any
// code change to the engine itself.
//
// Usage:
//
//	mgr, _ := plugins.NewManager(plugins.DefaultDir())
//	registry := extensions.DefaultRegistry()
//	registry.Executors = append(registry.Executors, mgr)
//
// The engine's node-resolution loop asks each registered executor
// "CanHandle(nodeType)?" before falling through to the built-in
// switch. Plugin-registered types are handled by this manager; the
// engine never learns about them individually.
type Manager struct {
	dir string

	// Default timeout for plugin invocations. Can be overridden per
	// call by the node executor via context deadlines.
	DefaultTimeout time.Duration

	mu        sync.RWMutex
	manifests map[string]*Manifest       // plugin name -> manifest
	nodeTypes map[string]*Manifest       // node type -> owning manifest
}

// DefaultDir returns the default plugin directory. In order of
// preference: BROKOLI_PLUGIN_DIR env var, then $XDG_DATA_HOME/brokoli/plugins,
// then $HOME/.brokoli/plugins. Never returns an error — if none of
// the above can be resolved it returns a relative "./plugins" and
// LoadAll silently does nothing when the directory doesn't exist.
func DefaultDir() string {
	if dir := os.Getenv("BROKOLI_PLUGIN_DIR"); dir != "" {
		return dir
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "brokoli", "plugins")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".brokoli", "plugins")
	}
	return "plugins"
}

// NewManager constructs a plugin manager rooted at dir and loads any
// manifests already present. Missing dir is not an error — the manager
// starts empty and a later `brokoli plugins install` populates it.
func NewManager(dir string) (*Manager, error) {
	m := &Manager{
		dir:            dir,
		DefaultTimeout: 5 * time.Minute,
		manifests:      make(map[string]*Manifest),
		nodeTypes:      make(map[string]*Manifest),
	}
	if err := m.LoadAll(); err != nil {
		return m, err
	}
	return m, nil
}

// LoadAll (re)scans the plugin directory and refreshes the registry.
// Safe to call concurrently with other manager operations — grabs the
// write lock for the swap. Errors loading individual plugins are
// logged but do not abort the whole scan; one bad plugin shouldn't
// take the whole host down.
func (m *Manager) LoadAll() error {
	info, err := os.Stat(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Fresh install: no plugins, no problem.
			m.mu.Lock()
			m.manifests = make(map[string]*Manifest)
			m.nodeTypes = make(map[string]*Manifest)
			m.mu.Unlock()
			return nil
		}
		return fmt.Errorf("stat plugin dir %s: %w", m.dir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("plugin dir %s is not a directory", m.dir)
	}

	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return fmt.Errorf("read plugin dir %s: %w", m.dir, err)
	}

	newManifests := make(map[string]*Manifest)
	newNodeTypes := make(map[string]*Manifest)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pluginDir := filepath.Join(m.dir, e.Name())
		man, err := LoadManifest(pluginDir)
		if err != nil {
			log.Printf("plugins: skipping %s: %v", e.Name(), err)
			continue
		}
		if existing := newManifests[man.Name]; existing != nil {
			log.Printf("plugins: duplicate plugin name %q (dirs %s and %s) — skipping second",
				man.Name, existing.Dir(), pluginDir)
			continue
		}
		newManifests[man.Name] = man
		for _, nt := range man.NodeTypes {
			if owner := newNodeTypes[nt.Type]; owner != nil {
				log.Printf("plugins: node type %q already owned by plugin %q (skipping declaration in %q)",
					nt.Type, owner.Name, man.Name)
				continue
			}
			newNodeTypes[nt.Type] = man
		}
	}

	m.mu.Lock()
	m.manifests = newManifests
	m.nodeTypes = newNodeTypes
	m.mu.Unlock()

	log.Printf("plugins: loaded %d plugin(s) registering %d node type(s) from %s",
		len(newManifests), len(newNodeTypes), m.dir)
	return nil
}

// Dir returns the directory this manager scans.
func (m *Manager) Dir() string { return m.dir }

// List returns a stable-ordered snapshot of installed plugin manifests.
// Used by the `brokoli plugins list` CLI command.
func (m *Manager) List() []*Manifest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Manifest, 0, len(m.manifests))
	for _, man := range m.manifests {
		out = append(out, man)
	}
	// Stable by name so CLI output is deterministic.
	sortManifestsByName(out)
	return out
}

// Get returns the manifest for a given plugin name, or nil if not
// installed.
func (m *Manager) Get(name string) *Manifest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.manifests[name]
}

// Resolve returns the manifest that owns the given node type, or nil
// if the type isn't registered with any plugin.
func (m *Manager) Resolve(nodeType string) *Manifest {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nodeTypes[nodeType]
}

// NodeTypes returns the full list of plugin-registered node types
// (names only). Used by the CLI and UI to enumerate available nodes.
func (m *Manager) NodeTypes() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.nodeTypes))
	for t := range m.nodeTypes {
		out = append(out, t)
	}
	return out
}

// Remove deletes a plugin directory from disk and drops it from the
// in-memory registry. Errors if the plugin isn't installed.
func (m *Manager) Remove(name string) error {
	m.mu.Lock()
	man := m.manifests[name]
	m.mu.Unlock()
	if man == nil {
		return fmt.Errorf("plugin %q is not installed", name)
	}
	if err := os.RemoveAll(man.Dir()); err != nil {
		return fmt.Errorf("remove plugin %s: %w", name, err)
	}
	return m.LoadAll()
}

// ─── extensions.NodeExecutor implementation ───────────────────────
//
// The engine's node resolver (engine/runner.go:runNodeLogic) iterates
// registered NodeExecutors before falling through to built-in types.
// A plugin manager satisfying this interface lets plugin-registered
// node types flow through the exact same path as the enterprise K8s
// executor — no special-casing in the engine.

// Name returns the executor name for log lines.
func (m *Manager) Name() string { return "plugins" }

// CanHandle reports whether any loaded plugin claims the given node
// type. Called for every node on every run, so hot; held behind the
// read lock.
func (m *Manager) CanHandle(nodeType string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.nodeTypes[nodeType] != nil
}

// Execute runs a pipeline node through its plugin. The plugin's
// declared Kind determines whether it's a source (read records),
// a sink (write records), or a transform (read + write).
func (m *Manager) Execute(ctx extensions.ExecutionContext) (*extensions.ExecutionResult, error) {
	man := m.Resolve(ctx.NodeType)
	if man == nil {
		// Shouldn't happen — the engine only calls Execute after
		// CanHandle returned true — but defensive.
		return nil, fmt.Errorf("plugin manager: no plugin registered for node type %q", ctx.NodeType)
	}
	kind := kindOfNodeType(man, ctx.NodeType)
	if kind == "" {
		return nil, fmt.Errorf("plugin %s: node type %q not declared in manifest", man.Name, ctx.NodeType)
	}

	// Config comes from the node. Stream is a required config field
	// for source/sink nodes — the plugin needs to know which of its
	// discoverable streams to read/write. Transforms don't need one.
	cfg := Config(ctx.Config)
	stream, _ := ctx.Config["stream"].(string)

	// Plugin-invocation timeout: prefer the parent context's deadline
	// if it has one, otherwise fall back to DefaultTimeout.
	timeout := m.DefaultTimeout
	if dl, ok := deadline(ctx); ok {
		timeout = time.Until(dl)
		if timeout <= 0 {
			return nil, fmt.Errorf("plugin %s: context already cancelled", man.Name)
		}
	}

	runner := NewRunner(man, timeout)
	// Capture plugin logs into the caller's ExecutionResult.Logs slice
	// so they land in the run log UI via the existing executor path.
	var logs []string
	runner.LogHandler = func(level LogLevel, msg string) {
		logs = append(logs, fmt.Sprintf("[%s] %s", level, msg))
	}

	start := time.Now()
	bgCtx := context.Background() // TODO: thread the real run context through ExecutionContext
	switch kind {
	case KindSource:
		if stream == "" {
			return nil, fmt.Errorf("plugin %s: source node %q missing required 'stream' config field",
				man.Name, ctx.NodeType)
		}
		res, err := runner.Read(bgCtx, cfg, stream, nil)
		if err != nil {
			return &extensions.ExecutionResult{Logs: logs}, err
		}
		ds := recordsToDataSet(res.Records)
		return &extensions.ExecutionResult{
			OutputData: ds,
			RowCount:   len(ds.Rows),
			DurationMs: time.Since(start).Milliseconds(),
			Logs:       logs,
		}, nil

	case KindSink:
		if stream == "" {
			return nil, fmt.Errorf("plugin %s: sink node %q missing required 'stream' config field",
				man.Name, ctx.NodeType)
		}
		input, ok := ctx.InputData.(*common.DataSet)
		if !ok || input == nil {
			return nil, fmt.Errorf("plugin %s: sink node %q requires input data",
				man.Name, ctx.NodeType)
		}
		if err := runner.Write(bgCtx, cfg, stream, dataSetToRecords(input)); err != nil {
			return &extensions.ExecutionResult{Logs: logs}, err
		}
		return &extensions.ExecutionResult{
			OutputData: input, // pass-through so downstream nodes (rare for sinks) still see the data
			RowCount:   len(input.Rows),
			DurationMs: time.Since(start).Milliseconds(),
			Logs:       logs,
		}, nil

	case KindTransform:
		// Transforms read the input via stdin, emit new records on
		// stdout. We reuse the same read path as sources but pipe the
		// upstream dataset in as the "extraStdin" body of the call.
		// Phase 1: shell out to Read() with a synthetic ReadParams —
		// plugins that declare transform kind must accept records on
		// stdin after the config header. Real support is a Phase 2
		// item; for now we return an explicit error so authors get a
		// clear signal.
		return nil, fmt.Errorf("plugin %s: transform node kind is not yet supported in Phase 1", man.Name)

	default:
		return nil, fmt.Errorf("plugin %s: unknown node kind %q", man.Name, kind)
	}
}

// ─── helpers ──────────────────────────────────────────────────────

// kindOfNodeType looks up a manifest's declared kind for the given
// node type. Returns "" if the type isn't in the manifest.
func kindOfNodeType(m *Manifest, nodeType string) NodeKind {
	for _, nt := range m.NodeTypes {
		if nt.Type == nodeType {
			return nt.Kind
		}
	}
	return ""
}

// recordsToDataSet converts a flat record list into a common.DataSet.
// Column order is stable: first-row key order, then keys seen later,
// appended in the order they show up. Later records with missing keys
// produce nil cells for those columns so the dataset stays rectangular.
func recordsToDataSet(records []map[string]interface{}) *common.DataSet {
	ds := &common.DataSet{}
	if len(records) == 0 {
		return ds
	}
	seen := make(map[string]bool)
	// First pass: collect column order.
	for _, rec := range records {
		for k := range rec {
			if !seen[k] {
				seen[k] = true
				ds.Columns = append(ds.Columns, k)
			}
		}
	}
	// Second pass: build rows.
	ds.Rows = make([]common.DataRow, 0, len(records))
	for _, rec := range records {
		row := make(common.DataRow, len(ds.Columns))
		for _, col := range ds.Columns {
			if v, ok := rec[col]; ok {
				row[col] = v
			}
		}
		ds.Rows = append(ds.Rows, row)
	}
	return ds
}

// dataSetToRecords converts a DataSet back into flat record form for
// sink plugins. Each row becomes a map[string]interface{} with the
// declared columns.
func dataSetToRecords(ds *common.DataSet) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(ds.Rows))
	for _, row := range ds.Rows {
		rec := make(map[string]interface{}, len(ds.Columns))
		for _, col := range ds.Columns {
			rec[col] = row[col]
		}
		out = append(out, rec)
	}
	return out
}

// deadline extracts a deadline from an ExecutionContext. The current
// extensions.ExecutionContext struct doesn't carry a context.Context
// directly, so there's nothing to extract in Phase 1 — this helper
// exists as the single site to update when we thread a real context
// through. Returns ok=false for now.
func deadline(_ extensions.ExecutionContext) (time.Time, bool) {
	return time.Time{}, false
}

// sortManifestsByName sorts in place.
func sortManifestsByName(m []*Manifest) {
	for i := 1; i < len(m); i++ {
		for j := i; j > 0 && m[j-1].Name > m[j].Name; j-- {
			m[j-1], m[j] = m[j], m[j-1]
		}
	}
}

// WriteManifest helper for install-time: serializes a manifest to a
// manifest.json file inside a plugin directory. Kept here so the CLI
// and integration tests share one code path.
func WriteManifest(dir string, m *Manifest) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	buf, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "manifest.json"), buf, 0o644)
}
