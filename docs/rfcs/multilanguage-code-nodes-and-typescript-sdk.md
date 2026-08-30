# Multi-language code nodes and the TypeScript SDK — engineering handoff

**Status:** ready to execute. ADR-029 is fully shipped (core #419–#424);
this document is the architect-level plan for what comes next, written so
the teams can execute without the original authors in the loop.
**Date:** 2026-08-30

Two workstreams, one contract:

- **Workstream A (core):** TypeScript as the second code-node language —
  a Node worker implementing the ADR-029 protocol, so pipelines can run
  `language: "typescript"` scripts.
- **Workstream B (brokoli-typescript):** finish the TypeScript SDK
  (issue ts#1's remainder) and make it the authoring surface for those
  TypeScript code nodes.

Read ADR-029 first (`docs/adr/029-code-node-worker-protocol.md`). Nothing
here overrides it; this document instantiates its Phase 4 for one
language and sequences the SDK work that consumes it.

---

## 0. The one rule that must survive every PR

**Never introduce a per-task signature declared on the authoring side and
bound by name on the execution side.** This is the verified failure mode
of Airflow's Java SDK (AIP-108): the Python stub re-declares the Java
method's parameters by hand, the Java annotation binds upstream XComs by
task-name string, and nothing checks either seam — two representations of
one fact, correctness by convention. Their transport is versioned
(`Airflow-Supervisor-Schema-Version`), but the version doesn't cover the
stub seam, so it catches nothing that actually drifts.

Brokoli's inline code nodes structurally avoid this: the execution side
owns the whole contract (the wrapper's names), the orchestration side
ships opaque script text plus a capability class, and dependency is an IR
edge — no parameter mirroring exists. Every decision below preserves that
shape. The known miniature of the tax — SDK decorator codegen writing
against the wrapper's implicit globals — is tracked in brokoli-sdk#85
with the differential-contract-test cure, and Workstream B applies that
cure from day one rather than retrofitting it.

Concretely, when adding TypeScript authoring: the SDK serializes a
function into script text and the worker provides a fixed, versioned
namespace. The SDK never declares "this task takes parameters X, Y" for
the engine to match. If a future design needs typed task parameters, stop
and write an ADR that explains how the two declarations are generated
from one schema — do not hand-sync them.

## 1. What already exists (inventory for the implementer)

All in core unless noted. These are the pieces you extend, not rewrite:

| Piece | Where | What it gives you |
| --- | --- | --- |
| Framed protocol v1 | `pkg/codeexec/protocol.go` + `pkg/codeexec/pywrapper/protocol.py` | Language-agnostic frames: `uint32 BE len \| uint8 version \| uint8 type \| JSON`. Verbs `init`/`exec`/`shutdown`, `hello`/`log`/`progress`/`result`/`error`, typed error kinds. The Node worker implements exactly this; no protocol changes needed. |
| Worker pool | `pkg/codeexec/pool.go`, `worker.go`, `exec.go` | Sub-pools keyed by (interpreter, limits); overflow one-shots at the cap (never queue — parallelism must not regress); kill-and-respawn on cancel/timeout/breach; idle reap; recycle after N execs. Language dispatch slots in here. |
| Process lifecycle | `pkg/proctree` | Group leadership + tree signalling (the ADR-013 orphan fix). Use for the Node worker unchanged. |
| Materialization | `pkg/codeexec/materialize.go`, `embed.go` | go:embed → content-hashed 0700 temp dir, atomic, concurrent-safe. Add the JS wrapper files to the same lists. |
| Limits | `pkg/codeexec/limits.go` + `engine/codenode_limits*.go` | Resolution (server defaults, node overrides, clamps) and breach-naming errors. Python applies rlimits in-process; Node needs the Go side to do it (§3.4). |
| Runtime resolution | `pkg/plugins.ResolvePython`, `resolveRuntimeBinary(name, constraint)` (`pkg/plugins/package.go`) | ADR-016/026 discipline: check for a runtime, never ship one. Export a `ResolveNode` the same one-line way `ResolvePython` was exported. |
| Launcher adapters | `engine/codenode_pooled.go` | Where `Request` is built from node config. Language dispatch lands here + `pool.Request`. |
| Contract tests | `pkg/codeexec/pywrapper/tests/` (pytest), `pkg/codeexec/pool_test.go`, `engine/codenode_test.go` etc. | The behaviors any second language must mirror where applicable, and the test style to copy. |
| Capabilities & gating | `api/capabilities.go`, `models/pipeline.go` `SupportedExecutionFeatures`, SDK preflights | The advertisement/refusal pattern (`code-streaming-emit` is the worked example, pinned in `api/capabilities_test.go`). |
| TS SDK | github.com/Tnsor-Labs/brokoli-typescript | Compiler + client + differential oracle vs the Python SDK (byte-identical IR, per `docs/schema/ir-canonicalization.md`); integration suite vs a live server (`tests/integration/`). |

Conventions that are not optional: small PRs in the staged order; ADR
first when a decision is being made (a change contradicting an ADR is a
review blocker); `./preflight.sh` green before push (never two at once);
live-test on a local server before push; gosec baseline is 347 — new
findings need documented `#nosec` or a baseline PR; EE and docs repos
have no CI (billing) — verify locally, note it in the PR.

---

## 2. Workstream A — TypeScript code nodes in the engine

### A1. ADR-030 first (small, one PR)

Title suggestion: "Code-node language dispatch and the TypeScript worker
contract". It must decide, explicitly:

1. **Selection:** `config.language`, enum `"python"` (default) |
   `"typescript"`. The key already exists as tolerated passthrough
   (`engine/blob_reclaim_test.go` carries it opaquely); this ADR makes it
   validated and meaningful. Unknown values are a deploy-time validation
   error, not a runtime surprise.
2. **The TS execution contract** (§A2 below) — names, semantics, what is
   deliberately NOT ported from Python (the mutation journal).
3. **Limits mechanism for Node** (§A4) and its documented degradation.
4. **Feature gates:** `SupportedExecutionFeatures` gains
   `"code-typescript"`; capabilities gains `"code_languages":
   ["python", "typescript"]` and `code_js_wrapper_version`. SDK
   preflights (both SDKs) require `code-typescript` when a code node
   carries `language: "typescript"` — same exact-string discipline as
   `data_intervals` and `code-streaming-emit`.
5. **Codify §0** in the ADR's what-not-to-do.

### A2. The JS wrapper (`pkg/codeexec/jswrapper/`)

Files, mirroring pywrapper: `worker_main.mjs`, `protocol.mjs`,
`contract.mjs`, `version.mjs` (`export const JS_WRAPPER_VERSION = 1` —
Go parses it from the embedded bytes at init exactly like
`version.py`; copy `mustParseWrapperVersion`'s pattern and pin it with a
test). Tests in `jswrapper/tests/` run by `node --test` (no npm deps —
the wrapper must run on a bare Node ≥ 20, same zero-install stance as
ADR-026 takes for Python), wired into CI and preflight next to pytest.

**The script's namespace — the contract, verbatim across languages:**

| Name | Type (TS) | Semantics |
| --- | --- | --- |
| `rows` | `Row[]` where `Row = Record<string, unknown>` | Lazy-materialized: implemented as a getter; first access reads and materializes the whole input (NDJSON file mode) or is already the inline array. Mirrors Python's `len()`/index materialization promise. |
| `rowsStream()` | `AsyncIterable<Row>` | The constant-memory input idiom: fresh pass over the NDJSON file per call, re-iterable by calling again. File mode only; inline mode yields from the array. |
| `columns` | `string[]` | From the input's declared column order. |
| `config`, `params` | objects | Verbatim from the exec frame. |
| `emit(row)` | `(row: Row) => void` | Streams one output row to the NDJSON output file. First call (or `begin_emit`) enters emit mode; `output_data` is then ignored. |
| `begin_emit(columns?)` | `(columns?: string[]) => void` | Declares emit mode explicitly — REQUIRED for filters that may match nothing, so the zero-emit case produces an empty dataset with declared columns instead of falling back to passthrough. Same trap, same cure, same wording as Python. |
| `output_data` | `{ columns: string[], rows: Row[] \| AsyncIterable<Row> \| Iterable<Row> }` | Default output. Iterables are drained to the output file without materializing. |

Names are snake_case on purpose — `emit`, `begin_emit`, `output_data`
are the cross-language contract, and per-language renames are exactly the
representation-drift §0 forbids. Do not add camelCase aliases.

**Execution model inside `worker_main.mjs`:** per exec frame, build a
fresh `vm.createContext` containing the namespace, wrap the user script
in an async IIFE (`(async () => { ...script... })()`), `await` it with
the frame's timeout as a backstop (the Go side still owns the real
timeout and will kill the worker). Top-level `await` therefore works in
user scripts. `console.log`/`console.error` are bound to write to the
worker's captured stdout/stderr, which the worker forwards as `log`
frames — same print-safety guarantee as Python. Output resolution after
the script: emit-mode → file written (or the zero-emit inline empty
dataset); else drain `output_data.rows` to the output file; inline input
returns inline results (the small-data contract). Every failure becomes a
typed `error` frame: `user_exception` with the JS stack (pointing at the
user script — use a named script filename like `<code-node>` in the vm
options so stacks are readable), `resource_limit` when the heap cap
throws `ERR_WORKER_OUT_OF_MEMORY`/`RangeError` heap failures, `internal`
otherwise.

**Deliberately not ported:** Python's `_Row`/`_Pass` mutation journal.
That machinery exists to keep decade-old Python scripts that mutate
`rows` in place working under lazy streaming. TypeScript has no legacy to
protect: `rows` (the materialized array) is freely mutable and honored,
and `rowsStream()` rows are read-only by documented contract (mutating a
streamed row does nothing — say so in docs; do not build a journal).
State this in ADR-030 so nobody "helpfully" ports it.

### A3. Engine dispatch (one PR)

- `engine/validate.go`: `language` must be `"python"`/`"typescript"`
  when present (extend `codeExecutionKeyErrors`).
- `engine/codenode_pooled.go` (`resolveCodeInterpreter` → becomes
  language-aware): `language: "typescript"` resolves via a new
  `plugins.ResolveNode(constraint)` (export `resolveRuntimeBinary("node",
  constraint)` the way `ResolvePython` was exported); node-level
  `node_path` override key, mirroring `python_path`, validated the same
  way. A server without Node fails at **deploy preflight** (capability
  not advertised — see A5) and, defense in depth, at exec with the
  resolver's reason string.
- `pkg/codeexec`: `Request.Language`; sub-pool key gains the language
  (interpreter path alone is not enough once wrapper versions differ);
  `spawnWorker` picks `worker_main.mjs` vs `worker_main.py` and, for
  Node, injects the heap flag (§A4). `hello` gains a `language` field
  (additive JSON — old workers simply don't send it; do NOT bump the
  protocol version for an additive field, that is what field-tolerance
  is for).
- The legacy spawn path does **not** learn TypeScript. It exists for one
  release as the Python fallback; `language: "typescript"` with
  `BROKOLI_CODE_POOL=0` (or on Windows) is a validation error naming the
  constraint. Keeps the matrix small and the removal clean.

### A4. Resource ceilings for Node (one PR, co-designed with A3)

Node has no in-process `setrlimit`. Layered enforcement, all from the
existing `Limits` values — no new knobs:

1. **Heap:** spawn with `--max-old-space-size=<memory_mb>` (and
   `--stack-size` default). Accurate, portable, catchable — breaches
   surface as heap-allocation failures the worker maps to
   `resource_limit` frames.
2. **Address space / CPU / files (Linux):** add
   `proctree.ApplyRlimits(pid, ...)` using `prlimit(2)` (unix build tag,
   Linux-only syscall — no-op elsewhere), called by `spawnWorker` after
   start, before `init`. This also closes a known Python gap: today a
   Python script can `resource.setrlimit` itself back up (the preamble is
   cooperative); prlimit from outside is not undoable by the child.
   Apply it to Python workers too in the same PR and note it in the
   changelog.
3. **Documented degradation:** macOS gets heap cap + RLIMIT_CPU only;
   the docs table in `deployment/operations.mdx` grows a per-OS row.

Tests copy `engine/codenode_limits_test.go`'s shape: a ballooning TS
script under a small cap fails with the limit-naming error; a CPU spin
under `max_cpu_seconds` likewise (Linux); under-limit scripts unaffected.

### A5. Capabilities, gating, changelog (small PR)

`api/capabilities.go`: `code_languages`, `code_js_wrapper_version`.
`models/pipeline.go`: `code-typescript` (with the honesty rule comment:
advertised only where a Node runtime actually resolved — compute it at
startup via `ResolveNode`, do not hardcode; a server without Node must
not advertise it). Pin all of it in `api/capabilities_test.go` next to
the `code-streaming-emit` pins. CHANGELOG under Unreleased. Python SDK
(`brokoli-sdk`): `compatibility.py` requires `code-typescript` for
`language: "typescript"` code nodes (trivial; same PR pattern as sdk#84).

### A6. Docs (docs repo; build locally, no CI)

`pipelines/node-types.mdx`: the `language` key, the TS namespace table,
`rowsStream()`/mutability semantics, per-OS limits row.
`deployment/docker.mdx`/`install-methods`: Node ≥ 20 as an optional
runtime, resolved-not-shipped. `lets-brokoli`: one TS code-node example.

### A-order and acceptance

A1 → A2 → A3+A4 → A5 → A6, each PR preflight-green and live-tested on a
local server (`brokoli serve --port 8123`; deploy a TS pipeline via curl
or the TS SDK, verify: runs green, warm second run, breach names the
limit, `print`/`console.log` lands in logs, cancel kills only the busy
worker). The arc is done when: a pipeline with
`{type: "code", config: {language: "typescript", script: "..."}}` runs
end to end on the pool; both wrapper test suites and both engine language
legs are in CI; capabilities honestly reflect runtime presence; and a
server without Node refuses TS pipelines at deploy time with the feature
name.

---

## 3. Workstream B — finish the TypeScript SDK

Repo: `Tnsor-Labs/brokoli-typescript`, tracking issue ts#1. Order chosen
so each step is independently shippable and the server dependency (A-arc)
gates only the steps that need it.

### B1. TypeScript code-node authoring (needs A3+A5 on a server)

- `code()` factory: `language?: "python" | "typescript"` emitted into
  config (default python — never guess from the host language; authoring
  in TS and running Python scripts is legitimate).
- `requiredExecutionFeatures` (`src/ir.ts`): `language: "typescript"` →
  `"code-typescript"`; scripts containing `emit(`/`begin_emit(` →
  `"code-streaming-emit"` (parity with the Python SDK's sdk#84 gate).
- **`task()` — a real function, not a string** (the payoff feature):

  ```ts
  const shaped = p.task("Shape", rows, (rows) => {
    return { columns: ["id", "total"], rows: rows.map(r => ({ id: r.id, total: Number(r.amt) * 2 })) };
  });
  ```

  Implementation: serialize the function with `Function.prototype.
  toString()` and wrap it as `output_data = await (<fnSource>)(rows);`
  targeting the vm contract. ts#1's warning ("do not assume toString()
  is a valid deployed script") is answered by *constraining* what
  `task()` accepts and verifying it: the function must be
  self-contained (no closure captures — there is no closure packaging in
  v1). Enforce what's enforceable (reject native functions and bound
  functions, whose `toString()` is not source), document the closure
  rule, and let the contract tests catch the rest. Closure/import
  packaging is explicitly out of scope until someone writes the ADR for
  it — say so in the API docs.
- **The sdk#85 cure, from day one:** a differential contract suite in
  this repo — fixtures where `task()`-generated script text is executed
  by the real `jswrapper` (fetched from the pinned core version in CI,
  the same way the differential oracle pins the Python SDK), asserting
  outputs round-trip and every namespace name the generator references
  exists. A generator or wrapper change that breaks the other side fails
  CI here, not in production. This suite is a merge requirement for
  `task()`, not a follow-up.
- Integration tests (`tests/integration/`): a TS code-node pipeline
  end to end against `BROKOLI_SERVER` — deploy, run, verify output,
  breach a limit, assert the deploy-time refusal against a server
  without `code-typescript` (mock the capabilities response for that
  case).

### B2. Schema-based IR validation (no server dependency)

Embed `docs/schema/pipeline-ir-2.1.json` (fetch-and-vendor with the core
version pinned; a CI check compares it to the pinned core tag so it
cannot drift silently) and validate in `validatePipeline` before the
rule-based checks. Keep the rule-based checks — the schema declares
shapes, the rules declare semantics (upsert-needs-keys etc.).

### B3. TS-native device auth (no server dependency)

The endpoints exist and are stable (core #414 / ee#176-179):
`POST /api/auth/oauth/device` → user+device codes, browser confirm,
`POST /api/auth/oauth/device/poll` → a revocable `brk_` token. The
shared credentials store is already byte-compatible (a Python
`brokoli auth` login works in TS today). Implement `brokoli auth` in the
CLI: request, print the link+code (flush!), poll with the server's
interval, store via `storeToken`. Follow the Python `device.py` shape;
the live-tested pitfalls are documented there (output buffering, poll
answer states `authorization_pending`/`slow_down`/`access_denied`/
`expired`/`approved`).

### B4. Decorator-parity surface (after B1)

`@task`-style wrappers mirroring the Python seven where they make sense
in TS (likely plain higher-order functions rather than decorators —
decorators on free functions are awkward in TS; don't force the syntax,
keep the names). Every generator goes through the B1 contract suite. The
Python SDK's memory-profile honesty applies: generated scripts that
materialize get the same doc callout, or better, generate against
`rowsStream()`/`emit()` where the shape allows and beat Python's
decorators at their own weakness.

### B5. Phase 2 client helpers (independent)

`watch()` (poll `/api/pipelines/{id}/runs` until terminal — a websocket
watch only when the server grows one; don't invent a protocol),
graph assertions (`expectGraph(p).hasEdge("a", "b")` for tests),
run snapshots (fetch run + node runs + logs into one comparable object),
`livePipeline` harness for integration tests (deploy-run-assert-cleanup,
factored from what `tests/integration/` already does inline).

### B-order

B2, B3, B5 are unblocked today and parallelizable. B1 lands behind the
A-arc (develop against a locally built core binary from the A branches —
the integration suite's `BROKOLI_SERVER` pattern makes this trivial).
B4 follows B1. Release the SDK only against released server
capabilities: the standing rule — **server advertises first, SDK gates
second** — and its concrete instance today: brokoli-sdk#84 is merged but
must not ship to PyPI before a core release advertising
`code-streaming-emit`; the same applies to any TS SDK release gating on
`code-typescript`.

---

## 4. Risks and open questions (decide-when-reached, with owners' notes)

- **npm-dependency creep in jswrapper.** The zero-install stance (bare
  Node, `node --test`) will chafe the first time someone wants a helper
  package. Hold the line; a wrapper dependency means shipping or
  installing node_modules on every host, which reopens ADR-002's
  dependency-hell argument.
- **`Function.toString()` edge cases** (transpiled output, minified
  bundles, `this`-binding). The constraint set in B1 (self-contained,
  non-native, non-bound) plus the contract suite is the answer; widen
  support only with fixtures proving each widening.
- **Worker warmth vs. TS compilation.** v1 executes plain JS the vm can
  run (the SDK emits JS — `task()` functions are already JS at runtime;
  authors wanting TS-only syntax in *inline strings* must transpile
  themselves). If demand appears for on-worker transpilation, that is an
  ADR (it drags a compiler into the wrapper).
- **Go as a code-node language** stays future work: for user code it
  means user-compiled binaries speaking the protocol via a small
  `codeexec/workerlib` Go module — a different distribution problem
  (artifact upload, checksums) that deserves its own ADR. Do not attempt
  it as "just another wrapper".
- **WASM spike (ADR-029 Phase 5)** remains parked until after TS ships;
  its natural scope is dependency-free scripts in *any* language, and
  the TS worker will teach us what the namespace-in-a-sandbox contract
  really needs.

## 5. Definition of done, whole handoff

1. `language: "typescript"` code nodes run warm-pooled, limit-bounded,
   with typed errors and consumed progress, on any server with Node ≥ 20
   — and are refused by name everywhere else, at deploy time.
2. The TS SDK authors them with real functions, gated by preflight,
   pinned by a differential contract suite against the real wrapper.
3. ts#1 closes: schema validation, device auth, decorator surface,
   Phase 2 helpers, integration coverage.
4. No new authoring-side/execution-side dual declarations exist anywhere
   (§0), and every cross-language contract added is versioned,
   advertised in capabilities, and refused on mismatch.
