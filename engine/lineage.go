package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
)

// LineageNode represents a data asset or processing step in the lineage graph.
type LineageNode struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"` // file, table, api, processing
	Name       string           `json:"name"`
	SubType    string           `json:"sub_type,omitempty"`    // for processing: transform, join, code, quality_check, sql_generate
	PipelineID string           `json:"pipeline_id,omitempty"` // which pipeline owns this (processing nodes only)
	Pipeline   string           `json:"pipeline,omitempty"`    // pipeline name (processing nodes only)
	Metadata   *LineageMetadata `json:"metadata,omitempty"`
	// ColumnsOpaque means this node cannot say which of its output
	// columns came from which input, so no column edges are drawn
	// through it (ADR-039).
	//
	// It is a statement, not a gap. A code node runs arbitrary user code
	// and nothing available to this engine can trace a column through
	// it; saying so is the correct answer, and it is rendered as one.
	// Node-level lineage through the same node stays complete.
	ColumnsOpaque bool `json:"columns_opaque,omitempty"`
	// OpaqueReason is why, in terms a reader of the graph can act on.
	OpaqueReason string `json:"opaque_reason,omitempty"`
}

// LineageMetadata is the latest observed metadata for a dataset or processing
// step. It is deliberately observation-shaped: absent metadata means the
// system has not profiled that node, not that the dataset has no schema.
type LineageMetadata struct {
	Namespace   string          `json:"namespace,omitempty"`
	Dataset     string          `json:"dataset,omitempty"`
	RowCount    int             `json:"row_count,omitempty"`
	ColumnCount int             `json:"column_count,omitempty"`
	Columns     []LineageColumn `json:"columns,omitempty"`
	ObservedAt  *time.Time      `json:"observed_at,omitempty"`
}

type LineageColumn struct {
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
	// NullPct and UniquePct are meaningfully zero (a column with no nulls,
	// or no duplicate values, is real profiling data, not "unprofiled") —
	// no omitempty, unlike the sibling string/slice fields where empty
	// really does mean absent.
	NullPct   float64 `json:"null_pct"`
	UniquePct float64 `json:"unique_pct"`
	MinVal    string  `json:"min_val,omitempty"`
	MaxVal    string  `json:"max_val,omitempty"`
}

// LineageProfile is the profile payload persisted by NodeProfileStore for a
// pipeline node. RunID/ObservedAt are optional because older stores expose
// only the latest profile JSON, not its source run identity.
type LineageProfile struct {
	Profile    *DataProfile
	Schema     *SchemaSnapshot
	RunID      string
	ObservedAt *time.Time
	// Provenance is this node's execution record from the same run as the
	// profile, when the store keeps one (ADR-039). It is what can promote a
	// declared column edge to attested.
	Provenance *models.NodeProvenance
}

// LineageEdge represents data flow between nodes through a pipeline.
type LineageEdge struct {
	From       string `json:"from"`
	To         string `json:"to"`
	PipelineID string `json:"pipeline_id"`
	Pipeline   string `json:"pipeline"`
}

// LineageGraph is the full cross-pipeline data flow graph.
type LineageGraph struct {
	Nodes       []LineageNode       `json:"nodes"`
	Edges       []LineageEdge       `json:"edges"`
	ColumnEdges []LineageColumnEdge `json:"column_edges,omitempty"`
}

type LineageColumnEdge struct {
	From       string `json:"from"`
	FromColumn string `json:"from_column"`
	To         string `json:"to"`
	ToColumn   string `json:"to_column"`
	// Evidence is how this edge was established (ADR-039). It replaces a
	// Confidence float that was 0.7 on every edge ever produced, which
	// carried no information while reading as a calibrated probability.
	//
	// A consumer that wants only facts filters to "declared" and
	// "attested".
	Evidence EvidenceLevel `json:"evidence"`
	// MappingReason states the derivation in the pipeline's own terms:
	// "renamed from qty", "price * qty", "join key: id matched against
	// customer_id". It used to be the constant string "observed column
	// name match".
	MappingReason string `json:"mapping_reason"`
}

// BuildLineageGraph scans all pipelines and constructs a lineage graph
// by walking actual DAG edges inside each pipeline.
func BuildLineageGraph(pipelines []models.Pipeline) *LineageGraph {
	return buildLineageGraph(pipelines, nil)
}

// BuildLineageGraphWithProfiles adds observed schema/statistics and
// evidence-backed column mappings to the topology graph.
func BuildLineageGraphWithProfiles(pipelines []models.Pipeline, profiles map[string]LineageProfile) *LineageGraph {
	return buildLineageGraph(pipelines, profiles)
}

func buildLineageGraph(pipelines []models.Pipeline, profiles map[string]LineageProfile) *LineageGraph {
	assetNodes := make(map[string]LineageNode) // shared across pipelines, deduped by asset ID
	procNodes := make(map[string]LineageNode)  // pipeline-scoped processing nodes
	edgeSet := make(map[string]LineageEdge)    // deduped by from|to|pipeline_id
	columnEdgeSet := make(map[string]LineageColumnEdge)

	for _, p := range pipelines {
		// Resolve variables in configs
		varCtx := NewVariableContext(p.Params, "lineage", time.Now())

		// Build pipeline node map with resolved configs
		pipeNodes := make(map[string]models.Node, len(p.Nodes))
		for _, n := range p.Nodes {
			resolved := n
			resolved.Config = varCtx.ResolveConfig(n.Config)
			pipeNodes[n.ID] = resolved
		}

		// Map each pipeline node ID -> its lineage node ID
		lineageID := make(map[string]string, len(p.Nodes))

		for _, n := range p.Nodes {
			resolved := pipeNodes[n.ID]
			profile, hasProfile := profiles[profileKey(p.ID, n.ID)]

			switch n.Type {
			// Sources -> extract external asset
			case models.NodeTypeSourceFile:
				id := extractFileAsset(resolved.Config, assetNodes)
				if hasProfile {
					attachLineageProfile(assetNodes, id, id, profile)
				}
				lineageID[n.ID] = id
			case models.NodeTypeSourceAPI:
				id := extractAPIAsset(resolved.Config, assetNodes)
				if hasProfile {
					attachLineageProfile(assetNodes, id, id, profile)
				}
				lineageID[n.ID] = id
			case models.NodeTypeSourceDB:
				id := extractSourceDBAsset(resolved.Config, assetNodes)
				if hasProfile {
					attachLineageProfile(assetNodes, id, id, profile)
				}
				lineageID[n.ID] = id

			// Sinks -> extract external asset
			case models.NodeTypeSinkFile:
				id := extractFileAsset(resolved.Config, assetNodes)
				if hasProfile {
					attachLineageProfile(assetNodes, id, id, profile)
				}
				lineageID[n.ID] = id
			case models.NodeTypeSinkDB:
				tableName := findUpstreamTableName(n.ID, pipeNodes, p.Edges)
				id := extractSinkDBAsset(resolved.Config, tableName, assetNodes)
				if hasProfile {
					attachLineageProfile(assetNodes, id, id, profile)
				}
				lineageID[n.ID] = id

			// Processing nodes -> create pipeline-scoped lineage node
			default:
				procID := fmt.Sprintf("proc:%s:%s", p.ID, n.ID)
				name := n.Name
				if name == "" {
					name = string(n.Type)
				}
				procNodes[procID] = LineageNode{
					ID:         procID,
					Type:       "processing",
					Name:       name,
					SubType:    string(n.Type),
					PipelineID: p.ID,
					Pipeline:   p.Name,
				}
				lineageID[n.ID] = procID
				if hasProfile {
					attachLineageProfile(procNodes, procID, procID, profile)
				}

				// sql_generate also produces a table asset (its output)
				if n.Type == models.NodeTypeSQLGenerate {
					if table, _ := resolved.Config["table"].(string); table != "" {
						id := "table:" + table
						assetNodes[id] = LineageNode{ID: id, Type: "table", Name: table}
					}
				}
			}
		}

		// Convert pipeline edges to lineage edges
		for _, e := range p.Edges {
			fromLID, ok1 := lineageID[e.From]
			toLID, ok2 := lineageID[e.To]
			if !ok1 || !ok2 || fromLID == "" || toLID == "" {
				continue
			}

			key := fromLID + "|" + toLID + "|" + p.ID
			edgeSet[key] = LineageEdge{
				From:       fromLID,
				To:         toLID,
				PipelineID: p.ID,
				Pipeline:   p.Name,
			}

		}

		// Column edges, from what each node type declares (ADR-039).
		//
		// Per node rather than per edge, because the declaration is a
		// property of the node: a join's output depends on both its
		// inputs together, and asking one edge at a time cannot express
		// that.
		for _, n := range p.Nodes {
			toLID, ok := lineageID[n.ID]
			if !ok || toLID == "" {
				continue
			}

			req := ColumnLineageRequest{Node: pipeNodes[n.ID]}
			for _, e := range p.Edges {
				if e.To != n.ID {
					continue
				}
				fromLID, ok := lineageID[e.From]
				if !ok || fromLID == "" {
					continue
				}
				req.Inputs = append(req.Inputs, NodeInput{
					Node:    fromLID,
					Columns: observedColumns(profiles, p.ID, e.From),
				})
			}

			decl := ColumnLineageFor(req)
			// The run the profile came from stored this node's single input
			// and its output with the same digest: the bytes did not change,
			// so an identity edge from that input is proven, not only
			// declared.
			attestedFrom, attestedRun := unchangedInput(profiles, p.ID, n.ID, lineageID)
			if decl.Opaque {
				// The node says it cannot trace columns, and the graph
				// says so too rather than leaving a reader to wonder why
				// a node has no column edges. No edges are drawn through
				// it: a plausible claim about a black box is worse than
				// no claim.
				markOpaque(procNodes, toLID, decl.Reason)
				markOpaque(assetNodes, toLID, decl.Reason)
				continue
			}
			for _, d := range decl.Derivations {
				for _, src := range d.From {
					edge := LineageColumnEdge{
						From: src.Node, FromColumn: src.Column,
						To: toLID, ToColumn: d.Output,
						Evidence: d.Evidence, MappingReason: d.Rule,
					}
					if attestedFrom != "" && src.Node == attestedFrom && src.Column == d.Output {
						edge.Evidence = EvidenceAttested
						edge.MappingReason = d.Rule + "; run " + attestedRun + " stored identical bytes in and out"
					}
					columnEdgeSet[edge.From+"|"+edge.FromColumn+"|"+edge.To+"|"+edge.ToColumn] = edge
				}
			}
		}
	}

	// Merge all nodes
	allNodes := make([]LineageNode, 0, len(assetNodes)+len(procNodes))
	for _, n := range assetNodes {
		allNodes = append(allNodes, n)
	}
	for _, n := range procNodes {
		allNodes = append(allNodes, n)
	}

	// Collect edges
	allEdges := make([]LineageEdge, 0, len(edgeSet))
	for _, e := range edgeSet {
		allEdges = append(allEdges, e)
	}
	columnEdges := make([]LineageColumnEdge, 0, len(columnEdgeSet))
	for _, edge := range columnEdgeSet {
		columnEdges = append(columnEdges, edge)
	}

	return &LineageGraph{Nodes: allNodes, Edges: allEdges, ColumnEdges: columnEdges}
}

func profileKey(pipelineID, nodeID string) string { return pipelineID + ":" + nodeID }

// ProfileKey is the key profiles are looked up by, exported so a caller
// assembling profiles uses this one definition rather than rebuilding or
// parsing the format.
func ProfileKey(pipelineID, nodeID string) string { return profileKey(pipelineID, nodeID) }

func attachLineageProfile(nodes map[string]LineageNode, id, datasetID string, profile LineageProfile) {
	node, ok := nodes[id]
	if !ok || profile.Profile == nil {
		return
	}
	metadata := metadataFromProfile(datasetID, profile)
	// A shared asset (e.g. a file two pipelines both write to) can carry a
	// profile from each pipeline; keep whichever was actually observed most
	// recently. Column count says nothing about recency — an older, wider
	// schema must not beat a genuinely newer, narrower one.
	if node.Metadata == nil || node.Metadata.ObservedAt == nil ||
		(metadata.ObservedAt != nil && metadata.ObservedAt.After(*node.Metadata.ObservedAt)) {
		node.Metadata = metadata
	}
	nodes[id] = node
}

func metadataFromProfile(datasetID string, profile LineageProfile) *LineageMetadata {
	metadata := &LineageMetadata{Dataset: datasetID, ObservedAt: profile.ObservedAt}
	if profile.Profile == nil {
		return metadata
	}
	metadata.RowCount = profile.Profile.RowCount
	metadata.ColumnCount = profile.Profile.ColumnCount
	metadata.Columns = make([]LineageColumn, 0, len(profile.Profile.Columns))
	for _, column := range profile.Profile.Columns {
		metadata.Columns = append(metadata.Columns, LineageColumn{
			Name: column.Name, Type: column.Type, NullPct: column.NullPct,
			UniquePct: column.UniquePct, MinVal: column.MinVal, MaxVal: column.MaxVal,
		})
	}
	return metadata
}

// observedColumns returns the column names last observed on a node's
// output, or nothing when the pipeline has never run.
//
// A source's columns are not in the pipeline definition: a CSV's header
// is a fact about the file, not about the DAG. So the declared mapping
// rule comes from the node type and the column names come from the last
// execution. A pipeline that has never run still has a lineage graph;
// it just has no column edges to draw yet, which is the honest answer
// rather than a guess at what the file might contain.
func observedColumns(profiles map[string]LineageProfile, pipelineID, nodeID string) []string {
	profile, ok := profiles[profileKey(pipelineID, nodeID)]
	if !ok || profile.Profile == nil {
		return nil
	}
	out := make([]string, 0, len(profile.Profile.Columns))
	for _, c := range profile.Profile.Columns {
		out = append(out, c.Name)
	}
	return out
}

// unchangedInput reports the lineage ID of a node's input when the run
// its profile came from proves the node changed nothing: exactly one
// input, and that input and the output both stored with the same digest.
//
// One input only. With two, an output matching one of them says nothing
// about what happened to the other. And a digest on both sides, never an
// empty one on each: an absent digest means the dataset was not stored,
// which proves nothing, and "" == "" must not read as "identical".
func unchangedInput(profiles map[string]LineageProfile, pipelineID, nodeID string, lineageID map[string]string) (inputLID, runID string) {
	prof, ok := profiles[profileKey(pipelineID, nodeID)]
	if !ok || prof.Provenance == nil {
		return "", ""
	}
	rec := prof.Provenance
	if rec.Output == nil || len(rec.Inputs) != 1 {
		return "", ""
	}
	in := rec.Inputs[0]
	if in.Digest == "" || rec.Output.Digest == "" || in.Digest != rec.Output.Digest {
		return "", ""
	}
	return lineageID[in.Node], rec.RunID
}

// markOpaque records on the node that it cannot trace columns, and why.
//
// A no-op when the node is not in this map: a lineage ID is either an
// asset or a processing node, and the caller tries both rather than
// working out which.
func markOpaque(nodes map[string]LineageNode, id, reason string) {
	node, ok := nodes[id]
	if !ok {
		return
	}
	node.ColumnsOpaque = true
	node.OpaqueReason = reason
	nodes[id] = node
}

// --- Asset extraction helpers ---

func extractFileAsset(config map[string]interface{}, nodes map[string]LineageNode) string {
	path, _ := config["path"].(string)
	if path == "" {
		return ""
	}
	id := "file:" + path
	// Use the filename as display name
	name := path
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		name = path[idx+1:]
	}
	nodes[id] = LineageNode{ID: id, Type: "file", Name: name}
	return id
}

func extractAPIAsset(config map[string]interface{}, nodes map[string]LineageNode) string {
	url, _ := config["url"].(string)
	if url == "" {
		return ""
	}
	id := "api:" + url
	// Truncate to just host+path for display
	name := url
	if strings.HasPrefix(url, "http") {
		if idx := strings.Index(url[8:], "/"); idx >= 0 {
			name = url[:8+idx+1] + "..."
		}
	}
	nodes[id] = LineageNode{ID: id, Type: "api", Name: name}
	return id
}

func extractSourceDBAsset(config map[string]interface{}, nodes map[string]LineageNode) string {
	query, _ := config["query"].(string)
	table := extractTableFromQuery(query)
	id := "table:" + table
	nodes[id] = LineageNode{ID: id, Type: "table", Name: table}
	return id
}

func extractSinkDBAsset(config map[string]interface{}, upstreamTable string, nodes map[string]LineageNode) string {
	// Prefer the table name from upstream sql_generate
	tableName := upstreamTable
	if tableName == "" {
		// Try to extract from URI or table config
		if t, _ := config["table"].(string); t != "" {
			tableName = t
		} else {
			// Fall back to extracting DB name from URI
			uri, _ := config["uri"].(string)
			tableName = extractDBName(uri)
		}
	}
	id := "table:" + tableName
	nodes[id] = LineageNode{ID: id, Type: "table", Name: tableName}
	return id
}

// findUpstreamTableName walks backward from a sink_db node to find
// an upstream sql_generate node's table config.
func findUpstreamTableName(sinkNodeID string, pipeNodes map[string]models.Node, edges []models.Edge) string {
	// BFS backward through edges
	visited := make(map[string]bool)
	queue := []string{sinkNodeID}
	visited[sinkNodeID] = true

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		// Find all nodes that have an edge TO current
		for _, e := range edges {
			if e.To == current && !visited[e.From] {
				visited[e.From] = true
				if n, ok := pipeNodes[e.From]; ok {
					if n.Type == models.NodeTypeSQLGenerate {
						if table, _ := n.Config["table"].(string); table != "" {
							return table
						}
					}
				}
				queue = append(queue, e.From)
			}
		}
	}
	return ""
}

// extractDBName pulls a database name from a connection URI.
func extractDBName(uri string) string {
	if uri == "" {
		return "unknown_db"
	}
	// Handle postgres://user:pass@host:port/dbname
	if idx := strings.LastIndex(uri, "/"); idx >= 0 {
		name := uri[idx+1:]
		// Strip query params
		if qi := strings.Index(name, "?"); qi >= 0 {
			name = name[:qi]
		}
		if name != "" {
			return name
		}
	}
	return "unknown_db"
}

// extractTableFromQuery does basic extraction of table name from SQL.
func extractTableFromQuery(query string) string {
	lower := strings.ToLower(query)

	// Try FROM <table>
	patterns := []string{"from ", "join "}
	for _, pat := range patterns {
		idx := strings.Index(lower, pat)
		if idx < 0 {
			continue
		}
		start := idx + len(pat)
		// Skip whitespace
		for start < len(lower) && (lower[start] == ' ' || lower[start] == '\t') {
			start++
		}
		end := start
		for end < len(lower) && lower[end] != ' ' && lower[end] != '\n' && lower[end] != '\t' && lower[end] != ';' && lower[end] != ',' && lower[end] != ')' {
			end++
		}
		if end > start {
			return query[start:end]
		}
	}
	return "unknown_table"
}
