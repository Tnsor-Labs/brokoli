package models

// Physical execution plan (ADR-015, issue #90 M2). The authored pipeline
// is a compact *logical* graph; a physical plan is what the engine would
// actually schedule for one run — stages of independently-placeable work
// units, each linked back to its logical node. This first slice computes
// the plan on demand for *explanation before execution* (ADR-015 §8): it
// makes the logical→physical expansion visible without yet persisting
// instances or driving dispatch (those are the heavier M2/M3 slices that
// need the store-interface split and instance retention).
//
// A plan is honest about what is knowable before the run: static
// structure (stages, per-node work-unit shape, retry scope, concurrency
// groups) is resolved now; counts that depend on upstream data — dynamic
// expansion fan-out, total pages — are marked runtime-resolved rather
// than guessed.

// WorkUnitKind classifies how a logical node becomes physical work.
type WorkUnitKind string

const (
	// WorkUnitSingle: one node, one unit — the default.
	WorkUnitSingle WorkUnitKind = "single"
	// WorkUnitPagination: a source that fetches pages; page count is
	// resolved at runtime, fan-out bounded by the concurrency group.
	WorkUnitPagination WorkUnitKind = "pagination"
	// WorkUnitExpansion: a dynamic-expansion node; one unit per item in
	// the resolved upstream collection, count known only at runtime.
	WorkUnitExpansion WorkUnitKind = "expansion"
)

// RetryScope is the smallest durable unit retried on failure (ADR-015
// §6): the whole node, or an individual work unit whose successful
// siblings are preserved.
type RetryScope string

const (
	RetryScopeNode     RetryScope = "node"
	RetryScopeWorkUnit RetryScope = "work_unit"
)

// PhysicalWorkUnit describes one node's physical shape within a stage.
type PhysicalWorkUnit struct {
	// LogicalNodeID is the authored node this unit derives from.
	LogicalNodeID string       `json:"logical_node_id"`
	NodeType      string       `json:"node_type"`
	Kind          WorkUnitKind `json:"kind"`

	// InstanceKeyTemplate is the deterministic identity each physical
	// instance of this unit carries. For a single unit it is the logical
	// node ID; for fan-out kinds it includes a runtime-resolved suffix
	// (e.g. "load[<index>]"), shown as a template here.
	InstanceKeyTemplate string `json:"instance_key_template"`

	// StaticInstanceCount is the number of instances knowable now: 1 for
	// single units, 0 for fan-out kinds whose count is runtime-resolved
	// (RuntimeResolved is then true).
	StaticInstanceCount int  `json:"static_instance_count"`
	RuntimeResolved     bool `json:"runtime_resolved"`

	RetryScope RetryScope `json:"retry_scope"`

	// ConcurrencyGroup names the rate/concurrency bucket this unit's
	// instances share (empty = unbounded within the stage). For
	// pagination it is the node's own group so pages don't stampede.
	ConcurrencyGroup string `json:"concurrency_group,omitempty"`
	// MaxConcurrency is the resolved bound for the group, 0 = default.
	MaxConcurrency int `json:"max_concurrency,omitempty"`

	// Explain is a one-line human-readable account of this unit's shape,
	// for the "planner explanations before execution" surface.
	Explain string `json:"explain"`
}

// PhysicalStage is one wave of work units with no intra-stage
// dependencies — every unit in a stage can be placed concurrently once
// the previous stage's outputs it depends on are materialized.
type PhysicalStage struct {
	Index     int                `json:"index"`
	WorkUnits []PhysicalWorkUnit `json:"work_units"`
}

// PhysicalPlan is the full per-run plan (computed on demand for now).
type PhysicalPlan struct {
	PipelineID string          `json:"pipeline_id"`
	IRVersion  string          `json:"ir_version,omitempty"`
	Stages     []PhysicalStage `json:"stages"`

	// StaticInstanceCount sums the knowable instances; DynamicNodes counts
	// nodes whose fan-out resolves at runtime. Together they let the UI
	// say "at least N instances, plus fan-out at M nodes".
	StaticInstanceCount int `json:"static_instance_count"`
	DynamicNodes        int `json:"dynamic_nodes"`
}
