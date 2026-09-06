package engine

// 'task' IR node execution (ADR-033 rollout phase 2b, issue #439 step
// 5): the first real dispatch path for a task-runtime/v1 harness. Local
// (in-process) execution only -- remote/distributed dispatch of task
// nodes is a later phase, matching how task_bundle/v1 code nodes are
// also local-only on the remote instance-worker path today
// (executeCodeWorkOrder's own explicit refusal of task_bundle nodes
// there). Trusted isolation profile only, per this ADR's own OSS
// scoping decision.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
	"github.com/Tnsor-Labs/brokoli/pkg/proctree"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness/pyharness"
	"github.com/Tnsor-Labs/brokoli/store"
)

// taskBundleV2Reference parses a task node's 'task_bundle' config object
// into the digest it names. Unlike a code node (where task_bundle is one
// of two mutually exclusive script sources), a 'task' node has no
// bare-script form -- task_bundle is mandatory. Format is re-verified
// here, not just at validation, matching taskBundleReference's own
// reasoning in engine/codenode.go: a run-time path is not gated by
// validation for a pipeline that reached run time some other way.
func taskBundleV2Reference(config map[string]interface{}) (digest string, err error) {
	raw, present := config["task_bundle"]
	if !present {
		return "", fmt.Errorf("task node requires 'task_bundle' in config")
	}
	m, isMap := raw.(map[string]interface{})
	if !isMap {
		return "", fmt.Errorf("task node 'task_bundle' must be an object with digest and format")
	}
	digest, _ = m["digest"].(string)
	if !taskbundlev2.IsDigest(digest) {
		return "", fmt.Errorf("task node 'task_bundle.digest' must be a content address of the form \"sha256:<64 hex chars>\"")
	}
	if format, _ := m["format"].(string); format != taskbundlev2.Format {
		return "", fmt.Errorf("task node 'task_bundle.format' is unsupported: %q", format)
	}
	return digest, nil
}

// materializeTaskBundleV2 fetches a task-bundle/v2 archive from the
// org-scoped store and safely extracts it to a fresh temp dir, verifying
// every declared file's size and digest along the way (Extract does
// this). The returned dir is owned by the caller and must be removed
// when execution is done.
func (r *Runner) materializeTaskBundleV2(digest string) (*taskbundlev2.Manifest, string, error) {
	if r.store == nil {
		return nil, "", fmt.Errorf("task bundle %s: this server has no task bundle store", digest)
	}
	sb, ok := r.store.(store.TaskBundleV2Store)
	if !ok {
		return nil, "", fmt.Errorf("task bundle %s: this server has no task bundle store", digest)
	}
	archive, err := sb.GetTaskBundleV2(r.orgID, digest)
	if err != nil {
		if errors.Is(err, store.ErrTaskBundleV2NotFound) {
			return nil, "", fmt.Errorf("task bundle %s is not stored for this org: upload the bundle before running a pipeline that references it", digest)
		}
		return nil, "", fmt.Errorf("task bundle %s: %w", digest, err)
	}
	dir, err := os.MkdirTemp("", "brokoli-taskbundlev2-")
	if err != nil {
		return nil, "", err
	}
	m, err := taskbundlev2.Extract(archive, dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, "", fmt.Errorf("task bundle %s: %w", digest, err)
	}
	return m, dir, nil
}

// runTask executes a 'task' IR node: fetch+extract its task-bundle/v2,
// select the python payload (trusted profile only, phase 2b), and run
// it through pkg/taskharness+pyharness.
func (r *Runner) runTask(ctx context.Context, node models.Node, input *common.DataSet) (*common.DataSet, error) {
	digest, err := taskBundleV2Reference(node.Config)
	if err != nil {
		return nil, err
	}
	manifest, bundleDir, err := r.materializeTaskBundleV2(digest)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(bundleDir)

	payload, err := taskbundlev2.SelectPythonPayload(manifest)
	if err != nil {
		return nil, fmt.Errorf("task bundle %s: %w", digest, err)
	}

	pythonPath, reason := plugins.ResolvePython("")
	if reason != "" {
		return nil, fmt.Errorf("task node requires a python interpreter: %s", reason)
	}

	attemptDir, err := os.MkdirTemp("", "brokoli-task-attempt-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(attemptDir)

	harnessPath, err := pyharness.Materialize(attemptDir)
	if err != nil {
		return nil, fmt.Errorf("materialize task harness: %w", err)
	}

	outputStagingDir := filepath.Join(attemptDir, "out")
	if err := os.MkdirAll(outputStagingDir, 0o750); err != nil {
		return nil, err
	}
	resultPath := filepath.Join(attemptDir, "result.json")
	invocationPath := filepath.Join(attemptDir, "invocation.json")

	// A task's keyword parameters come from the pipeline's run
	// parameters -- the same "sugar for a pipeline-level parameter"
	// mechanism ADR-032 step 3 already established for an inferred task
	// keyword parameter (issue #439's own step-3 scoping note), since no
	// typed per-node parameter binding exists yet.
	var kwargs map[string]interface{}
	if r.varCtx != nil && len(r.varCtx.Params) > 0 {
		kwargs = make(map[string]interface{}, len(r.varCtx.Params))
		for k, v := range r.varCtx.Params {
			kwargs[k] = v
		}
	}

	if err := pyharness.WriteInvocation(invocationPath, pyharness.Invocation{
		SysPath:         []string{bundleDir},
		Module:          payload.Entrypoint.Module,
		Symbol:          payload.Entrypoint.Symbol,
		Kwargs:          kwargs,
		InterfaceDigest: manifest.InterfaceDigest,
	}); err != nil {
		return nil, fmt.Errorf("write task invocation: %w", err)
	}

	timeoutSec := 30
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// Trusted-profile resource baseline: reused, not reimplemented, from
	// the same ADR-029 primitive a code node is bounded by. Memory
	// (RLIMIT_AS) is applied by the harness process itself, reading the
	// same BROKED_LIMIT_MEMORY_MB env var a code node's wrapper reads --
	// pkg/proctree.Rlimits has no memory field (ADR-030: RLIMIT_AS is
	// wrong for a Node worker, but a task harness is always Python here).
	limits := codeexec.Resolve(node.Config)
	env := append(os.Environ(), limits.Env()...)

	r.log(node.ID, models.LogLevelInfo, "task exec: bundle=%s payload=%s %s", digest, payload.ID, limits)

	start := taskharness.NewStartFrame(invocationPath, resultPath, outputStagingDir)
	result, err := taskharness.Run(runCtx, start, taskharness.Options{
		Command: pyharness.Command(pythonPath, harnessPath),
		Env:     env,
		Rlimits: proctree.Rlimits{
			CPUSeconds:    uint64(max(limits.CPUSeconds, 0)),
			FileSizeBytes: uint64(max(limits.FileSizeMB, 0)) * 1024 * 1024,
			OpenFiles:     uint64(max(limits.OpenFiles, 0)),
		},
	}, taskharness.Handlers{
		OnLog: func(l taskharness.Log) {
			level := models.LogLevelInfo
			switch l.Level {
			case "warning":
				level = models.LogLevelWarning
			case "error":
				level = models.LogLevelError
			}
			r.log(node.ID, level, "%s", l.Message)
		},
		OnProgress: func(p taskharness.Progress) {
			r.log(node.ID, models.LogLevelInfo, "progress: %v", p.Completed)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("task bundle %s: run harness: %w", digest, err)
	}
	if result.Failure != nil {
		return nil, fmt.Errorf("task bundle %s: %s: %s", digest, result.Failure.Category, result.Failure.Message)
	}

	return readTaskResult(resultPath, manifest.InterfaceDigest)
}

// readTaskResult reads and interprets a task-result-v1 candidate
// manifest, mapping its single "result" output port into the row-shaped
// DataSet contract every other node type returns downstream. Phase 2b
// supports only the "scalar" output kind -- "dataset"/"artifact"/
// "collection" kinds need a codec reader this phase does not build; a
// task declaring one of those fails clearly rather than silently
// mishandling it.
func readTaskResult(resultPath, wantInterfaceDigest string) (*common.DataSet, error) {
	raw, err := os.ReadFile(resultPath) // #nosec G304 -- resultPath is this attempt's own worker-generated scratch path, not attacker-controlled
	if err != nil {
		return nil, fmt.Errorf("read task result: %w", err)
	}
	var candidate struct {
		Contract        string `json:"contract"`
		InterfaceDigest string `json:"interface_digest"`
		Outputs         map[string]struct {
			Kind  string      `json:"kind"`
			Value interface{} `json:"value"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal(raw, &candidate); err != nil {
		return nil, fmt.Errorf("task result is not valid JSON: %w", err)
	}
	if candidate.Contract != "brokoli.task-result/v1" {
		return nil, fmt.Errorf("task result has unexpected contract %q", candidate.Contract)
	}
	if candidate.InterfaceDigest != wantInterfaceDigest {
		return nil, fmt.Errorf("task result interface_digest %q does not match the bundle's %q", candidate.InterfaceDigest, wantInterfaceDigest)
	}
	out, ok := candidate.Outputs["result"]
	if !ok {
		return nil, fmt.Errorf("task result has no \"result\" output port")
	}
	if out.Kind != "scalar" {
		return nil, fmt.Errorf("task output kind %q is not yet supported (phase 2b handles \"scalar\" only)", out.Kind)
	}
	return &common.DataSet{
		Columns: []string{"result"},
		Rows:    []common.DataRow{{"result": out.Value}},
	}, nil
}
