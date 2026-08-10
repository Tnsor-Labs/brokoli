# Changelog

All notable changes to Brokoli are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project loosely follows [Semantic Versioning](https://semver.org/).

Every entry names its owner. With more than one contributor in the
codebase, "who shipped this" is part of the record, not something to
reconstruct from git archaeology.

## [Unreleased]

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
