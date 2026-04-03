package engine

import (
	"testing"
	"time"
)

func timePtr(t time.Time) *time.Time { return &t }

func TestCheckDependencies_AllSatisfied(t *testing.T) {
	now := time.Now()
	since := now.Add(-1 * time.Hour)

	deps := []PipelineDep{
		{PipelineID: "p1", Name: "ETL-A"},
		{PipelineID: "p2", Name: "ETL-B"},
	}
	runs := map[string][]RunInfo{
		"p1": {{ID: "r1", PipelineID: "p1", Status: "completed", FinishedAt: timePtr(now.Add(-30 * time.Minute))}},
		"p2": {{ID: "r2", PipelineID: "p2", Status: "completed", FinishedAt: timePtr(now.Add(-10 * time.Minute))}},
	}

	statuses := CheckDependencies(deps, runs, since)
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}
	for _, s := range statuses {
		if !s.Satisfied {
			t.Errorf("expected %s to be satisfied", s.Name)
		}
	}
}

func TestCheckDependencies_OneMissing(t *testing.T) {
	now := time.Now()
	since := now.Add(-1 * time.Hour)

	deps := []PipelineDep{
		{PipelineID: "p1", Name: "ETL-A"},
		{PipelineID: "p2", Name: "ETL-B"},
	}
	runs := map[string][]RunInfo{
		"p1": {{ID: "r1", PipelineID: "p1", Status: "completed", FinishedAt: timePtr(now.Add(-30 * time.Minute))}},
		"p2": {{ID: "r2", PipelineID: "p2", Status: "failed", StartedAt: timePtr(now.Add(-20 * time.Minute))}},
	}

	statuses := CheckDependencies(deps, runs, since)
	if statuses[0].Satisfied != true {
		t.Error("p1 should be satisfied")
	}
	if statuses[1].Satisfied != false {
		t.Error("p2 should NOT be satisfied (failed)")
	}
	if statuses[1].Reason == "" {
		t.Error("p2 should have a reason")
	}
}

func TestCheckDependencies_NoRuns(t *testing.T) {
	since := time.Now().Add(-1 * time.Hour)
	deps := []PipelineDep{{PipelineID: "p1", Name: "ETL-A"}}
	runs := map[string][]RunInfo{}

	statuses := CheckDependencies(deps, runs, since)
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Satisfied {
		t.Error("should not be satisfied with no runs")
	}
	if statuses[0].Reason != "no runs found" {
		t.Errorf("unexpected reason: %s", statuses[0].Reason)
	}
}

func TestCheckDependencies_RunBeforeThreshold(t *testing.T) {
	now := time.Now()
	since := now.Add(-1 * time.Hour)

	deps := []PipelineDep{{PipelineID: "p1", Name: "ETL-A"}}
	runs := map[string][]RunInfo{
		"p1": {{ID: "r1", PipelineID: "p1", Status: "completed", FinishedAt: timePtr(now.Add(-2 * time.Hour))}},
	}

	statuses := CheckDependencies(deps, runs, since)
	if statuses[0].Satisfied {
		t.Error("should not be satisfied when run finished before threshold")
	}
}

func TestCheckDependencies_FailedRun(t *testing.T) {
	now := time.Now()
	since := now.Add(-1 * time.Hour)

	deps := []PipelineDep{{PipelineID: "p1", Name: "ETL-A"}}
	runs := map[string][]RunInfo{
		"p1": {{ID: "r1", PipelineID: "p1", Status: "failed", StartedAt: timePtr(now.Add(-30 * time.Minute))}},
	}

	statuses := CheckDependencies(deps, runs, since)
	if statuses[0].Satisfied {
		t.Error("failed run should not satisfy dependency")
	}
	if statuses[0].LastStatus != "failed" {
		t.Errorf("expected LastStatus=failed, got %s", statuses[0].LastStatus)
	}
}

func TestCheckDependencies_RunningNotSatisfied(t *testing.T) {
	now := time.Now()
	since := now.Add(-1 * time.Hour)

	deps := []PipelineDep{{PipelineID: "p1", Name: "ETL-A"}}
	runs := map[string][]RunInfo{
		"p1": {{ID: "r1", PipelineID: "p1", Status: "running", StartedAt: timePtr(now.Add(-5 * time.Minute))}},
	}

	statuses := CheckDependencies(deps, runs, since)
	if statuses[0].Satisfied {
		t.Error("running pipeline should not satisfy dependency")
	}
}

func TestAllDependenciesMet_True(t *testing.T) {
	statuses := []DepStatus{
		{Satisfied: true},
		{Satisfied: true},
	}
	if !AllDependenciesMet(statuses) {
		t.Error("expected all met")
	}
}

func TestAllDependenciesMet_False(t *testing.T) {
	statuses := []DepStatus{
		{Satisfied: true},
		{Satisfied: false},
	}
	if AllDependenciesMet(statuses) {
		t.Error("expected not all met")
	}
}

func TestAllDependenciesMet_Empty(t *testing.T) {
	if !AllDependenciesMet([]DepStatus{}) {
		t.Error("empty deps should be considered met")
	}
	if !AllDependenciesMet(nil) {
		t.Error("nil deps should be considered met")
	}
}
