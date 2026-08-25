// Package dbtmanifest reads dbt's manifest.json.
//
// ADR-025 makes a model, not a command, the unit of work, and the manifest
// is where the model DAG lives: every model, what it depends on, its tests,
// its materialization, and the columns dbt already knows about. This package
// is the reader for it and nothing else -- no execution, no Python, no
// process. That is deliberate: it is the one part of the dbt integration
// that ADR-026's runtime requirement does not gate, so it can be built and
// trusted before any of the machinery around it exists.
//
// # Reading a format someone else owns
//
// manifest.json is dbt's internal artifact. It carries a schema version, and
// the shape moves underneath that version: dbt 1.8.9 and 1.10.11 both write
// schema v12, and 1.10.11's nodes carry three fields 1.8.9's do not
// (doc_blocks, primary_key, time_spine).
//
// So this reader is built for a format that changes without announcing it:
//
//   - Only the fields the DAG needs are decoded. encoding/json ignores the
//     rest, so a version that adds fields is read without complaint --
//     which is exactly what happened between the two fixture versions.
//   - The schema version is checked against a known set and an unrecognised
//     one is refused by name, because a shape that has moved is better
//     reported than parsed into something plausible.
//   - Fields that are structurally optional are treated as optional. A
//     node's depends_on may have no "nodes" key at all; a reader that
//     assumed it would fail on a real manifest, which is how this was
//     found.
package dbtmanifest

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
)

// SupportedSchemaVersions are the manifest schema versions this reader is
// tested against. A manifest outside this set is refused rather than parsed:
// the fields below may still be present, but nothing has demonstrated it,
// and a wrong DAG is worse than a named refusal.
//
// Widening this set means adding a fixture from that version and letting the
// differential test below prove the shape still holds. ADR-025 requires
// exactly that, because a parser written against one version and never
// checked against another is the defect class that shipped in #348.
var SupportedSchemaVersions = []int{12}

// ResourceType is the kind of thing a manifest node is. dbt has more of
// these than the DAG needs; the ones here are the ones that execute.
type ResourceType string

const (
	ResourceModel     ResourceType = "model"
	ResourceSeed      ResourceType = "seed"
	ResourceTest      ResourceType = "test"
	ResourceSnapshot  ResourceType = "snapshot"
	ResourceAnalysis  ResourceType = "analysis"
	ResourceOperation ResourceType = "operation"
)

// Node is one executable resource in a dbt project.
type Node struct {
	// UniqueID is dbt's own identifier, e.g. "model.my_project.orders". It
	// is what depends_on refers to and what --select accepts, so it is the
	// identity this package uses rather than the bare name.
	UniqueID string
	Name     string
	Type     ResourceType
	// DependsOn lists the UniqueIDs this node needs built first. Empty for
	// a source-less model, and empty rather than absent when the manifest
	// omits the key entirely.
	DependsOn []string
	// Materialization is "table", "view", "incremental", "ephemeral", or
	// whatever a custom materialization is called. Empty when the manifest
	// does not say.
	Materialization string
	// Schema and Database are where dbt will build it. RelationName is the
	// fully-qualified, quoted relation dbt itself would write -- which is
	// what makes a built model addressable as an ADR-023 TableRef.
	Schema       string
	Database     string
	RelationName string
	// Columns are the columns dbt knows about, which is whatever the
	// project documented rather than the full set. Keyed by column name.
	Columns map[string]Column
	// Path is the file the resource came from, for error messages that
	// point at something a person can open.
	Path string
}

// Column is what dbt records about one column. DataType is only populated
// when the project declared it, so it is a hint rather than a schema.
type Column struct {
	Name        string
	Description string
	DataType    string
}

// Project is a parsed manifest: the nodes that execute, and the metadata
// needed to say which dbt wrote it.
type Project struct {
	// SchemaVersion is the integer from the manifest's schema URL.
	SchemaVersion int
	// DBTVersion is the dbt that wrote this manifest, useful in errors
	// because "which dbt produced this" is the first question when a
	// manifest does not parse as expected.
	DBTVersion  string
	ProjectName string

	// Nodes is every executable resource, keyed by UniqueID.
	Nodes map[string]Node
}

// Models returns the model nodes in a stable order.
func (p *Project) Models() []Node { return p.byType(ResourceModel) }

// Seeds returns the seed nodes in a stable order.
func (p *Project) Seeds() []Node { return p.byType(ResourceSeed) }

// Tests returns the test nodes in a stable order.
func (p *Project) Tests() []Node { return p.byType(ResourceTest) }

func (p *Project) byType(t ResourceType) []Node {
	out := make([]Node, 0, len(p.Nodes))
	for _, n := range p.Nodes {
		if n.Type == t {
			out = append(out, n)
		}
	}
	// Sorted by UniqueID so a caller's output does not depend on Go's map
	// iteration order, which would make a pipeline's node list unstable
	// between runs of the same project.
	sort.Slice(out, func(i, j int) bool { return out[i].UniqueID < out[j].UniqueID })
	return out
}

// BuildOrder returns the nodes in an order where every node follows
// everything it depends on, or reports a cycle.
//
// Dependencies on nodes outside this manifest are ignored rather than
// treated as missing: a manifest can legitimately reference something that
// is disabled or lives in a package, and refusing to order the DAG because
// of one is worse than ordering what is there.
func (p *Project) BuildOrder() ([]Node, error) {
	state := make(map[string]int, len(p.Nodes)) // 0 unvisited, 1 in progress, 2 done
	var out []Node
	var visit func(id string, path []string) error

	visit = func(id string, path []string) error {
		switch state[id] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("dependency cycle: %v -> %s", path, id)
		}
		state[id] = 1
		n := p.Nodes[id]
		deps := append([]string(nil), n.DependsOn...)
		sort.Strings(deps)
		for _, dep := range deps {
			if _, inManifest := p.Nodes[dep]; !inManifest {
				continue
			}
			if err := visit(dep, append(path, id)); err != nil {
				return err
			}
		}
		state[id] = 2
		out = append(out, n)
		return nil
	}

	ids := make([]string, 0, len(p.Nodes))
	for id := range p.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id, nil); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// rawManifest is the subset decoded from the file. Everything not named
// here is ignored by encoding/json, which is what makes a manifest from a
// newer dbt readable when it adds fields.
type rawManifest struct {
	Metadata struct {
		SchemaVersion string `json:"dbt_schema_version"`
		DBTVersion    string `json:"dbt_version"`
		ProjectName   string `json:"project_name"`
	} `json:"metadata"`
	Nodes map[string]rawNode `json:"nodes"`
}

type rawNode struct {
	UniqueID     string `json:"unique_id"`
	Name         string `json:"name"`
	ResourceType string `json:"resource_type"`
	Schema       string `json:"schema"`
	Database     string `json:"database"`
	RelationName string `json:"relation_name"`
	Path         string `json:"path"`
	DependsOn    struct {
		// Optional in practice: a manifest may omit it entirely for a
		// node with no node dependencies.
		Nodes []string `json:"nodes"`
	} `json:"depends_on"`
	Config struct {
		Materialized string `json:"materialized"`
	} `json:"config"`
	Columns map[string]struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		DataType    string `json:"data_type"`
	} `json:"columns"`
}

var schemaVersionPattern = regexp.MustCompile(`/v(\d+)\.json$`)

// ParseFile reads a manifest from disk.
func ParseFile(path string) (*Project, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dbt manifest: %w", err)
	}
	defer f.Close()
	p, err := Parse(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// ParseProject reads the manifest from a dbt project's target directory.
func ParseProject(projectDir string) (*Project, error) {
	return ParseFile(filepath.Join(projectDir, "target", "manifest.json"))
}

// Parse reads a manifest.
func Parse(r io.Reader) (*Project, error) {
	var raw rawManifest
	if err := json.NewDecoder(r).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode dbt manifest: %w", err)
	}

	version, err := schemaVersion(raw.Metadata.SchemaVersion)
	if err != nil {
		return nil, err
	}
	if !supported(version) {
		return nil, fmt.Errorf(
			"dbt manifest schema v%d is not supported (written by dbt %s); this build reads %v -- "+
				"the manifest format is dbt's own and changes between releases, so it is refused rather than "+
				"parsed into a shape that may have moved",
			version, orUnknown(raw.Metadata.DBTVersion), SupportedSchemaVersions)
	}

	p := &Project{
		SchemaVersion: version,
		DBTVersion:    raw.Metadata.DBTVersion,
		ProjectName:   raw.Metadata.ProjectName,
		Nodes:         make(map[string]Node, len(raw.Nodes)),
	}
	for id, rn := range raw.Nodes {
		uid := rn.UniqueID
		if uid == "" {
			// The map key is the unique id in every manifest seen, but
			// the field is the authority when both exist.
			uid = id
		}
		n := Node{
			UniqueID:        uid,
			Name:            rn.Name,
			Type:            ResourceType(rn.ResourceType),
			DependsOn:       rn.DependsOn.Nodes,
			Materialization: rn.Config.Materialized,
			Schema:          rn.Schema,
			Database:        rn.Database,
			RelationName:    rn.RelationName,
			Path:            rn.Path,
		}
		if n.DependsOn == nil {
			n.DependsOn = []string{}
		}
		if len(rn.Columns) > 0 {
			n.Columns = make(map[string]Column, len(rn.Columns))
			for cname, rc := range rn.Columns {
				name := rc.Name
				if name == "" {
					name = cname
				}
				n.Columns[name] = Column{
					Name:        name,
					Description: rc.Description,
					DataType:    rc.DataType,
				}
			}
		}
		p.Nodes[uid] = n
	}
	return p, nil
}

// schemaVersion pulls the integer out of dbt's schema URL, which looks like
// https://schemas.getdbt.com/dbt/manifest/v12.json.
func schemaVersion(url string) (int, error) {
	if url == "" {
		return 0, fmt.Errorf("dbt manifest has no dbt_schema_version; it may not be a manifest")
	}
	m := schemaVersionPattern.FindStringSubmatch(url)
	if m == nil {
		return 0, fmt.Errorf("dbt manifest schema version %q is not a recognised form", url)
	}
	v, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, fmt.Errorf("dbt manifest schema version %q: %w", url, err)
	}
	return v, nil
}

func supported(v int) bool {
	for _, s := range SupportedSchemaVersions {
		if s == v {
			return true
		}
	}
	return false
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
