# ADR-029: Code-node worker pool and framed execution protocol

**Status:** accepted
**Date:** 2026-08-30

## Context

Brokoli has three mechanisms by which foreign code participates in a
pipeline, built at three different levels of rigor:

| Mechanism | Spec | Versioned? | Negotiated? |
| --- | --- | --- | --- |
| Plugin subprocess protocol | ADR-002/013/016 + `pkg/plugins/protocol.go` | yes (`ProtocolVersion = 1`, declared in the manifest, refused at load) | advertised at `/api/capabilities` |
| SDK ↔ core pipeline IR | ADR-014 + `docs/schema/ir-canonicalization.md` + `pipeline-ir-2.1.json` | yes (`ir_version`, `supported_execution_features`) | SDK preflight refuses by feature name |
| Code-node wrapper | none — only the source of `engine/codenode.go` | **no** | **no** |

The third row is the problem. The code-node executor is architecturally
a sibling of the ADR-002 plugin protocol — the same subprocess
primitive, `exec.CommandContext` with stdin/stdout and environment
variables — built with none of the protocol discipline that ADR-002
established as this project's standard for exactly this boundary:

- The Python wrapper is a ~370-line Go string literal, `%s`-interpolated
  with the user's script, unversioned and untestable as Python. A bugfix
  to its `_Pass`/`emit()` semantics silently changes behavior for every
  existing pipeline on upgrade, with no way to pin, detect, or audit the
  change. This is the risk category ADR-002 explicitly designed around
  (`SupportedProtocolVersions`, the 6-month deprecation window) and code
  nodes simply do not have.
- Every invocation spawns a fresh interpreter. ADR-002 recorded the
  ~100 ms boot cost as acceptable because it is "amortized over the
  whole node run … painful for per-row micro-invocations — which we
  don't do." Dynamic expansion (`engine/expansion.go`) now does exactly
  that: one spawn per fan-out item, plus pandas/pyarrow import cost when
  scripts use them.
- Config and params ride on `BROKED_*` environment variables; row data
  rides on a three-way transport switch (stdin JSON below 10k rows,
  NDJSON files above, CSV files as fallback). None of it is versioned,
  and the whole host environment is passed to the user's script
  (`os.Environ()` at spawn), which is a secrets leak in any deployment
  where the engine process holds credentials.
- The only resource guard is a wall-clock timeout (default 30 s). No
  memory ceiling, no CPU ceiling. A script that materializes a large
  list OOMs the host — in Kubernetes, the pod cgroup kills the whole
  worker including the Go engine (ADR-019 already noted the Python
  child lands in the pod's cgroup). For multi-tenant deployments this
  is a P0 gap.
- Two independent launchers embed the same wrapper and duplicate its
  plumbing: `ExecuteCodeNodeContext` (`engine/codenode.go`) and
  `executeCodeNodeStreamed` (`engine/stream_exec.go`) each resolve
  `python_path`, strip `#PROGRESS:` stderr lines (which nothing ever
  consumes), and parse fallback output.
- User `print()` corrupts the output in stdin-JSON mode, because the
  protocol and the user's stdout are the same stream.

Meanwhile ADR-002's "transform plugins" remain deferred
(`pkg/plugins/manager.go` still refuses them), so code nodes are the de
facto transform-in-foreign-code mechanism — carrying production load on
the least rigorous of the three contracts.

Three axes were conflated in "tighter code-node integration": process
lifecycle (spawn-per-invocation vs warm), state transport (how
config/data cross the boundary), and language coverage (how many
runtimes implement the contract). The first two can be fixed without
touching the isolation model at all; the third is a separate, later
workstream that only becomes credible once the contract is a spec
rather than a string constant.

## Decision

Code nodes get their own **sibling protocol** to ADR-002: the same
discipline — versioned frames, `SupportedCodeProtocolVersions`,
capabilities advertisement, process-group cancellation, minimal
environment — with a different verb set for a different workload shape
(short, high-frequency, no discovery step, no manifest, no install).
This is explicitly *not* a rewrite of the plugin protocol and *not* the
deferred transform-plugin feature.

Concretely:

1. **Warm worker pool.** The engine maintains long-lived Python worker
   processes (package `pkg/codeexec`), each speaking the protocol below
   over a private Unix domain socket. Interpreter and library import
   cost is paid once per worker lifetime, not once per node execution.
   Workers are recycled after a bounded number of executions, reaped
   when idle, killed and respawned on cancel/timeout/limit breach
   (interrupted user-script state is untrusted), and respawned lazily
   after a crash — a dying worker affects only its own in-flight
   execution. When all workers are busy at the cap, overflow spawns a
   one-shot ephemeral worker rather than queueing, so today's
   effective parallelism (N concurrent nodes → N interpreters) is
   preserved exactly and pool exhaustion cannot deadlock anything.
2. **One framed, versioned protocol** replaces env-var config transport
   and the stdin/stdout data mode. Frames are
   `uint32 length | uint8 protocol version | uint8 frame type | JSON payload`.
   Host→worker: `init`, `exec`, `shutdown`. Worker→host: `hello`,
   `log`, `progress`, `result`, `error`. The host refuses a `hello`
   carrying an unsupported protocol version. User stdout/stderr are
   ordinary pipes routed to run logs; the protocol lives on the socket,
   so user `print()` can never corrupt results again, and progress
   becomes a typed frame that is actually consumed.
3. **The data plane stays file-by-reference for contract-v1 scripts.**
   Row data above the inline threshold travels as NDJSON file paths
   named inside the `exec`/`result` frames; small datasets travel
   inline in the frames themselves (replacing stdin/stdout JSON mode).
   This is deliberate, not an omission — see "Lazy rows compatibility
   policy" below.
4. **The wrapper becomes a real, versioned Python library** in-repo:
   `pkg/codeexec/pywrapper/` holds genuine `.py` files (the
   `_LazyRows`/`_Pass`/journal machinery, `emit`/`begin_emit`, the
   worker main loop), embedded into the binary via `go:embed`,
   pytest-tested in CI, and materialized at runtime to a
   content-hash-verified directory. Per ADR-026 the engine always
   injects its own embedded copy — a Python runtime is resolved, never
   provisioned, and no pip package is ever required in a user
   environment. The user script is no longer `%s`-interpolated into
   source text; it is compiled and executed in a fresh namespace per
   invocation.
5. **Resource ceilings, before anything else.** Workers apply
   `resource.setrlimit` at boot — address-space (`memory_mb`), CPU
   seconds, file size, open files — from server-level defaults clamped
   by server-level maxima, overridable per node. On Linux, an opt-in
   cgroup-v2 backstop (`code_execution.cgroup_dir`) gives a true RSS
   ceiling. A breach produces a typed error naming the limit — never a
   bare "signal: killed" — and the worker is recycled. These ceilings
   also ship as a preamble on the legacy spawn path, so the safety fix
   does not wait for the pool.
6. **Every node run records its execution contract**: wrapper version,
   protocol version, interpreter path, warm/cold, and effective limits,
   in an additive `NodeRun` field. Upgrades become auditable; a run
   executed under wrapper v1 (the legacy string) is forever
   distinguishable from one under v2 (the extracted library).
7. **Capabilities and preflight.** `/api/capabilities` advertises
   `code_protocol_version` and `code_wrapper_version`, and
   `supported_execution_features` gains `code-streaming-emit`, closing
   the standing gap where a script using `emit()` deploys silently to a
   server whose wrapper predates it. The Python SDK's preflight learns
   to require that feature when a code-node script uses the emit API.

### Lazy rows compatibility policy (the ADR-019 Milestone 1.5 note)

ADR-019's follow-up list promised an SDK note on the lazy-`rows`
compatibility policy that was never written. This section is that note.

The Milestone 1.5 contract is: `rows` is **re-iterable** (every fresh
iteration is a fresh pass), and `len(rows)`/indexing/slicing
**transparently materialize** at the old memory cost. Both properties
require a complete, seekable file behind `rows`. A FIFO or socket
stream cannot provide them — a second iteration or a `len()` call would
deadlock — so *pipelined subprocess input is forbidden for contract-v1
scripts*, exactly as ADR-019's Milestone 2 note concluded. This ADR
adopts that conclusion as protocol: the v1 data plane is
file-by-reference, and true row streaming over the socket is reserved
for a future **declared per-script strict-streaming capability** that
explicitly trades away re-iteration and `len()`. Scripts opt in; the
default contract never silently changes underneath them. The journal
mechanism's semantics (mutation of the live row persists; mutation of
an already-passed row fails loudly) are part of contract v1 and are
preserved verbatim, message text included.

## Consequences

### Positive

- Boot cost amortized: expansion fan-out of N items pays one
  interpreter/pandas boot instead of N.
- The P0 multi-tenant gap closes: memory/CPU ceilings with clear,
  limit-naming errors, plus a true cgroup backstop where delegated.
- The wrapper is testable (pytest in CI), lintable, versioned, and
  auditable per run — the ADR-002 rigor gap is gone.
- One launcher path instead of two: `stream_exec.go`'s duplicated
  resolution/stripping/fallback logic is deleted.
- User `print()` and library chatter can no longer corrupt output.
- The host environment stops leaking into user scripts (allowlist +
  `code_execution.pass_env` escape hatch) — a deliberate,
  changelogged break; it is the secrets fix.
- Crash isolation strictly improves: a worker crash costs one
  execution and one lazy respawn, never a stalled pipeline.

### Negative

- Warm workers hold memory between executions (a pandas-warm worker is
  hundreds of MB RSS). Pool sizing defaults are conservative
  (`max(2, NumCPU/2)`), idle reaping is on by default, and deployment
  docs state pod sizing as Go + max_workers × limit.
- Warm workers share an import cache across executions — that *is* the
  win, but module-level state from one script can in principle be
  observed by the next within the same worker. Fresh namespaces per
  exec and recycle-after-N bound this; per-run worker affinity and a
  fork-per-exec warm-template are recorded below as future hardening
  for multi-tenant postures.
- Two execution paths exist for one release (pool default-on for
  linux/darwin, legacy spawn behind `code_execution.pool_enabled=false`
  and on Windows), with a CI matrix keeping both green until the
  legacy path is removed.
- `RLIMIT_AS` limits address space, not RSS — mmap-heavy numpy can
  trip it below true usage. Documented; the cgroup backstop is the
  accurate ceiling where available.

### Deferred

- **Phase 4 — language coverage.** The protocol is the contract; a new
  language is a new worker implementation, not a new mechanism. Go is
  the special case worth naming: a native in-binary executor against
  the same `Exec` interface is legitimate (no subprocess needed) and is
  the one place the isolation/tightness trade-off can genuinely be
  beaten. Node/TS would use the same pool model with the interpreter
  resolved per ADR-016's runtime classes. None of this starts until the
  Python pool has validated the protocol in production.
- **Phase 5 — WASM spike.** ADR-002 rejected WASM for connectors
  because cloud SDKs don't compile to it; that reasoning does not
  apply to dependency-free transform scripts (filter/map/rename),
  where WASM offers sub-millisecond startup and stronger sandboxing.
  An opt-in fast path for that subset — flagged per node, never the
  general case — is worth a spike after the pool/protocol work lands.
- **Strict-streaming capability** (FIFO/socket rows for scripts that
  declare away re-iteration and `len()`), per the policy above.
- **Fork-per-exec warm template** (warm parent forks a child per
  execution; cancel kills only the child): strictly better isolation
  on Linux, but fork-safety hazards with numpy/objc on macOS. Recorded
  as evaluated future hardening.
- **Per-pipeline-run worker affinity** for multi-tenant EE postures.
- **Environment minimization for plugins.** `pkg/plugins`'
  `minimalEnv()` is `os.Environ()` today ("Phase 1: trust the local
  box") — the allowlist discipline introduced here for code nodes is
  new work for plugins too, not existing parity.

## Alternatives considered

- **Embedded interpreter** (`go-python3`, CGO) — rejected again for
  the same reasons as ADR-002: kills the single-binary story,
  dependency hell, no isolation. The tightness this redesign wants is
  amortized lifecycle and a real protocol, not process-boundary
  removal.
- **Reusing the ADR-002 JSONL codec for this protocol** — rejected.
  `DecodeStream`'s garbage-line tolerance exists because plugin stdout
  legitimately catches stray `print()`s; on a private socket that
  tolerance is an anti-feature (corruption should kill and respawn the
  worker, not degrade to a warn log). JSONL also has no per-frame
  version tag, and its scanner ceiling is a poor fit for inline row
  payloads. The *versioning discipline* is reused; the wire format is
  deliberately not.
- **Forcing code nodes into the five-verb connector protocol** —
  rejected. `spec`/`check`/`discover` model connectors (auth, schema,
  long streams); code nodes are short, high-frequency executions with
  no discovery step. A shared verb set would fit neither well. Shared
  discipline, sibling protocols.
- **Pool speaking the old env/file transport as a transitional step** —
  rejected. Per-invocation state cannot ride environment variables
  into an already-running process, so any pool needs a per-request
  channel; a "transitional" stdin mini-protocol would be a second
  throwaway protocol. The pool ships with the framed protocol from
  day one; the protocol's completion (progress, capabilities, SDK
  gating) follows as its own stage.
- **Queueing at the worker cap instead of overflow spawning** —
  rejected as the default (kept as opt-in config for
  memory-constrained hosts): queueing reduces today's effective
  parallelism and creates a deadlock class the overflow design makes
  structurally impossible.

## Amendments to prior ADRs

- **ADR-002, Deferred — "Long-lived plugin daemons":** realized here
  *for code nodes*, under this sibling protocol. Plugin daemons proper
  remain deferred; when they happen they should adopt this ADR's pool
  and framing rather than inventing a third lifecycle.
- **ADR-002, Deferred — "Transform plugins":** still deferred; code
  nodes (this ADR) are the supported transform-in-foreign-code path. A
  future transform-plugin design should build on this protocol, not on
  threading records through stdin after the config header.
- **ADR-002, Follow-ups — `brokoli-connector-sdk` on PyPI:** never
  built (the docs already warn it does not exist). Its first real
  content is this wrapper library, whose source of truth is in-repo at
  `pkg/codeexec/pywrapper/`; PyPI publication remains a future
  authoring convenience, never a runtime dependency. The stale claim
  in `pkg/plugins/protocol.go`'s package doc is corrected alongside
  this ADR.
- **ADR-019, Milestone 2 note:** its conclusion (the lazy-rows
  contract forbids FIFO input; the honest path is a declared opt-in
  capability) is adopted as protocol by this ADR's data-plane
  decision. The follow-up "Milestone 1.5 SDK note: lazy `rows`
  compatibility policy" is discharged by the section above.

## Follow-ups

- PR staging for this arc: (1) this ADR + comment/doc corrections;
  (2) resource ceilings + per-run contract recording on the legacy
  path; (3) wrapper extraction (`pywrapper/`, embed, materialize,
  pytest); (4) protocol v1 + pool core in `pkg/codeexec`, default off;
  (5) launcher convergence + flip default on for linux/darwin;
  (6) progress/capabilities/SDK preflight completion; (7) docs sweep.
- Legacy spawn path removal after one dual-mode release.
- SDK release gating `emit()` scripts on `code-streaming-emit`.
- Phase 4 / Phase 5 / strict-streaming / fork-template as listed under
  Deferred.
