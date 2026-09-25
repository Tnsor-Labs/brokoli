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
// bundle, select a payload whose runtime class this build has a
// reference adapter for (phase 4a added the second one, node, alongside
// python), and run it through pkg/taskharness. Runner.runTask supplies these from its own
// fields for local execution; ExecuteTaskWorkOrderContext supplies them
// from an extensions.InstanceWorkOrder for remote execution — "how do I
// actually run a task bundle" is answered once, not once per transport,
// the same discipline ExecuteInstanceWorkOrderContext's own doc comment
// states for code/source_api.
//
// Trusted isolation profile only, per this ADR's own OSS scoping
// decision.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Tnsor-Labs/brokoli/extensions"
	"github.com/Tnsor-Labs/brokoli/models"
	"github.com/Tnsor-Labs/brokoli/pkg/artifact"
	"github.com/Tnsor-Labs/brokoli/pkg/codeexec"
	"github.com/Tnsor-Labs/brokoli/pkg/common"
	"github.com/Tnsor-Labs/brokoli/pkg/plugins"
	"github.com/Tnsor-Labs/brokoli/pkg/proctree"
	"github.com/Tnsor-Labs/brokoli/pkg/taskbundlev2"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness/jvmharness"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness/nodeharness"
	"github.com/Tnsor-Labs/brokoli/pkg/taskharness/pyharness"
	"github.com/Tnsor-Labs/brokoli/pkg/taskinterface"
	"github.com/Tnsor-Labs/brokoli/pkg/taskruntime"
	"github.com/Tnsor-Labs/brokoli/store"
)

// ErrTaskInputContractViolation reports rows reaching a task node that
// its own declared input port refuses. Distinct from
// ErrTaskOutputContractViolation because the two name different faults:
// an output violation is the task's own code getting its contract
// wrong, while an input violation means an UPSTREAM node produced
// something this task never agreed to accept -- the task itself is
// blameless, and pointing at it would send an author to the wrong file.
var ErrTaskInputContractViolation = errors.New("task input violates its declared contract")

// ErrTaskOutputContractViolation reports a task node whose returned value
// does not conform to its own declared output port type (ADR-032 section
// 10, ADR-033 rollout phase 3b). This is the engine's own post-hoc check
// on a harness that already claimed "completed" -- independent of
// pkg/taskharness's wire-level failure taxonomy (FailureContractViolation
// there covers a harness's OWN self-reported failures; this sentinel
// covers a claim the engine itself caught being wrong). Mirrors
// ErrParameterResolution's wrapping pattern.
var ErrTaskOutputContractViolation = errors.New("task output violates its declared contract")

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

// taskRuntimeCapabilityFor names the capability a worker advertises when
// it can actually run a given runtime class, e.g. "task-runtime-v1:node".
// The protocol tag above says "I speak task-runtime/v1"; this one says
// "and I have this adapter's runtime available" -- two separate claims,
// because phase 4a made them genuinely separable (a host with python but
// no node satisfies the first and only one half of the second).
func taskRuntimeCapabilityFor(runtimeClass string) string {
	return taskRuntimeCapability + ":" + runtimeClass
}

// taskRuntimeCapabilities returns the capability tags a task job should
// require, given the bundle it will run.
//
// Capability matching is AND-superset (a worker must advertise every tag
// a job asks for), which cannot express "python OR node". So a bundle
// whose payloads all share one runtime class gets that class's tag; a
// bundle offering a CHOICE of runtimes gets only the protocol tag, since
// any task-capable worker can pick a payload it can run -- and would be
// wrongly excluded by a tag naming a class it happens to lack.
//
// This deliberately does NOT resolve or pin a payload control-plane-side
// the way ADR-033 section 4 ultimately calls for. Payload selection is
// platform-dependent (taskbundlev2.PlatformMatches compares against the
// resolving host's own GOOS/GOARCH), so pinning here would pin for the
// CONTROL PLANE's platform and silently change which payload a
// heterogeneous fleet runs. Resolution stays worker-side until workers
// advertise their platform and the control plane can resolve against
// the target rather than itself; this function only reads what the
// manifest offers, and changes nothing about who decides.
func taskRuntimeCapabilities(manifest *taskbundlev2.Manifest) []string {
	caps := []string{taskRuntimeCapability}
	if manifest == nil || len(manifest.Payloads) == 0 {
		return caps
	}
	first := manifest.Payloads[0].Runtime
	for _, p := range manifest.Payloads[1:] {
		if p.Runtime != first {
			return caps // a choice of runtimes: no single tag can describe it
		}
	}
	return append(caps, taskRuntimeCapabilityFor(first))
}

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

// executionProfile names the one resource/isolation policy this rollout
// phase applies to every task attempt -- "trusted@1": the trusted
// isolation profile (ADR-033 section 12), revision 1 of its concrete
// resource baseline (pkg/codeexec.Resolve's limits, reused as-is per
// executeTaskBundle). Pinned into every resolved execution record; a
// future revision or profile bumps this string, the same "name@revision"
// convention ADR-033 section 4's own examples use ("standard@7").
const executionProfile = "trusted@1"

// resolveExecutionRecord returns the payload a task attempt must run and
// the execution record pinning that choice (ADR-033 section 4): the
// first attempt for (runID, nodeID) resolves fresh and pins the result;
// every later attempt (a retry, or a redelivery of the same remote job)
// fetches that same pin and reuses it verbatim rather than re-resolving
// -- so a worker-fleet change between attempts can never silently swap
// out what an already-running attempt lineage executes. Degrades to
// fresh resolution every time (the pre-phase-2d behavior) on a store
// that has not adopted the optional ResolvedExecutionRecordStore
// capability yet.
func resolveExecutionRecord(s store.Store, runID, nodeID, digest string, manifest *taskbundlev2.Manifest) (*taskbundlev2.Payload, *taskruntime.ResolvedExecutionRecord, error) {
	rs, ok := s.(store.ResolvedExecutionRecordStore)
	if !ok {
		payload, err := taskbundlev2.SelectPayload(manifest, supportedTaskRuntimes)
		if err != nil {
			return nil, nil, fmt.Errorf("task bundle %s: %w", digest, err)
		}
		return payload, nil, nil
	}

	pinned, err := rs.GetResolvedExecutionRecord(runID, nodeID)
	if err == nil {
		payload, found := taskbundlev2.FindPayload(manifest, pinned.PayloadID)
		if !found {
			// Cannot happen for a content-addressed, immutable bundle (the
			// same digest always yields the same manifest) short of a
			// genuine bug -- named loudly rather than silently falling
			// back to a fresh selection, which would be exactly the
			// determinism violation this phase exists to prevent.
			return nil, nil, fmt.Errorf("task bundle %s: resolved execution record pins payload %q, which is not in this bundle's manifest", digest, pinned.PayloadID)
		}
		if !taskbundlev2.PlatformMatches(payload.OS, payload.Arch) {
			return nil, nil, fmt.Errorf(
				"task bundle %s: platform: pinned payload %q (os=%s arch=%s) is not available on this host (%s/%s) -- retries reuse the resolved execution record rather than silently re-resolving to a different payload; an operator-approved re-resolution is required (not yet built)",
				digest, payload.ID, payload.OS, payload.Arch, runtime.GOOS, runtime.GOARCH,
			)
		}
		return payload, pinned, nil
	}
	if !errors.Is(err, store.ErrResolvedExecutionRecordNotFound) {
		return nil, nil, fmt.Errorf("task bundle %s: read resolved execution record: %w", digest, err)
	}

	payload, err := taskbundlev2.SelectPayload(manifest, supportedTaskRuntimes)
	if err != nil {
		return nil, nil, fmt.Errorf("task bundle %s: %w", digest, err)
	}
	envDigest, err := computeExecutionEnvironmentDigest(payload, manifest)
	if err != nil {
		return nil, nil, fmt.Errorf("task bundle %s: compute execution environment digest: %w", digest, err)
	}
	record := &taskruntime.ResolvedExecutionRecord{
		RuntimeProtocol:            taskharness.Protocol,
		BundleDigest:               digest,
		PayloadID:                  payload.ID,
		PayloadDigest:              payload.PayloadDigest,
		ExecutionEnvironmentDigest: envDigest,
		InterfaceDigest:            manifest.InterfaceDigest,
		ExecutionProfile:           executionProfile,
	}
	if _, err := rs.PutResolvedExecutionRecord(runID, nodeID, record); err != nil {
		if errors.Is(err, store.ErrResolvedExecutionRecordConflict) {
			return nil, nil, fmt.Errorf("task bundle %s: a different execution environment is already pinned for this run's node -- refusing to run (this should be impossible for one serialized attempt lineage; investigate a concurrency bug rather than retrying)", digest)
		}
		return nil, nil, fmt.Errorf("task bundle %s: pin resolved execution record: %w", digest, err)
	}
	return payload, record, nil
}

// computeExecutionEnvironmentDigest hashes together everything ADR-033
// section 4 rule 3 says an execution environment digest must cover that
// this phase can actually observe: the harness adapter identity, the
// resolved runtime's own reported version, the payload's dependency lock
// digest (already verified by pkg/taskbundlev2.Extract's own per-file
// digest check -- reused here, not rehashed), and the platform ABI.
// "Declared system libraries" is the one listed component with nothing
// to hash yet anywhere in this codebase -- a real, documented gap, not
// silently ignored.
//
// The adapter identity and runtime version are per runtime class, so a
// python payload and a node payload of the same bundle never collide on
// one environment digest -- exactly the distinction a pinned record
// exists to preserve across retries.
func computeExecutionEnvironmentDigest(payload *taskbundlev2.Payload, manifest *taskbundlev2.Manifest) (string, error) {
	var adapter, adapterVersion, runtimeBin string
	switch payload.Runtime {
	case taskbundlev2.RuntimePython:
		adapter, adapterVersion = pyharness.Adapter, pyharness.AdapterVersion
		path, reason := plugins.ResolvePython("")
		if reason != "" {
			return "", fmt.Errorf("resolve python interpreter: %s", reason)
		}
		runtimeBin = path
	case taskbundlev2.RuntimeNode:
		adapter, adapterVersion = nodeharness.Adapter, nodeharness.AdapterVersion
		path, reason := plugins.ResolveNode("")
		if reason != "" {
			return "", fmt.Errorf("resolve node runtime: %s", reason)
		}
		runtimeBin = path
	case taskbundlev2.RuntimeJVM:
		adapter, adapterVersion = jvmharness.Adapter, jvmharness.AdapterVersion
		tc, err := jvmharness.Resolve()
		if err != nil {
			return "", fmt.Errorf("resolve jvm runtime: %w", err)
		}
		runtimeBin = tc.Java
	default:
		return "", fmt.Errorf("payload %q declares runtime %q, which this server has no adapter for (supported: %s)", payload.ID, payload.Runtime, strings.Join(supportedTaskRuntimes, ", "))
	}
	runtimeVersion, err := runtimeVersionString(runtimeBin)
	if err != nil {
		return "", err
	}
	depLockDigest := "none"
	if payload.DependencyLock != "" {
		for _, f := range manifest.Files {
			if f.Path == payload.DependencyLock {
				depLockDigest = f.SHA256
				break
			}
		}
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"adapter=%s;adapter_version=%s;runtime=%s;runtime_version=%s;os=%s;arch=%s;dependency_lock_sha256=%s",
		adapter, adapterVersion, payload.Runtime, runtimeVersion, runtime.GOOS, runtime.GOARCH, depLockDigest,
	)))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// runtimeVersionString runs "<binPath> --version" and returns its
// trimmed output (e.g. "Python 3.12.3", "v20.20.2") -- part of the
// execution environment digest's "exact runtime build" coverage. Both
// reference adapters' runtimes answer the same flag.
func runtimeVersionString(binPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, binPath, "--version").CombinedOutput() // #nosec G204 -- binPath is resolved via pkg/plugins' runtime resolution, not attacker input
	if err != nil {
		return "", fmt.Errorf("%s --version: %w", binPath, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// executeTaskBundle fetches+extracts digest, resolves (or reuses a
// pinned) payload, and runs it through pkg/taskharness and that
// payload's own reference adapter,
// mapping the outcome into the DataSet contract every other node type
// returns. Shared by Runner.runTask (local) and ExecuteTaskWorkOrderContext
// (remote) — see this file's own doc comment. runID/nodeID identify the
// execution lineage a resolved execution record (ADR-033 section 4) is
// pinned against.
func executeTaskBundle(ctx context.Context, s store.Store, blobs artifact.Store, orgID, runID, nodeID, digest string, config, nodeInterface map[string]interface{}, runParams map[string]string, input *common.DataSet, timeoutSec int, handlers taskharness.Handlers) (*common.DataSet, error) {
	manifest, bundleDir, err := materializeTaskBundleV2(s, orgID, digest)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(bundleDir)

	payload, _, err := resolveExecutionRecord(s, runID, nodeID, digest, manifest)
	if err != nil {
		return nil, err
	}

	attemptDir, err := os.MkdirTemp("", "brokoli-task-attempt-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(attemptDir)

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

	// Trusted-profile resource baseline: reused, not reimplemented, from
	// the same ADR-029 primitive a code node is bounded by.
	limits := codeexec.Resolve(config)

	// Only stage input for a node that actually declares an input port:
	// handing rows to a task whose contract never mentioned them would
	// invent a parameter the task author did not declare.
	var inputPath string
	if inPort, declaresInput := portValueFromInterface(nodeInterface, "inputs", "input"); declaresInput {
		// ADR-032 section 10: "Input validation occurs before user code
		// observes a value." Checked here, before the rows are staged --
		// a task that would receive data its own declared contract
		// rejects should never be started at all, so the failure names
		// the contract rather than surfacing later as a confusing error
		// inside the task.
		if err := validateTaskInputDataset(inPort, input); err != nil {
			return nil, err
		}
		if inputPath, err = writeTaskInputDataset(attemptDir, input); err != nil {
			return nil, err
		}
	}

	command, err := prepareTaskHarness(payload, manifest, bundleDir, attemptDir, invocationPath, kwargs, nodeInterface, inputPath, limits)
	if err != nil {
		return nil, err
	}

	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// The memory ceiling is applied per adapter, not here: harness.py
	// self-applies RLIMIT_AS from the BROKED_LIMIT_MEMORY_MB env var
	// below, while the Node adapter takes V8's --max-old-space-size in
	// its argv (pkg/proctree.Rlimits has no memory field, and ADR-030
	// records why RLIMIT_AS is the wrong instrument for Node) -- see
	// prepareTaskHarness. CPU, file size and open files are enforced
	// externally, identically, for both.
	// codeexec.WorkerEnv, not os.Environ: a task runs user code, and
	// handing it the engine's whole environment hands it whatever
	// credentials the engine holds. ADR-029 found and fixed exactly that
	// for code nodes; task nodes reintroduced it by inheriting the
	// parent environment wholesale, which a test now proves is not the
	// case. Same policy, same escape hatch (BROKOLI_CODE_PASS_ENV),
	// because "what may user code see" should not differ by which
	// execution path happened to run it.
	env := append(codeexec.WorkerEnv(), limits.Env()...)

	start := taskharness.NewStartFrame(invocationPath, resultPath, outputStagingDir)
	result, err := taskharness.Run(runCtx, start, taskharness.Options{
		Command: command,
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

	return readTaskResult(ctx, blobs, runID, resultPath, outputStagingDir, manifest.InterfaceDigest, nodeInterface)
}

// declaredOutputMediaType is the first media type the node's output port
// allows, handed to the harness so an artifact it writes is labelled the
// way the contract says rather than defaulting to opaque bytes. Empty
// when the port constrains nothing -- the harness then says
// octet-stream, and artifactMediaType accepts it because an
// unconstrained port accepts anything.
func declaredOutputMediaType(nodeInterface map[string]interface{}) string {
	pv, ok := portValueFromInterface(nodeInterface, "outputs", "result")
	if !ok || len(pv.MediaTypes) == 0 {
		return ""
	}
	return pv.MediaTypes[0]
}

// inputCodecFor names how a staged input file is encoded, and says
// nothing at all when there is no input -- an empty path with a codec
// would claim a format for a file that does not exist.
func inputCodecFor(inputPath string) string {
	if inputPath == "" {
		return ""
	}
	return CodecNDJSON
}

// declaredOutputKind reports the ADR-032 value kind the node's declared
// interface says its "result" port produces, or "" when the node
// declares no interface (absence stays honest -- the harness then uses
// its inline scalar default rather than being told to produce something
// nothing asked for).
func declaredOutputKind(nodeInterface map[string]interface{}) string {
	pv, ok := portValueFromInterface(nodeInterface, "outputs", "result")
	if !ok {
		return ""
	}
	return string(pv.Kind)
}

// taskBlobStore returns this Runner's blob store when it has one, for a
// task producing an artifact output.
//
// Optional by the same type assertion attachArtifactSink uses: an
// ArtifactStore that is not a BlobStoreProvider has nowhere to put
// bytes. nil is a legitimate answer -- readTaskArtifactOutput then
// refuses an artifact output by name, which is the honest result when
// there is nowhere to store one.
func (r *Runner) taskBlobStore() artifact.Store {
	provider, ok := r.artifactStore.(BlobStoreProvider)
	if !ok {
		return nil
	}
	return provider.Blobs()
}

// taskSharedBlobStore is the store for bytes another process will read:
// a task input staged for a remote worker, or an output that worker
// writes back.
//
// Distinct from taskBlobStore for the reason ADR-038 gives. That one is
// local-disk scratch in a distributed deployment, and handing it to a
// caller staging bytes for a different pod produced a 404 three hops
// later, from a component that had done nothing wrong (#572).
//
// nil is a legitimate answer, and callers must refuse by name rather
// than falling back to the per-pod store.
func (r *Runner) taskSharedBlobStore() artifact.Store {
	provider, ok := r.artifactStore.(SharedBlobStoreProvider)
	if !ok {
		return nil
	}
	return provider.SharedBlobs()
}

// supportedTaskRuntimes are the runtime classes this build has a
// reference adapter for (ADR-033 section 3's required two). Order here
// does not express a preference -- taskbundlev2.SelectPayload takes the
// first payload in MANIFEST order whose runtime is in this set, so a
// bundle author picks between equivalent payloads, not this list.
var supportedTaskRuntimes = []string{taskbundlev2.RuntimePython, taskbundlev2.RuntimeNode, taskbundlev2.RuntimeJVM}

// prepareTaskHarness materializes the reference adapter for payload's
// runtime class into attemptDir, writes that adapter's own invocation
// descriptor, and returns the argv to launch it.
//
// This switch is the ONLY language-specific invocation code in the
// engine (ADR-033 section 3: "the scheduler does not contain Python- or
// Node-specific invocation code. It selects an adapter by runtime class
// and protocol version"). Everything after it -- the JSONL protocol
// exchange, resource ceilings, result reading, contract validation -- is
// identical for every adapter.
func prepareTaskHarness(payload *taskbundlev2.Payload, manifest *taskbundlev2.Manifest, bundleDir, attemptDir, invocationPath string, kwargs map[string]interface{}, nodeInterface map[string]interface{}, inputPath string, limits codeexec.Limits) ([]string, error) {
	outputKind := declaredOutputKind(nodeInterface)
	outputMediaType := declaredOutputMediaType(nodeInterface)
	switch payload.Runtime {
	case taskbundlev2.RuntimePython:
		pythonPath, reason := plugins.ResolvePython("")
		if reason != "" {
			return nil, fmt.Errorf("task node requires a python interpreter: %s", reason)
		}
		harnessPath, err := pyharness.Materialize(attemptDir)
		if err != nil {
			return nil, fmt.Errorf("materialize task harness: %w", err)
		}
		if err := pyharness.WriteInvocation(invocationPath, pyharness.Invocation{
			SysPath:         []string{bundleDir},
			Module:          payload.Entrypoint.Module,
			Symbol:          payload.Entrypoint.Symbol,
			Kwargs:          kwargs,
			InterfaceDigest: manifest.InterfaceDigest,
			OutputKind:      outputKind,
			OutputMediaType: outputMediaType,
			InputPath:       inputPath,
			InputCodec:      inputCodecFor(inputPath),
		}); err != nil {
			return nil, fmt.Errorf("write task invocation: %w", err)
		}
		return pyharness.Command(pythonPath, harnessPath), nil
	case taskbundlev2.RuntimeNode:
		nodePath, reason := plugins.ResolveNode("")
		if reason != "" {
			return nil, fmt.Errorf("task node requires a node runtime: %s", reason)
		}
		harnessPath, err := nodeharness.Materialize(attemptDir)
		if err != nil {
			return nil, fmt.Errorf("materialize task harness: %w", err)
		}
		if err := nodeharness.WriteInvocation(invocationPath, nodeharness.Invocation{
			ModuleRoots:     []string{bundleDir},
			Module:          payload.Entrypoint.Module,
			Symbol:          payload.Entrypoint.Symbol,
			Kwargs:          kwargs,
			InterfaceDigest: manifest.InterfaceDigest,
			OutputKind:      outputKind,
			OutputMediaType: outputMediaType,
			InputPath:       inputPath,
			InputCodec:      inputCodecFor(inputPath),
		}); err != nil {
			return nil, fmt.Errorf("write task invocation: %w", err)
		}
		return nodeharness.Command(nodePath, harnessPath, limits.MemoryMB), nil
	case taskbundlev2.RuntimeJVM:
		// The one structural difference between adapters, and the reason
		// ADR-036 exists: python and node materialize their harness into
		// the ATTEMPT dir because an interpreter reads source directly,
		// while this one compiles once into a HOST-level cache and reuses
		// the classes. Everything after this -- the protocol exchange,
		// resource ceilings, result reading, contract validation -- is
		// identical to the other two.
		tc, err := jvmharness.Resolve()
		if err != nil {
			return nil, fmt.Errorf("task node requires a JVM: %w", err)
		}
		classDir, err := jvmharness.Materialize(jvmharness.DefaultCacheDir(), tc)
		if err != nil {
			return nil, fmt.Errorf("materialize task harness: %w", err)
		}
		// Module/Symbol carry the fully-qualified class and its static
		// method. No new manifest fields: the managed-language entrypoint
		// shape task-bundle/v2 already defines fits the JVM exactly, and
		// the classpath is derived from the bundle's own file list rather
		// than declared a second time (see jvmharness.Classpath).
		if err := jvmharness.WriteInvocation(invocationPath, jvmharness.Invocation{
			Classpath:       jvmharness.Classpath(bundleDir, manifestFilePaths(manifest)),
			ClassName:       payload.Entrypoint.Module,
			MethodName:      payload.Entrypoint.Symbol,
			Kwargs:          kwargs,
			InterfaceDigest: manifest.InterfaceDigest,
			OutputKind:      outputKind,
			OutputMediaType: outputMediaType,
			InputPath:       inputPath,
			InputCodec:      inputCodecFor(inputPath),
		}); err != nil {
			return nil, fmt.Errorf("write task invocation: %w", err)
		}
		return jvmharness.Command(tc.Java, classDir, limits.MemoryMB), nil
	default:
		return nil, fmt.Errorf(
			"task payload %q declares runtime %q, which this server has no adapter for (supported: %s)",
			payload.ID, payload.Runtime, strings.Join(supportedTaskRuntimes, ", "),
		)
	}
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
		return r.dispatchTaskInstanceRemotely(node, digest, input, timeoutSec, attempt, execFencingGen)
	}

	var runParams map[string]string
	if r.varCtx != nil {
		runParams = r.varCtx.Params
	}
	limits := codeexec.Resolve(node.Config)
	r.log(node.ID, models.LogLevelInfo, "task exec: bundle=%s %s", digest, limits)
	return executeTaskBundle(ctx, r.store, r.taskBlobStore(), r.orgID, r.run.ID, node.ID, digest, node.Config, node.Interface, runParams, input, timeoutSec, taskharness.Handlers{
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

// maxInlineTaskInputRows bounds what dispatchTaskInstanceRemotely will
// inline into a job payload. A task node's input travels inside the
// WorkOrder (no second fetch for the claimant), which is fine for the
// modest datasets this phase's tasks pass around and wrong for a large
// one -- ADR-033's own follow-up list carries "move large
// InstanceWorkOrder inputs to ADR-012 references" for exactly that. The
// cap makes the boundary explicit instead of letting a big upstream
// silently produce a huge queue message.
const maxInlineTaskInputRows = 10000

// taskWorkOrderInput prepares a task node's input for remote dispatch,
// or nothing at all when the node declares no input port (a task that
// consumes nothing must not have rows attached just because an edge
// happens to feed it).
func taskWorkOrderInput(node models.Node, input *common.DataSet) ([]string, []map[string]interface{}, error) {
	if _, declaresInput := portValueFromInterface(effectiveNodeInterface(node), "inputs", "input"); !declaresInput {
		return nil, nil, nil
	}
	if input == nil || len(input.Rows) == 0 {
		return nil, nil, nil
	}
	if len(input.Rows) > maxInlineTaskInputRows {
		// Not an error any more: the caller spills it to the blob store
		// and sends a reference. Reporting "too big to inline" separately
		// from "broken" is what lets that happen.
		return nil, nil, errTaskInputTooLargeToInline
	}
	rows := make([]map[string]interface{}, 0, len(input.Rows))
	for _, r := range input.Rows {
		rows = append(rows, map[string]interface{}(r))
	}
	return input.Columns, rows, nil
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
func (r *Runner) dispatchTaskInstanceRemotely(node models.Node, digest string, input *common.DataSet, timeoutSec, attempt int, execFencingGen int64) (*common.DataSet, error) {
	var runParams map[string]string
	if r.varCtx != nil {
		runParams = r.varCtx.Params
	}
	inputColumns, inputRows, err := taskWorkOrderInput(node, input)
	var inputRef, inputCapability, planeURL string
	if errors.Is(err, errTaskInputTooLargeToInline) {
		// Too large to ride in the order, so it goes to the blob store
		// once and the order carries a digest plus a read capability.
		// Every prerequisite has to be present: with no blob store there
		// is nowhere to put it, and with no issuer no grant can be
		// minted -- either way the honest answer is the same named
		// refusal this replaced, not a silent fallback to a truncated
		// input.
		inputRef, inputCapability, planeURL, err = r.stageTaskInputByReference(node, input, attempt, execFencingGen)
		if err != nil {
			return nil, err
		}
		inputColumns = input.Columns
	} else if err != nil {
		return nil, err
	}
	// A declared artifact or collection output needs somewhere to put its
	// bytes, and a remote claimant holds no blob store -- so the order
	// carries a write grant it can present instead.
	var outputCapability string
	if taskNeedsOutputBlobStore(node) {
		var outURL string
		outputCapability, outURL, err = r.stageTaskOutputCapability(node, attempt, execFencingGen)
		if err != nil {
			return nil, err
		}
		planeURL = outURL
	}

	workOrder := &extensions.InstanceWorkOrder{
		NodeType:          string(models.NodeTypeTask),
		OrgID:             r.pipe.OrgID,
		Config:            node.Config,
		NodeInterface:     node.Interface,
		InputColumns:      inputColumns,
		InputRows:         inputRows,
		InputRef:          inputRef,
		InputCapability:   inputCapability,
		OutputCapability:  outputCapability,
		ControlPlaneURL:   planeURL,
		CapabilityAttempt: attempt,
		RunParams:         runParams,
		TimeoutSeconds:    timeoutSec,
	}
	return r.dispatchInstanceWorkOrderRemotely(node.ID, attempt, "", execFencingGen, workOrder, timeoutSec, r.taskJobCapabilities(digest))
}

// taskJobCapabilities reads the bundle's manifest to learn which runtime
// classes it offers, so the job only goes to a worker that actually has
// the right runtime (see taskRuntimeCapabilities).
//
// Best-effort by design: a bundle this Runner cannot read here is NOT a
// dispatch failure, because the worker fetches and validates the bundle
// itself anyway and will report a far better error than "could not
// pre-read the manifest" -- and failing dispatch here would turn a
// placement optimization into a new way for runs to break. Falling back
// to the bare protocol tag restores exactly the pre-phase-4b behavior.
func (r *Runner) taskJobCapabilities(digest string) []string {
	sb, ok := r.store.(store.TaskBundleV2Store)
	if !ok {
		return []string{taskRuntimeCapability}
	}
	archive, err := sb.GetTaskBundleV2(r.pipe.OrgID, digest)
	if err != nil {
		return []string{taskRuntimeCapability}
	}
	manifest, err := taskbundlev2.ReadManifest(archive)
	if err != nil {
		return []string{taskRuntimeCapability}
	}
	return taskRuntimeCapabilities(manifest)
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
//
// runID/nodeID identify the execution lineage a resolved execution
// record is pinned against (ADR-033 section 4) -- not carried on the
// WorkOrder itself (which stays a self-contained work description);
// callers with a extensions.RunJob already have both (job.RunID,
// job.NodeID) at hand, exactly the values a matching local execution
// would use (r.run.ID, node.ID).
func ExecuteTaskWorkOrderContext(ctx context.Context, s store.Store, runID, nodeID string, wo *extensions.InstanceWorkOrder) (*common.DataSet, error) {
	return ExecuteTaskWorkOrderWithArtifacts(ctx, s, nil, runID, nodeID, wo)
}

// ExecuteTaskWorkOrderWithArtifacts is ExecuteTaskWorkOrderContext with
// the claimant's artifact store, so a task producing an artifact or
// collection output has somewhere to put the bytes.
//
// Split from ExecuteTaskWorkOrderContext rather than added to its
// signature because that function is called from outside this module
// (the enterprise WorkPool worker), and a claimant without a blob store
// is a legitimate configuration that should keep compiling and keep
// getting today's honest refusal.
//
// artifacts may be nil, and may be an ArtifactStore that is not a
// BlobStoreProvider; both mean "no blob store", which
// readTaskArtifactOutput already refuses by name. What must not happen
// -- and did until now -- is a claimant that HAS one being told the
// server has none: engine/instance_worker.go holds an ArtifactStore at
// the call site and passed nothing, so a task node dispatched to an
// in-process instance worker could not emit an artifact even against a
// SQLArtifactStore whose Blobs() was right there.
func ExecuteTaskWorkOrderWithArtifacts(ctx context.Context, s store.Store, artifacts ArtifactStore, runID, nodeID string, wo *extensions.InstanceWorkOrder) (*common.DataSet, error) {
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
	var input *common.DataSet
	if wo.InputRef != "" {
		// The order carries a reference instead of rows. Fetch it through
		// the capability, which is the only thing that makes the digest
		// usable -- a claimant holding the digest alone can do nothing
		// with it.
		fetched, ferr := fetchTaskInputByReference(ctx, wo, runID, nodeID)
		if ferr != nil {
			return nil, fmt.Errorf("execute task instance work order: %w", ferr)
		}
		input = fetched
	} else if len(wo.InputRows) > 0 {
		rows := make([]common.DataRow, 0, len(wo.InputRows))
		for _, r := range wo.InputRows {
			rows = append(rows, common.DataRow(r))
		}
		input = &common.DataSet{Columns: wo.InputColumns, Rows: rows}
	}
	return executeTaskBundle(ctx, s, taskOutputStore(artifacts, wo, runID, nodeID), wo.OrgID, runID, nodeID, digest, wo.Config, wo.NodeInterface, wo.RunParams, input, wo.TimeoutSeconds, taskharness.Handlers{})
}

// taskOutputPort is one entry of a task-result-v1 candidate's outputs,
// and — via Items — one entry of a collection's item list too, since the
// contract defines those recursively (a collection's items are output
// port results in their own right, each carrying whatever its own kind
// requires).
type taskOutputPort struct {
	Kind      string           `json:"kind"`
	Value     interface{}      `json:"value"`
	Path      string           `json:"path"`
	Codec     string           `json:"codec"`
	SizeBytes int64            `json:"size_bytes"`
	Checksum  string           `json:"checksum"`
	Items     []taskOutputPort `json:"items"`
	ItemKey   interface{}      `json:"item_key"`
}

// readTaskResult reads and interprets a task-result-v1 candidate
// manifest, mapping its single "result" output port into the row-shaped
// DataSet contract every other node type returns downstream. Phase 2b
// supports only the "scalar" output kind -- "dataset"/"artifact"/
// "collection" kinds need a codec reader this phase does not build; a
// task declaring one of those fails clearly rather than silently
// mishandling it.
//
// nodeInterface is the task node's own ADR-032 Interface (nil when the
// node carries none -- a hand-authored task with no SDK-inferred
// contract), consulted to validate the harness's claimed value before
// trusting it downstream (ADR-032 section 10, phase 3b): a task's own
// claim that it produced valid output is not trusted uncritically. Only
// validated when the declared output port is itself scalar-kind --
// nothing in this codebase can produce a non-scalar candidate yet (the
// "not yet supported" refusal above), so a declared dataset/artifact/
// collection output has nothing here to check against and is silently
// skipped, exactly like effectiveNodeInterface's own "absence is honest"
// rule for a genuinely unknown case.
func readTaskResult(ctx context.Context, blobs artifact.Store, runID, resultPath, outputStagingDir, wantInterfaceDigest string, nodeInterface map[string]interface{}) (*common.DataSet, error) {
	raw, err := os.ReadFile(resultPath) // #nosec G304 -- resultPath is this attempt's own worker-generated scratch path, not attacker-controlled
	if err != nil {
		return nil, fmt.Errorf("read task result: %w", err)
	}
	var candidate struct {
		Contract        string                    `json:"contract"`
		InterfaceDigest string                    `json:"interface_digest"`
		Outputs         map[string]taskOutputPort `json:"outputs"`
	}
	// UseNumber, not a plain Unmarshal: every JSON number would otherwise
	// become a float64, and a float64 cannot hold an integer above 2^53.
	// That is #479 exactly -- fixed there for dataset rows, and still
	// live on this path until #496, where all three adapters emitted
	// 9007199254740993 correctly and the engine handed the pipeline
	// ...992. normalizeJSONNumbers below restores int64 where the literal
	// was integral.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&candidate); err != nil {
		return nil, fmt.Errorf("task result is not valid JSON: %w", err)
	}
	// Convert the json.Numbers UseNumber left behind into int64/float64
	// immediately, so nothing downstream has to know this path decodes
	// differently. Skipping this is not merely untidy: taskinterface's
	// int64 validation accepts a tagged value, an int64 or a whole
	// float64 and would reject a json.Number outright, so a task with a
	// declared int64 output port would fail validation instead of
	// passing it.
	for name, out := range candidate.Outputs {
		candidate.Outputs[name] = normalizeTaskOutputNumbers(out)
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
	// A dataset output is rows the task wrote to a staging file; a
	// scalar is one value carried inline. "artifact"/"collection" still
	// have no reader (each needs its own reference-handling contract) and
	// are refused by name rather than mishandled.
	if out.Kind == "dataset" {
		ds, err := readTaskDatasetOutput(outputStagingDir, out.Path, out.Codec, out.SizeBytes, out.Checksum)
		if err != nil {
			return nil, err
		}
		// Section 10's output half: check the rows against the declared
		// row type before they are committed and flow downstream, the
		// same way the scalar branch below checks a scalar. Skipped when
		// the port declares no row shape (absence is honest).
		if pv, ok := portValueFromInterface(nodeInterface, "outputs", "result"); ok {
			if verr := validateTaskDatasetOutput(pv, ds); verr != nil {
				return nil, verr
			}
		}
		return ds, nil
	}
	if out.Kind == "artifact" {
		// An artifact's bytes stay opaque: they move into the
		// content-addressed blob store and the node's output is the
		// REFERENCE, in the same four-column shape a source_api node
		// with response="artifact" already produces.
		pv, _ := portValueFromInterface(nodeInterface, "outputs", "result")
		return readTaskArtifactOutput(ctx, blobs, runID, outputStagingDir, out.Path, out.Codec, out.SizeBytes, out.Checksum, pv)
	}
	if out.Kind == "collection" {
		pv, _ := portValueFromInterface(nodeInterface, "outputs", "result")
		return readTaskCollectionOutput(ctx, blobs, runID, outputStagingDir, out.Items, pv)
	}
	if out.Kind != "scalar" {
		return nil, fmt.Errorf("task output kind %q is not recognised (this server reads \"scalar\", \"dataset\", \"artifact\" and \"collection\")", out.Kind)
	}
	if pv, ok := portValueFromInterface(nodeInterface, "outputs", "result"); ok && pv.Kind == taskinterface.ValueScalar && pv.ScalarType != nil {
		if verr := taskinterface.ValidateValue(out.Value, *pv.ScalarType, "$"); verr != nil {
			// Output ports carry no "sensitive" flag today (only
			// ParameterDeclaration does -- see NewValidationFailure's own
			// doc comment), so a task output is never redacted here.
			failure := taskinterface.NewValidationFailure(taskinterface.DirectionOutput, "result", *pv.ScalarType, out.Value, verr, "$", false)
			// Multi-%w for the same reason the input boundary uses it
			// (engine/task_dataset.go): errors.Is classifies the fault,
			// errors.As reaches the structured report's own fields.
			return nil, fmt.Errorf("%w: %w", ErrTaskOutputContractViolation, failure)
		}
	}
	return &common.DataSet{
		Columns: []string{"result"},
		Rows:    []common.DataRow{{"result": out.Value}},
	}, nil
}

// workOrderBlobStore mirrors Runner.taskBlobStore for the claimant side:
// the same optional-capability type assertion, so local and remote
// execution answer "is there somewhere to put artifact bytes" the same
// way rather than by two different rules.
func workOrderBlobStore(artifacts ArtifactStore) artifact.Store {
	provider, ok := artifacts.(BlobStoreProvider)
	if !ok {
		return nil
	}
	return provider.Blobs()
}

// manifestFilePaths is the manifest's declared file list, in order.
//
// Only the JVM adapter needs it today (to put a bundle's jars on the
// classpath); python and node resolve modules from the bundle root
// alone.
func manifestFilePaths(manifest *taskbundlev2.Manifest) []string {
	if manifest == nil {
		return nil
	}
	paths := make([]string, 0, len(manifest.Files))
	for _, f := range manifest.Files {
		paths = append(paths, f.Path)
	}
	return paths
}

// normalizeTaskOutputNumbers turns the json.Numbers a UseNumber decode
// leaves in a task-result-v1 candidate into int64 or float64, recursing
// into a collection's items because those carry values of their own.
//
// Shares normalizeJSONNumbers with the dataset path deliberately: one
// numeric contract for task output, however it was declared, is what
// #479 and #496 were both about.
func normalizeTaskOutputNumbers(out taskOutputPort) taskOutputPort {
	out.Value = normalizeJSONNumbers(out.Value)
	out.ItemKey = normalizeJSONNumbers(out.ItemKey)
	for i, item := range out.Items {
		out.Items[i] = normalizeTaskOutputNumbers(item)
	}
	return out
}
