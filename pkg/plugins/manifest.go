package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
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

	// PackagingVersion marks a manifest that ships as a distributable
	// package (ADR-016). 0 (absent) means a plain installed directory —
	// every pre-packaging manifest stays valid. Currently only version 1
	// exists.
	PackagingVersion int `json:"packaging_version,omitempty"`

	// Payloads lists the per-platform/runtime alternatives inside a
	// package archive. Installation selects the first feasible entry in
	// order and resolves Binary/Args from it; installed directories
	// created by older releases have none.
	Payloads []Payload `json:"payloads,omitempty"`

	// dir is the absolute path to the directory that contains this
	// manifest file. Populated by LoadManifest; not marshaled.
	dir string `json:"-"`
}

// Runtime classes a payload may declare (ADR-016). The class determines
// exactly two things: how install-time feasibility is checked and how the
// launch command is derived. All four are resolvable — native/python by
// probing the host, node/jvm by locating node/java. Unknown classes are
// install-time errors, never runtime surprises.
const (
	RuntimeNative = "native"
	RuntimePython = "python"
	RuntimeNode   = "node"
	RuntimeJVM    = "jvm"
)

var knownRuntimes = map[string]bool{
	RuntimeNative: true,
	RuntimePython: true,
	RuntimeNode:   true,
	RuntimeJVM:    true,
}

// CurrentPackagingVersion is the packaging_version this build writes, and
// the highest it can install. Additive changes bump it; older manifests
// stay valid (see Manifest.Validate).
const CurrentPackagingVersion = 1

// SupportedPackagingVersions lists every packaging_version this build can
// install. Advertised via /api/capabilities so a client can tell, before
// uploading, whether a package's format is understood here.
var SupportedPackagingVersions = []int{CurrentPackagingVersion}

// SupportedRuntimeClasses lists the runtime classes this build knows how
// to resolve and launch. Advertised via /api/capabilities. Whether a
// class is actually available on a given host (e.g. python3 present) is a
// separate, install-time check that fails with a named reason — this list
// is what the installer *understands*, not what happens to be installed.
var SupportedRuntimeClasses = []string{
	RuntimeNative, RuntimePython, RuntimeNode, RuntimeJVM,
}

// Payload is one installable alternative inside a package archive.
type Payload struct {
	// Runtime is the runtime class (RuntimeNative etc.).
	Runtime string `json:"runtime"`
	// OS/Arch use Go's GOOS/GOARCH vocabulary ("linux"/"amd64"...) or
	// "any" for platform-independent payloads (interpreted runtimes).
	OS   string `json:"os"`
	Arch string `json:"arch"`
	// Path is the payload's directory inside the archive, relative to
	// the archive root (e.g. "payloads/linux-amd64").
	Path string `json:"path"`
	// Entrypoint is the file to execute (native) or hand to the
	// resolved interpreter (python), relative to Path.
	Entrypoint string `json:"entrypoint"`
	// Args are fixed args between the entrypoint/interpreter and the
	// protocol command.
	Args []string `json:"args,omitempty"`
	// Requires maps a runtime name to a version constraint the host
	// must satisfy, e.g. {"python": ">=3.9"}. Only ">=MAJOR.MINOR" is
	// understood in packaging version 1.
	Requires map[string]string `json:"requires,omitempty"`
	// SHA256 is the canonical tree hash of the payload directory (see
	// HashPayloadTree). Mandatory; verified before anything installs.
	SHA256 string `json:"sha256"`
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

	// SupportsPlan declares that this node type implements the `plan`
	// command (ADR-013 M3): given a stream, it can break that stream's
	// work into independent units the host schedules and tracks
	// separately, instead of one blocking `read` for the whole stream.
	// Optional, defaults to false — `plan` is a refinement a plugin
	// author opts into per node type, not a requirement every source
	// must satisfy (agreed on issue #39: "optional per-stream
	// refinement step after discover"). Only meaningful for Kind ==
	// KindSource; Validate rejects it set on a sink or transform.
	SupportsPlan bool `json:"supports_plan,omitempty"`
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
	if m.Binary == "" && len(m.Payloads) == 0 {
		// A package manifest carries payloads instead; installation
		// resolves one of them into Binary/Args. An installed directory
		// must have Binary either way.
		return fmt.Errorf("binary is required")
	}
	if len(m.Payloads) > 0 && m.PackagingVersion != CurrentPackagingVersion {
		return fmt.Errorf("payloads require packaging_version %d, got %d", CurrentPackagingVersion, m.PackagingVersion)
	}
	for i, p := range m.Payloads {
		if !knownRuntimes[p.Runtime] {
			return fmt.Errorf("payloads[%d].runtime %q is not a known runtime class", i, p.Runtime)
		}
		if p.Runtime == RuntimeNative && (p.OS == "" || p.OS == "any" || p.Arch == "" || p.Arch == "any") {
			return fmt.Errorf("payloads[%d]: native payloads need concrete os and arch", i)
		}
		if p.Path == "" || p.Path != filepath.ToSlash(filepath.Clean(p.Path)) ||
			strings.HasPrefix(p.Path, "..") || strings.HasPrefix(p.Path, "/") {
			return fmt.Errorf("payloads[%d].path %q must be a clean relative path", i, p.Path)
		}
		if p.Entrypoint == "" {
			return fmt.Errorf("payloads[%d].entrypoint is required", i)
		}
		if len(p.SHA256) != 64 {
			return fmt.Errorf("payloads[%d].sha256 must be 64 hex chars", i)
		}
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
		if nt.SupportsPlan && nt.Kind != KindSource {
			return fmt.Errorf("node_types[%d].supports_plan is only valid for kind=source, got kind=%q",
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
