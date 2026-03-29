package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/hc12r/broked/models"
)

// LineageNode represents a data asset or processing step in the lineage graph.
type LineageNode struct {
	ID         string `json:"id"`
	Type       string `json:"type"` // file, table, api, processing
	Name       string `json:"name"`
	SubType    string `json:"sub_type,omitempty"`    // for processing: transform, join, code, quality_check, sql_generate
	PipelineID string `json:"pipeline_id,omitempty"` // which pipeline owns this (processing nodes only)
	Pipeline   string `json:"pipeline,omitempty"`    // pipeline name (processing nodes only)
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
	Nodes []LineageNode `json:"nodes"`
	Edges []LineageEdge `json:"edges"`
}

// BuildLineageGraph scans all pipelines and constructs a lineage graph
// by walking actual DAG edges inside each pipeline.
func BuildLineageGraph(pipelines []models.Pipeline) *LineageGraph {
	assetNodes := make(map[string]LineageNode) // shared across pipelines, deduped by asset ID
	procNodes := make(map[string]LineageNode)  // pipeline-scoped processing nodes
	edgeSet := make(map[string]LineageEdge)    // deduped by from|to|pipeline_id

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

			switch n.Type {
			// Sources -> extract external asset
			case models.NodeTypeSourceFile:
				id := extractFileAsset(resolved.Config, assetNodes)
				lineageID[n.ID] = id
			case models.NodeTypeSourceAPI:
				id := extractAPIAsset(resolved.Config, assetNodes)
				lineageID[n.ID] = id
			case models.NodeTypeSourceDB:
				id := extractSourceDBAsset(resolved.Config, assetNodes)
				lineageID[n.ID] = id

			// Sinks -> extract external asset
			case models.NodeTypeSinkFile:
				id := extractFileAsset(resolved.Config, assetNodes)
				lineageID[n.ID] = id
			case models.NodeTypeSinkDB:
				tableName := findUpstreamTableName(n.ID, pipeNodes, p.Edges)
				id := extractSinkDBAsset(resolved.Config, tableName, assetNodes)
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

	return &LineageGraph{Nodes: allNodes, Edges: allEdges}
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
