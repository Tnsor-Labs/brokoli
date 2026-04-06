package engine

import (
	"time"
)

// PipelineDep represents a dependency between pipelines.
type PipelineDep struct {
	PipelineID string `json:"pipeline_id"`
	Name       string `json:"name"`
}

// DepStatus represents whether a dependency is satisfied.
type DepStatus struct {
	PipelineID string `json:"pipeline_id"`
	Name       string `json:"name"`
	Satisfied  bool   `json:"satisfied"`
	LastRunAt  string `json:"last_run_at,omitempty"`
	LastStatus string `json:"last_status,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// RunInfo is minimal run info for dependency checking.
type RunInfo struct {
	ID         string
	PipelineID string
	Status     string // "completed", "failed", "running", etc.
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// CheckDependencies verifies all dependencies are satisfied.
// A dependency is satisfied if the upstream pipeline has a successful run
// that completed after the given threshold time (usually start of today or last run of this pipeline).
func CheckDependencies(deps []PipelineDep, recentRuns map[string][]RunInfo, since time.Time) []DepStatus {
	results := make([]DepStatus, 0, len(deps))
	for _, dep := range deps {
		ds := DepStatus{
			PipelineID: dep.PipelineID,
			Name:       dep.Name,
		}

		runs, ok := recentRuns[dep.PipelineID]
		if !ok || len(runs) == 0 {
			ds.Reason = "no runs found"
			results = append(results, ds)
			continue
		}

		// Find most recent completed run after 'since'
		for _, r := range runs {
			if r.Status == "completed" && r.FinishedAt != nil && r.FinishedAt.After(since) {
				ds.Satisfied = true
				ds.LastRunAt = r.FinishedAt.Format(time.RFC3339)
				ds.LastStatus = r.Status
				break
			}
		}

		if !ds.Satisfied {
			ds.LastStatus = runs[0].Status
			if runs[0].StartedAt != nil {
				ds.LastRunAt = runs[0].StartedAt.Format(time.RFC3339)
			}
			ds.Reason = "no successful run since " + since.Format("15:04")
		}

		results = append(results, ds)
	}
	return results
}

// AllDependenciesMet returns true only if every dependency is satisfied.
func AllDependenciesMet(statuses []DepStatus) bool {
	for _, s := range statuses {
		if !s.Satisfied {
			return false
		}
	}
	return true
}
