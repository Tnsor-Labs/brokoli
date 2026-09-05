# ADR-034: TypeScript SDK authoring ergonomics

**Status:** proposed
**Date:** 2026-08-31

## Context

The TypeScript SDK (`brokoli` on npm) shipped its authoring surface
(`brokoli-typescript#6`) as plain `Pipeline` instance methods. For
code-node-heavy pipelines the same pipeline reads roughly twice as
verbose as the Python SDK equivalent (`content/docs/sdk` vs
`content/docs/typescript-sdk`). The premium is mechanical, not
semantic:

1. **Explicit call sites.** Python binds name + pipeline through the
   `@task` decorator and chains with `a >> b`. TS repeats
   `name, input, fn` at every node and wires with `.then(...)`.
2. **The I/O contract is spelled out.** A TS task returns
   `{ columns, rows }`; Python returns `list[dict]` and derives columns
   from the data.
3. **Typing.** `Row` is `Record<string, unknown>`, so every field read
   needs `Number(...)`/`String(...)` that dynamic Python omits.
4. **No dependency capture.** `functionSource` is
   `Function.prototype.toString()` with no closure/import packaging
   (v1), so helpers must be inlined in the task body — the biggest
   single source of tokens, and the one that duplicates logic across
   pipelines.

Two constraints bound any fix:

- **The emitted contract cannot change.** ADR-030 §0: the authoring
  side ships opaque script text plus a capability class; the execution
  side owns the whole namespace; dependency is an IR edge. The
  differential oracle asserts byte-identical canonical IR against the
  Python SDK, and the wrapper (`JS_WRAPPER_VERSION = 1`) is already
  release-staged. Ergonomic sugar must therefore *compile away* into
  the existing v1 forms — never introduce a second hand-synced
  representation, never ship a callable the wrapper does not know.
- **Backwards compatibility for a shipped surface.** New helpers are
  additive; nothing an existing example does is re-serialized.

## Decision

Add four authoring-time helpers, all of which expand to the existing
v1 IR before `compile()`/`toJSON()` and thus leave digests, the
differential oracle, and the wrapper contract untouched.

### 1. `pipe(...)` — linear DAGs as one expression

A module function (and a `Pipeline.from(...)` alias) that builds a
`Pipeline`, wires `source → task → sink…` edges in argument order, and
returns the ready pipeline. Non-linear graphs keep the raw instance
builder; `pipe` refuses to be the shapes `branch`, `union`, or
`expand` need (a start node and every downstream must
single-input-chain).

```ts
const p = pipe("Quarterly Revenue", { pipelineId: "qrv-1" },
  sourceDb("Load Orders", { connId: "warehouse", query: "SELECT region, revenue, cost, month FROM orders" }),
  task("Revenue Score", src => ({ columns: ["region", "margin"], rows: src.map(...) })),
  sinkDb("Save", { table: "fact_revenue", connId: "warehouse" }),
);
```

This restores `a >> b >> c` — the one Python operator TS cannot have.

### 2. `cast` — kill the `Number()/String()` ceremony

A tiny typed helper surface (`num`, `str`, `bool`, `maybe`) returning
`Row`-compatible scalars:

```ts
rows.map(r => ({ region: str(r.region), margin: num(r.revenue) - num(r.cost) }))
```

### 3. `helpers` on code-node authoring — fix the root cause

`task`/`source`/`sink`/`map`/`filter`/`validate`/`sensor` and `code`
gain a `helpers` option that serializes each helper once and rewrites
references into a generated single script — the Python
`package="auto"`/ADR-031 story for a language with no bytecode
inspection. The node config grows a `"helpers"` block (`name → source`)
under the existing script; **rewriting happens at authoring time**, so
the worker still sees one opaque script and nothing changes downstream.

```ts
task("Revenue Score", src, rows => grossMargin(rows, TAX_RATE), {
  helpers: { grossMargin, TAX_RATE },
});
```

The rewrite is small and testable: helper→helper references resolve
within the block; a collision with a local name in the task body, or a
helper that itself closes over a module scope we cannot capture, is a
named `PipelineError` at authoring time (mirroring Python's
auto-include refusal) rather than a silent remote `ReferenceError`.

### 4. Default name from `fn.name`

`task(fn)` / `map(fn)` overloads derive the node name from the
function's own name; an explicit `name` argument always wins. Because
`fn.name` is unreliable under bundler minification, non-minified
source functions (declarations and named function expressions) are
the only ones that default; anything else still requires the explicit
name.

Things we are deliberately **not** adopting now: a `withPipeline(ctx)`
context sugar (marginal once `pipe` exists) and a declarative,
config-first whole-graph DSL (a different authoring mode, parked for a
future ADR).

## Consequences

### Positive

- Code-node-heavy pipelines approach Python density while staying
  *dumb*: every helper expands before canonicalization, so emitted IR,
  `renderIR`, `irDigest`, and the differential oracle are unchanged.
- The `helpers` block removes duplicated inline logic — the chunk of
  verbosity that is structural, not syntactic.
- Purely additive: existing examples, tests, and the wrapper `.d.ts`
  surface keep working; the new surface can ship without core changes.

### Negative

- `helpers` rewriting is the one place with real edge cases
  (helper↔helper references, name collisions, arrow vs declaration).
  It needs its own rejection-list tests to stay fail-closed.
- Default naming from `fn.name` is a footgun under web bundlers;
  bounded, but must be documented as build-tool-dependent.
- `pipe` is a new way to build a pipeline, so docs must present both
  it and the expressive builder rather than one canonical style.

### Deferred

- True TS bundle parity with Python's `package="bundle"` (ADR-031) —
  on-disk project packing, import capture, content-addressed upload.
  `helpers` is the v1 stopgap; shipping the real thing should reuse
  the ADR-031/ADR-030 machinery and stay a separate ADR.
- Config-first declarative DSL layered on top of the builder.
- `withPipeline` context sugar.

## Alternatives considered

- **Document the verbosity away** — rewrite examples to the compact
  style, change no SDK. Rejected early: teaches the limitation instead
  of removing it, and the inline-helper duplication (item 4) cannot be
  dieted out of docs.
- **JS decorator API** — `@task` to mirror Python exactly. Rejected:
  JS decorators are class/field-oriented, are compiled out under some
  targets, and still need the wiring pass `pipe` provides.
- **Build-time bundler for task bodies** (esbuild/rollup). Deferred as
  the v2 path to true bundle parity: for v1 it reopens node_modules
  shipping, dependency resolution, and the wrapper/import-space rules
  ADR-030 §0 exists to keep closed.
- **Replace the builder with a config-first DSL.** Rejected now: the
  programmatic builder is what remains for branching graphs; the DSL is
  safer layered on top later than as a replacement.

## Follow-ups

- Prototype `cast` + `pipe` against `src/code.ts` and `src/pipeline.ts`:
  assert emitted IR is byte-identical to the verbose hand-written form
  through the existing differential fixtures, and pass `bun test` +
  `bun run typecheck`.
- Implement the `helpers` rewrite engine with an explicit rejection
  list (collisions, uncapturable closures) and differential coverage.
- Decide default-name semantics against `tsc` vs `bun` vs minified
  builds and document the caveat.
- Update `content/docs/typescript-sdk/overview.mdx` so the tabbed
  Python/TypeScript example uses the new sugar where it wins.