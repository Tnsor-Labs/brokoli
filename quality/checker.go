package quality

import (
	"fmt"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Checker runs a set of quality checks against a dataset.
type Checker struct{}

// NewChecker creates a new quality checker.
func NewChecker() *Checker {
	return &Checker{}
}

// Result contains all check outcomes and whether the overall check passed.
type Result struct {
	Passed  bool          `json:"passed"`
	Results []CheckResult `json:"results"`
	Summary string        `json:"summary"`
}

// Run executes all checks against the dataset.
// Returns the result and whether execution should continue (based on on_failure policy).
func (c *Checker) Run(checks []Check, dataset *common.DataSet) (*Result, error) {
	if dataset == nil {
		return nil, fmt.Errorf("quality check requires input data")
	}

	results := make([]CheckResult, 0, len(checks))
	allPassed := true
	failCount := 0

	for _, check := range checks {
		cr := RunCheck(check, dataset)
		results = append(results, cr)
		if !cr.Passed {
			allPassed = false
			failCount++
		}
	}

	summary := fmt.Sprintf("%d/%d checks passed", len(checks)-failCount, len(checks))
	if allPassed {
		summary = fmt.Sprintf("All %d checks passed", len(checks))
	}

	return &Result{
		Passed:  allPassed,
		Results: results,
		Summary: summary,
	}, nil
}

// ShouldBlock returns true if any failing check has on_failure="block".
func (r *Result) ShouldBlock() bool {
	for _, cr := range r.Results {
		if !cr.Passed && cr.Check.OnFailure == "block" {
			return true
		}
	}
	return false
}
