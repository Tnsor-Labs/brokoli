package engine

// 'task' IR node execution (ADR-033 rollout, issue #439 step 5). Phase 2b
// shipped local (in-process) execution only. Phase 2c adds remote
// dispatch: a task node whose Runner has both an execution-attempt store
// and an instance job queue wired (the same condition code-node dynamic
// expansion already uses) dispatches through the existing instance job
// queue instead of running in-process — one job per node, not a fan-out,
// since a task node has no expansion semantics.
//
// executeTaskBundle is the shared core both paths call: given a resolved
// task_bundle digest, an org, and run parameters, fetch+extract the
// bundle, select its python payload, and run it through
// pkg/taskharness+pyharness. Runner.runTask supplies these from its own
// fields for local execution; ExecuteTaskWorkOrderContext supplies them
// from an extensions.InstanceWorkOrder for remote execution — "how do I
// actually run a task bundle" is answered once, not once per transport,
// the same discipline ExecuteInstanceWorkOrderContext's own doc comment
// states for code/source_api.
//
// Trusted isolation profile only, per this ADR's own OSS scoping
// decision.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
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

// taskRuntimeCapability is the flat capability tag a remote worker must
// advertise to be eligible for any task-node job (the OSS-side half of
// the EE capability-flattening decision, brokoli-ee#55: ADR-033 section
// 5's structured requirements collapse to plain AND-matched strings).
// Deliberately coarse: naming a specific runtime/adapter-version tier
// would require the DISPATCHER to fetch and inspect the bundle manifest
// before dispatching it, defeating the point of not doing that work
// twice (once here, once on the worker that actually runs it) — a finer
// tag scheme is real, separate design work, left open exactly as
// recorded on ee#55.
const taskRuntimeCapability = "task-runtime-v1"

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
// when execution is done. A free function, not a Runner method: the
// remote executor (ExecuteTaskWorkOrderContext) has a store but no
// Runner.
func materializeTaskBundleV2(s store.Store, orgID, digest string) (*taskbundlev2.Manifest, string, error) {
	if s == nil {
		return nil, "", fmt.Errorf("task bundle %s: this server has no task bundle store", digest)
	}
	sb, ok := s.(store.TaskBundleV2Store)
	if !ok {
		return nil, "", fmt.Errorf("task bundle %s: this server has no task bundle store", digest)
	}
	archive, err := sb.GetTaskBundleV2(orgID, digest)
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

// executeTaskBundle fetches+extracts digest, selects its python payload,
// and runs it through pkg/taskharness+pyharness, mapping the outcome
// into the DataSet contract every other node type returns. Shared by
// Runner.runTask (local) and ExecuteTaskWorkOrderContext (remote) — see
// this file's own doc comment.
func executeTaskBundle(ctx context.Context, s store.Store, orgID, digest string, config map[string]interface{}, runParams map[string]string, timeoutSec int, handlers taskharness.Handlers) (*common.DataSet, error) {
	manifest, bundleDir, err := materializeTaskBundleV2(s, orgID, digest)
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
	if len(runParams) > 0 {
		kwargs = make(map[string]interface{}, len(runParams))
		for k, v := range runParams {
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

	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// Trusted-profile resource baseline: reused, not reimplemented, from
	// the same ADR-029 primitive a code node is bounded by. Memory
	// (RLIMIT_AS) is applied by the harness process itself, reading the
	// same BROKED_LIMIT_MEMORY_MB env var a code node's wrapper reads --
	// pkg/proctree.Rlimits has no memory field (ADR-030: RLIMIT_AS is
	// wrong for a Node worker, but a task harness is always Python here).
	limits := codeexec.Resolve(config)
	env := append(os.Environ(), limits.Env()...)

	start := taskharness.NewStartFrame(invocationPath, resultPath, outputStagingDir)
	result, err := taskharness.Run(runCtx, start, taskharness.Options{
		Command: pyharness.Command(pythonPath, harnessPath),
		Env:     env,
		Rlimits: proctree.Rlimits{
			CPUSeconds:    uint64(max(limits.CPUSeconds, 0)),
			FileSizeBytes: uint64(max(limits.FileSizeMB, 0)) * 1024 * 1024,
			OpenFiles:     uint64(max(limits.OpenFiles, 0)),
		},
	}, handlers)
	if err != nil {
		return nil, fmt.Errorf("task bundle %s: run harness: %w", digest, err)
	}
	if result.Failure != nil {
		return nil, fmt.Errorf("task bundle %s: %s: %s", digest, result.Failure.Category, result.Failure.Message)
	}

	return readTaskResult(resultPath, manifest.InterfaceDigest)
}

// runTask executes a 'task' IR node. Local (in-process) when this Runner
// has no execution-attempt store or no instance job queue wired (single-
// process deployments, or a shared-store deployment with no remote
// workers configured) -- the exact condition code-node dynamic expansion
// already uses to make the same choice. Remote otherwise: dispatched as
// one instance job, not a fan-out (a task node has no expansion
// semantics), through dispatchTaskInstanceRemotely.
//
// attempt/execFencingGen are the caller's own node-level attempt number
// and already-claimed fencing generation for this node's whole-node
// execution attempt (see runNodeLogic's own doc comment) -- meaningless
// when there is no execution-attempt store, which is exactly when they
// go unused below.
func (r *Runner) runTask(ctx context.Context, node models.Node, input *common.DataSet, attempt int, execFencingGen int64) (*common.DataSet, error) {
	digest, err := taskBundleV2Reference(node.Config)
	if err != nil {
		return nil, err
	}

	timeoutSec := 30
	if t, ok := node.Config["timeout"].(float64); ok && t > 0 {
		timeoutSec = int(t)
	}

	if _, ok := r.store.(store.ExecutionAttemptStore); ok && r.instanceJobQueue != nil {
		return r.dispatchTaskInstanceRemotely(node, digest, timeoutSec, attempt, execFencingGen)
	}

	var runParams map[string]string
	if r.varCtx != nil {
		runParams = r.varCtx.Params
	}
	limits := codeexec.Resolve(node.Config)
	r.log(node.ID, models.LogLevelInfo, "task exec: bundle=%s %s", digest, limits)
	return executeTaskBundle(ctx, r.store, r.orgID, digest, node.Config, runParams, timeoutSec, taskharness.Handlers{
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
}

// dispatchTaskInstanceRemotely enqueues this task node as a single
// WorkOrder-bearing extensions.RunJob and waits for a remote worker to
// complete it, reusing dispatchInstanceWorkOrderRemotely's existing
// enqueue-and-wait machinery (shared with code expansion items and
// source_api pagination pages -- only the WorkOrder payload and
// capability tags differ).
//
// Unlike expansion items and pagination pages (each their own distinct
// "idx:N"/"page-N" instance, claimed fresh here), a task node's one
// instance IS the whole node: runNodeLogic's caller already claimed and
// is already renewing the lease on (run, node.ID, "", attempt) before
// runTask ever runs (see runNodeLogic's own doc comment on
// execFencingGen), and settles it (CompleteAttempt/FailAttempt) generically
// after this function returns, exactly as it already does for a local
// task-node failure. Dispatching here just reuses that same row and
// fencing generation rather than claiming a second, competing one at the
// same key -- dispatchInstanceWorkOrderRemotely's own renewal goroutine
// then redundantly (but harmlessly -- RenewLease is idempotent per
// fencing generation) renews the same lease the outer caller is already
// renewing.
func (r *Runner) dispatchTaskInstanceRemotely(node models.Node, digest string, timeoutSec, attempt int, execFencingGen int64) (*common.DataSet, error) {
	var runParams map[string]string
	if r.varCtx != nil {
		runParams = r.varCtx.Params
	}
	workOrder := &extensions.InstanceWorkOrder{
		NodeType:       string(models.NodeTypeTask),
		OrgID:          r.pipe.OrgID,
		Config:         node.Config,
		RunParams:      runParams,
		TimeoutSeconds: timeoutSec,
	}
	return r.dispatchInstanceWorkOrderRemotely(node.ID, attempt, "", execFencingGen, workOrder, timeoutSec, []string{taskRuntimeCapability})
}

// ExecuteTaskWorkOrderContext runs a task-node WorkOrder's actual work --
// the worker-side half of remote task-node dispatch. Unlike
// ExecuteInstanceWorkOrderContext (code/source_api, which carry their
// work inline and need no store), a task node's work is "go fetch this
// bundle and run it," so this function takes an explicit store rather
// than growing ExecuteInstanceWorkOrderContext's own signature -- doing
// that would ripple to every existing caller of that function (the
// enterprise WorkPool worker included) for a capability only the
// shared-store worker path (engine/instance_worker.go's
// executeInstanceJobContext) can support today. A caller without a
// store-aware dispatch loop yet (calling ExecuteInstanceWorkOrderContext
// directly) still gets today's "unsupported node type" refusal for task
// nodes, exactly like task_bundle/1 code nodes already do on that same
// path -- this function is the new, additional capability a dispatch
// loop opts into, not a replacement.
func ExecuteTaskWorkOrderContext(ctx context.Context, s store.Store, wo *extensions.InstanceWorkOrder) (*common.DataSet, error) {
	if wo == nil {
		return nil, fmt.Errorf("execute task instance work order: nil work order")
	}
	digest, err := taskBundleV2Reference(wo.Config)
	if err != nil {
		return nil, fmt.Errorf("execute task instance work order: %w", err)
	}
	// No log/progress handlers: this function has no Runner (and so no
	// run-scoped log sink) to attribute them to, matching
	// executeCodeWorkOrder's own documented choice to drop stderr here.
	return executeTaskBundle(ctx, s, wo.OrgID, digest, wo.Config, wo.RunParams, wo.TimeoutSeconds, taskharness.Handlers{})
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
