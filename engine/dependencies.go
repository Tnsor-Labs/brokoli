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
	Rule         models.DependencyRule `json:"rule"`
	UpstreamName string                `json:"upstream_name,omitempty"`
	Satisfied    bool                  `json:"satisfied"`
	Reason       string                `json:"reason,omitempty"`
	LastStatus   models.RunStatus      `json:"last_status,omitempty"`
	LastRunAt    *time.Time            `json:"last_run_at,omitempty"`
	Missing      bool                  `json:"missing,omitempty"` // upstream pipeline does not exist (or lives in another tenant)
}

// checkResult accumulates per-rule outcomes during CheckDependencies. Using an explicit
// struct instead of named returns makes the mutation flow local and auditable.
type checkResult struct {
	statuses []DependencyStatus
	blockers []string
}

func (r *checkResult) fail(st DependencyStatus, blockerMsg string) {
	if st.Rule.Mode == models.DepModeGate {
		r.blockers = append(r.blockers, blockerMsg)
	}
	r.statuses = append(r.statuses, st)
}

func (r *checkResult) pass(st DependencyStatus) {
	st.Satisfied = true
	r.statuses = append(r.statuses, st)
}

// CheckDependencies evaluates all dependency rules for a pipeline.
// Returns whether all gate-mode rules are satisfied, the per-rule status, and a blocked reason if not.
//
// Batch strategy: one call to GetLatestRunsByPipelineIDs and one lookup per upstream against
// an already-loaded org adjacency map, avoiding the per-rule N+1 that was here previously.
func CheckDependencies(s store.Store, p *models.Pipeline, now time.Time) (satisfied bool, statuses []DependencyStatus, reason string) {
	rules := p.EffectiveDependencies()
	if len(rules) == 0 {
		return true, nil, ""
	}

	// Load the org's adjacency/summary index in one query so we can look up upstream
	// name + org in O(1) without hitting the DB per rule.
	summaries, err := s.ListPipelineDepsByOrg(p.OrgID)
	if err != nil {
		// Best-effort fallback: fail closed on gate rules with a clear reason.
		return false, nil, fmt.Sprintf("dependency check failed: %v", err)
	}
	byID := make(map[string]*models.PipelineDepSummary, len(summaries))
	for i := range summaries {
		byID[summaries[i].ID] = &summaries[i]
	}

	// Batch-fetch the latest run per upstream in one query.
	ids := make([]string, 0, len(rules))
	for _, r := range rules {
		if _, ok := byID[r.PipelineID]; ok {
			ids = append(ids, r.PipelineID)
		}
	}
	latest, err := s.GetLatestRunsByPipelineIDs(ids)
	if err != nil {
		return false, nil, fmt.Sprintf("dependency check failed: %v", err)
	}

	result := checkResult{statuses: make([]DependencyStatus, 0, len(rules))}

	for _, rule := range rules {
		st := DependencyStatus{Rule: rule}

		upstream, ok := byID[rule.PipelineID]
		// Cross-tenant isolation: listing is already org-scoped, so a missing entry
		// means either "doesn't exist" or "lives in another org" — indistinguishable by design.
		if !ok {
			st.Missing = true
			st.Reason = "upstream pipeline not found"
			result.fail(st, fmt.Sprintf("%s: not found", rule.PipelineID))
			continue
		}
		st.UpstreamName = upstream.Name

		last, hasRun := latest[rule.PipelineID]
		if !hasRun {
			st.Reason = "no runs yet"
			result.fail(st, fmt.Sprintf("%s: no runs yet", upstream.Name))
			continue
		}

		st.LastStatus = last.Status
		if last.FinishedAt != nil {
			ft := *last.FinishedAt
			st.LastRunAt = &ft
		} else if last.StartedAt != nil {
			sta := *last.StartedAt
			st.LastRunAt = &sta
		}

		if !matchesState(last.Status, rule.State) {
			st.Reason = fmt.Sprintf("last run state is %q, need %q", last.Status, rule.State)
			result.fail(st, fmt.Sprintf("%s: %s", upstream.Name, st.Reason))
			continue
		}

		if within := rule.Within(); within > 0 && st.LastRunAt != nil {
			age := now.Sub(*st.LastRunAt)
			if age > within {
				ageStr := age.Truncate(time.Second)
				st.Reason = fmt.Sprintf("last run is stale (age %s > window %s)", ageStr, within)
				result.fail(st, fmt.Sprintf("%s: stale (%s old)", upstream.Name, ageStr))
				continue
			}
		}

		result.pass(st)
	}

	if len(result.blockers) > 0 {
		return false, result.statuses, "dependencies not satisfied: " + strings.Join(result.blockers, "; ")
	}
	return true, result.statuses, ""
}

// matchesState reports whether the actual run status satisfies the requested dependency state.
func matchesState(actual models.RunStatus, want models.DependencyState) bool {
	switch want {
	case models.DepStateSucceeded:
		return actual == models.RunStatusSuccess
	case models.DepStateFailed:
		return actual == models.RunStatusFailed
	case models.DepStateCompleted:
		return actual == models.RunStatusSuccess ||
			actual == models.RunStatusFailed ||
			actual == models.RunStatusCancelled
	}
	return false
}

// DetectDependencyCycle reports an error if the target pipeline's dependency graph contains
// a cycle. candidate may be a new or updated pipeline not yet persisted — its rules are
// overlaid on the persisted adjacency for the walk.
//
// Traversal is scoped to the candidate's org, so cycle error messages can never echo
// pipeline IDs from other tenants, and cross-org references are treated as dead-ends.
func DetectDependencyCycle(s store.Store, candidate *models.Pipeline) error {
	if candidate == nil {
		return nil
	}

	summaries, err := s.ListPipelineDepsByOrg(candidate.OrgID)
	if err != nil {
		return err
	}

	// Build adjacency from persisted state, then overlay the candidate.
	adj := make(map[string][]string, len(summaries)+1)
	names := make(map[string]string, len(summaries)+1)
	for i := range summaries {
		sum := &summaries[i]
		names[sum.ID] = sum.Name
		ids := make([]string, 0, len(sum.DependencyRules)+len(sum.DependsOn))
		for _, r := range sum.EffectiveDependencies() {
			ids = append(ids, r.PipelineID)
		}
		adj[sum.ID] = ids
	}

	candidateKey := candidate.ID
	if candidateKey == "" {
		candidateKey = "__candidate__"
	}
	names[candidateKey] = candidate.Name
	candIDs := make([]string, 0, len(candidate.DependencyRules)+len(candidate.DependsOn))
	for _, r := range candidate.EffectiveDependencies() {
		candIDs = append(candIDs, r.PipelineID)
	}
	adj[candidateKey] = candIDs

	return walkForCycle(candidateKey, adj, names)
}

// walkForCycle runs a DFS from start, returning an error describing the cycle if any node
// is revisited while still on the recursion stack.
func walkForCycle(start string, adj map[string][]string, names map[string]string) error {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int, len(adj))
	path := make([]string, 0, 16)

	var visit func(id string) error
	visit = func(id string) error {
		switch color[id] {
		case gray:
			cycle := append(append([]string{}, path...), id)
			return fmt.Errorf("dependency cycle detected: %s", formatCyclePath(cycle, names))
		case black:
			return nil
		}
		color[id] = gray
		path = append(path, id)
		for _, next := range adj[id] {
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
	return visit(start)
}

func formatCyclePath(ids []string, names map[string]string) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		if n := names[id]; n != "" {
			parts[i] = n
			continue
		}
		parts[i] = id
	}
	return strings.Join(parts, " -> ")
}
