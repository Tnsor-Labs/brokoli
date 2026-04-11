package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/store"
)

// DependencyStatus describes the runtime state of a single dependency rule for a pipeline.
type DependencyStatus struct {
	Rule       models.DependencyRule `json:"rule"`
	Satisfied  bool                  `json:"satisfied"`
	Reason     string                `json:"reason,omitempty"`
	LastStatus models.RunStatus      `json:"last_status,omitempty"`
	LastRunAt  *time.Time            `json:"last_run_at,omitempty"`
	Missing    bool                  `json:"missing,omitempty"` // upstream pipeline does not exist
}

// CheckDependencies evaluates all dependency rules for a pipeline.
// Returns whether all gate-mode rules are satisfied, the per-rule status, and a blocked reason if not.
func CheckDependencies(s store.Store, p *models.Pipeline, now time.Time) (satisfied bool, statuses []DependencyStatus, reason string) {
	rules := p.EffectiveDependencies()
	statuses = make([]DependencyStatus, 0, len(rules))
	satisfied = true
	var blockers []string

	for _, rule := range rules {
		rule.Normalize()
		st := DependencyStatus{Rule: rule}

		upstream, err := s.GetPipeline(rule.PipelineID)
		if err != nil || upstream == nil {
			st.Missing = true
			st.Reason = "upstream pipeline not found"
			if rule.Mode == models.DepModeGate {
				satisfied = false
				blockers = append(blockers, fmt.Sprintf("%s: not found", rule.PipelineID))
			}
			statuses = append(statuses, st)
			continue
		}

		runs, _ := s.ListRunsByPipeline(rule.PipelineID, 1)
		if len(runs) == 0 {
			st.Reason = "no runs yet"
			if rule.Mode == models.DepModeGate {
				satisfied = false
				blockers = append(blockers, fmt.Sprintf("%s: no runs yet", upstream.Name))
			}
			statuses = append(statuses, st)
			continue
		}

		last := runs[0]
		st.LastStatus = last.Status
		if last.FinishedAt != nil {
			ft := *last.FinishedAt
			st.LastRunAt = &ft
		} else if last.StartedAt != nil {
			sta := *last.StartedAt
			st.LastRunAt = &sta
		}

		stateOK := matchesState(last.Status, rule.State)
		if !stateOK {
			st.Reason = fmt.Sprintf("last run state is %q, need %q", last.Status, rule.State)
			if rule.Mode == models.DepModeGate {
				satisfied = false
				blockers = append(blockers, fmt.Sprintf("%s: %s", upstream.Name, st.Reason))
			}
			statuses = append(statuses, st)
			continue
		}

		if within := rule.Within(); within > 0 && st.LastRunAt != nil {
			age := now.Sub(*st.LastRunAt)
			if age > within {
				st.Reason = fmt.Sprintf("last run is stale (age %s > window %s)", age.Truncate(time.Second), within)
				if rule.Mode == models.DepModeGate {
					satisfied = false
					blockers = append(blockers, fmt.Sprintf("%s: stale (%s old)", upstream.Name, age.Truncate(time.Second)))
				}
				statuses = append(statuses, st)
				continue
			}
		}

		st.Satisfied = true
		statuses = append(statuses, st)
	}

	if !satisfied {
		reason = "dependencies not satisfied: " + strings.Join(blockers, "; ")
	}
	return
}

// matchesState reports whether the actual run status satisfies the requested dependency state.
func matchesState(actual models.RunStatus, want models.DependencyState) bool {
	switch want {
	case models.DepStateSucceeded:
		return actual == models.RunStatusSuccess
	case models.DepStateFailed:
		return actual == models.RunStatusFailed
	case models.DepStateCompleted:
		return actual == models.RunStatusSuccess || actual == models.RunStatusFailed || actual == models.RunStatusCancelled
	}
	return false
}

// DetectDependencyCycle reports an error if the target pipeline's dependency graph contains a cycle.
// candidate may be a new or updated pipeline not yet persisted — its rules are evaluated in-memory.
func DetectDependencyCycle(s store.Store, candidate *models.Pipeline) error {
	if candidate == nil {
		return nil
	}
	// Build an id -> rules map by reading the current store state, overriding with the candidate.
	all, err := s.ListPipelines()
	if err != nil {
		return err
	}
	deps := make(map[string][]string, len(all)+1)
	for _, p := range all {
		ids := make([]string, 0)
		for _, r := range p.EffectiveDependencies() {
			ids = append(ids, r.PipelineID)
		}
		deps[p.ID] = ids
	}
	if candidate.ID != "" {
		ids := make([]string, 0)
		for _, r := range candidate.EffectiveDependencies() {
			ids = append(ids, r.PipelineID)
		}
		deps[candidate.ID] = ids
	} else {
		// Unsaved candidate: evaluate from a synthetic id
		ids := make([]string, 0)
		for _, r := range candidate.EffectiveDependencies() {
			ids = append(ids, r.PipelineID)
		}
		deps["__candidate__"] = ids
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(deps))
	var path []string
	var visit func(id string) error
	visit = func(id string) error {
		switch color[id] {
		case gray:
			// cycle — assemble path
			cycle := append([]string{}, path...)
			cycle = append(cycle, id)
			return fmt.Errorf("dependency cycle detected: %s", strings.Join(cycle, " -> "))
		case black:
			return nil
		}
		color[id] = gray
		path = append(path, id)
		for _, next := range deps[id] {
			if next == "" {
				continue
			}
			if err := visit(next); err != nil {
				return err
			}
		}
		color[id] = black
		path = path[:len(path)-1]
		return nil
	}

	start := candidate.ID
	if start == "" {
		start = "__candidate__"
	}
	return visit(start)
}
