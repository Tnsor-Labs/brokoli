package engine

import (
	"fmt"

	"github.com/Tnsor-Labs/brokoli/models"
)

/*
 * What each transform rule needs before it can run, declared once.
 *
 * These checks used to live inside the eleven rule handlers, which meant
 * they only ran at execution time. A rule that could not work under any
 * input -- a sort with no columns, an aggregate with no group_by -- saved
 * cleanly, scheduled, fetched its data, and failed on the last step.
 * Validation looked only at whether the rules array was non-empty (#738).
 *
 * The table is the single source of truth: applyRule consults it before
 * dispatching, so execution enforces exactly what validation promised,
 * and validateTransformRules consults it at save time. A requirement
 * cannot be added to one and forgotten in the other, which is what a
 * second copy in the validator would have guaranteed within a release.
 *
 * Keyed by every accepted spelling, aliases included, so the table and
 * applyRule's switch cover the same set. A type in neither is rejected by
 * both, with the same message.
 *
 * Note what is NOT here. These are requirements on the rule's own shape,
 * answerable from the config alone. Anything needing the data -- whether
 * a named column exists in the incoming dataset, whether an expression
 * evaluates -- stays at execution time, where the dataset is. A validator
 * that guessed at those would reject pipelines that work.
 */

// transformRuleRequirements maps a rule type to the reason it cannot run,
// or nil when the rule is well formed.
var transformRuleRequirements = map[string]func(TransformRule) error{
	"rename_columns": requireMapping,
	"rename":         requireMapping,

	"add_column": func(r TransformRule) error {
		if r.Name == "" || r.Expression == "" {
			return fmt.Errorf("add_column requires name and expression")
		}
		return nil
	},

	"project":    requireProjections,
	"projection": requireProjections,

	"filter_rows": requireCondition,
	"filter":      requireCondition,

	"apply_function": requireColumnAndFunction,
	"function":       requireColumnAndFunction,

	"replace_values": requireColumnAndMapping,
	"replace":        requireColumnAndMapping,

	"drop_columns": func(r TransformRule) error {
		if len(r.Columns) == 0 {
			return fmt.Errorf("drop_columns requires columns list")
		}
		return nil
	},
	"drop": func(r TransformRule) error {
		if len(r.Columns) == 0 {
			return fmt.Errorf("drop_columns requires columns list")
		}
		return nil
	},

	"sort": func(r TransformRule) error {
		if len(r.Columns) == 0 {
			return fmt.Errorf("sort requires columns list")
		}
		return nil
	},

	"deduplicate": requireKeyColumns,
	"dedup":       requireKeyColumns,

	"aggregate": requireAggregation,
	"agg":       requireAggregation,

	"filter_native": func(r TransformRule) error {
		if r.ExpressionVersion != 1 || len(r.Predicate) == 0 {
			return fmt.Errorf("filter requires expression_version 1 and predicate")
		}
		return nil
	},
}

func requireMapping(r TransformRule) error {
	if len(r.Mapping) == 0 {
		return fmt.Errorf("rename_columns requires mapping")
	}
	return nil
}

func requireProjections(r TransformRule) error {
	if len(r.Projections) == 0 {
		return fmt.Errorf("project requires projections")
	}
	if r.ExpressionVersion != 1 {
		return fmt.Errorf("project requires expression_version 1")
	}
	return nil
}

func requireCondition(r TransformRule) error {
	if r.Condition == "" {
		return fmt.Errorf("filter_rows requires condition")
	}
	return nil
}

func requireColumnAndFunction(r TransformRule) error {
	if r.Column == "" || r.Function == "" {
		return fmt.Errorf("apply_function requires column and function")
	}
	return nil
}

func requireColumnAndMapping(r TransformRule) error {
	if r.Column == "" || len(r.Mapping) == 0 {
		return fmt.Errorf("replace_values requires column and mapping")
	}
	return nil
}

func requireKeyColumns(r TransformRule) error {
	if len(r.Columns) == 0 {
		return fmt.Errorf("deduplicate requires columns (key columns)")
	}
	return nil
}

// requireAggregation mirrors aggregate's own normalisation: "aggregations"
// is accepted as an alias for "agg_fields" (template compatibility), so a
// rule carrying either has its aggregation and only a rule carrying
// neither is unrunnable.
func requireAggregation(r TransformRule) error {
	if len(r.GroupBy) == 0 {
		return fmt.Errorf("aggregate requires group_by columns")
	}
	if len(r.AggFields) == 0 && len(r.Aggregations) == 0 {
		return fmt.Errorf("aggregate requires at least one aggregation (e.g. sum, count, avg) — add aggregation fields in the transform config")
	}
	return nil
}

// transformRuleError reports why a rule cannot run, from its config
// alone, or nil when nothing is missing.
//
// An unrecognised type is reported here rather than left to applyRule's
// default branch, so a typo in a rule type is caught when the pipeline is
// saved instead of when it runs.
func transformRuleError(r TransformRule) error {
	check, known := transformRuleRequirements[r.Type]
	if !known {
		return fmt.Errorf("unsupported transform type: %s", r.Type)
	}
	return check(r)
}

// transformRuleErrors reports every rule in a transform node that cannot
// run, for validation at save time.
//
// It decodes through parseNodeTransformRules, the same decoder runTransform
// uses, so validation judges exactly the rules execution will receive
// rather than a second reading of the same config.
func transformRuleErrors(node models.Node) []string {
	raw, ok := node.Config["rules"]
	if !ok || raw == nil {
		// Absence is the existing "no transform rules defined" warning's
		// business, not an error here.
		return nil
	}
	rules, err := parseNodeTransformRules(node)
	if err != nil {
		return []string{fmt.Sprintf("transform rules cannot be read: %v", err)}
	}
	var out []string
	for i, rule := range rules {
		if rule.Type == "" {
			out = append(out, fmt.Sprintf("rule %d: no type set", i+1))
			continue
		}
		if err := transformRuleError(rule); err != nil {
			out = append(out, fmt.Sprintf("rule %d (%s): %v", i+1, rule.Type, err))
		}
	}
	return out
}
