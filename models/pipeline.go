package models

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Pipeline represents a data processing pipeline with its nodes and edges.
type Pipeline struct {
	ID          string `json:"id"`
	IRVersion   string `json:"ir_version,omitempty"` // pipeline IR protocol version (e.g. "2.0"); empty means pre-versioned (pre-Phase-0 SDK clients)
	Name        string `json:"name"`
	Description string `json:"description"`
	Nodes       []Node `json:"nodes"`
	Edges       []Edge `json:"edges"`
	Schedule    string `json:"schedule"` // cron expression, empty for manual-only
	// Catchup opts this pipeline into per-interval catch-up (ADR-028
	// phase 2, #397): after downtime, one run per missed schedule
	// interval, oldest first, instead of the default single catch-up run
	// for the most recently missed tick.
	Catchup          bool              `json:"catchup,omitempty"`
	WebhookURL       string            `json:"webhook_url"`                 // URL for event notifications
	Params           map[string]string `json:"params"`                      // default parameter values
	Tags             []string          `json:"tags"`                        // labels for filtering/grouping
	Hooks            map[string]Hook   `json:"hooks,omitempty"`             // on_start, on_success, on_failure, on_node_failure
	ScheduleTimezone string            `json:"schedule_timezone,omitempty"` // e.g. "America/New_York", defaults to UTC
	SLADeadline      string            `json:"sla_deadline,omitempty"`      // "HH:MM" — must complete by this time daily
	SLATimezone      string            `json:"sla_timezone,omitempty"`      // e.g. "America/New_York", defaults to UTC
	DependsOn        []string          `json:"depends_on,omitempty"`        // legacy: pipeline IDs that must succeed before this runs
	DependencyRules  []DependencyRule  `json:"dependency_rules,omitempty"`  // rich cross-pipeline dependency rules
	WebhookToken     string            `json:"webhook_token,omitempty"`     // token for triggering via webhook
	// Extensions is the one open namespace in an otherwise fail-closed
	// payload (ADR-014 rules 8/9): newer clients park fields this server
	// doesn't understand here, and the server persists and echoes them back
	// without interpreting them (whitespace-normalized, semantically
	// intact). Everything outside it that the
	// server doesn't recognize is a 400 at decode time.
	Extensions  map[string]json.RawMessage `json:"extensions,omitempty"`
	Enabled     bool                       `json:"enabled"`
	PipelineID  string                     `json:"pipeline_id"`            // stable slug for git-sync matching
	Source      string                     `json:"source"`                 // "ui" or "git"
	WorkspaceID string                     `json:"workspace_id,omitempty"` // workspace isolation
	OrgID       string                     `json:"org_id,omitempty"`       // tenant isolation
	CreatedAt   time.Time                  `json:"created_at"`
	UpdatedAt   time.Time                  `json:"updated_at"`
}

const (
	PipelineSourceUI  = "ui"
	PipelineSourceGit = "git"
)

// CurrentIRVersion is the pipeline intermediate-representation protocol
// version this host emits/expects by default. Bump when the deployed
// pipeline JSON shape changes in a way the host needs to be aware of
// (e.g. Phase 0's addition of node capabilities).
const CurrentIRVersion = "2.0"

// ConditionalEdgesIRVersion is the first IR version whose edges may carry
// branch selection.
const ConditionalEdgesIRVersion = "2.1"

// SupportedIRVersions lists every ir_version this host can accept in a
// deployed pipeline. Expand when introducing a new version; only remove
// an old entry when formally deprecating it.
var SupportedIRVersions = []string{"2.0", ConditionalEdgesIRVersion}

// SupportedExecutionFeatures names the pipeline semantics this host can
// actually EXECUTE, advertised at /api/capabilities (ADR-014 rule 5) so
// client preflight can gate features instead of only IR versions. The
// honesty rule for this list: a feature appears here only when a
// pipeline using it runs — not when it merely persists. dataset_map and
// dataset_filter are deliberately absent: they persist and dispatch, but
// their runtime rejects SDK-emitted configs (no function source), so a
// client that gates on this list correctly refuses to deploy them.
var SupportedExecutionFeatures = []string{
	// Conditional edge routing: true/false branches, skipped
	// propagation, convergence-after-resolution (IR 2.1, v0.10.9).
	"conditional-routing",
	// @task.expand() fan-out. Executes per item — sequentially inside
	// one node today; parallel work-unit scheduling is #90 M2/M3.
	"dynamic-expansion",
	// Multi-input union node (row concatenation).
	"union",
	// Declarative source_api pagination with persisted, resumable
	// checkpoints and per-page retry policy (config.pagination +
	// config.execution).
	"pagination-checkpoints",
}

var supportedConditionExpressions = []*regexp.Regexp{
	regexp.MustCompile(`^row_count\s*(==|!=|>=|<=|>|<)\s*\d+$`),
	regexp.MustCompile(`^column_exists\(\s*"[^"]+"\s*\)$`),
	regexp.MustCompile(`^null_pct\(\s*"[^"]+"\s*\)\s*(==|!=|>=|<=|>|<)\s*(?:\d+(?:\.\d*)?|\.\d+)$`),
	regexp.MustCompile(`^(min|max)\(\s*"[^"]+"\s*\)\s*(==|!=|>=|<=|>|<)\s*(?:\d+(?:\.\d*)?|\.\d+)$`),
}

// IsConditionExpressionSupported reports whether expr belongs to the
// condition language implemented by the runtime.
func IsConditionExpressionSupported(expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "always_true" || expr == "always_false" {
		return true
	}
	for _, pattern := range supportedConditionExpressions {
		if pattern.MatchString(expr) {
			return true
		}
	}
	return false
}

// IsIRVersionSupported returns true if the host can accept a pipeline
// carrying the given ir_version. An empty version is always accepted —
// it means the pipeline predates ir_version being introduced (old SDK
// clients or hand-written JSON), and is treated as implicitly supported
// for backward compatibility.
func IsIRVersionSupported(v string) bool {
	if v == "" {
		return true
	}
	for _, ok := range SupportedIRVersions {
		if ok == v {
			return true
		}
	}
	return false
}

// NodeType defines the kind of operation a node performs.
type NodeType string

const (
	NodeTypeSourceFile   NodeType = "source_file"
	NodeTypeSourceAPI    NodeType = "source_api"
	NodeTypeSourceDB     NodeType = "source_db"
	NodeTypeTransform    NodeType = "transform"
	NodeTypeQualityCheck NodeType = "quality_check"
	NodeTypeSQLGenerate  NodeType = "sql_generate"
	NodeTypeCode         NodeType = "code"
	NodeTypeJoin         NodeType = "join"
	NodeTypeSinkFile     NodeType = "sink_file"
	NodeTypeSinkDB       NodeType = "sink_db"
	NodeTypeSinkAPI      NodeType = "sink_api"
	NodeTypeMigrate      NodeType = "migrate"
	NodeTypeCondition    NodeType = "condition" // if/else branching
	NodeTypeDBT          NodeType = "dbt"       // dbt run/test/build
	NodeTypeNotify       NodeType = "notify"    // send Slack/email/webhook notification

	// NodeTypeUnion concatenates two or more upstream datasets into one.
	// Emitted by brokoli-sdk's union(name, *refs) and
	// CollectionRef.collect(mode="union") (both compile to this same node
	// type — see brokoli-sdk pipeline.py _build_union_node).
	NodeTypeUnion NodeType = "union"
	// NodeTypeDatasetMap applies a referenced function to every row of the
	// WHOLE upstream dataset (no per-partition execution yet — see
	// engine.runPartitionTransform). Emitted by brokoli-sdk's
	// DatasetRef.map(fn).
	NodeTypeDatasetMap NodeType = "dataset_map"
	// NodeTypeDatasetFilter applies a referenced predicate to the WHOLE
	// upstream dataset (no per-partition execution yet — see
	// engine.runPartitionTransform). Emitted by brokoli-sdk's
	// DatasetRef.filter(fn).
	NodeTypeDatasetFilter NodeType = "dataset_filter"
)

// Position represents a node's position on the visual canvas.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Node represents a single processing step in a pipeline.
type Node struct {
	ID           string                 `json:"id"`
	Type         NodeType               `json:"type"`
	Name         string                 `json:"name"`
	Config       map[string]interface{} `json:"config"`
	Position     Position               `json:"position"`
	Capabilities []string               `json:"capabilities,omitempty"` // e.g. ["source", "dataset-output"], ["sink"], ["compute"]; see CapabilitySource etc.
}

// Node capability tags. IR v2 pipelines (ir_version >= "2.0") declare a
// node's role explicitly via Node.Capabilities, independent of its
// concrete Type. This lets decorator-based SDK nodes (e.g. Type ==
// "code" wrapping a user function) participate in structural validation
// (source/sink detection) without the host special-casing every
// user-defined node type. Nodes with an empty Capabilities slice (old
// SDK clients, hand-written JSON) fall back to type-based inference.
const (
	CapabilitySource        = "source"
	CapabilitySink          = "sink"
	CapabilityCompute       = "compute"
	CapabilityDatasetOutput = "dataset-output"
	// CapabilityDynamicExpansion and CapabilityCollectionOutput are emitted
	// by brokoli-sdk's _TaskWrapper.expand() (Tnsor-Labs/brokoli#31) on the
	// `code` node it builds: ["compute", "dynamic-expansion",
	// "collection-output"]. The engine does not currently gate expansion
	// recognition on these tags (it keys off the `expansion` config block
	// itself, which is authoritative and present regardless of whether a
	// given SDK/client version also sets capabilities) — they're recorded
	// here for documentation and for any future code that wants to detect
	// expansion nodes without inspecting Config.
	CapabilityDynamicExpansion = "dynamic-expansion"
	CapabilityCollectionOutput = "collection-output"
)

// Hook defines a lifecycle callback (webhook, slack, email).
type Hook struct {
	Type    string            `json:"type"` // webhook, slack, email
	URL     string            `json:"url"`  // webhook URL or Slack webhook
	Enabled bool              `json:"enabled"`
	Extra   map[string]string `json:"extra,omitempty"` // additional config
}

// Validate checks a pipeline for structural errors before persisting.
func (p *Pipeline) Validate() error {
	if !IsIRVersionSupported(p.IRVersion) {
		return fmt.Errorf("unsupported pipeline IR version %q", p.IRVersion)
	}
	if p.Name == "" {
		return fmt.Errorf("pipeline name is required")
	}
	if strings.ContainsAny(p.Name, "<>\"'&") {
		return fmt.Errorf("pipeline name contains invalid characters")
	}
	if len(p.Name) > 255 {
		return fmt.Errorf("pipeline name too long (max 255 characters)")
	}
	if len(p.Description) > 2000 {
		return fmt.Errorf("description too long (max 2000 characters)")
	}
	if len(p.Nodes) > 500 {
		return fmt.Errorf("too many nodes (max 500)")
	}
	if p.ScheduleTimezone != "" {
		if _, err := time.LoadLocation(p.ScheduleTimezone); err != nil {
			return fmt.Errorf("invalid schedule timezone %q: %w", p.ScheduleTimezone, err)
		}
	}
	if p.SLATimezone != "" {
		if _, err := time.LoadLocation(p.SLATimezone); err != nil {
			return fmt.Errorf("invalid timezone %q: %w", p.SLATimezone, err)
		}
	}
	if p.SLADeadline != "" {
		parts := strings.SplitN(p.SLADeadline, ":", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid SLA deadline format, expected HH:MM")
		}
	}
	// Check for duplicate node IDs
	seen := make(map[string]bool)
	nodeTypes := make(map[string]NodeType)
	inputDegree := make(map[string]int)
	for _, n := range p.Nodes {
		if seen[n.ID] {
			return fmt.Errorf("duplicate node ID: %s", n.ID)
		}
		seen[n.ID] = true
		nodeTypes[n.ID] = n.Type
	}
	// Validate dependency rules
	for i := range p.DependencyRules {
		if err := p.DependencyRules[i].Validate(); err != nil {
			return err
		}
		if p.DependencyRules[i].PipelineID == p.ID && p.ID != "" {
			return fmt.Errorf("dependency rule: pipeline cannot depend on itself")
		}
	}
	for _, dep := range p.DependsOn {
		if dep == p.ID && p.ID != "" {
			return fmt.Errorf("depends_on: pipeline cannot depend on itself")
		}
	}
	// Validate edges reference existing nodes
	for _, e := range p.Edges {
		if !seen[e.From] {
			return fmt.Errorf("edge references unknown source node: %s", e.From)
		}
		if !seen[e.To] {
			return fmt.Errorf("edge references unknown target node: %s", e.To)
		}
		inputDegree[e.To]++
		if e.Condition != nil {
			if p.IRVersion != ConditionalEdgesIRVersion {
				return fmt.Errorf("conditional edge %s -> %s requires pipeline IR %s", e.From, e.To, ConditionalEdgesIRVersion)
			}
			if nodeTypes[e.From] != NodeTypeCondition {
				return fmt.Errorf("conditional edge %s -> %s must originate from a condition node", e.From, e.To)
			}
		}
		if p.IRVersion == ConditionalEdgesIRVersion && nodeTypes[e.From] == NodeTypeCondition && e.Condition == nil {
			return fmt.Errorf("condition node edge %s -> %s requires an explicit true or false branch", e.From, e.To)
		}
	}
	if p.IRVersion == ConditionalEdgesIRVersion {
		for _, n := range p.Nodes {
			if n.Type != NodeTypeCondition {
				continue
			}
			if inputDegree[n.ID] != 1 {
				return fmt.Errorf("condition node %s must have exactly 1 input, got %d", n.ID, inputDegree[n.ID])
			}
			expression, ok := n.Config["expression"].(string)
			if !ok || !IsConditionExpressionSupported(expression) {
				return fmt.Errorf("condition node %s has an unsupported expression", n.ID)
			}
		}
	}
	return nil
}

// EffectiveDependencies returns the normalized rules, merging legacy DependsOn strings
// (treated as default gate/succeeded rules) with explicit DependencyRules. When the same
// PipelineID appears in both, the explicit rule wins.
func (p *Pipeline) EffectiveDependencies() []DependencyRule {
	return EffectiveDependencies(p.DependencyRules, p.DependsOn)
}

// Edge represents a directed connection between two nodes.
type Edge struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Condition *bool  `json:"condition,omitempty"`
}
