package engine

import (
	"fmt"
	"strings"

	"github.com/Tnsor-Labs/brokoli/extensions"
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
//
// executors is optional (variadic so existing callers compile unchanged).
// An external node type is executable only when a non-nil executor claims it
// through CanHandle. Node capabilities and NodeKindDeclarer affect structural
// validation only; they do not authorize execution.
func ValidatePipeline(p *models.Pipeline, executors ...extensions.NodeExecutor) *ValidationError {
	ve := &ValidationError{}

	if p.Name == "" {
		ve.Add("Pipeline name is required")
	}

	if !models.IsIRVersionSupported(p.IRVersion) {
		ve.Add(fmt.Sprintf("Unsupported pipeline IR version %q (supported: %s)", p.IRVersion, strings.Join(models.SupportedIRVersions, ", ")))
	}

	if len(p.Nodes) == 0 {
		ve.Add("Pipeline must have at least one node")
		return ve
	}

	// Check for duplicate node IDs
	nodeIDs := make(map[string]bool)
	nodeTypes := make(map[string]models.NodeType)
	for _, n := range p.Nodes {
		if n.ID == "" {
			ve.Add(fmt.Sprintf("Node %q has empty ID", n.Name))
		} else {
			if nodeIDs[n.ID] {
				ve.Add(fmt.Sprintf("Duplicate node ID: %s", n.ID))
			}
			nodeIDs[n.ID] = true
			nodeTypes[n.ID] = n.Type
		}
		if !IsBuiltInNodeType(n.Type) && !executorCanHandle(n.Type, executors) {
			ve.Add(fmt.Sprintf("Node %q (%s) has unsupported type %q", n.Name, n.ID, n.Type))
		}
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
		if e.Condition != nil {
			if p.IRVersion != models.ConditionalEdgesIRVersion {
				ve.Add(fmt.Sprintf("Conditional edge %s -> %s requires pipeline IR %s", e.From, e.To, models.ConditionalEdgesIRVersion))
			}
			if nodeTypes[e.From] != models.NodeTypeCondition {
				ve.Add(fmt.Sprintf("Conditional edge %s -> %s must originate from a condition node", e.From, e.To))
			}
		}
		if p.IRVersion == models.ConditionalEdgesIRVersion && nodeTypes[e.From] == models.NodeTypeCondition && e.Condition == nil {
			ve.Add(fmt.Sprintf("Condition node edge %s -> %s requires an explicit true or false branch", e.From, e.To))
		}
	}

	// Check semantic connection rules
	validateEdgeSemantics(p.IRVersion, p.Nodes, p.Edges, ve, executors)

	// Check for cycles
	if _, err := topoSort(p.Nodes, p.Edges); err != nil {
		ve.Add("Pipeline contains a cycle")
	}

	// Check at least one source node (dbt and migrate also produce/handle data without pipeline inputs)
	hasSource := false
	for _, n := range p.Nodes {
		if nodeIsSourceCapable(n, executors) {
			hasSource = true
			break
		}
	}
	if !hasSource {
		ve.Add("Pipeline must have at least one source node (source_file, source_api, source_db, dbt, or migrate)")
	}

	// Check disconnected nodes
	connected := make(map[string]bool)
	for _, e := range p.Edges {
		connected[e.From] = true
		connected[e.To] = true
	}
	if len(p.Nodes) > 1 {
		for _, n := range p.Nodes {
			if n.Type == models.NodeTypeMigrate {
				continue // migrate is intentionally standalone with no edges
			}
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

// IsBuiltInNodeType reports whether the in-process runner implements nodeType.
// Keep this list aligned with Runner.runNodeLogic, including condition's
// control-plane dispatch before the built-in switch.
func IsBuiltInNodeType(nodeType models.NodeType) bool {
	switch nodeType {
	case models.NodeTypeSourceFile, models.NodeTypeSourceAPI, models.NodeTypeSourceDB,
		models.NodeTypeTransform, models.NodeTypeQualityCheck, models.NodeTypeSQLGenerate,
		models.NodeTypeCode, models.NodeTypeJoin, models.NodeTypeSinkFile,
		models.NodeTypeSinkDB, models.NodeTypeSinkAPI, models.NodeTypeMigrate,
		models.NodeTypeCondition, models.NodeTypeDBT, models.NodeTypeNotify,
		models.NodeTypeUnion, models.NodeTypeDatasetMap, models.NodeTypeDatasetFilter:
		return true
	}
	return false
}

func executorCanHandle(nodeType models.NodeType, executors []extensions.NodeExecutor) bool {
	for _, exec := range executors {
		if exec != nil && exec.CanHandle(string(nodeType)) {
			return true
		}
	}
	return false
}

func validateEdgeSemantics(irVersion string, nodes []models.Node, edges []models.Edge, ve *ValidationError, executors []extensions.NodeExecutor) {
	nodesByID := make(map[string]models.Node, len(nodes))
	for _, n := range nodes {
		nodesByID[n.ID] = n
	}

	inputDegree := make(map[string]int)

	for _, e := range edges {
		fromNode := nodesByID[e.From]
		toNode := nodesByID[e.To]

		switch fromNode.Type {
		case models.NodeTypeSinkFile, models.NodeTypeSinkDB, models.NodeTypeSinkAPI,
			models.NodeTypeNotify, models.NodeTypeMigrate:
			ve.Add(fmt.Sprintf("Invalid connection: node %q (type %s) cannot have outgoing edges", e.From, fromNode.Type))
		}

		if nodeIsSourceCapable(toNode, executors) {
			ve.Add(fmt.Sprintf("Invalid connection: node %q (type %s) cannot receive incoming edges", e.To, toNode.Type))
		}

		inputDegree[e.To]++
	}

	for _, n := range nodes {
		switch n.Type {
		case models.NodeTypeCondition:
			if irVersion == models.ConditionalEdgesIRVersion {
				if count := inputDegree[n.ID]; count != 1 {
					ve.Add(fmt.Sprintf("Node %q (condition) must have exactly 1 input, got %d", n.Name, count))
				}
				expression, ok := n.Config["expression"].(string)
				if !ok || !models.IsConditionExpressionSupported(expression) {
					ve.Add(fmt.Sprintf("Node %q (condition) has an unsupported expression", n.Name))
				}
			}
		case models.NodeTypeJoin:
			count := inputDegree[n.ID]
			if count != 2 {
				ve.Add(fmt.Sprintf("Node %q (join) must have exactly 2 inputs, got %d", n.Name, count))
			}
		case models.NodeTypeUnion:
			// Matches brokoli-sdk's own union()/collect() requirement
			// (_build_union_node: "union()/collect() requires at least
			// one upstream ref") plus the semantic requirement that
			// union of fewer than 2 inputs isn't a union at all — fail
			// loudly at deploy time instead of the previous silent
			// pass-through of whatever single input happened to exist.
			//
			// Exception (#31): exactly 1 incoming edge is allowed when
			// that sole upstream node is a dynamic-expansion `code` node
			// (carries an `expansion` config block). This is the exact
			// compiled shape of `CollectionRef.collect(mode="union")` on
			// a `.expand()` node — brokoli-sdk's _build_union_node always
			// emits one edge per ref, and .expand() only ever produces
			// one CollectionRef — so a single edge here is a legitimate
			// "identity union" this issue introduces, not a gap in this
			// check. See runUnion (engine/node_handlers.go) for the
			// matching runtime pass-through.
			count := inputDegree[n.ID]
			if count < 2 && !(count == 1 && unionFedBySingleExpansion(n.ID, edges, nodesByID)) {
				ve.Add(fmt.Sprintf("Node %q (union) requires at least 2 incoming edges, got %d", n.Name, count))
			}
		}
	}
}

// unionFedBySingleExpansion reports whether unionNodeID has exactly one
// incoming edge and that edge's source is a dynamic-expansion `code` node
// (see nodeHasExpansion) — the one case validateEdgeSemantics allows a
// union node to have fewer than 2 incoming edges.
func unionFedBySingleExpansion(unionNodeID string, edges []models.Edge, nodesByID map[string]models.Node) bool {
	var from string
	count := 0
	for _, e := range edges {
		if e.To == unionNodeID {
			count++
			from = e.From
		}
	}
	if count != 1 {
		return false
	}
	return nodeHasExpansion(nodesByID[from])
}

// functionRefConfigError checks that cfg carries a well-formed function
// reference for a dataset_map/dataset_filter node — i.e. the shape
// brokoli-sdk's DatasetRef.map()/.filter() actually emit:
// {"function": {"name": "..."}}. Returns "" when valid, or a
// human-readable message describing what's wrong/missing.
//
// This deliberately does NOT require config.function.script — today's
// real brokoli-sdk payloads never include it (see functionRefScript in
// engine/partition_transform.go for why), so requiring it here would
// reject every pipeline using this real, merged SDK feature outright.
// Its absence is instead a clear, specific *execution-time* error
// (ExecutePartitionTransform), not a deploy-time validation failure —
// the config is well-formed per the SDK's own contract even though the
// engine can't yet execute it.
func functionRefConfigError(cfg map[string]interface{}, kind string) string {
	raw, ok := cfg["function"]
	if !ok {
		return fmt.Sprintf("'function' reference is required for %s (e.g. {\"function\": {\"name\": \"my_func\"}})", kind)
	}
	fnRef, ok := raw.(map[string]interface{})
	if !ok {
		return fmt.Sprintf("'function' must be an object with a 'name' for %s", kind)
	}
	if getStr(fnRef, "name") == "" {
		return fmt.Sprintf("'function.name' is required for %s", kind)
	}
	return ""
}

// nodeIsSourceCapable reports whether a node can act as a pipeline source
// (i.e. it may have no incoming edges and satisfies the "must have a
// source" structural rule). IR v2 pipelines declare this explicitly via
// Node.Capabilities, which lets decorator-based nodes (e.g. Type ==
// "code" wrapping a user function tagged @source) count as sources even
// though their Type isn't one of the built-in source types. Nodes with
// an empty Capabilities slice next check executors for a
// NodeKindDeclarer that recognizes the type (e.g. a plugin manifest's
// declared Kind — Tnsor-Labs/brokoli#62); failing that, they fall back
// to the hardcoded type list so pre-Phase-0 pipelines validate exactly
// as before.
func nodeIsSourceCapable(n models.Node, executors []extensions.NodeExecutor) bool {
	if len(n.Capabilities) > 0 {
		for _, c := range n.Capabilities {
			if c == models.CapabilitySource {
				return true
			}
		}
		return false
	}
	for _, exec := range executors {
		declarer, ok := exec.(extensions.NodeKindDeclarer)
		if !ok {
			continue
		}
		caps, found := declarer.DeclaredCapabilities(string(n.Type))
		if !found {
			continue
		}
		for _, c := range caps {
			if c == models.CapabilitySource {
				return true
			}
		}
		return false
	}
	switch n.Type {
	case models.NodeTypeSourceFile, models.NodeTypeSourceAPI, models.NodeTypeSourceDB,
		models.NodeTypeDBT, models.NodeTypeMigrate:
		return true
	}
	return false
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
		if getStr(n.Config, "uri") == "" && getStr(n.Config, "conn_id") == "" {
			ve.Add(fmt.Sprintf("Node %q: 'uri' or 'conn_id' is required for source_db", n.Name))
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
		if getStr(n.Config, "uri") == "" && getStr(n.Config, "conn_id") == "" {
			ve.Add(fmt.Sprintf("Node %q: 'uri' or 'conn_id' is required for sink_db", n.Name))
		}
		// ADR-027 phase 3: the upsert refusal moves to validation time,
		// where the editor shows it, rather than waiting for a run to
		// fail. Only the explicit-uri form is checkable statically; a
		// conn_id resolves at run time and the same refusal catches it
		// there (refuseUnearnedWrite).
		if strings.HasPrefix(getStr(n.Config, "uri"), "clickhouse://") &&
			strings.EqualFold(strings.TrimSpace(getStr(n.Config, "mode")), ModeUpsert) {
			ve.Add(fmt.Sprintf(
				"Node %q: ClickHouse has no upsert -- there is no synchronous merge to map mode: upsert "+
					"onto. ReplacingMergeTree deduplicates eventually, at merge time, which is a different "+
					"promise; append into a ReplacingMergeTree table you create if eventual dedup is what "+
					"you want", n.Name))
		}
	case models.NodeTypeUnion:
		if mode := getStr(n.Config, "mode"); mode != "" && mode != "union" {
			ve.Add(fmt.Sprintf("Node %q: union only supports mode=\"union\" (got %q)", n.Name, mode))
		}
	case models.NodeTypeDatasetMap:
		if msg := functionRefConfigError(n.Config, "dataset_map"); msg != "" {
			ve.Add(fmt.Sprintf("Node %q: %s", n.Name, msg))
		}
	case models.NodeTypeDatasetFilter:
		if msg := functionRefConfigError(n.Config, "dataset_filter"); msg != "" {
			ve.Add(fmt.Sprintf("Node %q: %s", n.Name, msg))
		}
	case models.NodeTypeCode:
		if nodeHasExpansion(n) {
			// parseExpansionConfig's errors already name the node
			// (Name/ID) themselves — see engine/expansion.go — so append
			// as-is rather than re-wrapping with another "Node %q:" prefix.
			if _, err := parseExpansionConfig(n); err != nil {
				ve.Add(err.Error())
			}
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

	writtenHere := sinkFilePaths(nodes)
	for _, n := range nodes {
		r := NodeValidationResult{NodeID: n.ID, NodeName: n.Name}
		validateNodeConfigDetailed(n, &r)
		validateFileStorage(n, writtenHere, &r)
		if len(r.Errors) > 0 || len(r.Warnings) > 0 {
			results = append(results, r)
		}
	}
	return results
}

// sinkFilePaths collects the paths this pipeline writes for itself.
func sinkFilePaths(nodes []models.Node) map[string]bool {
	paths := map[string]bool{}
	for _, n := range nodes {
		if n.Type == models.NodeTypeSinkFile {
			if p := getStr(n.Config, "path"); p != "" {
				paths[p] = true
			}
		}
	}
	return paths
}

// validateFileStorage warns about file dependencies this deployment
// cannot honour reliably.
//
// A pipeline that writes a file and reads it back in the same run is
// safe: every node of a run executes on one worker. A pipeline that reads
// a file *nobody in it wrote* depends on something outside the run — an
// earlier run, or an operator's upload — and with per-worker filesystems
// that dependency is a coin flip. The author is told at deploy time,
// which is the last moment where changing the design is cheap.
func validateFileStorage(n models.Node, writtenHere map[string]bool, r *NodeValidationResult) {
	if n.Type != models.NodeTypeSourceFile || !unsharedFileStorage() {
		return
	}
	path := getStr(n.Config, "path")
	if path == "" || writtenHere[path] {
		return
	}
	r.Warnings = append(r.Warnings,
		"reads a file this pipeline does not write, and this deployment gives each worker its own filesystem: "+
			"the run may land on a worker without the file, or read a stale copy left by an older run. "+
			"Mount shared storage at the data directories and set BROKOLI_DATA_DIRS_SHARED=1, "+
			"or produce the file within this pipeline")
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
		if getStr(n.Config, "uri") == "" && getStr(n.Config, "conn_id") == "" {
			r.Errors = append(r.Errors, "'uri' or 'conn_id' is required")
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
		if getStr(n.Config, "uri") == "" && getStr(n.Config, "conn_id") == "" {
			r.Errors = append(r.Errors, "'uri' or 'conn_id' is required")
		}
	case models.NodeTypeCode:
		if getStr(n.Config, "script") == "" {
			r.Errors = append(r.Errors, "'script' is required")
		}
		if nodeHasExpansion(n) {
			if _, err := parseExpansionConfig(n); err != nil {
				r.Errors = append(r.Errors, err.Error())
			}
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
	case models.NodeTypeUnion:
		if mode := getStr(n.Config, "mode"); mode != "" && mode != "union" {
			r.Errors = append(r.Errors, fmt.Sprintf("union only supports mode=\"union\" (got %q)", mode))
		}
	case models.NodeTypeDatasetMap:
		if msg := functionRefConfigError(n.Config, "dataset_map"); msg != "" {
			r.Errors = append(r.Errors, msg)
		}
	case models.NodeTypeDatasetFilter:
		if msg := functionRefConfigError(n.Config, "dataset_filter"); msg != "" {
			r.Errors = append(r.Errors, msg)
		}
	}
}
