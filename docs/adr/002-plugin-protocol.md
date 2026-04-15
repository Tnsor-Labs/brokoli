# ADR-002: Plugin protocol — Airbyte-style JSONL over subprocess

**Status:** accepted
**Date:** 2026-04-14

## Context

Brokoli ships with ~15 built-in node types (sources, transforms,
sinks). To cover what modern data teams actually use — Snowflake,
BigQuery, Stripe, HubSpot, Salesforce, Slack, and the hundreds of
other SaaS tools — we either need a plugin system or a team of people
writing Go glue for the next year. Writing every connector in Go is a
multi-year march. Embedding a Python interpreter in the core binary
kills the "single binary, zero deps" story the OSS product depends on
and introduces dependency-conflict nightmares between connectors
(every connector's own preferred versions of boto3, requests, etc.).

We need a plugin model that:

1. Lets connectors be written in any language (especially Python,
   where the data ecosystem lives)
2. Keeps the Brokoli core a single Go binary
3. Isolates plugin dependencies from each other and from the core
4. Supports a subprocess-based execution model that's debuggable with
   standard OS tools (`ps`, `strace`, log files)
5. Is simple enough that a shell script can implement it for testing

## Decision

Adopt a subprocess plugin protocol modeled on
[Airbyte's connector protocol](https://docs.airbyte.com/understanding-airbyte/airbyte-protocol):

A plugin is an executable that supports five subcommands —
`spec`, `check`, `discover`, `read`, `write` — and communicates with
the host over stdin/stdout as newline-delimited JSON ("JSONL").
Plugins have a single JSON config passed on the first stdin line,
stream records out (for sources) or in (for sinks), and report
structured logs/errors on their stdout stream with typed message
kinds.

The protocol is defined in `pkg/plugins/protocol.go` and uses
Protocol Version 1 as of v0.8.0.

## Consequences

### Positive

- **Language-agnostic.** A plugin can be a bash script, a Go binary,
  a Rust binary, or (most commonly) a Python package with a shim
  launcher. The reference test plugin is POSIX sh.
- **Real isolation.** Plugin crashes don't kill Brokoli — the process
  boundary is enforced by the OS.
- **Dependency isolation.** Every plugin brings its own deps
  (Python's venv, Rust's compiled binary, etc.). Two connectors that
  want different versions of the same library both work.
- **Independent release cycles.** A plugin fix doesn't require
  rebuilding Brokoli core.
- **Community contribution is unblocked.** Contributors don't need to
  know Go or touch the core repo — they publish their own plugin
  packages.
- **Debuggable with standard tools.** You can attach to a stuck
  plugin process, inspect its env, read its stderr.

### Negative

- **IPC overhead per node invocation.** Spawn cost is ~100 ms for a
  Python plugin (interpreter boot) amortized over the whole node run.
  Fine for "read a Snowflake table," painful for "per-row micro-
  invocations" — which we don't do.
- **Protocol versioning matters.** Breaking protocol changes require
  a deprecation window. We track supported versions in
  `SupportedProtocolVersions` and will honor v1 for 6+ months after
  any v2 ships.
- **Debugging across a process boundary is harder than in-process.**
  We mitigate with structured logs, exit codes, and a `plugins test`
  CLI smoke command.

### Deferred

- **Transform plugins** are not supported in v0.8.0 — only source
  and sink. A transform plugin needs the protocol to thread input
  records through stdin *after* the config header, and the engine
  to wire that up. Phase 2 work.
- **Long-lived plugin daemons.** Today a plugin process spawns for
  every node invocation. For latency-sensitive use cases (a sensor
  polling every 10 seconds), we'd want to keep the process alive and
  drive it over a persistent request/response loop. Not needed for
  v0.8.0.

## Alternatives considered

- **Embedded Python** via `go-python3` or similar — rejected. Kills
  the single-binary story, introduces CGO, dependency hell across
  connectors, no real isolation.
- **WASM plugins** — interesting but currently unusable for the
  connector use case because Python / Node ecosystem libraries
  (boto3, snowflake-connector-python, salesforce-sdk) don't compile
  to WASM. Would be reinventing every cloud SDK.
- **Shared-library plugins** (Go's `plugin.Open`) — rejected. Go's
  plugin system is fragile in practice: version-mismatch issues,
  no cross-platform support, symbols can't be unloaded.
- **HTTP API for connectors** (plugin = a running service that
  Brokoli calls over HTTP) — considered. Rejected for Phase 1 because
  it requires plugins to be long-running processes with their own
  port allocation, lifecycle management, and health checks. Adds
  complexity before there's a need for it. Might be layered on top
  of the subprocess protocol later as an optimization for hot paths.
- **Build our own protocol from scratch** — rejected because
  Airbyte's has 300+ production connectors validating the design.

## Follow-ups

- [ADR-003](./003-plugin-hybrid-model.md) — which connectors get
  built as first-class Go code vs subprocess plugins.
- [ADR-004](./004-plugin-runtime-in-oss.md) — where the plugin
  runtime lives (OSS vs EE).
- [ADR-005](./005-plugin-distribution-shared-volume.md) — how
  plugins get from the admin's control plane to the worker pods.
- Phase 2: Python SDK (`brokoli-connector-sdk` on PyPI) so authors
  don't touch the protocol directly — `@source` / `@sink`
  decorators handle the JSONL marshaling.
