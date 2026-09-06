# ADR-033: Polyglot task runtime and worker protocol

**Status:** proposed
**Date:** 2026-09-05

## Context

Brokoli's control plane is language-neutral, but executable code nodes are not
yet a language-neutral execution system. Python decorators generate source text
against implicit wrapper globals. TypeScript generates JavaScript source
against a related but separately maintained wrapper contract. The Go engine's
legacy code-node path launches Python directly, and runtime selection is not a
first-class, closed, validated part of the logical task declaration.

This creates several scaling and product risks:

- every new language can become another special case in the scheduler;
- workers may need every interpreter and dependency even when they execute only
  one runtime class;
- generated source, package metadata, dependency identity, and wrapper ABI can
  drift independently;
- queue names or caller-provided labels can accidentally become the runtime
  compatibility contract;
- retries can execute different code if payload selection is repeated on each
  worker;
- large inputs and outputs can be copied through queue messages or process
  pipes instead of the artifact/data plane;
- subprocesses inherit host environment and credentials unless each language
  path independently implements isolation;
- local execution can differ materially from worker execution, damaging the
  low-friction developer experience.

The target is not “rewrite the scheduler in every language” or “install every
language on every worker.” The Go control plane continues to own scheduling,
physical planning, retries, leases, fencing, policy, and persistence. Language
runtimes own only the execution of an immutable task implementation through a
small versioned protocol.

### Existing decisions this ADR preserves

- ADR-002 and ADR-013 establish language-neutral subprocess protocols for
  connectors. Their envelope and structured-event lessons are reusable, but
  connector commands (`discover`, `read`, `write`) do not fit ordinary tasks.
- ADR-010 pins historical run definitions and code/package digests.
- ADR-012 owns dataset and artifact references. Task workers exchange those
  references rather than inventing a second blob plane.
- ADR-015 keeps physical planning and instance identity in Go.
- ADR-016 establishes safe archive extraction, content hashing, multi-payload
  packages, and runtime classes. Task bundles reuse those mechanics but not the
  globally installed plugin lifecycle.
- ADR-017 owns instance claim identity, lease renewal, fencing, idempotency, and
  whole-run migration. This ADR adds a task execution envelope; it does not
  create a competing retry or lease model.
- ADR-019 owns segmenting, streaming barriers, and reference-passing dataflow.
- ADR-022 owns tenant isolation and outbound-request policy.
- ADR-032 owns parameter and input/output type semantics. This ADR transports
  and enforces those contracts but does not redefine them.

### Design goals

1. Python, TypeScript/JavaScript, and future languages can execute real task
   code without changes to scheduler semantics.
2. Adding a runtime adapter does not require every worker to install it.
3. A task attempt executes an immutable, digest-pinned implementation.
4. Compatibility fails before dispatch/attempt claim, not after repeated exec
   failures.
5. Short tasks can use warm processes and caches; containers remain a portable
   fallback rather than mandatory per-task overhead.
6. Inputs, outputs, logs, metrics, progress, cancellation, and failures have one
   protocol meaning across runtimes.
7. Isolation and secret scoping are server policy, not optional SDK behavior.
8. Local testing uses the same adapter and protocol as remote execution.
9. The simplest task remains simple to author.

### Non-goals

This ADR does not standardize language syntax, dependency managers, or build
systems. It does not guarantee that arbitrary native objects cross languages.
It does not guarantee exactly-once execution. It does not require Kubernetes,
containers, WASM, or a central public package registry.

## Decision

Brokoli will execute user tasks through a versioned, language-neutral **Task
Runtime Protocol** implemented by runtime adapters. Logical IR references an
immutable task bundle and portable interface. The control plane resolves and
pins a concrete payload before dispatch, derives worker requirements, and sends
an instance work order containing references rather than executable source or
large data. Workers dequeue only compatible jobs, materialize the pinned
payload, and run it under a server-enforced execution profile. Per ADR-017, the
dispatcher/control plane owns the execution-attempt claim and lease renewal;
the worker executes on behalf of that fenced claim. A queue delivery is not a
second execution-attempt claim.

Python and Node adapters are the initial reference implementations. Additional
languages implement the same conformance suite; they do not add language logic
to the scheduler.

### 1. Separate task definition, implementation, invocation, and attempt

These identities must not be collapsed:

| Object | Identity/lifetime | Purpose |
|---|---|---|
| Task interface | semantic digest | Portable parameters and ports from ADR-032 |
| Task implementation | immutable bundle digest | Code, entrypoint, dependencies, runtime requirements |
| Task invocation | pipeline node/version | Bind upstream ports and pipeline parameters to a task |
| Task attempt | run/node/instance/attempt/fence | One leased execution under ADR-017 |

A new implementation may preserve the same interface. A changed interface may
reuse source files but has a new interface digest. A retry of one attempt uses
the exact same resolved implementation and payload digest.

Logical IR contains a task declaration similar to:

```json
{
  "id": "score_orders",
  "type": "task",
  "config": {
    "interface": {
      "contract": "brokoli.task-interface/v1",
      "digest": "sha256:...",
      "inputs": {},
      "outputs": {}
    },
    "implementation": {
      "bundle": {
        "format": "brokoli.task-bundle/v2",
        "digest": "sha256:4a8d..."
      },
      "runtime": {
        "class": "python",
        "requires": ">=3.11,<3.14",
        "protocol": "brokoli.task-runtime/v1"
      }
    },
    "execution": {
      "profile": "standard"
    }
  }
}
```

The author requests an execution profile; the server resolves and may
strengthen it. The author cannot select a worker ID, disable mandatory
isolation, inject host paths, or claim unsupported capabilities.

### 2. Task bundle format and immutable identity

Task bundles are deployment-scoped, content-addressed artifacts distinct from
run-scoped output artifacts and globally installed connector plugins.

`brokoli.task-bundle/v2` is a deterministic archive containing:

```json
{
  "format": "brokoli.task-bundle/v2",
  "name": "score-orders",
  "interface_digest": "sha256:...",
  "source_digest": "sha256:...",
  "build": {
    "builder": "brokoli-python-sdk/0.9.0",
    "attestation": "sha256:verified-build-record"
  },
  "payloads": [
    {
      "id": "python-any",
      "runtime": "python",
      "requires": ">=3.11,<3.14",
      "os": "any",
      "arch": "any",
      "entrypoint": {"module": "src.tasks", "symbol": "score_orders"},
      "effects": "pure",
      "dependency_lock": "deps/requirements.lock",
      "payload_digest": "sha256:..."
    }
  ],
  "files": [
    {"path": "src/tasks.py", "size": 1942, "sha256": "..."}
  ]
}
```

Normative rules:

1. Archive path, ordering, timestamps, ownership, modes, compression, and tree
   hashing are canonicalized. Equivalent input produces the same digest.
2. Paths are relative and normalized. Absolute paths, traversal, symlinks,
   devices, FIFOs, and archive bombs are rejected using ADR-016's safe
   extraction baseline.
3. The bundle digest covers the canonical manifest and every payload byte.
4. The interface digest is included and verified against logical IR.
5. Dependencies are locked and hash-verifiable. Every dependency artifact is
   included in the file manifest or referenced by immutable digest. A lockfile
   that still requires mutable registry resolution is not a production bundle.
   An adapter must not resolve floating package versions during an attempt.
6. Source provenance and signatures are additive metadata; digest verification
   is mandatory even when signatures are not.
7. Bundles are immutable and coexist by digest. “Upgrade in place” is not a
   valid task implementation operation.
8. A bundle may contain multiple equivalent payloads for OS/architecture or
   execution target, but each payload has its own digest.
9. Entrypoints use one structured grammar (`module` plus `symbol` for managed
   languages, relative `executable` for native payloads, OCI command/args for
   containers). Free-form colon/dot strings are not portable entrypoints.
10. `os: any`/`arch: any` is valid only when the full payload and dependencies
    contain no platform-specific binaries. Otherwise each feasible platform is
    a separate payload.
11. OCI payloads are immutable external payload references containing an image
    manifest digest; their layers need not be copied into the task archive, but
    the reference is covered by the bundle digest.

The selected payload's digest-covered entrypoint and effect declaration are
authoritative. Logical IR does not carry editable duplicates. The work order
may copy them for self-contained dispatch, but admission derives and
byte-compares those copies against the pinned payload; they participate in the
resolved execution identity and cannot override the manifest.

The existing Python bundle format remains a legacy input and is translated or
executed by the Python compatibility adapter. New language support targets v2.

### 3. Execution targets and runtime classes

Runtime class is a closed, advertised vocabulary describing how a payload is
launched, not the business semantics of the task:

| Runtime class | Initial status | Launch model |
|---|---|---|
| `python` | required reference adapter | managed Python harness |
| `node` | required reference adapter | managed Node.js harness |
| `native` | supported when payload speaks protocol directly | executable |
| `container` | portable fallback | OCI image by immutable digest |
| `wasi` | deferred | WASI component/runtime |
| `jvm` | deferred until adapter exists | managed JVM harness |

The scheduler does not contain Python- or Node-specific invocation code. It
selects an adapter by runtime class and protocol version. A runtime adapter is
responsible for dependency environment materialization, native value
conversion, invoking the declared entrypoint, and translating native errors to
the common result protocol.

Containers are not mandatory for every task. They provide the universal escape
hatch for custom system dependencies and stronger image-level reproducibility,
but creating one Pod/container per tiny transform is too expensive for the
single-binary and high-throughput use cases. Managed adapters with warm
environment/supervisor caches are the default for ordinary Python and Node
tasks; arbitrary tenant code still gets a fresh sandboxed child per attempt.

### 4. Resolve and pin payloads before dispatch

Payload selection is deterministic and control-plane-owned:

1. Admission validates the runtime class, version range, protocol, interface,
   dependency policy, and execution profile.
2. Physical planning resolves an eligible payload for an execution platform.
3. The resolved execution record pins bundle digest, payload ID, payload
   digest, execution-environment digest, interface digest, and execution profile
   revision. The environment digest covers adapter/harness bytes, exact runtime
   build, dependency artifacts, platform ABI, and declared system libraries.
4. Worker matching uses requirements derived from that record.
5. Retries and resumed runs reuse the pinned record. A newer interpreter,
   adapter, or bundle does not silently change an existing run.

If several worker platforms are eligible, scheduling chooses one and persists
payload, platform, adapter, and environment digest before the dispatcher calls
`ClaimAttempt`. Worker capabilities advertise exact identities already present
or an authenticated ability to materialize the pinned environment. Dequeue
never chooses or changes execution identity. All retries honor that pin. If it
becomes unavailable, the run waits or fails with `platform`; an
operator-approved re-resolution creates a new, explicitly non-equivalent
attempt lineage and is not called a retry.

The worker never chooses “the first payload I can run” independently. That
would allow retries to execute different bytes.

### 5. Worker capabilities and non-bottleneck routing

Workers advertise structured capabilities on registration and heartbeat:

```json
{
  "worker_id": "worker-17",
  "protocols": ["brokoli.instance/v2", "brokoli.task-runtime/v1"],
  "platform": {"os": "linux", "arch": "amd64"},
  "runtimes": [
    {"class": "python", "versions": ["3.11.9", "3.12.4"], "adapter": "1.2.0"},
    {"class": "node", "versions": ["22.4.1"], "adapter": "1.1.0"}
  ],
  "io": ["batch-reference", "stream-v1"],
  "isolation": ["process", "container"],
  "resources": {"cpu_millis": 8000, "memory_bytes": 17179869184},
  "labels": {"accelerator": "none"}
}
```

Capabilities are authenticated observations, not author assertions. The server
normalizes them and derives a placement predicate from the resolved task:

```text
protocol task-runtime/v1
AND runtime python@3.11.9
AND adapter >=1.2
AND io batch-reference
AND isolation >=process
AND memory >=512MiB
```

Workers dequeue only work that matches. A pool may be Python-only, Node-only,
GPU-specialized, container-only, or mixed. Adding a language adds an adapter
and capable workers, not a requirement to install that language everywhere.
Queue names and free-form labels may influence placement after compatibility,
but cannot substitute for runtime/protocol matching.

This removes language as a central bottleneck: the control plane remains one
implementation, task queues remain shared infrastructure, and runtime capacity
scales independently by adding workers of the needed class.

### 6. Instance work-order envelope

ADR-017's attempt identity and zero-based logical attempt numbering remain
authoritative. The dispatcher claims and renews the attempt, then enqueues the
work order carrying that claim's fencing generation. The worker never calls a
second claim operation. The task extension to the existing work order is
additive and versioned. The nested JSON below is its normative wire projection,
not a requirement to replace the existing Go struct or transport in one flag
day; existing identity fields retain their meanings:

```json
{
  "protocol": "brokoli.instance/v2",
  "identity": {
    "run_id": "...",
    "node_id": "score_orders",
    "instance_key": "single",
    "attempt": 0,
    "fencing_generation": 4,
    "idempotency_key": "..."
  },
  "task": {
    "runtime_protocol": "brokoli.task-runtime/v1",
    "bundle": {"digest": "sha256:...", "capability": "bundle-cap:opaque"},
    "payload": {
      "id": "python-any",
      "digest": "sha256:...",
      "entrypoint": {"module": "src.tasks", "symbol": "score_orders"}
    },
    "interface_digest": "sha256:...",
    "execution_profile": "standard@7"
  },
  "inputs": {
    "orders": {
      "kind": "dataset",
      "ref": {"manifest_version": 1, "capability": "input-cap:opaque", "checksum": "sha256:..."}
    }
  },
  "parameters": {"threshold": 0.5},
  "context": {
    "deadline": "2026-09-05T12:00:00Z",
    "checkpoint_capability": null,
    "resource_capabilities": {"warehouse": "resource-cap:opaque"},
    "effect_idempotency_key": "stable-operation-key"
  }
}
```

Queue messages contain identities, small typed values, and references. They do
not contain whole source trees, container layers, large datasets, secret
values, or arbitrary SDK-native objects. Protocol references are opaque
control-plane-issued capabilities, never general URLs, URIs, or host paths.
Each capability is bound to tenant, run, attempt, fencing generation,
direction, object/checksum, allowed operation, and expiry. The trusted worker
resolves capabilities through approved bundle/artifact clients; task code
cannot substitute `file://`, metadata-service, cross-tenant, or arbitrary
network references. ADR-022 policy applies to bundle, dependency, input,
checkpoint, output, and task business traffic.

### 7. Task Runtime Protocol v1

The adapter and task harness communicate through a small protocol independent
of the worker transport. V1 uses duplex JSON Lines: worker control frames on
the harness's stdin and harness event frames on stdout. Each frame is exactly
one UTF-8 JSON object terminated by LF, contains no literal unescaped newline,
rejects duplicate keys, and is limited to 1 MiB including LF. Empty lines,
invalid UTF-8/JSON, oversized frames, and output before `ready` are protocol
failures. Large data travels through worker-provisioned staging files and
opaque capabilities, never inside control frames.

The worker sends one `start` message after preparing the sandbox:

```json
{
  "type": "start",
  "protocol": "brokoli.task-runtime/v1",
  "invocation_path": "/work/invocation.json",
  "result_path": "/work/result.json",
  "output_staging_dir": "/work/output",
  "event_stream": "stdout"
}
```

The paths are fixed by the trusted worker, scoped beneath the attempt sandbox,
and cannot be replaced by task frames. The invocation file contains the
portable subset of the work order, worker-materialized input paths or opaque
handles, and no control-plane credential. The harness must emit `ready` before
the handshake deadline:

```json
{"type":"ready","protocol":"brokoli.task-runtime/v1","adapter":"python","adapter_version":"1.2.0","capabilities":[]}
{"type":"log","level":"info","message":"scoring rows","fields":{"batch":2}}
{"type":"progress","completed":500,"total":1000,"unit":"rows"}
{"type":"warning","code":"LOW_CONFIDENCE","message":"12 rows below confidence target"}
{"type":"metric","name":"rows_scored","kind":"counter","value":500}
{"type":"completed"}
```

Before `completed`, the harness writes the candidate manifest only at the
prescribed path:

```json
{
  "contract": "brokoli.task-result/v1",
  "interface_digest": "sha256:...",
  "outputs": {
    "result": {
      "kind": "dataset",
      "path": "result.arrow",
      "codec": "arrow-ipc/v1",
      "size_bytes": 48120,
      "checksum": "sha256:..."
    }
  },
  "metadata": {"rows_scored": 500}
}
```

Or terminates with a structured failure:

```json
{
  "type": "failed",
  "failure": {
    "category": "user_code",
    "code": "VALUE_ERROR",
    "message": "threshold must be positive",
    "retryable": false,
    "details": {},
    "stack": "redacted according to policy"
  }
}
```

Protocol rules:

1. The state machine is `start -> ready -> zero or more events -> completed |`
   `failed`. Exactly one terminal frame is valid. Exit before a terminal frame,
   a frame after terminal, or a non-zero exit after `completed` is a protocol
   failure. The worker waits for process exit before accepting terminal state.
2. Before loading user modules, managed harnesses redirect user stdout/stderr
   to structured `log` events; protocol stdout is held on a private descriptor.
   A direct native/container protocol task must reserve stdout itself and use
   stderr or `log` events. Corrupting stdout is its protocol failure.
3. The worker may send `{"type":"cancel","reason":"...","deadline":"..."}`
   after `ready`. The harness replies `{"type":"cancel_ack"}` and exits by the
   deadline. A terminal/cancel race is resolved by the first frame read; the
   trusted worker still applies control-plane fencing and cancellation state.
4. Required capabilities are listed in `start`; the `ready.capabilities` set
   must satisfy them. Unknown required capabilities or protocol versions fail
   the handshake. Event names in v1 are closed; additive optional events use an
   `extension` envelope and are ignored but recorded when unknown.
5. The prescribed `result_path` is the sole authoritative *candidate result*
   from untrusted code. It lists declared output ports and relative files under
   `output_staging_dir`, with kind, codec, size, checksum, and schema metadata.
   Task code cannot mint artifact URIs or output capabilities. Informational
   progress events never publish output.
6. `completed` means only “the harness returned and wrote a candidate result.”
   The worker first freezes/terminates the complete sandbox process tree. It
   opens candidates relative to a trusted staging-directory descriptor with
   no-follow/beneath semantics, accepts regular files only, enforces per-file,
   aggregate-size, and file-count limits, and hashes/uploads from the same held
   descriptors to prevent symlink/replacement races. It then validates declared
   ports, codecs, interface digest, and ADR-032 contracts.
7. Uploaded blobs remain non-authoritative attempt-scoped staging objects.
   `CommitAttemptResult` is one atomic metadata compare-and-swap containing
   identity, attempt, fencing generation, and one immutable manifest for all
   output ports. The CAS both publishes that manifest and settles the attempt;
   multi-port publication is all-or-nothing. Cancellation or lease loss that
   wins before the CAS makes it fail. A crash after upload but before CAS leaves
   invisible orphan blobs reclaimed by attempt-retention cleanup. ADR-012 and
   ADR-017 must be updated with this publication operation before acceptance.
8. A non-zero exit is not automatically retryable. Task-supplied failure
   category, code, and retry hint are advisory; trusted worker observations and
   server retry policy are authoritative.
9. Warnings and errors are events, not magic `#WARNING:` text prefixes.
   Progress is monotonic per scope; metrics use `counter`, `gauge`, or
   `distribution` in v1.
10. The trusted worker derives adapter/runtime/environment identity from the
    selected payload and what it launched. `ready` echoes identity for
    diagnostics and handshake consistency but cannot attest it, especially for
    direct native/container tasks. The worker-verified identity is persisted.

JSON is selected for v1 control messages because it is easy to implement in a
new language and inspect during development. The semantic protocol is transport
independent. A future Protobuf/gRPC binding may be added only with byte-level
conformance proving equivalent behavior.

### 8. Portable data boundary

The runtime adapter converts only ADR-032 portable values and ADR-012
references:

- small scalars and parameters may be inline JSON;
- datasets normally arrive as immutable references with schema metadata;
- artifacts always arrive as opaque references plus media metadata;
- collections contain references/items according to their declared contract;
- remote v1 tasks use materialized references; future streaming handles require
  an ADR-019 revision.

Python objects, pickles, JavaScript prototypes, closures, JVM serialization,
and native pointers never cross the boundary. Inside a Python adapter, a
dataset may become an iterator, list of dictionaries, Arrow table, or DataFrame
according to its declared adapter API. Inside Node it may become an async
iterator or records. Those are language-local conveniences and must convert
back to the same portable output.

Apache Arrow IPC is the preferred optional dataset transport when both sides
advertise it, because it is columnar and cross-language. NDJSON remains the
baseline interoperable format. Transport choice is physical-plan metadata and
does not change the logical dataset contract.

### 9. Runtime I/O capabilities and physical selection

Task implementations declare semantic requirements/capabilities, not a chosen
transport mode:

| Mode | Contract |
|---|---|
| `batch-reference` | finite, re-readable inputs and materialized outputs by reference |
| `batch-inline` | bounded small values only; server policy sets limits |
| `stream-v1` | one-pass records with backpressure and explicit completion (deferred remotely) |

Examples are `requires_reiterable_input`, `accepts_one_pass_input`, and
`produces_incrementally`. Per ADR-019, users do not choose physical I/O mode.
The planner selects codecs and materialization per edge/port and inserts
barriers. Legacy Python `rows` declares `requires_reiterable_input` even if the
engine streams while materializing it. An adapter must not claim one-pass
support merely because its native language has an iterator.

Remote task dispatch in protocol v1 is a materialization barrier and uses
`batch-reference` (or bounded `batch-inline`). Cross-worker `stream-v1` is
deferred until an ADR-019 revision defines ownership, retries, fan-out,
backpressure, teardown, and fenced commit. This protocol does not create a
second streaming scheduler.

### 10. Caching and warm execution

To keep language from becoming a latency bottleneck:

1. Workers maintain a content-addressed bundle cache with digest verification,
   bounded size, LRU/age eviction, and in-use leases.
2. Managed adapters maintain dependency environments keyed by the complete
   execution-environment digest.
3. Adapters may use warm trusted supervisor processes keyed by that environment
   identity. Reusing a process that has loaded arbitrary user modules is allowed
   only for `trusted` code or a runtime with a conformance-proven snapshot/reset
   boundary.
4. Each invocation receives a fresh logical context and isolated working
   directory. No mutable module/global state may be assumed to survive.
5. A task can opt out of process reuse; server policy may require one-shot
   isolation.
6. Cache hits and environment preparation time are observable attempt metrics.
7. Warm supervisors are never shared across tenants or trust domains. For
   `tenant` and `untrusted`, each attempt runs in a fresh child/sandbox even
   when environment and supervisor caches are warm. The worker closes inherited
   descriptors, removes secret mounts and scratch data, and kills descendant
   processes after every attempt.

Warm supervision is an optimization, never a semantic requirement. Conformance
tests execute the same task cold, warm, retried, and after cancellation to
detect leaked state. A generic interpreter “reset” is not treated as proof that
monkey patches, imported-module globals, signal handlers, or native-library
state were restored.

### 11. Dependency environments

SDK build tooling produces a lock appropriate to its ecosystem:

- Python: hashed wheels or a fully pinned lock plus platform constraints;
- Node: lockfile plus packaged production dependency graph and runtime range;
- JVM: resolved jars/classpath digest;
- native: platform-specific executable and linked-runtime metadata;
- container: immutable OCI manifest digest.

The worker materializes environments outside the task's writable sandbox and
mounts them read-only. Network dependency installation during task execution is
forbidden by default. Build and dependency resolution happen before run
admission or in a separately governed build service, with logs and provenance.

An unlocked development bundle may run locally, but a remote deployment must
reference immutable dependency artifacts and a build attestation issued by a
trusted builder after verifying the environment digest. `reproducible` is not a
self-asserted manifest boolean. “Whatever packages happen to be installed on
the worker” remains only a named legacy profile.

### 12. Isolation and trust profiles

User code is arbitrary code. The runtime protocol does not make it safe by
itself. Every resolved execution uses a server-owned trust profile:

| Profile | Intended use | Minimum isolation |
|---|---|---|
| `trusted` | operator-controlled code on dedicated infrastructure | process boundary, minimal env, limits |
| `tenant` | authenticated tenant code | OS/container sandbox, scoped identity, network/filesystem policy |
| `untrusted` | externally supplied code | stronger sandbox runtime; disabled unless configured |

Plain process isolation is permitted only for `trusted`. `tenant` requires a
concrete supported sandbox boundary: isolated user/PID/mount/network namespaces
or equivalent container isolation, read-only root/bundle/dependency mounts,
cgroup-equivalent resource enforcement, syscall filtering where the platform
supports it, and externally enforced egress policy. Unsupported hosts reject
admission rather than degrading to best effort. `untrusted` requires a
separately accepted stronger sandbox profile before it can be enabled.

Baseline requirements for all profiles:

- minimal allowlisted environment, never `os.Environ()` wholesale;
- per-attempt working directory and non-root identity;
- CPU, memory, process-count, file-size, and wall-clock limits;
- process-group cancellation with bounded graceful termination and forced kill;
- read-only bundle/dependency mounts and bounded writable scratch;
- secrets resolved just in time to scoped handles/files and redacted from
  protocol/logs;
- outbound network policy enforced outside user code, since language libraries
  can bypass Go HTTP guards;
- no control-plane database or broad API credentials in the sandbox;
- audit record of bundle, payload, adapter, worker, profile, and policy revision.

An author can request resources or narrower access but cannot weaken policy.
Workers advertise isolation mechanisms; the scheduler never routes a task to a
worker unable to satisfy the resolved profile.

### 13. Cancellation, leases, retries, and idempotency

ADR-017 remains the source of truth:

- the dispatcher/control plane renews the attempt lease independently of
  runtime progress; worker heartbeat reports liveness but is not a second lease;
- cancellation propagates from control plane to worker, adapter, process group,
  and any child processes;
- output commit and terminal settlement are fencing-checked;
- a stale worker cannot publish output after lease loss;
- task execution is at-least-once under failure;
- idempotency keys are available to task context and connector/resource APIs.

The adapter sends a cooperative cancellation event first, waits the profile's
grace period, then terminates the complete process/container tree. A task that
ignores cancellation cannot extend its lease indefinitely. Partial outputs are
staged under attempt identity and become visible only through a successful,
fenced commit. Streaming sinks need their own idempotent/transactional contract;
this ADR does not manufacture exactly-once effects for arbitrary external
systems.

### 14. Failure taxonomy

Every failure is classified independently from retry policy:

| Category | Examples | Default retry stance |
|---|---|---|
| `user_code` | exception, rejected business input | no unless configured |
| `contract_violation` | wrong output port/type/schema | no |
| `dependency` | declared external service unavailable | policy-dependent |
| `resource_exhausted` | memory/CPU/disk limit | policy-dependent, often needs profile change |
| `cancelled` | user or upstream cancellation | no |
| `deadline_exceeded` | task deadline | policy-dependent |
| `runtime_protocol` | malformed/missing frames | worker/adapter incident |
| `platform` | runtime disappeared, corrupt cache | retry elsewhere after quarantine |
| `lease_lost` | stale/fenced attempt | no settlement by stale worker |

SDK exceptions map to this taxonomy through adapters. Language-specific stack
traces are preserved as protected diagnostics, while the stable category/code
drives scheduling and automation.

Task-supplied category, code, and retry hint are advisory. Only the trusted
worker may originate `runtime_protocol`, `platform`, `resource_exhausted`, and
`lease_lost`, and only the control plane may determine final retryability.
Policy combines trusted origin, category, attempt count, elapsed retry budget,
side-effect/idempotency declaration, exponential backoff with jitter, and
cancellation state. Maximum attempts and elapsed budget are mandatory. Repeated
protocol/platform failures quarantine the environment/worker rather than
creating a poison-task retry loop.

Every implementation declares an effect class:

| Effect | Meaning | Retry rule |
|---|---|---|
| `pure` | output depends only on declared inputs/parameters; no external effects | retry within policy |
| `idempotent_with_key` | external effects deduplicate on a stable logical-operation key | retry only through APIs/resources proven to bind that key |
| `non_idempotent` | effects may repeat or cannot be proven idempotent | no automatic retry after invocation starts |

Undeclared arbitrary code defaults to `non_idempotent`; the SDK cannot infer
purity from syntax. Resource/connector adapters that claim
`idempotent_with_key` must use `effect_idempotency_key`, scoped to
`(run_id, node_id, instance_key, logical operation)` and reused across every
retry attempt. It is separate from ADR-017's attempt/delivery idempotency key,
which changes with attempt identity. Adapters must prove the stable key is sent
to the external operation and persist enough state to resolve a
lost-response/commit-uncertainty case.
Changing effect class changes implementation identity. A control-plane retry
never turns an idempotency key into an exactly-once guarantee by itself.

### 15. Runtime adapter contract and language onboarding

A new official runtime adapter must provide:

1. capability probe and exact runtime version reporting;
2. bundle/payload validation and deterministic environment preparation;
3. entrypoint loading without executing unrelated project discovery code;
4. ADR-032 value conversion and validation;
5. invocation, structured event capture, cancellation, and process-tree cleanup;
6. cold/warm execution with state-isolation proof;
7. resource and trust-profile integration;
8. a local runner using the same implementation;
9. conformance results for protocol, types, failures, retries, and cancellation;
10. compatibility/version policy and maintained SDK documentation.

The adapter can be written in Go, the target language, or both. Its externally
observable protocol must remain identical. No language receives privileged
access to scheduler internals.

### 16. Python and TypeScript developer experience

The architecture is portable; the authoring experience remains idiomatic. The
following APIs are illustrative requirements, not claims that these exact
symbols already ship.

Python:

```python
from typing import TypedDict
from brokoli import Pipeline, dataset, parameter, task

class Order(TypedDict):
    id: int
    amount: float

class ScoredOrder(TypedDict):
    id: int
    score: float

@task(resources={"memory": "512MiB"})
def score(rows: list[Order], threshold: float = 0.5) -> list[ScoredOrder]:
    return [{"id": row["id"], "score": row["amount"] * threshold} for row in rows]

with Pipeline("scores", parameters={"minimum": parameter.float64(default=0.5)}) as p:
    orders = p.input("orders", dataset(Order))
    scored = score(orders, threshold=p.parameters.minimum)
```

TypeScript:

```typescript
import { dataset, float64, parameter, Pipeline, task } from "brokoli";

type Order = { id: bigint; amount: number };
type ScoredOrder = { id: bigint; score: number };

const score = task({
  input: dataset<Order>(),
  output: dataset<ScoredOrder>(),
  parameters: { threshold: float64({ default: 0.5 }) },
  resources: { memory: "512MiB" }
}, async (rows, ctx) => rows.map(row => ({
  id: row.id,
  score: row.amount * ctx.params.threshold,
})));

const p = new Pipeline("scores", {
  parameters: { minimum: parameter.float64({ default: 0.5 }) },
});
const orders = p.input("orders", dataset<Order>());
const scored = p.invoke(score, {
  input: orders,
  parameters: { threshold: p.parameters.minimum },
});
```

Both compile `Order`, `ScoredOrder`, local parameter `threshold`, and its
binding to pipeline parameter `minimum` to the same normalized ADR-032
interface and invocation. Python and Node bundles differ in runtime payload and
implementation digest; their semantic interface digest is identical. SDK
tooling handles bundle construction, dependency locking, digest upload, and
source mapping.

Required tooling:

- `brokoli task test` invokes native functions directly with contract
  validation;
- `brokoli task run-local` builds the bundle and executes the real adapter in a
  local sandbox;
- `brokoli task inspect` shows interface, entrypoint, files, dependencies,
  runtime requirements, and digests;
- `brokoli compile` performs target-server capability and policy checks;
- `brokoli replay-attempt` downloads a redacted invocation manifest and runs it
  locally when policy permits;
- build errors identify the source import/dependency and remediation rather
  than exposing archive internals;
- source maps/traceback rewriting point failures to the author's source file.

V1 SDK acceptance is testable: unsupported native types produce stable
diagnostic codes; build/validation failures exit non-zero; local execution uses
the same adapter, bundle, value codecs, and result manifest as a worker; logs
and replay bundles redact secret values and capabilities; and Python/TypeScript
conformance examples produce equal semantic contracts. Exact OS sandbox parity
is not promised locally, and tooling reports missing isolation mechanisms
rather than implying production equivalence.

Users do not manually operate an expansion service or write protocol frames.
Advanced users can provide a direct protocol-speaking native/container task,
but the common SDK path remains one decorator/builder and normal language code.

### 17. Capability negotiation

The server advertises:

- task-runtime protocol versions;
- task-bundle format versions;
- runtime classes and version ranges available somewhere in the fleet;
- portable codecs/transports;
- execution profiles and limits;
- optional features such as streaming or warm reuse.

Fleet-wide advertisement does not promise immediate capacity. Deployment proves
semantic compatibility; run admission additionally proves an eligible worker
class exists or leaves the run explicitly waiting for capacity. Missing runtime
support is never reported as a generic task exception.

Workers advertise their own concrete subset. Unknown required runtime features
fail closed. Old servers that omit runtime capability fields cannot accept new
task bundles under a legacy waiver because absence cannot prove executable
machinery exists.

### 18. Migration from inline code nodes

Migration is staged and non-destructive:

1. Pin and test the existing Python/TypeScript generated-wrapper behavior as a
   named legacy ABI.
2. Implement Python and Node v1 adapters capable of executing both legacy
   generated scripts and v2 bundles.
3. Have SDKs emit bundle-backed task nodes by default when the server advertises
   support; retain explicit legacy compilation for older supported servers.
4. Translate magic stderr markers into structured events only in the legacy
   adapter. New tasks emit protocol events directly.
5. Stop accepting new unpinned inline executable code under strict production
   policy.
6. Announce deprecation before removing any legacy IR/runtime path. Historical
   pipeline versions remain executable through a compatibility adapter for the
   declared retention window.

Compile-only task forms remain rejected until their payload and runtime
semantics are represented by this protocol. Packaging metadata alone is not
evidence of executability.

### 19. Conformance and release discipline

The core repository owns a language-neutral conformance suite containing:

- canonical work orders and protocol traces;
- bundle canonicalization and malicious archive vectors;
- ADR-032 value and schema vectors;
- output-reference and artifact-integrity cases;
- structured logs, warnings, metrics, and progress;
- user, contract, platform, timeout, and cancellation failures;
- lease loss and fenced late-output attempts;
- cold/warm state-isolation cases;
- cross-language pipelines where Python output feeds Node and vice versa;
- compatibility fixtures for one previous protocol/bundle version.

An SDK's generated task must execute through the released adapter against a
pinned core backend in CI. An adapter release must execute bundles generated by
supported SDK versions. A scheduled compatibility build tests current main
branches to expose drift before releases. No runtime is listed as generally
available until these gates pass.

## Industry evidence

### Apache Beam

Beam separates an SDK-independent Runner API from language-owned SDK harnesses
and a Fn API with control, data, state, progress, metrics, and bundle-finalize
channels. Primitive transforms use stable URNs and pipelines declare
requirements unknown runners must reject. Cross-language edges use portable
coders rather than native object serialization. Brokoli adopts this separation
and fail-closed capability model, while avoiding the developer burden of making
users operate expansion services:
<https://beam.apache.org/documentation/runtime/model/> and
<https://github.com/apache/beam/tree/v2.67.0/model>.

### Temporal

Temporal's service orchestrates durable state but workers execute application
code by polling task queues. Pull provides backpressure and avoids inbound
worker networking. Worker versioning and immutable deployment identities keep
executions on compatible code. Temporal also makes clear that activities may
execute more than once, requiring idempotency. Brokoli adopts pull-compatible
workers, immutable implementation pinning, and at-least-once honesty, but does
not use queue names as the sole compatibility mechanism:
<https://docs.temporal.io/workers>,
<https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning>.

### Flyte

Flyte's `TaskTemplate` separates a typed interface from extensible targets such
as containers, Pods, and SQL. Its data-loading configuration can side-load
inputs and collect outputs so raw containers need not embed an SDK. Brokoli
adopts typed-interface/execution-target separation and SDK-free protocol tasks:
<https://github.com/flyteorg/flyte/blob/v1.16.1/flyteidl/protos/flyteidl/core/tasks.proto>.

### Argo Workflows and Tekton

Argo and Tekton demonstrate containers as a practical universal execution
contract, OCI/digest-based distribution, resource/security integration, and
separate channels for parameters, results, workspaces, and artifacts. Brokoli's
single-binary and short-task latency goals make per-task Pod startup an
unacceptable mandatory baseline, so containers remain a fallback while warm
adapters handle ordinary short tasks. Their narrow string/file result contracts
also motivate Brokoli's portable value layer:
<https://argo-workflows.readthedocs.io/en/latest/workflow-concepts/>,
<https://tekton.dev/docs/pipelines/tasks/>.

### Airflow 3

Airflow 3 separates DAG parsing/serialization from task execution and versions
its serialized schema and Task Execution API independently. Versioned DAG
bundles pin code for a run. Brokoli adopts independent schema/runtime versioning
and code pinning while requiring language adapters to pass one conformance
contract:
<https://airflow.apache.org/docs/apache-airflow/stable/administration-and-deployment/dag-serialization.html>,
<https://airflow.apache.org/docs/task-sdk/stable/index.html>.

### Dagster Pipes

Dagster isolates code locations in separate processes and Pipes exchanges
bootstrap context and structured events with external compute over pluggable
transports. Brokoli adopts the small context/event protocol and process
isolation lesson, while avoiding Python runtime types as cross-language
contracts:
<https://docs.dagster.io/integrations/external-pipelines/dagster-pipes-details-and-customization>.

The combined lesson is consistent: language neutrality requires a neutral
graph, portable data boundaries, immutable executable identity, and a
conformant worker protocol. Merely allowing a node to contain a `language`
string leaves the scheduler and data plane language-coupled.

## Consequences

### Positive

- Python, Node, native, container, and future runtimes share one scheduler and
  result model.
- Runtime capacity scales independently; no worker must install every language.
- Digests and pinned payload resolution make retries and historical runs
  reproducible.
- Reference-based I/O prevents queue/database payload size from scaling with
  datasets or source trees.
- Warm adapters and caches keep short-task latency low without sacrificing the
  container escape hatch.
- Structured warnings, errors, metrics, and progress replace magic text
  parsing.
- Isolation, secrets, and network policy apply consistently across languages.
- Local execution can reproduce the worker path instead of approximating it.

### Negative

- Runtime adapters, bundle builders, caches, and conformance tests are a
  substantial maintained platform surface.
- Dependency locking across ecosystems is difficult and cannot be perfectly
  uniform.
- Worker matching and resolved execution records add planning and operational
  complexity.
- Stronger isolation may require OS/container capabilities unavailable in the
  simplest deployment; those deployments must limit accepted trust profiles.
- Warm processes improve latency but create state-leak risks requiring
  adversarial tests and opt-out controls.
- Supporting previous bundle/protocol versions creates a deliberate
  compatibility burden.

### Deferred

- WASI and JVM reference adapters.
- Remote build service and cryptographically signed attestations beyond the
  required digest-bound local/trusted build record.
- Public task-bundle registry and private mirrors.
- GPU/framework-specific environment builders.
- Strict continuous streaming across arbitrary language adapters.
- Multi-region bundle replication and cache prewarming policy.
- Confidential-compute execution profiles.

## Alternatives considered

- **Add Python and Node branches directly to the Go code-node runner.** Rejected
  because every language would expand scheduler code, capability checks,
  cancellation, logging, and security independently.
- **Require every task to be an OCI container.** Rejected as the only path
  because it imposes image-building and startup overhead on simple tasks and
  makes a container runtime mandatory for the single-binary product. Retained
  as the universal fallback.
- **Install every supported runtime on every worker.** Rejected because runtime
  and dependency matrices become a fleet-wide bottleneck and security burden.
  Structured capability matching allows specialized workers.
- **Route languages only by queue name.** Rejected because a typo or mixed
  worker registration causes runtime failure. Queue selection may refine
  placement but cannot prove compatibility.
- **Let workers choose any feasible bundle payload.** Rejected because retries
  could execute different bytes or runtimes. Payload choice is pinned.
- **Serialize functions with pickle/cloudpickle or JavaScript closures.**
  Rejected because formats are language/version-specific, unsafe to load, hard
  to audit, and incompatible across workers.
- **Send source and data in queue messages.** Rejected because it couples queue
  capacity and retry cost to payload size and bypasses content addressing.
- **Reuse the connector plugin protocol unchanged.** Rejected because task
  invocation has different lifecycle commands and typed ports. The structured
  envelope pattern is reused in a sibling protocol.
- **Use only HTTP/gRPC runtime services.** Rejected as the mandatory baseline
  because it adds service discovery, networking, and credential surface for
  local workers. The semantic protocol remains transport-independent so remote
  adapters can be added.
- **Use WASM as the universal runtime.** Rejected for v1 because Python/Node
  ecosystems and native data libraries do not generally compile to WASI.
  Retained as a promising future sandboxed runtime class.
- **Allow runtime package installation during each attempt.** Rejected because
  it is slow, nondeterministic, network-dependent, and vulnerable to upstream
  package changes. Dependencies are resolved before execution and pinned.

## Follow-ups

- Define `brokoli.task-bundle/v2` canonical archive and malicious-input
  fixtures, reusing ADR-016 package primitives where appropriate.
- Define `brokoli.task-runtime/v1` schemas, framing, event limits, and reference
  traces next to the worker protocol.
- Add resolved execution records containing bundle, payload, adapter,
  interface, profile, and platform digests.
- Extend worker registration with structured runtime, protocol, I/O, isolation,
  and resource capabilities; derive placement requirements server-side.
- Build Python and Node reference adapters and make both pass the same cold,
  warm, retry, cancellation, and cross-language data tests.
- Move large `InstanceWorkOrder` inputs and task code to ADR-012 references.
- Implement content-addressed worker caches with verified extraction and
  bounded eviction.
- Define execution-profile policy and minimum OSS process-isolation behavior.
- Add SDK bundle builders, `task inspect`, `task run-local`, and
  `replay-attempt` workflows.
- Migrate legacy generated wrappers through an explicit compatibility adapter
  and publish their support window.
- Add a pinned cross-repository compatibility matrix for core, Python, and
  TypeScript releases.

## Acceptance gates

This ADR remains proposed until all of the following are demonstrated:

- ADR-017 claim/lease ownership is preserved in code and protocol tests: the
  dispatcher owns claim/renewal, resolved platform/environment is pinned before
  claim, and a worker cannot create a competing claim;
- ADR-019 is updated only if remote streaming is introduced; protocol v1 proves
  materialized/reference-based remote dispatch and fenced cleanup;
- task-bundle v2 canonicalization, dependency identity, environment digest, and
  malicious archive vectors pass on every supported platform;
- the JSONL state machine passes malformed-frame, handshake-timeout,
  cancellation race, process-exit race, and oversized-frame tests;
- candidate outputs remain invisible until one trusted, atomic, fenced
  `CommitAttemptResult`, and a stale worker cannot publish any port; crash
  recovery proves upload-before-CAS orphan cleanup and all-or-nothing multi-port
  publication;
- output collection rejects symlink swaps, path replacement, non-regular files,
  descendant writers, and aggregate-limit violations without TOCTOU reads;
- opaque capabilities reject path traversal, general URLs, expired grants,
  cross-tenant objects, wrong direction, wrong checksum, and stale fencing;
- Python and Node adapters execute equivalent tasks cold and warm, prove no
  state/secret/descriptor leakage, and preserve ADR-032 values losslessly;
- one Python task feeds a Node task and one Node task feeds a Python task through
  the same pinned backend and artifact/data plane;
- tenant tasks are rejected on workers lacking the required sandbox instead of
  degrading to process isolation;
- retries reuse an identical execution-environment digest, while explicit
  re-resolution creates visibly non-equivalent lineage;
- pure, keyed-idempotent, and non-idempotent fixtures cover retry after external
  effect, lost response, commit uncertainty, stable effect keys across changing
  attempt IDs, and exhausted elapsed budgets;
- local-run tooling reports adapter, environment, codec, contract, and sandbox
  parity and never claims production isolation it did not provide;
- capability negotiation fails closed for bundle, protocol, runtime, codec,
  interface, and isolation requirements.

## Update (2026-09-05)

This ADR shipped without referencing ADR-029 (the code-node worker pool
and framed protocol) or ADR-031 (`task-bundle/1`), both already released.
That left three relationships unstated that a reader or implementor could
not have inferred correctly: whether `task-bundle/v2` (§2) supersedes
`task-bundle/1`, whether this ADR's fresh-child-per-attempt warm-supervisor
model (§10) replaces ADR-029's exec-multiplexing pool, and whether
`brokoli.task-runtime/v1` (§7) replaces ADR-029's framed socket protocol.
ADR-035 resolves all three: `task-bundle/v2` and the `task` node are
additive, scoped to servers advertising `task-runtime-v1`; ADR-029's pool
and protocol continue to govern `code` nodes (bare or carrying
`task-bundle/1`) unchanged; this ADR's model and protocol apply to `task`
nodes exclusively.

## Update (2026-09-06) — rollout scoped as a phased roadmap; phase 0 shipped

Scoped as issue #439 step 5 (tracking ADR-032/033's combined rollout).
This ADR alone is 19 decision sections and a 15-item acceptance-gate
list -- comparable to or larger than the entire ADR-029 arc, and its
hardest part (§5's structured worker capability negotiation) has zero
prior art anywhere in this codebase (confirmed by direct survey: nothing
like it exists in `engine`/`extensions`). Staged as a 7-phase roadmap
(full detail in issue #439's step-5 checklist) rather than attempted as
one arc. Two scope decisions, confirmed with the repository owner before
starting: OSS core targets the `trusted` isolation profile only (§12) --
real OS/container sandboxing for `tenant`/`untrusted` is out of scope for
this roadmap; EE coordination (worker protocol is one of three tight
EE-core coupling seams, ee#55) is deferred until the phase that actually
touches live worker dispatch, not required for this phase.

This PR ships phase 0 -- schema, fixtures, and structural `task`-node
acceptance, zero execution, mirroring ADR-032 step 1's proven pattern:

- `docs/schema/task-bundle-v2.json` (§2's manifest shape) +
  `task-bundle-v2-canonicalization.md` (the archive-level rules a schema
  over the manifest *document* can't express) + fixtures. Additive
  alongside `pkg/taskbundle`'s `task-bundle/1` (ADR-031) -- this phase
  does not touch that package. Flagged, not fixed: `pkg/taskbundle`'s
  `Extract` already independently duplicates `pkg/plugins/package.go`'s
  safe-extraction logic; a real v2 `Extract` would be a third copy unless
  a shared helper is carved out first -- deferred to the phase that
  actually writes that code, with a real diff in hand, per the
  canonicalization doc's own note.
- `docs/schema/task-runtime-v1.json` (§7's ten frame types: `start`,
  `cancel`, `ready`, `log`, `progress`, `warning`, `metric`, `completed`,
  `failed`, `cancel_ack`) + `task-runtime-v1-framing.md` (the wire-level
  rules -- 1 MiB/frame, LF-terminated, the state machine, cancellation
  race resolution, result-collection order -- a per-frame JSON Schema
  cannot express) + fixtures, one positive fixture per frame type.
- `docs/schema/task-result-v1.json` (§7 rule 5's candidate result
  manifest, its own separate contract name) + fixtures.
- `models.NodeTypeTask` ("task"): a recognized `NodeType` that round-trips
  through JSON/model parsing with zero schema changes needed (the IR
  schema's `node.type` was already deliberately not enum-constrained, to
  let executors/plugins claim types beyond the built-in set -- confirmed
  by reading `pipeline-ir-2.2.json`'s own field description). The new
  work is entirely in `engine.ValidatePipeline`: a `task` node is now
  hard-refused with a specific, named message ("recognized but cannot yet
  execute") instead of falling through to the generic "unsupported type"
  message a real typo would produce -- matching ADR-033 §18's own stated
  policy that compile-only task forms stay rejected until their runtime
  semantics exist.
- Deliberately did NOT advertise `task-runtime-v1`/`task-bundle-v2` as
  execution features in `models.SupportedExecutionFeatures` this phase.
  That list's own doc comment is explicit -- "a feature appears here only
  when a pipeline using it runs, not when it merely persists" -- and
  nothing runs yet. This is the one place this phase deliberately does
  not follow ADR-035's own Follow-up list verbatim; that advertisement
  moves to phase 1/2, once there is something real behind it.

Verification: schema fixtures follow the exact positive/negative +
`_violation` pattern `models/task_interface_schema_test.go` established
for ADR-032 step 1. The `task`-node refusal is covered by two tests: one
proving JSON round-trip (structural acceptance), one proving the specific
refusal message -- mutation-tested by disabling the refusal check and
confirming the second test fails with the pipeline instead falling
through to the generic "unsupported type" message, then restored. Full
`./preflight.sh` green.

## Update (2026-09-06) — rollout phase 1: worker capabilities and placement predicate

Ships the "worker capability model + resolved execution records"
half of phase 1 (issue #439 step 5's roadmap): pure new types and a pure
matching function, nothing wired into a worker registration endpoint,
physical planning, or `ClaimAttempt` yet.

- `docs/schema/worker-capabilities-v1.json` (section 5's worker
  registration/heartbeat shape: protocols, platform, runtimes, io,
  isolation, resources, labels) + fixtures, following the same
  positive/negative/`_violation` pattern as every prior schema in this
  arc. `isolation` is deliberately `array of string`, not a closed enum
  -- section 12 names trust profiles, never a fixed mechanism vocabulary,
  and this schema doesn't invent one.
- `docs/schema/resolved-execution-record-v1.json` (section 4 rule 3's
  pinned selection: runtime protocol, bundle/payload/environment/
  interface digests, execution profile revision) + fixtures. Field names
  mirror section 6's instance work-order `task` object exactly, so a
  future implementation copies rather than re-derives them.
- New `pkg/taskruntime` package: `WorkerCapabilities`/
  `ResolvedExecutionRecord` Go types parsing both schemas, plus a pure
  `Requirements`/`Match(Requirements, WorkerCapabilities) MatchResult`
  function implementing section 5's AND-chain placement predicate
  (protocol, runtime+version, adapter, io, isolation, resources) exactly
  as worked out in that section's own example -- pinned verbatim as
  `TestADR033WorkedExample`. Semver comparison for the adapter-version
  lower bound uses `golang.org/x/mod/semver`, already in the module
  graph as an indirect dependency (govulncheck's own tooling), moved to
  direct via `go mod tidy` rather than adding a new one.
- Deliberate scoping call, recorded in `Requirements.Isolation`'s doc
  comment: section 5's own worked example writes `isolation >=process`,
  implying a strength ordering between isolation mechanism names, but
  section 12 never defines one (only trust profiles). `Match` checks
  exact set membership, not an invented ordering -- asserting a ranking
  ADR-033 itself doesn't state would be this package making policy, not
  implementing it. `TestMatch_IsolationIsMembershipNotOrdering` pins this
  choice explicitly so it isn't silently "fixed" into an assumed
  ordering later without a real decision.
- This package does not itself assemble a `Requirements` value from a
  `ResolvedExecutionRecord` and a bundle payload -- that assembly is the
  "physical planning" step section 4 describes, and stays out of scope
  for this phase along with everything else that would need a live
  bundle-resolution/dispatch path to be meaningful.

Verification: fixture-conformance tests parse the real JSON fixtures
(not just inline Go literals) for both new types. `TestADR033WorkedExample`
reproduces section 5's own example verbatim. Mutation-tested `Match`'s
adapter-version comparison (flipped `< 0` to `> 0`), confirming exactly
the boundary-sensitive tests fail (`TestMatch_AdapterTooOld`,
`TestMatch_AdapterVersionWithoutVPrefixComparesCorrectly`) while the
worked-example test's exact-equality case coincidentally still passed --
then restored. Full `./preflight.sh` green.

## Update (2026-09-06) — rollout phase 2a: harness protocol + Python reference harness

Ships phase 2a (issue #439 step 5's roadmap): the `task-runtime/v1`
duplex protocol client and a working Python reference harness, proven
fully offline against a real `task-bundle/v2` archive. Nothing here is
called from the engine's dispatch path yet -- that is phase 2b.

- New `pkg/archiveextract`: the gzipped-tar traversal/size/entry-count
  extraction guard, factored out of `pkg/plugins/package.go`'s
  `extractArchive` (behavior-preserving migration, same tests unchanged)
  so `task-bundle/v2` extraction doesn't become a third independent copy
  of the same guard. `pkg/taskbundle`'s `task-bundle/1` `Extract` stays
  frozen and independent per ADR-035 Decision 1 -- only `pkg/plugins`
  and the new `pkg/taskbundlev2` build on this package.
- New `pkg/taskbundlev2`: the `task-bundle/v2` manifest contract
  (`docs/schema/task-bundle-v2.json`, section 2) as Go types plus
  `Validate`/`Extract`/`SelectPythonPayload`. `Extract` verifies every
  manifest-declared file's size *and* sha256 against its actual content
  after extraction (section 2 rule 3's digest coverage) -- stronger than
  `task-bundle/1`'s existence-only check, now that the manifest carries
  per-file digests to check against. `SelectPythonPayload` only ever
  resolves the `python` runtime class; every other class in the schema's
  enum parses and validates, since phase 2a is trusted-profile Python
  only (the OSS scoping decision recorded in this ADR's own rollout
  history) -- selecting anything else is phase 4's job.
- New `pkg/taskharness`: `Run` implements section 7's full
  `start -> ready -> (log|progress|warning|metric)* -> completed|failed`
  handshake over duplex JSON Lines, plus the framing rules a per-frame
  JSON Schema can't express (duplicate-key rejection, UTF-8 validation,
  the 1 MiB frame cap, one-JSON-object-per-line) -- `decodeFrameLine`
  scans tokens itself rather than trusting `encoding/json`'s
  last-value-wins duplicate-key behavior. Cancellation sends a protocol
  `cancel` frame once `ready` has been observed and waits (bounded) for
  `cancel_ack` before escalating to SIGTERM/SIGKILL via `pkg/proctree`
  (reused, not reimplemented); before `ready`, there is nothing to
  gracefully cancel, so `Run` terminates the process tree directly, per
  section 7's own scoping of what `cancel` means. The framing doc's
  "exiting before any terminal frame" and "a non-zero process exit after
  `completed`" rules are both real checks here, not just prose --
  the latter reclassifies a completed-but-then-crashed harness as a
  protocol failure rather than trusting a candidate result that might
  never have finished writing. A generic post-mortem failure is
  reclassified as `resource_exhausted` when the exit signal is
  attributable to a configured rlimit (`pkg/taskharness/limits_unix.go`),
  mirroring `engine.signalBreachError`'s approach for code nodes.
- New `pkg/taskharness/pyharness`: the Python reference harness
  (embedded, materialized to disk the same way `pkg/codeexec` embeds its
  ADR-029 wrapper) implementing the harness side of the same protocol,
  plus this phase's own invocation-descriptor convention (`Invocation`
  in `invocation.go`) for what lives at a `start` frame's
  `invocation_path` -- the wire schema only promises that's a string;
  phase 0 left what's actually there unspecified, since no adapter
  existed yet to need an answer. An exception raised by the task itself
  is reported as `user_code`; anything wrong with what the worker handed
  the harness (a malformed `start` frame, an invocation descriptor
  missing a required key) is `contract_violation` -- the harness never
  claims a worker-only category (`runtime_protocol`/`platform`/
  `resource_exhausted`/`lease_lost`), per section 14 rule 8.
- Known, documented phase 2a limitation (not a silent gap): the
  reference harness doesn't read for a mid-task `cancel` frame -- the
  task call is one synchronous Python call with no natural interruption
  point without threads or signals, which is real future work. `Run`'s
  forceful SIGTERM/SIGKILL fallback after the cancellation grace period
  already covers this harness fully; only the cooperative, clean-exit
  path is unimplemented.
- The trusted-profile resource baseline is `pkg/proctree.Rlimits` +
  `ApplyRlimits`, reused as-is (no new limits model) -- the same
  primitive ADR-029's code-node workers already apply.

Verification: `pkg/taskharness`'s state machine is tested through a
re-exec'd fake harness (the standard library's own `os/exec` test
pattern) driven by a small script language, covering the happy path,
every protocol-violation branch (output before ready, malformed/
duplicate-key JSON, a frame after the terminal frame, a non-zero exit
after `completed`, exiting before any terminal frame), both cancellation
outcomes (`cancel_ack` received; grace period expires), and a real
CPU-rlimit test that gets killed and reclassified as
`resource_exhausted`. Mutation-tested the `cancel_ack` frame-type match
(renamed the case label), confirming exactly
`TestRun_CancellationAfterReadyReceivesCancelAck` fails with a
`runtime_protocol`/`unexpected_frame_type` result instead -- then
restored. Every encoded `start`/`cancel` frame and every existing
`task-runtime-v1` positive fixture round-trips through this package's
own encoder/decoder and validates against
`docs/schema/task-runtime-v1.json` (reusing phase 0's fixtures, not
duplicating them). An offline end-to-end test builds a real
`task-bundle/v2` archive, extracts it with `pkg/taskbundlev2`, runs it
through `pkg/taskharness`+`pyharness` with a real `python3`, and
validates the produced candidate result against
`docs/schema/task-result-v1.json` -- the whole phase 2a loop, no engine
involved, skipped only when `python3` isn't on `PATH`. Full
`./preflight.sh` green.

## Update (2026-09-06) — rollout phase 2b: local task-node dispatch through the real engine

Ships phase 2b (issue #439 step 5's roadmap): the first real `task` IR
node dispatch, wired into `Runner.runNodeLogic` and proven through the
actual engine (`RunPipeline`, not just `pkg/taskharness` standalone).
Local (in-process) execution only -- remote/distributed dispatch of task
nodes is a later phase, matching a fact this phase's own investigation
surfaced: `task_bundle`/1 code nodes are *also* local-only on the remote
instance-worker path today (`executeCodeWorkOrder`'s own explicit
refusal, pinned again here by
`TestExecuteInstanceWorkOrder_RefusesTaskNodeType`). That finding
changed this phase's shape from what issue #439 originally said: "EE
coordination becomes a hard prerequisite starting here" describes a
*remote* task-node dispatch phase, not this one -- getting a task node
running at all needed no EE coordination, since it never leaves the
process that already has the bundle.

- **Storage**: new `store.TaskBundleV2Store` capability (`PutTaskBundleV2`/
  `GetTaskBundleV2`), same content-addressed, org-scoped, immutable
  contract as `TaskBundleStore` (ADR-031) against a separate
  `task_bundles_v2` table -- a distinct namespace, never shared with
  `task_bundles` (ADR-035 Decision 1). SQLite and Postgres implementations
  mirror the v1 methods' race-safe `INSERT ... ON CONFLICT DO NOTHING`
  exactly.
- **API**: `POST`/`GET /api/task-bundles-v2/{digest}`, mirroring
  `/api/task-bundles/{digest}`'s upload/fetch contract (URL-named digest,
  re-hash-and-verify before persisting). Upload validates by fully
  extracting into a scratch directory with `pkg/taskbundlev2.Extract`
  (discarded after) rather than a lighter parse-only pass -- catching a
  content/digest mismatch at upload time too, a real improvement over
  v1's structural-only manifest check, made possible by v2's manifest
  carrying a size+sha256 per declared file.
- **`pkg/taskbundlev2.Assemble`**: added alongside `Extract` (mirrors
  `pkg/taskbundle.Assemble`) so tests across `store`/`api`/`engine` build
  real archives instead of hand-rolling tar bytes -- auto-computes
  `Manifest.Files` from the given file contents when left nil, closing
  off the exact by-hand-digest-transcription mistake that produced a real
  bug in this ADR's own phase 0 fixtures.
- **`engine/task.go`**: `taskBundleV2Reference` (parses+validates a task
  node's mandatory `task_bundle` config, mirroring `taskBundleReference`
  for code nodes) and `Runner.runTask` (fetch+extract the bundle, select
  the python payload, build a `pyharness.Invocation`, run it through
  `pkg/taskharness`, map the outcome). A task's keyword parameters come
  from the pipeline's run parameters -- the same "sugar for a
  pipeline-level parameter" mechanism ADR-032 step 3 already established
  for an inferred task keyword parameter, since no typed per-node
  parameter binding exists yet. The trusted-profile resource baseline is
  `pkg/codeexec.Resolve`'s existing `Limits`, reused as-is: CPU/file-size/
  open-files applied externally via `pkg/proctree` before the harness
  starts, memory (`RLIMIT_AS`) applied by the harness process itself from
  the same `BROKED_LIMIT_MEMORY_MB` env var a code node's wrapper reads
  -- `pyharness/harness.py` gained the identical resource-ceiling
  preamble `pkg/codeexec/pywrapper/wrapper.py` already has, verbatim.
- **Output mapping (`readTaskResult`)**: phase 2b supports only the
  `"scalar"` output kind, matching what the reference harness actually
  produces -- `"dataset"`/`"artifact"`/`"collection"` kinds need a codec
  reader this phase does not build, and fail with a named
  not-yet-supported error rather than being silently mishandled.
- **Validation**: phase 0's blanket `models.NodeTypeTask` refusal in
  `engine/validate.go` is lifted, replaced by the same "is `task_bundle`
  well-formed" check the code-node config validator already runs. A task
  node is also now source-capable (`nodeIsSourceCapable`) -- like
  dbt/migrate, it produces data from scratch and (by the same rule every
  source-capable type gets) cannot receive incoming edges.
- **Capabilities**: `"task-runtime-v1"` and `"task-bundle-v2"` join
  `models.SupportedExecutionFeatures` for the first time (phase 0
  deliberately deferred this until something real existed behind them).

Verification: store/API layers get the exact same test shapes as their
v1 counterparts (identical re-upload is a no-op, tenant isolation, digest
mismatch refused, live-Postgres semantics). The real payoff test is
`engine/task_exec_test.go`: a task-bundle/v2 archive is uploaded to a
real `SQLiteStore`, a pipeline referencing it is created and run through
`Engine.RunPipeline` for real, and the task's Python return value comes
back out through the artifact store as a `DataSet` row -- covering the
happy path, a raising task surfacing as `user_code`, a missing bundle
failing clearly, and run parameters actually reaching the task as
keyword arguments. Mutation-tested `readTaskResult`'s interface-digest
check (disabled it, confirmed exactly
`TestReadTaskResult_InterfaceDigestMismatchIsRefused` fails), then
restored. Skipped only when `python3` isn't on `PATH`. Full
`./preflight.sh` green.

## Update (2026-09-06) — rollout phase 2c: remote task-node dispatch

Ships phase 2c: a task node whose Runner has both an execution-attempt
store and an instance job queue wired (the same condition code-node
dynamic expansion already uses) now dispatches through the existing
instance job queue instead of running in-process. This is the phase
issue #439 actually meant by "EE coordination becomes a hard
prerequisite" — confirmed for real this time, since phase 2b's own
investigation found remote dispatch has no scheduler-side placement step
anywhere in this codebase: it is pull-based self-selection by a worker's
own queue backend, filtering on `RequiredCapabilities` tags it is handed
opaquely. `pkg/taskruntime.Match` (phase 1) still has no call site in
OSS and, per the design below, is not expected to gain one here either —
its real consumer, if it has one, is a queue backend's own filtering
logic (EE's), not OSS engine code.

- **`extensions.InstanceWorkOrder` gains one new field: `OrgID`.** Every
  other node type dispatched so far carries its work inline (Script/
  Config) and never needed the org; a task node's work is "go fetch this
  bundle," which is inherently org-scoped. No new bundle-specific fields
  were needed — the digest already flows through the existing `Config`
  field exactly like a code node's `task_bundle` reference does, re-parsed
  worker-side by the same `taskBundleV2Reference` phase 2b already wrote.
- **`engine/task.go`** gained `executeTaskBundle`, the shared core both
  local and remote execution now call (extracted from phase 2b's
  `Runner.runTask`, which is now a thin router), `Runner.dispatchTaskInstanceRemotely`,
  and the new exported `ExecuteTaskWorkOrderContext(ctx, store.Store, *extensions.InstanceWorkOrder)`
  — the worker-side executor a store-aware dispatch loop calls.
  `ExecuteInstanceWorkOrderContext` itself is deliberately UNCHANGED:
  giving it a store parameter would ripple to every existing caller
  (including the enterprise WorkPool worker) for a capability only the
  shared-store worker path can support today. `engine/instance_worker.go`'s
  `executeInstanceJobContext` routes `NodeType == "task"` to the new
  function before reaching the old one; a caller still calling
  `ExecuteInstanceWorkOrderContext` directly gets today's "unsupported
  node type" refusal for task nodes, exactly like `task_bundle`/1 code
  nodes already do there (`TestExecuteInstanceWorkOrder_RefusesTaskNodeType`,
  phase 2b, is unchanged and still passes) — this is a new, additional
  capability a dispatch loop opts into, not a signature change existing
  callers must absorb.
- **A real architectural correction found while wiring this up**: a task
  node's single instance is NOT a new, separate execution attempt the way
  an expansion item or pagination page is — `engine/runner.go`'s outer
  per-node wrapper already claims and renews `(run, node.ID, "", attempt)`
  before `runNodeLogic` ever runs, for every node type uniformly (crash-
  recovery bookkeeping predating this ADR entirely), and settles it
  (Complete/FailAttempt) generically after. A first draft had
  `dispatchTaskInstanceRemotely` claim a SECOND, competing attempt at the
  same key, which collided immediately ("already claimed"). Fixed by
  threading the outer already-claimed `execFencingGen` (and the node's
  own `attempt` number) into `runNodeLogic` as a new parameter — task
  dispatch reuses that same row and fencing generation instead of
  claiming a new one, exactly matching how a task node's local execution
  (phase 2b) never needed its own attempt bookkeeping either.
  `dispatchInstanceWorkOrderRemotely` gained an `extraCapabilities []string`
  parameter (nil for its two existing callers) so a task job's
  `RequiredCapabilities` can carry `"task-runtime-v1"` alongside the
  run-level tags every job already gets — the coarse capability tag this
  phase ships; a finer per-adapter-version tier scheme stays deferred
  exactly as recorded on the private EE coordination thread, since
  computing one would require the dispatcher to fetch and inspect the
  bundle manifest before dispatching it, which defeats dispatching at all.

Verification: `engine/task_remote_dispatch_test.go` reuses
`fakeInstanceJobQueue` (the existing code-node remote-dispatch test
harness) but has its simulated worker call the REAL
`ExecuteTaskWorkOrderContext` against a real store, proving the actual
worker-side execution path end to end (happy path and a raising task
surfacing through the real run) rather than only the generic dispatch-
and-wait machinery the code-node tests already cover. A separate test
drives `ExecuteInstanceJob`/`executeInstanceJobContext` directly to pin
the routing decision itself. Mutation-tested that routing check (forced
it permanently false), confirmed the routing test fails for the right
reason (attempt settles failed via the generic refusal instead of
completed), then restored. Full `./preflight.sh` green.
