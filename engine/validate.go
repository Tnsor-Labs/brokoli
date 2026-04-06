package engine

import (
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/models"
)

// ValidationError holds all issues found during validation.
type ValidationError struct {
	Errors []string `json:"errors"`
}

func (v *ValidationError) Error() string {
	return strings.Join(v.Errors, "; ")
}

func (v *ValidationError) Add(msg string) {
	v.Errors = append(v.Errors, msg)
}

func (v *ValidationError) HasErrors() bool {
	return len(v.Errors) > 0
}

// ValidatePipeline checks a pipeline for structural and config issues.
func ValidatePipeline(p *models.Pipeline) *ValidationError {
	ve := &ValidationError{}

	if p.Name == "" {
		ve.Add("Pipeline name is required")
	}

	if len(p.Nodes) == 0 {
		ve.Add("Pipeline must have at least one node")
		return ve
	}

	// Check for duplicate node IDs
	nodeIDs := make(map[string]bool)
	for _, n := range p.Nodes {
		if n.ID == "" {
			ve.Add(fmt.Sprintf("Node %q has empty ID", n.Name))
			continue
		}
		if nodeIDs[n.ID] {
			ve.Add(fmt.Sprintf("Duplicate node ID: %s", n.ID))
		}
		nodeIDs[n.ID] = true
	}

	// Check edges reference valid nodes
	for _, e := range p.Edges {
		if !nodeIDs[e.From] {
			ve.Add(fmt.Sprintf("Edge references unknown source node: %s", e.From))
		}
		if !nodeIDs[e.To] {
			ve.Add(fmt.Sprintf("Edge references unknown target node: %s", e.To))
		}
		if e.From == e.To {
			ve.Add(fmt.Sprintf("Self-loop on node: %s", e.From))
		}
	}

	// Check for cycles
	if _, err := topoSort(p.Nodes, p.Edges); err != nil {
		ve.Add("Pipeline contains a cycle")
	}

	// Check at least one source node
	hasSource := false
	for _, n := range p.Nodes {
		switch n.Type {
		case models.NodeTypeSourceFile, models.NodeTypeSourceAPI, models.NodeTypeSourceDB:
			hasSource = true
		}
	}
	if !hasSource {
		ve.Add("Pipeline must have at least one source node (source_file, source_api, or source_db)")
	}

	// Check disconnected nodes
	connected := make(map[string]bool)
	for _, e := range p.Edges {
		connected[e.From] = true
		connected[e.To] = true
	}
	if len(p.Nodes) > 1 {
		for _, n := range p.Nodes {
			if !connected[n.ID] {
				ve.Add(fmt.Sprintf("Node %q (%s) is disconnected", n.Name, n.ID))
			}
		}
	}

	// Check required config per node type
	for _, n := range p.Nodes {
		validateNodeConfig(n, ve)
	}

	return ve
}

func validateNodeConfig(n models.Node, ve *ValidationError) {
	switch n.Type {
	case models.NodeTypeSourceFile:
		if getStr(n.Config, "path") == "" {
			ve.Add(fmt.Sprintf("Node %q: 'path' is required for source_file", n.Name))
		}
	case models.NodeTypeSourceAPI:
		if getStr(n.Config, "url") == "" {
			ve.Add(fmt.Sprintf("Node %q: 'url' is required for source_api", n.Name))
		}
	case models.NodeTypeSourceDB:
		if getStr(n.Config, "uri") == "" {
			ve.Add(fmt.Sprintf("Node %q: 'uri' is required for source_db", n.Name))
		}
		if getStr(n.Config, "query") == "" {
			ve.Add(fmt.Sprintf("Node %q: 'query' is required for source_db", n.Name))
		}
	case models.NodeTypeSQLGenerate:
		if getStr(n.Config, "table") == "" {
			ve.Add(fmt.Sprintf("Node %q: 'table' is required for sql_generate", n.Name))
		}
	case models.NodeTypeSinkFile:
		if getStr(n.Config, "path") == "" {
			ve.Add(fmt.Sprintf("Node %q: 'path' is required for sink_file", n.Name))
		}
	case models.NodeTypeSinkDB:
		if getStr(n.Config, "uri") == "" {
			ve.Add(fmt.Sprintf("Node %q: 'uri' is required for sink_db", n.Name))
		}
	}
}

func getStr(m map[string]interface{}, key string) string {
	v, _ := m[key].(string)
	return v
}

// NodeValidationResult holds per-node validation issues.
type NodeValidationResult struct {
	NodeID   string   `json:"node_id"`
	NodeName string   `json:"node_name"`
	Errors   []string `json:"errors"`
	Warnings []string `json:"warnings"`
}

// ValidateNodes checks each node's config individually and returns per-node results.
// Only nodes with issues are returned.
func ValidateNodes(nodes []models.Node) []NodeValidationResult {
	var results []NodeValidationResult

	for _, n := range nodes {
		r := NodeValidationResult{NodeID: n.ID, NodeName: n.Name}
		validateNodeConfigDetailed(n, &r)
		if len(r.Errors) > 0 || len(r.Warnings) > 0 {
			results = append(results, r)
		}
	}
	return results
}

func validateNodeConfigDetailed(n models.Node, r *NodeValidationResult) {
	switch n.Type {
	case models.NodeTypeSourceFile:
		if getStr(n.Config, "path") == "" {
			r.Errors = append(r.Errors, "'path' is required")
		}
	case models.NodeTypeSourceAPI:
		if getStr(n.Config, "url") == "" {
			r.Errors = append(r.Errors, "'url' is required")
		}
		if getStr(n.Config, "method") == "" {
			r.Warnings = append(r.Warnings, "'method' not set, defaults to GET")
		}
	case models.NodeTypeSourceDB:
		if getStr(n.Config, "uri") == "" {
			r.Errors = append(r.Errors, "'uri' is required")
		}
		if getStr(n.Config, "query") == "" {
			r.Errors = append(r.Errors, "'query' is required")
		}
	case models.NodeTypeSQLGenerate:
		if getStr(n.Config, "table") == "" {
			r.Errors = append(r.Errors, "'table' is required")
		}
		if getStr(n.Config, "dialect") == "" {
			r.Warnings = append(r.Warnings, "'dialect' not set, defaults to generic")
		}
	case models.NodeTypeSinkFile:
		if getStr(n.Config, "path") == "" {
			r.Errors = append(r.Errors, "'path' is required")
		}
	case models.NodeTypeSinkDB:
		if getStr(n.Config, "uri") == "" {
			r.Errors = append(r.Errors, "'uri' is required")
		}
	case models.NodeTypeCode:
		if getStr(n.Config, "script") == "" {
			r.Errors = append(r.Errors, "'script' is required")
		}
	case models.NodeTypeTransform:
		// Check if rules exist
		if rules, ok := n.Config["rules"]; ok {
			if arr, ok := rules.([]interface{}); ok && len(arr) == 0 {
				r.Warnings = append(r.Warnings, "no transform rules defined")
			}
		} else {
			r.Warnings = append(r.Warnings, "no transform rules defined")
		}
	case models.NodeTypeJoin:
		if getStr(n.Config, "join_type") == "" {
			r.Warnings = append(r.Warnings, "'join_type' not set, defaults to inner")
		}
	}
}
