# Changelog

All notable changes to Brokoli are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project loosely follows [Semantic Versioning](https://semver.org/).

Every entry names its owner. With more than one contributor in the
codebase, "who shipped this" is part of the record, not something to
reconstruct from git archaeology.

## [Unreleased]

## [0.10.18] - 2026-08-12

### Changed

- **Remote dynamic-expansion instances now dispatch concurrently** (#164,
  ADR-017) — @hc12r. Found under real load in a Kubernetes deployment: a
  30-item expansion node with 6 idle worker pods still ran no faster
  than one worker, because instances were dispatched strictly one at a
  time, waiting for each to fully settle before starting the next.
  Remote dispatch (`Engine.InstanceJobQueue` set) now fans out up to 16
  instances at once; local execution is unchanged. No config needed —
  existing opt-in behavior via `BROKOLI_INSTANCE_DISPATCH=1` (#158) is
  unaffected.

## [0.10.17] - 2026-08-12

### Added

- **ADR-017 dispatcher-side wait-and-fetch loop** (#155) — @hc12r. The
  engine can now enqueue a dynamic-expansion instance for remote
  execution and wait for its result, instead of always running it
  in-process — `Engine.InstanceJobQueue`, opt-in, nil by default
  everywhere, zero behavior change for existing deployments.
- **ADR-017 worker-side execution of a `WorkOrder`** (#156) — @hc12r.
  `ExecuteInstanceWorkOrder`/`ExecuteInstanceJob` let a worker sharing
  the dispatcher's store actually run a dispatched instance and settle
  its claim; wired into `cmd/serve.go`'s `--mode worker` loop, the first
  real consumer.
- **`Engine.InstanceJobQueue` now actually wired in `cmd/serve.go`**
  (#158) — @hc12r. #155/#156 shipped the mechanism, but nothing wired
  the dispatcher side into either core's own binary or the enterprise
  binary (which reuses this same command) — found while testing the
  mechanism end-to-end against a real deployment, not by any test in
  the original PRs. Opt-in via `BROKOLI_INSTANCE_DISPATCH=1`.
- **`extensions.RunJob.DeliveryCount`** (#159) — @hc12r. Separates the
  queue-transport's own delivery/redelivery counter from `Attempt`
  (which mirrors `models.ExecutionAttempt.Attempt`, the node/instance
  execution-attempt identity a `WorkOrder`-bearing job settles under).
  Found live-testing ADR-017 in Kubernetes: the enterprise Redis-backed
  `JobQueue` was overwriting `Attempt` with its own delivery count on
  every dequeue, which silently broke every remote instance dispatch —
  no test caught it because every earlier test used a simplified
  fake/channel queue that never exercised real redelivery bookkeeping.

## [0.10.16] - 2026-08-12

### Added

- **Dynamic-expansion items get durable claim/lease/fencing** (#149,
  ADR-017) — @hc12r. Extends #144's node-level wiring down to instance
  granularity: each expansion item now gets its own claim/lease/fencing
  record, generalizing #145's in-memory-only retry-skip into the same
  durable mechanism nodes already have. Same-process only.
- **ADR-017's claim-payload work-description envelope** (#150) —
  @hc12r. `extensions.RunJob` gains `InstanceKey` and a `WorkOrder`
  field (`InstanceWorkOrder`): node type, script, config, the instance's
  input row, run params, timeout — what a remote claimant would need to
  execute one physical instance. Purely additive; nothing populates it
  yet.
- **ADR-017 output-reference delivery design** (#151) — @hc12r. Answers
  how a completed remote instance's output gets back: push not pull,
  `ArtifactStore` gains an instance dimension, `ExecutionAttempt` stays
  control-plane only, a worker→server endpoint modeled on the existing
  checkpoint-forwarding handlers, reusing ADR-012's inline-vs-spill
  threshold.
- **`ArtifactStore` gains instance-level identity** (#152, ADR-017) —
  @hc12r. Key widens from `(run_id, node_id)` to `(run_id, node_id,
  instance_key)`, byte-identical paths for the empty-instance-key case
  (every existing artifact stays readable), purely additive.

## [0.10.15] - 2026-08-12

### Added

- **Node-level execution attempts now go through claim/lease/fencing**
  (#144, #90 M3) — @hc12r. `store.ExecutionAttemptStore`'s claim/lease/
  fencing contract existed but had no production caller below the
  pipeline-level outbox row; node dispatch now creates, claims, acks, and
  settles its own attempt row with a short renewed lease
  (`store.DefaultLeaseDuration`/`RenewInterval`), so a crashed process's
  lease becomes reclaimable within seconds instead of being deferred to
  for however long the node's own configured timeout is. Optional
  capability throughout — a store that doesn't implement
  `ExecutionAttemptStore` is unaffected.
- **Dynamic-expansion node retries skip already-succeeded siblings**
  (#145, #90 M3) — @hc12r. A retry of a `.expand()` node used to
  re-execute every item from scratch, even ones that had already
  succeeded before a later sibling failed. Now only items that failed or
  were never reached run again; reused items still get their own
  `models.ExpansionInstance` row for the new attempt, so per-attempt
  history stays complete.
- **ADR-017 (proposed): worker protocol v2, instance-level dispatch**
  (#146) — @hc12r. Scopes moving the unit of worker claim from a whole
  pipeline run to a physical instance (node/page/expansion-item) — what
  #90 M3's acceptance gate (a worker killed mid-instance, fenced out, its
  instance retried by a different worker without disturbing successful
  siblings) actually needs. Status proposed; implementation gated on
  co-design review.
- **`execution_attempts`' claim key gains an instance dimension** (#147,
  ADR-017 slice 1) — @hc12r. `(run_id, node_id, attempt)` widens to
  `(run_id, node_id, instance_key, attempt)`, matching the physical-plan
  instance-identity scheme, so a pagination page or expansion item will
  be able to claim/lease/fence independently of its siblings instead of
  colliding with them. Purely additive — every existing caller passes an
  empty instance key and is unaffected. Migrates a live database in
  place on both backends.

## [0.10.14] - 2026-08-12

### Added

- **`sink_db`/`migrate` generate their own write SQL** (#135, #136,
  brokoli-sdk#12) — @hc12r. `source -> sink_db(table, mode)` now works on
  its own — no upstream `sql_generate` node needed. Both `sink_db` and
  `migrate` support `append`/`overwrite`/`upsert` (upsert keyed on
  `key_columns`; `ON CONFLICT` on Postgres/SQLite, `ON DUPLICATE KEY
  UPDATE` on MySQL), generating the SQL from the input rows directly. The
  older `sql_generate -> sink_db` pattern still works unchanged.
- **`filter_rows` comparison operators** (#133, brokoli-sdk#14) —
  @hc12r. `filter_rows` conditions now support `>`, `<`, `>=`, `<=` (in
  addition to the existing `=`/`==`/`!=`/`in [...]`), with numeric
  comparison when both sides parse as numbers and lexicographic
  otherwise. An unrecognized condition is now a run-time error, not a
  silent keep-all-rows pass-through.
- **`sink_api` resolves `conn_id`, matching `source_api`** (#139, closes
  #137) — @hc12r. The connection resolver had no case for `sink_api`, so
  a `conn_id` on that node type was silently ignored — no base URL, no
  headers, no Basic Auth ever got injected. It now resolves identically
  to `source_api` (relative/absolute URL, merged headers, Basic Auth).
- **`retry_backoff` actually selects a backoff strategy** (#140, closes
  #138) — @hc12r. The retry loop hardcoded exponential backoff and never
  read the `retry_backoff` config key. `"linear"` and `"fixed"` now work
  as documented, alongside the existing default `"exponential"`.
- **`GET /api/plugins` carries each plugin's payloads** (#141, #110 M3)
  — @hc12r. The list response now includes each plugin's
  runtime/os/arch/requires payload list, so a caller can resolve its own
  feasibility (`plugins.SelectPayload`) without downloading the archive
  first — the data prerequisite for per-worker plugin availability.

### Fixed

- **Physical-plan e2e test flake under parallel load** (#134) — @hc12r.
  Inline `NewEngine(s).RunPipeline(...)` calls that never closed the
  engine left background event goroutines writing to the store after the
  test returned, occasionally landing a write during `t.TempDir()`
  cleanup (`directory not empty`). Test-only fix; no production change.

## [0.10.13] - 2026-08-11

Full curated notes with per-change ownership:
[v0.10.13 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.13).

### Added

- **Plugin distribution UI and curated index** (#126, #129, #130, #131) —
  @hc12r. Plugins are now installable without the CLI: a Plugins page lists
  installed plugins, uploads a `.bkg`, and removes; a curated static index
  (`GET /api/plugins/index`, `BROKOLI_PLUGIN_INDEX` override, ADR-016 §5)
  is browsable and installable by name (`POST /api/plugins/index/{name}`),
  with the archive's sha256 verified against the index digest before any
  bytes reach the installer — no plugin executes without a digest match.
  Install/remove errors are surfaced verbatim, so a wrong-platform or
  tampered package shows the server's named reason.
- **Capability advertising for plugin packaging** (#127) — @hc12r.
  `GET /api/capabilities` now reports `supported_packaging_versions` and
  `supported_runtime_classes` (native, python, node, jvm), so clients can
  tell what a deployment understands before uploading — the same
  negotiate-before-you-act contract as `supported_ir_versions`.
- **Durable physical run instances** (#123, #125, ADR-015 §8, #90) —
  @hc12r. A run's physical instances (one per plain node, one per resolved
  expansion item) are projected at `GET /api/runs/{id}/instances` and, once
  a run records them, persisted in a durable `physical_instances` table
  (read durable-first with a projection fallback for older runs). A run now
  answers both what it *planned* (`/plan`) and what it *ran* (`/instances`)
  in the physical model.

### Fixed

- **Plugin runner SIGTERM grace on the spec path** (#128) — @hc12r. The
  install-time `spec` probe now applies the same SIGTERM-then-SIGKILL grace
  as the main run path (`Cancel` + `WaitDelay`) instead of killing a hung
  plugin immediately with no chance to shut down cleanly.
- **SODP dashboard eviction test no longer flakes after midnight** (#124) —
  @hc12r. The test derives its expected day buckets from the seeded
  timestamp rather than the wall clock.

## [0.10.12] - 2026-08-11

Full curated notes with per-change ownership:
[v0.10.12 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.12).

### Added

- **Plugin package format and management API** (#117, #118) — @hc12r.
  Plugins ship as a single `.bkg` archive (manifest + per-platform/runtime
  payloads) that installs on any supported host, or fails at install with
  a named reason: payload selection by os/arch and runtime (native,
  python, node, jvm with version checks), mandatory integrity hash, and a
  `spec` drift check — nothing lands before all three pass.
  `GET/POST/DELETE /api/plugins` and `GET /api/plugins/{name}/archive`
  install (from an uploaded archive), list, remove, and serve plugins;
  install and remove hot-reload the node-type registry without a restart.
  ADR-016.
- **Physical execution plans** (#119, #120, #121) — @hc12r. The authored
  logical pipeline and the per-run physical plan are now distinct: a
  planner expands the graph into scheduled stages and work units
  (pagination page-groups, dynamic-expansion fan-out) with deterministic
  instance keys and honest runtime-resolved counts. `GET
  /pipelines/{id}/plan` explains the plan before execution; every run
  persists its plan snapshot (`GET /runs/{id}/plan`) so recovery and
  audit see the plan as-decided, not a recomputation. Foundation for
  independently scheduled, retryable physical instances. ADR-015.

### Changed

- **The `Store` interface is composed from focused capability
  interfaces** (#120) — @hc12r. The 100+-method contract is now
  `PipelineStore`, `RunStore`, `AlertStore`, and the rest, embedded into
  `Store`. Pure refactor — no behavior change — but a new capability is
  added as its own interface instead of force-breaking every
  implementation, and a consumer can depend on the narrow slice it needs.

## [0.10.11] - 2026-08-10

Full curated notes with per-change ownership:
[v0.10.11 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.11).

### Added

- **Stateless validation** (#114) — @hc12r. `POST /api/pipelines/validate`
  runs full structural + executable validation on a request-body IR
  document without persisting anything.
- **`supported_execution_features` on `/api/capabilities`** (#115) —
  @hc12r. The list of pipeline semantics this host can actually
  *execute* — `conditional-routing`, `dynamic-expansion`, `union`,
  `pagination-checkpoints` — with an honesty rule: a feature is listed
  only when a pipeline using it runs. SDK preflight gates on it, so
  compile-only features are refused at deploy instead of failing at run
  time.
- **Cross-model contract fixtures** (#115) — @hc12r. A real SDK-compiled
  IR 2.1 payload is validated against the canonical schema on every test
  run; the SDK repo validates its emissions against a vendored schema
  copy, so contract drift in either direction fails a suite naming the
  payload.

### Changed

- **Unknown top-level fields are rejected** (#114) — @hc12r. Create,
  update, and JSON import decode fail-closed: an unrecognized field
  returns `400` naming it. Forward-compatible payloads belong under the
  new `extensions` namespace, which is persisted in both stores and
  echoed back uninterpreted. **Breaking** for clients that sent junk
  fields the server used to silently drop.
- **The JSON→YAML import fallback is gone** (#114) — @hc12r. A JSON type
  mismatch on `/api/pipelines/import` used to fall through to the YAML
  parser (YAML parses JSON) and return `201` with a silently mutilated
  pipeline — missing capabilities, params, hooks, SLA and dependency
  fields. A JSON body now gets the JSON path, full stop.
- Prose-only docs changes (Markdown, ADRs, RFCs) skip the Go CI pipeline
  (#113) — @hc12r; `docs/schema/**` deliberately still runs everything.

## [0.10.10] - 2026-08-10

Full curated notes with per-change ownership:
[v0.10.10 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.10).

### Added

- **Plugin subprocess cancellation** (#100) — @JefferMarcelino. Cancelling
  a run or hitting a node timeout now terminates the running plugin
  process: `SIGTERM`, then `SIGKILL` after the grace window (killed
  directly on non-Unix). Plugin-backed nodes no longer run to completion
  after a cancel. ADR-013 M2.
- **Canonical pipeline IR schema** (#111) — @hc12r.
  `docs/schema/pipeline-ir-2.1.json` is the machine-readable statement of
  the wire contract (ADR-014), bound to `models.Pipeline` by two-way
  contract tests: model/schema drift now fails CI naming the exact
  property. Adds `github.com/santhosh-tekuri/jsonschema/v6` (Apache-2.0).

### Changed

- **Persistence is fail-closed** (#106) — @hc12r. Create, update, import,
  clone, and rollback run full executable validation before anything is
  written: unknown node types are rejected with `400 … has unsupported
  type` instead of executing as silent successful no-ops, cycles and
  missing required config are caught at save time, and rolling back to a
  no-longer-valid snapshot is refused. Updates that leave the graph
  byte-identical (pause, rename, schedule) skip executable validation so
  legacy rows stay operable. The editor's Run/Preview/Check flows now
  bail out loudly when a save is rejected instead of silently executing
  the previously persisted graph, and the zero-node Blank template is no
  longer listed as creatable. Dry-run responses carry an `error` field
  alongside partial results.

### Fixed

- **Pipeline hooks persist** (#111) — @hc12r. `hooks` was decoded, echoed
  on the create response, and silently dropped by the store — gone on the
  next read. Both SQLite and Postgres now persist it through create,
  update, and every read path.
- **Dashboard day counts bucket in one timezone** (#108) — @hc12r.
  `runs_today` compared a local date against UTC-stored timestamps, so
  non-UTC servers miscounted for the first hours of every local day (and
  the corresponding test flaked after midnight).

### Roadmap

- **ADR-016 proposed: plugin packaging, runtime resolution, and
  distribution** (#105) — @hc12r. One published archive with per-payload
  runtime classes, install-time feasibility resolution, an install
  API/UI, worker fetch-by-digest, and a curated index. Discussion: #104;
  implementation tracker: #110.

## [0.10.9] - 2026-08-09

Full curated notes with per-change ownership:
[v0.10.9 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.9).

### Added

- **Conditional edge routing — IR 2.1** (#97, #101) — @hc12r. Condition
  nodes route true/false branches as first-class edges: skipped branches
  propagate transitively, convergence nodes wait for all inputs to
  resolve, and routing decisions, skipped outcomes, and resume lineage
  persist atomically across SQLite, PostgreSQL, JSON, YAML, clone, and
  version snapshots. #101 turned it on: the server now advertises IR 2.1
  in `supported_ir_versions` (while `CurrentIRVersion` stays 2.0),
  rejects malformed conditional graphs and unsupported expressions
  before persistence, and the UI authors branch metadata and renders
  skipped node states.

### Changed

- **SLA fields are labeled as an enterprise capability** (#98) — @hc12r.
  The deadline/timezone fields are stored by every edition but only
  monitored by an enterprise deployment; the editor now says so with a
  tag and tooltip instead of letting a community install configure a
  deadline nothing watches.

### Fixed

- **Connection Basic Auth applies on the read path, not only on writes**
  (#93) — @hc12r. `source_api` reads — the first fetch and every
  paginated page — now carry connection credentials, which were
  previously silently dropped on reads while the same connection worked
  on a sink. Deliberate precedence: connection credentials win over a
  handwritten `Authorization` header.
- **The engine can be shut down** (#96) — @hc12r. `Engine.Close(ctx)`
  refuses new dispatch, stops starting background work, and waits —
  bounded by the caller's context — for every goroutine the engine owns.
  Ends `database is closed` logs at shutdown and the `TempDir RemoveAll`
  CI failures caused by orphaned fan-out goroutines.
- **`GET /api/capabilities` is public again** (#102) — @hc12r. Exact
  anonymous capabilities discovery now passes the API-key, JWT/open-mode,
  workspace, and enterprise SSO auth layers, with per-layer regression
  coverage; every other API method and path stays protected. Unblocks
  SDK capability preflight on authenticated deployments.

## [0.10.8] - 2026-08-09

Full curated notes with per-change ownership:
[v0.10.8 release](https://github.com/Tnsor-Labs/brokoli/releases/tag/v0.10.8).

### Added

- **The artifact plane** (#85, #88, #89) — @hc12r. `pkg/artifact`:
  `ArtifactRef`/`DatasetRef` with a versioned manifest and a
  content-addressed local store; `response="artifact"` returns a real
  reference; node outputs above `BROKOLI_SPILL_THRESHOLD_BYTES` (default
  64 MiB) spill automatically. ADR-012, updated per milestone.
- **Persisted alert inbox, run events over HTTP, org-wide DLQ** (#80) —
  @hc12r.
- **Connector protocol `MsgProgress`** (#66) — @JefferMarcelino. First
  milestone of ADR-013, and the author's first shipped change. 🎉
- **Keyset pagination for run history** (#84) — @hc12r. `?after=`/`?limit=`
  cursors; `?page=` now returns real rows and a real total.
- **`union`/`dataset_map`/`dataset_filter` node types, executable
  `.expand()` fan-out** (#34, #35) — @hc12r.
- **source_api execution contract**: response contracts and pagination
  execute (#33); bounded page fan-out (#43); persisted, resumable
  checkpoints (#45, #53); page-retry config keys (#48) — @hc12r.
- **Database-backed, admin-editable pipeline templates** (#71, #72) —
  @hc12r.

### Changed

- **Dashboard rebuilt as a triage console**; light theme rebuilt on
  surface tones; dashboard figures read from the database instead of
  evicted live state (#83, #81) — @hc12r.
- **`response="artifact"` no longer inlines the body** — the output row
  is `uri`/`media_type`/`size_bytes`/`checksum` (#88) — @hc12r.
- Plugin node-type gating runs before dispatch (#65); manifest kinds
  propagate into node capabilities (#63) — @hc12r.

### Fixed

- `runs.org_id` populated at creation, legacy rows backfilled (#54);
  artifact/checkpoint stores wired into purge (#51); empty JSON array is
  zero records (#46); sample data 404 (#68); SODP presence cleanup (#56);
  `PORT` env synced with `--port` (#69); Data Quality template config
  (#70) — @hc12r.

> Releases 0.7.2 through 0.10.7 predate this file's revival; their
> histories live in the
> [GitHub releases](https://github.com/Tnsor-Labs/brokoli/releases).

## [0.7.1] - 2026-04-13

### Fixed

- **SSRF guard no longer blocks worker self-references.** `pkg/fetchers`
  rejected relative `/api/samples/...` URLs once they were resolved against
  `BROKOLI_SERVER_URL`, because in k8s/docker deployments the server
  hostname resolves to a private cluster IP (10.x / 172.16.x / ClusterIP) —
  which is exactly what `isBlockedHost` rejects. Workers running in a
  separate pod couldn't fetch sample data from the API they were already
  authenticated to. The fix tracks whether a URL originated as a relative
  path (an implicit trusted self-reference to the Brokoli server) and skips
  the SSRF check in that case only. Absolute URLs with private IPs are
  still blocked. Also removes a dead `HasPrefix` check that ran after the
  URL had already been rewritten to `http://...` form.

## [0.7.0]

### Added

- **SODP realtime stack** — `/api/ws` now speaks the State-Oriented Data
  Protocol ([orkestri/SODP](https://github.com/orkestri/SODP)) instead of a
  hand-rolled JSON event hub. Binary msgpack framing, per-key subscriptions,
  delta fanout, and reconnect-resilient via the protocol's `RESUME` frame.
  See [`docs/realtime.md`](docs/realtime.md) for the architecture, key model,
  wire format, and rate limits.
- **`pkg/sodp`** — new in-process Go implementation of SODP v0.1: versioned
  state store with ring-buffer delta log, per-org tenant-isolated fanout bus,
  per-session rate limiting and watch caps, JWT auth, eviction goroutine for
  completed run state. 50+ unit tests, 12 integration tests against a real
  WebSocket, and a cross-language test that runs `@sodp/client@0.2.1` from
  Node.js against the live server.
- **`api/eventbus_bridge_test.go`** — distributed-mode test: worker publishes
  to `EventBus` (in-memory in OSS, Redis pub/sub in enterprise), API pod
  subscribes and forwards events into the SODP fanout. The bus → bridge → SODP
  path is the only way events cross pod boundaries in distributed deployments.
- **`api/ws_jwt_context_test.go`** — regression test for the JWT-on-WebSocket
  context-propagation fix below.
- **Calendar redesign** — replaced the previous flat-grid heatmap with a
  GitHub-style horizontal layout (weeks as columns, days as rows). Each day
  cell shows a vertical color split sized by the actual success / running /
  failed proportions instead of going solid red on any failure. Activity
  intensity is log-scaled relative to the busiest day in the range. Includes
  a sparkline trend strip, muted data-viz palette local to the page, and a
  proportional detail bar with per-day success rate.

### Changed

- **WebSocket protocol on `/api/ws` is now binary msgpack, not JSON.** Any
  external tooling that opened `/api/ws` and parsed JSON event objects must
  switch to a SODP client. Reference clients available for TypeScript
  (`@sodp/client`), Python (`sodp`), and Java (`io.sodp:sodp-client`), or
  speak the wire format directly using the schemas documented in
  `docs/realtime.md`. The Brokoli UI handles this transparently via
  `ui/src/lib/ws.ts` — no UI-side migration required when upgrading.
- **`@sodp/client@^0.2.0`** is now a runtime UI dependency. Pinned in
  `ui/package.json`. Self-hosters who build the UI from source will pull it
  automatically via `npm install`.
- **`api.Hub` removed.** The pre-SODP `Hub` type and `handlers_ws.go` were
  deleted. `RegisterRoutes` now takes `*sodp.Server` instead of `*Hub`.
  Internal callers only.
- **`bridgeCh` buffer is 512 events.** Engine events that overflow it are
  dropped with a warning log line, instead of stalling the engine's event
  channel. This is a deliberate backpressure choice for high-frequency log
  events; the cap is sized for typical workloads but is not yet load-tested
  at thousands of events per second.
- **Calendar palette is now muted** (forest sage / terracotta / steel blue
  inspired by Observable's data-viz palettes) for large color regions where
  the global bright `--success`/`--failed` were "hot." The global theme is
  unchanged — every other status indicator across the app still uses the
  vivid colors where high saturation reads correctly at small sizes.

### Fixed

- **Multi-tenant isolation collapsed on the WebSocket path.** The HTTP
  `JWTAuth` middleware validated tokens on `/api/ws` upgrades but did not
  propagate `claims` or `org_id` into the request context. The SODP server
  read claims from the context to enforce per-session tenant filtering, found
  nothing, and treated every authenticated WebSocket session as the default
  org — meaning users from `org=acme` would receive realtime events from
  `org=widgets` and vice versa. The non-WebSocket branch of the same
  middleware was correct; the WebSocket branch is now aligned with it.
  `api/ws_jwt_context_test.go` locks this in as a regression test.
- **`Pipelines.svelte` run counts reset to 1 after each WebSocket event.**
  Two stacked bugs: the WS event handler replaced the cached pipeline-runs
  entry with a fresh single-element list missing the `_total/_success/...`
  counter fields; and the Run-button click handler did its own optimistic
  REST refetch that wiped the same fields a millisecond earlier. The handler
  now mutates the cached counters in place and tracks `seenStarted` run IDs
  to handle late-arriving terminal events; the click handler no longer
  refetches.
- **`Connections.svelte` and `Variables.svelte` empty-state CTAs were
  no-ops.** Both pages set a phantom `showCreateModal` variable that didn't
  exist; the actual modal state is `showModal`. The CTAs now call the proper
  `openCreate()` handler that initializes the form and opens the modal.
- **`RunIndicator` panel overflowed off-screen.** The expanded "Recent Runs"
  panel was anchored to the floating button's `left: 0`, so a 300px-wide
  panel attached to a button at `right: 24px` clipped past the viewport. Now
  anchored to `right: 0` so it expands leftward into the page.
- **WebSocket connection burned through retries before login.** `App.svelte`
  created the SODP client unconditionally on mount, before the user had
  authenticated. The client hit `503` (open mode) or `401` (locked mode) for
  the first 4-8 attempts and then sat in a 30-second backoff loop, missing
  events that fired immediately after login. Connection lifecycle is now
  driven reactively from `$authUser`: open on login, close on logout. No
  retries are wasted against an unauthenticated session.
- **`DecodeFrame` rejected bodyless frames.** Heartbeat and ack frames with
  a nil body decoded into an empty `msgpack.RawMessage`, which the body
  decoder treated as EOF. `DecodeFrame` now treats `len(raw[3]) == 0` as a
  nil body explicitly.
- **`sodp.Server` had a concurrent-write race on the WebSocket connection.**
  Handler responses (HELLO, STATE_INIT, RESULT, ERROR) wrote directly to the
  connection while the write-pump goroutine also wrote, which `gorilla/websocket`
  doesn't support. All handler responses now go through the session's `Send`
  channel; the write pump is the sole writer to the connection.

### Security

- The WebSocket JWT-context fix above is a security fix in addition to a
  correctness fix. The data leak only manifested in multi-tenant deployments
  with multiple orgs, but in those deployments it was complete: every realtime
  event visible to any org was visible to every other org. Single-tenant /
  community-mode deployments were never affected.

---

## Prior history

This file starts with the SODP migration. For commit-level history before
this point see `git log` — the project did not maintain a CHANGELOG previously.
