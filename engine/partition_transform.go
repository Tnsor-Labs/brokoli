package engine

import (
	"fmt"

	"github.com/Tnsor-Labs/brokoli/pkg/common"
)

// functionRefScript extracts the runnable Python source for a
// dataset_map/dataset_filter node's referenced function.
//
// Config shape verified against brokoli-sdk@main (pipeline.py: _func_ref,
// _add_partition_transform_node): DatasetRef.map(fn)/.filter(fn) compile
// to {"function": {"name": <fn.__name__>, "doc": <first line of
// fn.__doc__, if any>}} — a name/description reference only. The SDK
// deliberately does NOT serialize fn's source ("the function you pass is
// recorded as a name reference, not serialized as runnable code or
// invoked locally... there is no backend support to execute them yet" —
// pipeline.py's DatasetRef docstring), unlike the @task/@map/@filter
// decorators (which generate a runnable script for a "code" node) or a
// hand-written "code" node's config["script"].
//
// This engine defines a forward-compatible extension of that shape:
// config.function.script (+ optional config.function.language, reserved
// for future use — today only Python via ExecuteCodeNode is supported)
// may carry real source, using the exact same contract as a "code" node's
// script (see engine/codenode.go: rows/columns/config/params in, sets
// output_data). When script is present, ExecutePartitionTransform below
// actually runs it. When it's absent — which is what every pipeline
// emitted by today's SDK will have, since the SDK doesn't populate it —
// this returns a clear, specific error instead of silently no-oping.
func functionRefScript(cfg map[string]interface{}, kind string) (name, script string, err error) {
	if msg := functionRefConfigError(cfg, kind); msg != "" {
		return "", "", fmt.Errorf("%s node: %s", kind, msg)
	}
	fnRef, _ := cfg["function"].(map[string]interface{})
	name = getStr(fnRef, "name")
	script = getStr(fnRef, "script")
	if script == "" {
		return name, "", fmt.Errorf(
			"%s node references function %q, but its config carries no runnable source "+
				"(config.function.script). brokoli-sdk's DatasetRef.map()/.filter() "+
				"(brokoli-sdk pipeline.py: _func_ref, _add_partition_transform_node) currently "+
				"serialize only a name/doc reference for the function, not its source — there is "+
				"nothing to execute yet. This is a known, documented SDK-side limitation (RFC §12.1 "+
				"backend physical-planner territory), not a silent pass-through or an engine bug. "+
				"Supply config.function.script directly (same contract as a 'code' node's script) "+
				"to actually run a %s transform today",
			kind, name, kind)
	}
	return name, script, nil
}

// ExecutePartitionTransform runs a dataset_map/dataset_filter node's
// referenced function against the WHOLE upstream dataset, once, via the
// exact same subprocess bridge "code" nodes use (ExecuteCodeNode,
// engine/codenode.go). See functionRefScript's doc comment for the exact
// config shape and the current brokoli-sdk gap this works around.
//
// WHOLE-DATASET, NOT PER-PARTITION: brokoli-sdk's DatasetRef.map()/
// .filter() compile to these node types intending future per-partition,
// parallel execution once the backend physical planner exists (RFC
// §12.1). This engine has no partitioning system yet, so the referenced
// function's script runs exactly once against the entire upstream
// dataset in one shot — a correct, honest first version, not a silent
// stand-in for real partitioned execution. A future partitioned
// implementation is a distinct, later enhancement.
//
// It is a free function (not a Runner method) so it can be unit tested
// directly without a full Runner/store, matching this package's existing
// convention for node logic (JoinDatasets, ExecuteCodeNode).
func ExecutePartitionTransform(cfg map[string]interface{}, input *common.DataSet, runParams map[string]string, kind string) (result *common.DataSet, fnName string, stderr string, err error) {
	fnName, script, err := functionRefScript(cfg, kind)
	if err != nil {
		return nil, fnName, "", err
	}

	timeoutSec := 30
	if t, ok := cfg["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}

	// Strip "function" before handing config to the script — avoid
	// echoing the (potentially large) script source back into itself via
	// BROKED_CONFIG, and avoid it leaking into user-visible config output.
	configForScript := make(map[string]interface{}, len(cfg))
	for k, v := range cfg {
		if k != "function" {
			configForScript[k] = v
		}
	}

	result, stderr, err = ExecuteCodeNode(script, input, configForScript, runParams, timeoutSec)
	return result, fnName, stderr, err
}
