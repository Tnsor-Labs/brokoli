package models

import (
	"fmt"
	"time"
)

// DependencyState is the upstream pipeline state that satisfies a dependency.
type DependencyState string

const (
	// DepStateSucceeded requires the upstream's most recent run to be successful.
	DepStateSucceeded DependencyState = "succeeded"
	// DepStateCompleted accepts any terminal state (success, failed, cancelled).
	DepStateCompleted DependencyState = "completed"
	// DepStateFailed requires the upstream to have failed (rare — used for cleanup DAGs).
	DepStateFailed DependencyState = "failed"
)

// DependencyMode controls how the dependency is enforced.
type DependencyMode string

const (
	// DepModeGate blocks downstream runs until the dependency is satisfied.
	DepModeGate DependencyMode = "gate"
	// DepModeTrigger causes the downstream to auto-fire when the dependency transitions into the target state.
	DepModeTrigger DependencyMode = "trigger"
)

// DependencyRule describes a cross-pipeline dependency.
type DependencyRule struct {
	PipelineID string          `json:"pipeline_id"`
	State      DependencyState `json:"state,omitempty"`              // default: succeeded
	WithinSec  int64           `json:"within_seconds,omitempty"`     // 0 = no freshness requirement
	Mode       DependencyMode  `json:"mode,omitempty"`               // default: gate
}

// Normalize fills in defaults so callers can assume non-empty State/Mode.
func (d *DependencyRule) Normalize() {
	if d.State == "" {
		d.State = DepStateSucceeded
	}
	if d.Mode == "" {
		d.Mode = DepModeGate
	}
}

// Within returns the freshness window as a duration.
func (d *DependencyRule) Within() time.Duration {
	if d.WithinSec <= 0 {
		return 0
	}
	return time.Duration(d.WithinSec) * time.Second
}

// maxDependencyWindow caps the freshness window to ~100 years so WithinSec * time.Second
// cannot overflow time.Duration (which is int64 nanoseconds).
const maxDependencyWindow = int64(100 * 365 * 24 * 3600)

// Validate checks a rule for structural errors.
func (d *DependencyRule) Validate() error {
	if d.PipelineID == "" {
		return fmt.Errorf("dependency rule: pipeline_id is required")
	}
	s := d.State
	if s == "" {
		s = DepStateSucceeded
	}
	switch s {
	case DepStateSucceeded, DepStateCompleted, DepStateFailed:
	default:
		return fmt.Errorf("dependency rule: invalid state %q", d.State)
	}
	m := d.Mode
	if m == "" {
		m = DepModeGate
	}
	switch m {
	case DepModeGate, DepModeTrigger:
	default:
		return fmt.Errorf("dependency rule: invalid mode %q", d.Mode)
	}
	if d.WithinSec < 0 {
		return fmt.Errorf("dependency rule: within_seconds must be >= 0")
	}
	if d.WithinSec > maxDependencyWindow {
		return fmt.Errorf("dependency rule: within_seconds too large (max %d)", maxDependencyWindow)
	}
	return nil
}

// PipelineDepSummary is a lightweight projection of a Pipeline that only carries
// the fields needed for dependency-graph operations (cycle detection, save-time
// validation, reverse lookups, trigger-mode fan-out, /dependency-graph endpoint).
// Using it avoids loading multi-KB nodes/edges/params blobs that the graph code
// never reads.
type PipelineDepSummary struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	OrgID           string           `json:"org_id"`
	DependsOn       []string         `json:"depends_on,omitempty"`
	DependencyRules []DependencyRule `json:"dependency_rules,omitempty"`
}

// EffectiveDependencies merges legacy DependsOn + rich DependencyRules and
// returns the normalized rule set. Explicit rules win on conflict.
//
// Exported as a free function so both Pipeline and PipelineDepSummary share
// the same semantics without duplicating the logic.
func EffectiveDependencies(rules []DependencyRule, legacy []string) []DependencyRule {
	seen := make(map[string]bool, len(rules)+len(legacy))
	out := make([]DependencyRule, 0, len(rules)+len(legacy))
	for _, r := range rules {
		if r.PipelineID == "" || seen[r.PipelineID] {
			continue
		}
		rule := r
		rule.Normalize()
		seen[rule.PipelineID] = true
		out = append(out, rule)
	}
	for _, id := range legacy {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, DependencyRule{PipelineID: id, State: DepStateSucceeded, Mode: DepModeGate})
	}
	return out
}

// EffectiveDependencies returns normalized dependency rules for the summary.
func (s *PipelineDepSummary) EffectiveDependencies() []DependencyRule {
	return EffectiveDependencies(s.DependencyRules, s.DependsOn)
}
