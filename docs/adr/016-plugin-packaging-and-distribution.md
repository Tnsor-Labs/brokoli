# ADR-016: Plugin packaging, runtime resolution, and distribution

**Status:** proposed
**Date:** 2026-08-09

Companion issue: [#104](https://github.com/Tnsor-Labs/brokoli/issues/104) —
discussion and scope tracking.

## Context

The plugin *protocol* is deliberately language-neutral: a plugin is any
executable speaking JSONL over stdio (ADR-002), extended in place with
progress, planning, and cancellation (ADR-013). That part works. Everything
*around* the executable is where plugins stop being installable by anyone
but their author:

- **The manifest carries no platform or runtime metadata.** `manifest.json`
  declares `binary` and `args` and nothing else about what the file *is*
  (`pkg/plugins/manifest.go`). A compiled plugin built on darwin/arm64
  copied to a linux/amd64 server fails with an exec error at run time —
  nothing checks earlier. An interpreted plugin writes `"binary":
  "python3"` and blindly trusts the host `PATH`; no version requirement can
  be declared, let alone verified. JVM-based plugins have no story at all.
- **Install is a local directory copy.** `brokoli plugins install <dir>`
  copies a directory tree already present on the server's filesystem
  (`cmd/plugins.go`). Tarballs, URLs, upgrades ("already installed" is a
  hard refusal), and any remote source are explicitly unbuilt. There is no
  checksum or integrity verification at any point.
- **There is no plugin HTTP API and no UI page.** A non-technical user has
  no way to see what is installed, install something new, or remove
  something broken. The JSON-Schema config form (ADR-003) renders only
  after a plugin is somehow already on disk.
- **Workers need the bits pre-present.** Distributed workers run the same
  directory scan at startup, so plugin files must reach every worker's
  filesystem out of band — ADR-005's shared volume — and a plugin installed
  after startup needs a worker restart. ADR-005 explicitly deferred the
  alternative it wanted: workers fetching archives from the API server.
- **Install-time verification primitives exist but are unwired.** The
  `spec` subcommand is documented as the install-time capability check and
  `Runner.Spec()` exists, but the install path never calls it.

The main binary already solves the platform problem for itself: goreleaser
builds a CGO-free matrix of linux/darwin/windows × amd64/arm64, `install.sh`
picks the artifact by `uname`, and checksums ship alongside. Plugins get
none of this.

The distinct problem this ADR addresses: **one published plugin must be
installable, through the UI, onto hosts we don't control — different CPU
architectures, and runtimes ranging from a static binary to "needs a JVM".**

## Decision

Separate **what a plugin needs to run** (declared in the package) from
**how a host satisfies it** (resolved at install time). Five parts:

### 1. Package format: one archive, many payloads

A published plugin is a single gzipped tar archive,
`<name>-<version>.bkg`, containing the extended `manifest.json` and one or
more payload directories. The manifest gains a `packaging_version` (starts
at 1) and a `payloads` list; everything existing stays untouched:

```json
{
  "packaging_version": 1,
  "protocol_version": 1,
  "name": "snowflake",
  "version": "1.2.0",
  "node_types": [...],
  "config_schema": {...},
  "payloads": [
    {"runtime": "native", "os": "linux",  "arch": "amd64", "entrypoint": "bin/plugin", "sha256": "..."},
    {"runtime": "native", "os": "darwin", "arch": "arm64", "entrypoint": "bin/plugin", "sha256": "..."},
    {"runtime": "python", "os": "any", "arch": "any",
     "entrypoint": "src/main.py", "requires": {"python": ">=3.10"}, "sha256": "..."}
  ]
}
```

A payload is selected by the first match of (runtime feasible, os, arch),
in manifest order. `sha256` is over the payload subtree and is mandatory.
The installed on-disk layout is unchanged (`<dir>/manifest.json` + files);
installation *writes* the legacy `binary`/`args` fields from the selected
payload, so the loader, runner, and every existing plugin keep working —
a directory installed by hand with the old manifest shape remains valid.

### 2. Runtime classes: the abstraction

The `runtime` field is a closed, versioned vocabulary — initially:

| class | payload is | host must provide | launch shape |
|---|---|---|---|
| `native` | a compiled executable per os/arch | nothing | `entrypoint` |
| `python` | scripts/wheel tree, platform-`any` | interpreter satisfying `requires.python` | `<resolved-python> entrypoint` |
| `node` | JS tree, platform-`any` | node satisfying `requires.node` | `<resolved-node> entrypoint` |
| `jvm` | a jar, platform-`any` | JRE satisfying `requires.java` | `<resolved-java> -jar entrypoint` |

The class determines exactly two things: how feasibility is checked and
how the launch command is derived. It deliberately does **not** abstract
the language — the protocol already did that. Adding a class later
(`container`, `wasm`) is additive: old servers reject unknown classes at
install time with a clear message, never at run time.

**Runtime resolution** happens once, at install: the server probes for an
interpreter/VM satisfying the requirement (`python3 --version` against the
declared range, etc.), records the resolved absolute path into the
installed manifest, and refuses the install — naming exactly what is
missing and on which host — if nothing satisfies it. We check and resolve;
we do not provision. Installing a JRE is the operator's job, and the
feasibility error is how they learn it, before anything is downloaded
rather than when a pipeline fails.

### 3. Install API and UI

New authenticated endpoints, one new UI page:

- `GET /api/plugins` — installed plugins, their versions, node types,
  runtime resolution, and per-worker availability.
- `POST /api/plugins` — install from the index (`{"name","version"}`) or
  from an uploaded `.bkg` (air-gapped path). The server verifies the
  archive checksum, selects a payload, resolves the runtime, runs the
  `spec` drift check (`Runner.Spec()` — finally wired), and only then
  moves the plugin into the plugin dir. Upgrades are the same call:
  install to a staging dir, swap, keep the previous version for one-step
  rollback.
- `DELETE /api/plugins/{name}` — remove.
- `GET /api/plugins/{name}/archive` — the endpoint ADR-005 deferred:
  serves the installed plugin (re-packed, digest-stamped) to workers.

The UI page lists installed plugins, browses the index, and shows
feasibility *before* install ("needs Python ≥ 3.10 — not found on
worker-2"). Node types appear in the editor when the install completes;
gating and capability propagation are already in place and unchanged.

### 4. Workers fetch by digest

Workers stop assuming pre-present files. On claiming work that needs a
plugin they don't have (or have at the wrong digest), they fetch it from
`GET /api/plugins/{name}/archive`, verify the digest, unpack into a local
content-addressed cache, run their *own* runtime resolution (worker hosts
can differ from the API host — a payload feasible on the server may not be
on a worker, and that surfaces as a claim-time error naming the worker,
not a mid-run exec failure), and call `Manager.LoadAll()` — which is
already safe at runtime — so no restart is needed. ADR-005's shared volume
remains supported as a cache-priming optimization and the air-gapped
fallback; it stops being the only mechanism.

### 5. Distribution: a curated static index

The browse/install source is a static, versioned JSON index over HTTPS —
plugin names, versions, descriptions, archive URLs, and sha256 digests —
initially backed by GitHub Releases of a `brokoli-plugins` repository.
This honors ADR-004's phase-4 commitment (curated community index is OSS)
without building registry infrastructure: publishing a plugin is a PR to
the index. The server fetches and caches the index; an explicit URL
override (`BROKOLI_PLUGIN_INDEX`) supports mirrors and private indexes.
Signature verification beyond checksums stays where ADR-004 put it.

## Consequences

### Positive

- A plugin author publishes **one artifact** that installs on any
  supported platform, and the "will it run here?" question is answered at
  install time with a named, actionable error — on the server *and* every
  worker — instead of an exec failure mid-pipeline.
- Non-technical users get list/browse/install/upgrade/remove in the UI,
  with integrity verification they never have to think about.
- The protocol, loader, on-disk layout, gating, and every existing plugin
  are untouched; the extension is additive at the manifest level, in the
  spirit of ADR-013.
- Workers gain plugin delivery and hot reload, retiring ADR-005's two
  hardest negatives (single-node hostPath, restart-to-reload) while
  keeping its mechanism as an optimization.

### Negative

- The runtime-class vocabulary is a compatibility surface we must version
  and support: every class added is a promise. Starting with four keeps
  the promise small but will genuinely exclude some plugins (no `container`
  class yet; no WASM).
- "Resolve, don't provision" pushes interpreter installation onto
  operators. A managed-runtime experience (bundled Python, downloaded JRE)
  would be friendlier and is deliberately out of scope — the cost/benefit
  of shipping runtimes dwarfs this ADR.
- Python dependency isolation is *not* solved here: a `python` payload
  that needs third-party packages still needs them importable by the
  resolved interpreter. The honest v1 answer is vendored dependencies
  inside the payload tree; per-plugin venv creation is deferred.
- Upgrades swap in place; pipelines cannot pin a plugin version
  side-by-side in v1. A bad upgrade has one-step rollback, not gradual
  rollout.

### Deferred

- `container` runtime class and any remote/long-running transport —
  ADR-013's deferral stands; revisit on real need.
- WASM as a runtime class — ADR-002's rejection stands for connector SDKs;
  reconsider only for dependency-free transforms if demand appears.
- Per-plugin venv/dependency provisioning for interpreted runtimes.
- Signature verification (cosign) and private registries — per ADR-004.
- Index moderation/report tooling beyond PR review.

## Alternatives considered

**Per-connector container images (the Airbyte model).** Solves platform
and runtime in one move — and was already rejected in ADR-005 for
node-level cost (a 10-node pipeline becoming 10 pod spawns) and for
making Docker a hard dependency of a single-binary product. Reserving a
future `container` class captures the benefit later without making it the
baseline.

**WASM as the universal payload.** One artifact, sandboxed, no host
runtime — and ADR-002 already established the dealbreaker: the connector
ecosystems plugins exist for (boto3, snowflake-connector, JDBC drivers)
don't compile to it. Nothing has changed enough to reverse that.

**Ship interpreters with the product (bundled Python, jlink'd JRE).**
Friendliest UX; enormous cost — we'd become a runtime distributor across
three OSes and two architectures, with security patch obligations for
each. The feasibility check gets 80% of the value by making the missing
runtime visible early and precisely.

**Language package managers as the distribution channel (`pip install
brokoli-connector-x`).** Works only for Python, assumes a Python-literate
operator, and bypasses integrity, feasibility, and worker delivery
entirely. It remains a fine authoring workflow; it is not an installation
story for normal users.

**OS package managers (apt/brew per plugin).** Multiplies release
engineering per plugin by distro, and cannot reach the UI-driven audience
this ADR exists for.

**Do nothing (documented shared volume + CLI).** Keeps plugins a
developers-only feature and leaves ADR-004's curated-index promise
unmet. The plugin ecosystem cannot grow past its authors this way.

## Follow-ups

- Extend `/api/capabilities` with `supported_packaging_versions` and
  `supported_runtime_classes` so publishers and the index can target
  servers honestly (the ADR-014 negotiation pattern).
- Define the payload-tree canonicalization for `sha256` (file order,
  modes) in a short spec next to the manifest code; add a conformance
  fixture.
- A second sample plugin in an interpreted language with a vendored
  dependency, exercising runtime resolution end to end in CI.
- Teach `.goreleaser.yaml`-style matrix builds to plugin authors via a
  reusable GitHub Actions workflow in the index repository.
- User-facing docs page for installing plugins (ADR-005's still-open
  follow-up, subsumed here).
- Fix the runner's documented-but-absent SIGTERM grace (`exec.Cmd.Cancel`
  + `WaitDelay`) — found while writing this ADR; independent, small, and
  worth doing regardless.
