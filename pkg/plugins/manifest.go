package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Manifest describes one installed plugin. It's a cached snapshot of
// what the plugin's `spec` command emitted at install time, plus the
// install metadata (where on disk the binary lives). The host reads
// manifests at startup and uses them to register node types — it
// never executes the plugin just to learn about it.
//
// On disk, a manifest lives at
//
//	<plugin-dir>/manifest.json
//
// with the executable binary/entry point alongside it, referenced by
// the relative Binary field. The plugin-dir name doubles as the
// plugin's installed identity:
//
//	~/.brokoli/plugins/
//	    snowflake/
//	        manifest.json
//	        bin               # shell script / compiled binary / venv shim
//	    hello/
//	        manifest.json
//	        bin
type Manifest struct {
	// ProtocolVersion is the plugin protocol the plugin was built for.
	// Refused at load time if not in SupportedProtocolVersions.
	ProtocolVersion int `json:"protocol_version"`

	// Name is the plugin's canonical identifier — lowercase, no spaces,
	// matches the directory name under ~/.brokoli/plugins/.
	Name string `json:"name"`

	// Version is the plugin's own semver. Independent of Brokoli's.
	Version string `json:"version"`

	// Description is a one-line summary shown in `brokoli plugins list`
	// and in the UI's connector picker.
	Description string `json:"description,omitempty"`

	// Author and Homepage are free-form metadata for the UI/registry.
	Author   string `json:"author,omitempty"`
	Homepage string `json:"homepage,omitempty"`

	// Binary is the path to the plugin executable, relative to the
	// manifest's directory. Resolved to an absolute path at load time.
	Binary string `json:"binary"`

	// Args are the fixed leading args passed to Binary on every
	// invocation, before the command name. Used by shim launchers —
	// e.g. a Python plugin's Binary might be "python3" with Args
	// ["-m", "brokoli_connector_snowflake"].
	Args []string `json:"args,omitempty"`

	// NodeTypes declares the pipeline node types this plugin
	// implements. Each one shows up in the pipeline editor node
	// palette and routes to this plugin at execution time.
	NodeTypes []NodeTypeDecl `json:"node_types"`

	// ConfigSchema is a JSON Schema describing the config fields the
	// plugin's source/sink nodes accept. The UI renders a form from
	// this schema; the host validates submitted configs against it
	// before passing them to the plugin.
	//
	// Kept as raw JSON so the plugin can use any valid JSON Schema
	// feature without us having to model it in Go.
	ConfigSchema json.RawMessage `json:"config_schema,omitempty"`

	// dir is the absolute path to the directory that contains this
	// manifest file. Populated by LoadManifest; not marshaled.
	dir string `json:"-"`
}

// NodeTypeDecl is one node type a plugin implements.
type NodeTypeDecl struct {
	// Type is the unique node type identifier, e.g. "source_snowflake",
	// "sink_snowflake". The host uses this as the dispatch key when
	// routing pipeline nodes to the plugin.
	Type string `json:"type"`

	// Kind is one of "source", "sink", "transform". The host uses this
	// to decide whether to pipe input records in via stdin (sink),
	// collect output records from stdout (source), or do both
	// (transform).
	Kind NodeKind `json:"kind"`

	// DisplayName is shown in the pipeline editor palette.
	DisplayName string `json:"display_name,omitempty"`

	// Description is the one-line help shown under the display name.
	Description string `json:"description,omitempty"`

	// Icon is an optional icon identifier the UI can render next to
	// the node. Interpreted client-side.
	Icon string `json:"icon,omitempty"`
}

// NodeKind categorizes what a plugin node type does at execution time.
type NodeKind string

const (
	KindSource    NodeKind = "source"
	KindSink      NodeKind = "sink"
	KindTransform NodeKind = "transform"
)

// Dir returns the absolute directory containing this manifest on disk.
func (m *Manifest) Dir() string { return m.dir }

// BinaryPath returns the absolute path to the plugin's executable,
// resolving Binary against the manifest directory.
func (m *Manifest) BinaryPath() string {
	if filepath.IsAbs(m.Binary) {
		return m.Binary
	}
	return filepath.Join(m.dir, m.Binary)
}

// Validate checks a manifest for the minimum fields the host needs to
// load it. Returns a human-readable error identifying the problem;
// callers log it and skip the plugin rather than failing the whole
// startup.
func (m *Manifest) Validate() error {
	if !IsProtocolVersionSupported(m.ProtocolVersion) {
		return fmt.Errorf("unsupported protocol version %d (host speaks %v)",
			m.ProtocolVersion, SupportedProtocolVersions)
	}
	if !pluginNameRE.MatchString(m.Name) {
		return fmt.Errorf("invalid plugin name %q (must match %s)",
			m.Name, pluginNameRE.String())
	}
	if m.Version == "" {
		return fmt.Errorf("version is required")
	}
	if m.Binary == "" {
		return fmt.Errorf("binary is required")
	}
	if len(m.NodeTypes) == 0 {
		return fmt.Errorf("plugin declares no node types — at least one is required")
	}
	seen := make(map[string]bool, len(m.NodeTypes))
	for i, nt := range m.NodeTypes {
		if nt.Type == "" {
			return fmt.Errorf("node_types[%d].type is empty", i)
		}
		if seen[nt.Type] {
			return fmt.Errorf("duplicate node type %q in manifest", nt.Type)
		}
		seen[nt.Type] = true
		switch nt.Kind {
		case KindSource, KindSink, KindTransform:
		default:
			return fmt.Errorf("node_types[%d].kind must be source/sink/transform, got %q",
				i, nt.Kind)
		}
	}
	return nil
}

// LoadManifest reads and validates a manifest.json file from the given
// directory. The directory itself is recorded on the returned Manifest
// so BinaryPath() can resolve relative paths without reparsing.
func LoadManifest(dir string) (*Manifest, error) {
	path := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin dir %s: %w", dir, err)
	}
	m.dir = absDir
	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("invalid manifest %s: %w", path, err)
	}
	return &m, nil
}

// pluginNameRE restricts plugin names to lowercase identifiers so they
// can safely double as directory names on every filesystem we care
// about (Linux case-sensitive, macOS case-insensitive, Windows).
var pluginNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
