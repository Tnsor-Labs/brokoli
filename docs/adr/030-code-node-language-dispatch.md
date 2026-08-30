# ADR-030: Code-node language dispatch and the TypeScript worker contract

**Status:** accepted
**Date:** 2026-08-30

## Context

ADR-029 built the language-agnostic half of multi-language code nodes:
a framed, versioned worker protocol over a private Unix socket, a warm
pool with kill-and-respawn lifecycle, resource-ceiling resolution, and
capability advertisement. Its Phase 4 deferred the second language. The
engineering handoff (`docs/rfcs/multilanguage-code-nodes-and-typescript-
sdk.md`) chose TypeScript first and staged the work (core #426); the
TypeScript SDK has meanwhile shipped its authoring surface
(brokoli-typescript#6) against a *provisional* reading of this contract
and explicitly blocks its release-completeness on this ADR and the real
worker.

Three questions need decisions an implementer cannot make alone:

1. **Dispatch** — how a pipeline selects a language, and what happens
   everywhere a TypeScript node meets a surface that cannot run it.
2. **The TypeScript execution contract** — exactly what a script sees,
   which Python behaviors carry over, which are deliberately absent,
   and how the script sleeps (the SDK's sensor generator currently
   bets on `Atomics.wait`, an unspecified detail).
3. **Resource ceilings for Node** — ADR-029's Python mechanism is
   RLIMIT_AS applied in-process. Measured on Node 24: the V8 pointer-
   compression cage reserves ~1.3 GiB of address space at idle, and a
   1 GiB RLIMIT_AS **aborts the interpreter at startup**. The Python
   mechanism transplanted verbatim would kill every Node worker before
   hello.

One rule from the handoff binds everything here (§0, verified against
Airflow AIP-108): **no per-task signature declared on the authoring
side and bound by name on the execution side.** The authoring side
ships opaque script text plus a capability class; the execution side
owns the whole namespace contract; dependency is an IR edge. Nothing in
this ADR introduces a second, hand-synced representation of any fact.

## Decision

### 1. Language dispatch

- `config.language` on code nodes is a validated enum: `"python"`
  (the default; absent or empty means python) or `"typescript"`.
  Validation happens at deploy time in both validation surfaces
  (extending `codeExecutionKeyErrors`), with defense-in-depth at
  execution. An unknown language is a named validation error, never a
  runtime surprise. The key travels opaquely through the IR — both
  SDKs already emit it — and does not affect canonicalization.
- The pool dispatches on language: `Request.Language`, sub-pools keyed
  by (language, interpreter, limits), `spawnWorker` selecting
  `worker_main.mjs` vs `worker_main.py`. The worker's `hello` gains an
  additive `language` field — additive JSON fields do NOT bump the
  protocol version; field tolerance is what tolerance is for.
- The Node runtime is resolved, never shipped (ADR-026):
  `plugins.ResolveNode(constraint)` exported beside `ResolvePython`,
  runtime floor **Node ≥ 20** (the CI-pinned LTS; the wrapper uses
  `node:vm`, async iteration, and `node --test`). A per-node
  `node_path` override mirrors `python_path` — validated the same way,
  isolated in its own sub-pool.
- **The legacy spawn path never learns TypeScript.** It exists for one
  release as the Python fallback; `language: "typescript"` under
  `BROKOLI_CODE_POOL=0` or on Windows is an error naming the
  constraint. This keeps the dual-mode matrix small and the legacy
  removal clean.

### 2. The TypeScript worker contract (JS wrapper v1)

`pkg/codeexec/jswrapper/` — `worker_main.mjs`, `protocol.mjs`,
`contract.mjs`, `version.mjs` (`export const JS_WRAPPER_VERSION = 1`,
parsed from the embedded bytes by Go exactly like `version.py`, so the
two sides cannot drift). Bare Node, **zero npm dependencies** — a
wrapper dependency reopens ADR-002's dependency-hell argument. Tests
run under `node --test` in CI and preflight, beside pytest.

Each execution runs in a fresh `vm.createContext`, the user script
compiled with filename `<code-node>` (stacks name the user's code) and
wrapped in an async IIFE that the worker awaits — top-level `await`
works. The namespace, snake_case on purpose because it is a
cross-language protocol contract (per-language renames are exactly the
representation drift §0 forbids):

| Name | Type | Semantics |
| --- | --- | --- |
| `rows` | `Row[]` via lazy getter | First access materializes the whole input (file mode) or is the inline array. Mutable, and mutations are honored — it is a plain array. Mirrors Python's materialization promise. |
| `rowsStream()` | `AsyncIterable<Row>` | Constant-memory input: a fresh pass over the NDJSON file per call, re-iterable by calling again. Streamed rows are **read-only by contract**: mutating one persists nothing. |
| `columns` | `string[]` | Declared input column order. |
| `config`, `params` | objects | Verbatim from the exec frame. |
| `emit(row)` | function | Streams one output row; first call enters emit mode; `output_data` is then ignored. |
| `begin_emit(columns?)` | function | Declares emit mode explicitly — REQUIRED for filters that may match nothing, so zero matches produce an empty dataset with declared columns, never passthrough. Same trap, same cure, same wording as Python. |
| `output_data` | `{columns, rows}` | Default output; `rows` may be an array, `Iterable`, or `AsyncIterable` — iterables are drained to the output file without materializing. |
| `console` | `log`/`error`/`warn` | Bound to captured streams → typed log frames. `print`-safety guaranteed: script output can never corrupt results. |
| `sleep(ms)` | `(ms: number) => Promise<void>` | **The sanctioned way to wait.** Host-implemented by the worker; resolves after `ms` (clamped to ≥ 0). The Go-side exec timeout still governs — sleeping does not extend it. |

**`sleep(ms)` is the decision on sensor semantics.** The alternative —
guaranteeing `Atomics`/`SharedArrayBuffer` — would freeze two heavy
primitives into the contract purely as a sleep workaround, block the
worker thread wholesale, and constrain the Phase-5 WASM sandbox, whose
namespace this contract will one day be hosted in. Instead: `sleep` is
a first-class namespace function; **`Atomics`/`SharedArrayBuffer`,
`setTimeout`, and every other host or ES capability not named in the
table above are explicitly NOT part of the contract**, even where
today's `node:vm` happens to expose them. Script generators (the SDK's
`sensor()` first among them) must use `sleep`; the real-wrapper
contract suite pins it. Python needs no counterpart — `time.sleep` is
stdlib and already the idiom there.

**Deliberately not ported from Python:** the `_Row`/`_Pass` mutation
journal. It exists to keep legacy Python scripts that mutate rows
in-place working under lazy streaming. TypeScript has no legacy to
protect: `rows` is freely mutable (it is a real array), `rowsStream()`
rows are read-only by documented contract, and no journal machinery is
to be built. Also absent by design: on-worker transpilation — v1
executes the JavaScript the SDK emits; inline-string authors who want
TS-only syntax transpile themselves (bringing a compiler into the
wrapper is its own future ADR).

Failure taxonomy is ADR-029's, unchanged: `user_exception` (with the
JS stack), `mutation_after_pass` unused for TS (no journal),
`resource_limit`, `internal`.

### 3. Capabilities and gating

- `/api/capabilities` gains `code_languages` and
  `code_js_wrapper_version`, and `SupportedExecutionFeatures` gains
  `"code-typescript"` — **computed, not hardcoded**: advertised only
  when `ResolveNode` actually resolved at startup. The honesty rule:
  a feature appears only where a pipeline using it runs. A server
  without Node refuses TypeScript pipelines at deploy preflight, by
  feature name.
- SDK preflight policy, codified: for **runtime-existence features**
  (`code-typescript`, `code-streaming-emit`) clients fail closed when
  a server omits `supported_execution_features` — absence cannot prove
  a runtime exists. Declarative features keep the legacy waiver. The
  TypeScript SDK already implements this; the Python SDK's emit gate
  currently waves legacy servers through and is to be aligned
  (follow-up below).
- Wrapper-version gating: v1 clients match `code_js_wrapper_version`
  exactly; when a v2 contract exists, both sides move to a
  supported-versions list, mirroring `SupportedCodeProtocolVersions`.

### 4. Resource ceilings for Node

The `Limits` model and configuration surface are unchanged; only the
enforcement mechanism differs per runtime, because the mechanisms
don't transplant:

- **Memory: `--max-old-space-size=<memory_mb>` at spawn — never
  RLIMIT_AS.** Measured: Node 24 idles at ~1.3 GiB of address space
  (the V8 pointer-compression cage) and aborts at startup under a
  1 GiB RLIMIT_AS — the helm default would kill every worker before
  hello. The heap flag is accurate for what user scripts allocate; the
  opt-in cgroup-v2 backstop (`code_execution.cgroup_dir`, ADR-029)
  remains the true-RSS ceiling where delegated.
- **CPU, file size, open files: `proctree.ApplyRlimits(pid, …)` via
  `prlimit(2)`** after spawn, before init — Linux-only syscall, no-op
  elsewhere. Applied to **both** languages: for Python this also
  closes the known gap that the in-process preamble is cooperative
  (a script could raise its own limits back); prlimit from outside is
  not undoable by the child.
- **Breach surfaces:** a V8 heap breach is an uncatchable process
  abort (`FATAL ERROR: Reached heap limit … JavaScript heap out of
  memory`) — socket EOF, not a typed frame. To keep the limit-naming
  error contract, `spawnWorker` captures the worker process's own
  stderr into a bounded tail buffer (8 KiB) and worker-death errors
  include it alongside the configured-ceilings hint. This retrofits
  the same diagnostic to Python's uncatchable C-level deaths (the
  OpenBLAS case). Catchable allocation failures (`RangeError`) map to
  typed `resource_limit` frames in the wrapper.
- **Per-OS truth table (documented in operations docs):** Linux gets
  everything; macOS gets the heap flag and no prlimit; Windows has no
  pool and no TypeScript (see §1).

## Consequences

### Positive

- A second language costs one wrapper directory and one dispatch key —
  the protocol, pool, lifecycle, limits model, and capability pattern
  all carry over untouched, which is what ADR-029 promised Phase 4
  would look like.
- The sleep decision keeps the contract minimal and portable into the
  Phase-5 WASM sandbox; nothing in the namespace assumes a thread that
  may block or shared memory that may not exist.
- Worker-death diagnostics improve for both languages via the stderr
  tail capture.

### Negative

- The Node memory ceiling bounds the V8 heap, not the process:
  buffers outside the heap (native addons are absent by construction,
  but `Buffer` allocations count partially) can exceed it. The cgroup
  backstop is the accurate ceiling, as it already is for Python's
  RLIMIT_AS address-space approximation. Documented, not solved.
- Two wrapper contracts now version independently
  (`code_wrapper_version`, `code_js_wrapper_version`). The cost of
  honesty: they ARE independent artifacts; a shared version would lie.

### Deferred

- Go and further languages (compiled-binary distribution, its own ADR);
  Phase-5 WASM spike; strict-streaming per-script capability;
  on-worker transpilation; `sleep`-equivalent enrichment of the Python
  namespace (unneeded — stdlib).

## Alternatives considered

- **Guaranteeing `Atomics`/`SharedArrayBuffer` for sleep** — rejected:
  freezes heavy primitives into the contract as a workaround, blocks
  the worker thread, and pre-constrains the WASM sandbox. `sleep(ms)`
  is one host function.
- **RLIMIT_AS for Node** — rejected on measurement: the V8 cage makes
  address space a fiction for Node; the helm-default ceiling aborts
  the interpreter at boot.
- **A shared wrapper version across languages** — rejected: the
  wrappers evolve independently; one number would drift into a lie.
- **camelCase namespace for TypeScript** — rejected: the names are a
  cross-language protocol contract; per-language renames are the §0
  drift in miniature.
- **Teaching the legacy spawn path TypeScript** — rejected: it exists
  to be deleted.

## Follow-ups

- Implementation stages A2–A6 tracked in core #426; the real-wrapper
  differential contract suite in brokoli-typescript is that repo's B1
  release gate.
- brokoli-typescript: `sensor()` generator switches from `Atomics.wait`
  to `sleep(ms)` (its README already anticipates this outcome).
- brokoli-sdk: align the Python emit gate to fail closed on servers
  that omit `supported_execution_features`, per §3.
- ADR-029's Phase-4 deferred entry gains an in-place pointer here.
