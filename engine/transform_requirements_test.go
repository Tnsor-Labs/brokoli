package engine

import (
	"strings"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

/*
 * #738: a transform rule that cannot run under any input used to save
 * cleanly and fail at execution. Validation counted the rules array and
 * never looked inside a rule.
 *
 * A production run died on "rule 1 (sort): sort requires columns list"
 * after being accepted, scheduled and having fetched its data.
 */

func nodeErrors(t *testing.T, n models.Node) []string {
	t.Helper()
	for _, res := range ValidateNodes([]models.Node{n}) {
		if res.NodeID == n.ID {
			return res.Errors
		}
	}
	return nil
}

// The headline, and the exact production case.
func TestSortWithoutColumnsIsRejectedAtSaveTime(t *testing.T) {
	errs := nodeErrors(t, transformNode(map[string]interface{}{"type": "sort"}))

	if len(errs) == 0 {
		t.Fatal("a sort with no columns saved cleanly; it cannot run under any input")
	}
	joined := strings.Join(errs, "; ")
	for _, want := range []string{"rule 1", "sort", "requires columns list"} {
		if !strings.Contains(joined, want) {
			t.Errorf("validation errors %q do not mention %q", joined, want)
		}
	}
}

// It has to be an error. A warning leaves the pipeline saveable and the
// run just as doomed.
func TestAnUnrunnableRuleIsAnErrorNotAWarning(t *testing.T) {
	n := transformNode(map[string]interface{}{"type": "aggregate"})
	results := ValidateNodes([]models.Node{n})
	if len(results) == 0 {
		t.Fatal("no validation result at all")
	}
	if len(results[0].Errors) == 0 {
		t.Errorf("reported only warnings %v; an unrunnable rule must block the save",
			results[0].Warnings)
	}
}

// The drift guard, and the reason the requirements live in one table.
//
// For every rule type the engine accepts, a rule that validation rejects
// must be rejected by execution with the same message. Two copies of
// these conditions would pass on the day they were written and diverge
// on the first change to either.
func TestValidationAndExecutionAgreeOnEveryRuleType(t *testing.T) {
	for ruleType := range transformRuleRequirements {
		t.Run(ruleType, func(t *testing.T) {
			// An empty rule of this type: whatever the type requires, it
			// does not have it.
			empty := TransformRule{Type: ruleType}
			declared := transformRuleError(empty)

			ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": 1}}}
			execErr := ApplyTransforms([]TransformRule{empty}, ds)

			if declared == nil {
				// The table says this type needs nothing of its own
				// config, so execution must not turn round and demand
				// something. This is the direction a handler-local guard
				// would reintroduce: validation says fine, the run says
				// "requires ...".
				if execErr != nil && strings.Contains(execErr.Error(), "requires") {
					t.Fatalf("the requirements table declares nothing for %s but execution rejected it: %v",
						ruleType, execErr)
				}
				return
			}
			if execErr == nil {
				t.Fatalf("validation rejects an empty %s (%v) but execution accepted it",
					ruleType, declared)
			}
			if !strings.Contains(execErr.Error(), declared.Error()) {
				t.Errorf("messages differ:\n  validation: %v\n  execution:  %v", declared, execErr)
			}
		})
	}
}

// Every type the table declares must actually be dispatchable, or the
// table is promising to run something the switch drops.
func TestEveryDeclaredRuleTypeIsDispatchable(t *testing.T) {
	for ruleType := range transformRuleRequirements {
		t.Run(ruleType, func(t *testing.T) {
			ds := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": 1}}}
			err := ApplyTransforms([]TransformRule{{Type: ruleType}}, ds)
			if err != nil && strings.Contains(err.Error(), "unsupported transform type") {
				t.Errorf("%s is declared in the requirements table but applyRule does not handle it", ruleType)
			}
		})
	}
}

// A typo in a rule type is caught when the pipeline is saved.
func TestAnUnknownRuleTypeIsRejectedAtSaveTime(t *testing.T) {
	errs := nodeErrors(t, transformNode(map[string]interface{}{"type": "sorrt", "columns": []interface{}{"id"}}))
	if len(errs) == 0 {
		t.Fatal("a misspelled rule type saved cleanly")
	}
	if !strings.Contains(strings.Join(errs, "; "), "unsupported transform type") {
		t.Errorf("errors %v do not name the unsupported type", errs)
	}
}

// The control. Well-formed rules must produce no errors, or the gate is
// just refusing everything.
func TestWellFormedRulesValidateCleanly(t *testing.T) {
	errs := nodeErrors(t, transformNode(
		map[string]interface{}{"type": "sort", "columns": []interface{}{"id"}},
		map[string]interface{}{"type": "drop_columns", "columns": []interface{}{"tmp"}},
		map[string]interface{}{"type": "add_column", "name": "total", "expression": "a+b"},
		map[string]interface{}{"type": "rename_columns", "mapping": map[string]interface{}{"a": "b"}},
	))
	if len(errs) != 0 {
		t.Errorf("valid rules were rejected: %v", errs)
	}
}

// aggregate accepts "aggregations" as an alias for "agg_fields". The
// requirement has to know that, or it rejects a rule the engine runs.
func TestAggregateAcceptsTheAggregationsAlias(t *testing.T) {
	errs := nodeErrors(t, transformNode(map[string]interface{}{
		"type":         "aggregate",
		"group_by":     []interface{}{"dept"},
		"aggregations": []interface{}{map[string]interface{}{"column": "pay", "function": "sum"}},
	}))
	if len(errs) != 0 {
		t.Errorf("a rule using the aggregations alias was rejected: %v", errs)
	}
}

// The scope boundary. These requirements are about the rule's own shape,
// answerable from config alone. Anything needing the data stays at
// execution time -- a validator that guessed would reject pipelines that
// work, since the incoming columns are not knowable at save time.
func TestValidationDoesNotGuessAtTheData(t *testing.T) {
	errs := nodeErrors(t, transformNode(map[string]interface{}{
		"type": "sort", "columns": []interface{}{"a_column_that_may_or_may_not_exist"},
	}))
	if len(errs) != 0 {
		t.Errorf("validation rejected a well-formed rule over a column it cannot know about: %v", errs)
	}
}

// A rule with no type at all gets a message about that, rather than
// "unsupported transform type: ".
func TestARuleWithNoTypeSaysSo(t *testing.T) {
	errs := nodeErrors(t, transformNode(map[string]interface{}{"columns": []interface{}{"id"}}))
	if len(errs) == 0 {
		t.Fatal("a rule with no type saved cleanly")
	}
	if !strings.Contains(strings.Join(errs, "; "), "no type set") {
		t.Errorf("errors %v do not say the type is missing", errs)
	}
}
