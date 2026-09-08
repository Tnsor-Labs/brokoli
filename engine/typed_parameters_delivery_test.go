package engine

import (
	"context"
	"testing"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// Issue #487: ADR-032 declared parameters were resolved, validated, and
// recorded on the Run row, then never delivered -- a task ran with its
// defaults while the Run row said otherwise. These tests run a real
// Python child and assert on what the script actually observed, because
// the bug was invisible to everything that stopped short of that.
func runScriptWithParams(t *testing.T, script string, runParams map[string]string, taskParams map[string]interface{}) *common.DataSet {
	t.Helper()
	in := &common.DataSet{Columns: []string{"id"}, Rows: []common.DataRow{{"id": 1}}}
	out, stderr, err := ExecuteCodeNodeContext(context.Background(), script, in, map[string]interface{}{}, runParams, taskParams, 60)
	if err != nil {
		t.Fatalf("execute: %v (stderr: %s)", err, stderr)
	}
	return out
}

func TestDeclaredParametersReachTheScript(t *testing.T) {
	out := runScriptWithParams(t, `
output_data = {"columns": ["threshold", "region"],
               "rows": [{"threshold": parameters.get("threshold"),
                         "region": parameters.get("region")}]}
`, nil, map[string]interface{}{"threshold": 0.9, "region": "eu"})

	if len(out.Rows) != 1 {
		t.Fatalf("expected one row, got %d", len(out.Rows))
	}
	got := out.Rows[0]
	if got["region"] != "eu" {
		t.Errorf("region = %v, want eu -- the submitted value never reached the script", got["region"])
	}
	// The whole point of a separate binding: 0.9 must arrive as a number,
	// not as the string "0.9" a map[string]string would have produced.
	if f, ok := got["threshold"].(float64); !ok || f != 0.9 {
		t.Errorf("threshold = %#v (%T), want float64(0.9)", got["threshold"], got["threshold"])
	}
}

// The legacy string params and the declared ones are documented as
// "never silently merged" (models.Pipeline.Parameters). A same-named key
// in each must therefore remain independently readable, not collapse.
func TestDeclaredParametersDoNotMergeIntoParams(t *testing.T) {
	out := runScriptWithParams(t, `
output_data = {"columns": ["p", "d"],
               "rows": [{"p": params.get("region"), "d": parameters.get("region")}]}
`, map[string]string{"region": "legacy"}, map[string]interface{}{"region": "declared"})

	got := out.Rows[0]
	if got["p"] != "legacy" {
		t.Errorf("params[region] = %v, want legacy -- the declared value leaked into params", got["p"])
	}
	if got["d"] != "declared" {
		t.Errorf("parameters[region] = %v, want declared", got["d"])
	}
}

// A pipeline that declares nothing must see an empty mapping, never a
// missing name: a script referencing `parameters` should not NameError
// just because this run supplied none.
func TestParametersBindingExistsWhenNoneDeclared(t *testing.T) {
	out := runScriptWithParams(t, `
output_data = {"columns": ["n"], "rows": [{"n": len(parameters)}]}
`, nil, nil)
	if got := out.Rows[0]["n"]; got != float64(0) && got != int64(0) {
		t.Errorf("len(parameters) = %#v, want 0", got)
	}
}

// The warm pool (ADR-029) is default-on, so the tests above exercise the
// pooled path only. The one-shot path builds BROKED_TASK_PARAMS itself
// and is a completely separate code path -- mutation-testing the pooled
// one left this entirely uncovered, so it gets its own case rather than
// an assumption that "the delivery works".
func TestDeclaredParametersReachTheScriptWithoutThePool(t *testing.T) {
	t.Setenv("BROKOLI_CODE_POOL", "0")
	out := runScriptWithParams(t, `
output_data = {"columns": ["threshold"], "rows": [{"threshold": parameters.get("threshold")}]}
`, nil, map[string]interface{}{"threshold": 0.9})

	if f, ok := out.Rows[0]["threshold"].(float64); !ok || f != 0.9 {
		t.Errorf("threshold = %#v (%T), want float64(0.9) on the one-shot path", out.Rows[0]["threshold"], out.Rows[0]["threshold"])
	}
}

// The delivery is worthless to an SDK that cannot detect it. An SDK
// deploying a parameter-declaring pipeline needs to refuse a server that
// would accept the declaration and then ignore the values, which it can
// only do if this name is advertised.
func TestTaskParametersFeatureIsAdvertised(t *testing.T) {
	for _, f := range models.SupportedExecutionFeatures {
		if f == "task-parameters-v1" {
			return
		}
	}
	t.Fatal("'task-parameters-v1' is not in models.SupportedExecutionFeatures; the capabilities endpoint and SDK preflight cannot see it")
}
